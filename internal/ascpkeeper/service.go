package ascpkeeper

import (
	"context"
	"errors"
	"fmt"
)

// RunOnce claims and advances one durable job. A signed transaction is sealed
// before the state becomes BROADCASTING. On restart, PREPARED/BROADCASTING is
// rebroadcast byte-for-byte; a new nonce is never allocated for uncertain work.
func (s *Service) RunOnce(ctx context.Context) (Job, error) {
	lease, err := s.store.Claim(ctx, s.config.KeeperID, s.config.LeaseDuration)
	if err != nil {
		return Job{}, err
	}
	defer s.store.ReleaseLease(context.WithoutCancel(ctx), lease)
	if lease.Job.KeeperID != s.config.KeeperID || lease.Job.GasPayer != s.config.GasPayer {
		return Job{}, ErrInvalidJob
	}
	switch lease.Job.State {
	case StateQueued:
		return s.prepareAndBroadcast(ctx, lease)
	case StatePrepared, StateBroadcasting:
		return s.rebroadcast(ctx, lease)
	case StateTimedOut, StateReorged:
		return s.replace(ctx, lease)
	default:
		return Job{}, ErrStateConflict
	}
}

// ObserveOnce advances one submitted attempt using evidence produced by the
// independent settlement/reconciliation boundary.
func (s *Service) ObserveOnce(ctx context.Context) (Job, error) {
	lease, err := s.store.ClaimObservation(ctx, s.config.KeeperID, s.config.LeaseDuration)
	if err != nil {
		return Job{}, err
	}
	defer s.store.ReleaseLease(context.WithoutCancel(ctx), lease)
	attempt, err := s.store.CurrentAttempt(ctx, lease.Job.JobID)
	if err != nil {
		return Job{}, err
	}
	outcome, err := s.outcomes.Observe(ctx, lease.Job, attempt)
	if err != nil {
		return Job{}, fmt.Errorf("observe independently verified keeper outcome: %w", err)
	}
	if err := validateOutcome(lease.Job, attempt, outcome, s.config.Clock().UTC()); err != nil {
		return Job{}, err
	}
	return s.store.ApplyOutcome(ctx, lease, outcome)
}

func (s *Service) replace(ctx context.Context, lease Lease) (Job, error) {
	job := lease.Job
	previous, err := s.store.CurrentAttempt(ctx, job.JobID)
	if err != nil {
		return Job{}, err
	}
	if previous.Number > s.config.MaxFeeBumps || (job.State != StateTimedOut && job.State != StateReorged) {
		dead, markErr := s.store.MarkRecoveryDeadLetter(ctx, lease, ErrFeeBumpsExhausted.Error())
		return dead, errors.Join(ErrFeeBumpsExhausted, markErr)
	}
	if err := s.replacements.SafeToReplace(ctx, job, previous); err != nil {
		return Job{}, fmt.Errorf("prove same-nonce replacement safety: %w", errors.Join(ErrUnsafeReplacement, err))
	}
	now := s.config.Clock().UTC()
	var artifact []byte
	if job.RequiresBearer() {
		if now.Before(job.ValidAfter) || !now.Before(job.ValidBefore) {
			dead, markErr := s.store.MarkRecoveryDeadLetter(ctx, lease, ErrSignatureUnavailable.Error())
			return dead, errors.Join(ErrSignatureUnavailable, markErr)
		}
		epoch, epochErr := s.leadership.Current(ctx, job.OrganizationID)
		if epochErr != nil || epoch != job.LeadershipEpoch {
			return Job{}, errors.Join(ErrStateConflict, epochErr)
		}
		artifact, err = s.artifacts.Release(ctx, job.SignerHandle, s.config.KeeperID)
		if err != nil {
			return Job{}, fmt.Errorf("release activated signer artifact for replacement: %w", err)
		}
		defer clear(artifact)
	}
	fee, err := s.fees.Bump(ctx, job, previous)
	if err != nil || !validFee(fee) || !strictlyBumped(previous.Fee, fee) {
		return Job{}, errors.Join(ErrInvalidTransaction, err)
	}
	unsigned, err := s.assembler.Assemble(ctx, job, artifact, previous.Nonce, fee)
	if err != nil {
		return Job{}, err
	}
	if err := validateUnsigned(job, unsigned, s.config); err != nil {
		return Job{}, err
	}
	if err := s.verifier.Verify(ctx, job, unsigned, artifact); err != nil {
		return Job{}, fmt.Errorf("verify replacement transaction binding: %w", err)
	}
	signed, err := s.wallet.Sign(ctx, unsigned)
	if err != nil {
		return Job{}, err
	}
	defer clear(signed.Raw)
	if err := verifySignedTransaction(unsigned, signed); err != nil || signed.Hash == previous.TransactionHash {
		return Job{}, ErrInvalidTransaction
	}
	number := previous.Number + 1
	sealed, keyID, err := s.sealer.Seal(ctx, signed.Raw, aad(job.JobID, number, signed.Hash))
	if err != nil {
		return Job{}, err
	}
	attempt := Attempt{JobID: job.JobID, Number: number, Nonce: previous.Nonce, GasPayer: job.GasPayer,
		Fee: fee, TransactionHash: signed.Hash, SealedRawTransaction: sealed, SealingKeyID: keyID,
		State: AttemptPrepared, PreparedAt: now}
	if _, err := s.store.RecordReplacement(ctx, lease, previous, attempt); err != nil {
		return Job{}, err
	}
	return s.broadcast(ctx, lease, attempt, signed.Raw)
}

func (s *Service) prepareAndBroadcast(ctx context.Context, lease Lease) (Job, error) {
	job := lease.Job
	now := s.config.Clock().UTC()
	if now.Before(job.EligibleAfter) {
		return Job{}, ErrNoWork
	}
	var artifact []byte
	if job.RequiresBearer() {
		if now.Before(job.ValidAfter) || !now.Before(job.ValidBefore) {
			dead, markErr := s.store.MarkRecoveryDeadLetter(ctx, lease, ErrSignatureUnavailable.Error())
			return dead, errors.Join(ErrSignatureUnavailable, markErr)
		}
		epoch, err := s.leadership.Current(ctx, job.OrganizationID)
		if err != nil {
			return Job{}, fmt.Errorf("read keeper leadership epoch: %w", err)
		}
		if epoch != job.LeadershipEpoch {
			dead, markErr := s.store.MarkRecoveryDeadLetter(ctx, lease, "stale leadership epoch")
			return dead, errors.Join(ErrStateConflict, markErr)
		}
		artifact, err = s.artifacts.Release(ctx, job.SignerHandle, s.config.KeeperID)
		if err != nil {
			return Job{}, fmt.Errorf("release activated signer artifact: %w", err)
		}
		defer clear(artifact)
		if len(artifact) == 0 || len(artifact) > 4096 {
			return Job{}, ErrSignatureUnavailable
		}
	}
	observedNonce, err := s.nonces.PendingNonce(ctx, job.ChainID, job.GasPayer)
	if err != nil {
		return Job{}, fmt.Errorf("observe keeper pending nonce: %w", err)
	}
	fee, err := s.fees.Initial(ctx, job)
	if err != nil || !validFee(fee) {
		return Job{}, fmt.Errorf("select keeper fee: %w", errors.Join(err, ErrInvalidTransaction))
	}
	unsigned, err := s.assembler.Assemble(ctx, job, artifact, observedNonce, fee)
	if err != nil {
		return Job{}, fmt.Errorf("preflight keeper transaction: %w", err)
	}
	if err := validateUnsigned(job, unsigned, s.config); err != nil {
		return Job{}, err
	}
	if err := s.verifier.Verify(ctx, job, unsigned, artifact); err != nil {
		return Job{}, fmt.Errorf("verify exact keeper transaction binding: %w", err)
	}
	nonce, err := s.store.AllocateNonce(ctx, lease, observedNonce)
	if err != nil {
		return Job{}, fmt.Errorf("allocate keeper nonce: %w", err)
	}
	if nonce != observedNonce {
		unsigned, err = s.assembler.Assemble(ctx, job, artifact, nonce, fee)
		if err != nil {
			return Job{}, fmt.Errorf("assemble allocated keeper transaction: %w", err)
		}
		if err := validateUnsigned(job, unsigned, s.config); err != nil {
			return Job{}, err
		}
		if err := s.verifier.Verify(ctx, job, unsigned, artifact); err != nil {
			return Job{}, fmt.Errorf("verify allocated keeper transaction binding: %w", err)
		}
	}
	signed, err := s.wallet.Sign(ctx, unsigned)
	if err != nil {
		return Job{}, fmt.Errorf("sign keeper transaction: %w", err)
	}
	defer clear(signed.Raw)
	if err := verifySignedTransaction(unsigned, signed); err != nil {
		return Job{}, err
	}
	sealed, keyID, err := s.sealer.Seal(ctx, signed.Raw, aad(job.JobID, 1, signed.Hash))
	if err != nil {
		return Job{}, fmt.Errorf("seal keeper transaction: %w", err)
	}
	attempt := Attempt{JobID: job.JobID, Number: 1, Nonce: nonce, GasPayer: job.GasPayer, Fee: fee,
		TransactionHash: signed.Hash, SealedRawTransaction: sealed, SealingKeyID: keyID,
		State: AttemptPrepared, PreparedAt: now}
	if _, err := s.store.RecordPrepared(ctx, lease, attempt); err != nil {
		return Job{}, err
	}
	return s.broadcast(ctx, lease, attempt, signed.Raw)
}

func (s *Service) rebroadcast(ctx context.Context, lease Lease) (Job, error) {
	attempt, err := s.store.CurrentAttempt(ctx, lease.Job.JobID)
	if err != nil {
		return Job{}, err
	}
	if attempt.State != AttemptPrepared && attempt.State != AttemptBroadcasting {
		return Job{}, ErrStateConflict
	}
	raw, err := s.sealer.Open(ctx, attempt.SealedRawTransaction, attempt.SealingKeyID,
		aad(attempt.JobID, attempt.Number, attempt.TransactionHash))
	if err != nil {
		return Job{}, fmt.Errorf("open durable keeper transaction: %w", err)
	}
	defer clear(raw)
	if err := verifyRecoveredSigned(lease.Job, attempt, raw); err != nil {
		return Job{}, err
	}
	return s.broadcast(ctx, lease, attempt, raw)
}

func (s *Service) broadcast(ctx context.Context, lease Lease, attempt Attempt, raw []byte) (Job, error) {
	if _, err := s.store.MarkBroadcasting(ctx, lease, attempt.Number); err != nil {
		return Job{}, err
	}
	hash, err := s.broadcaster.Broadcast(ctx, raw)
	if err == nil {
		if hash != attempt.TransactionHash {
			job, markErr := s.store.MarkAmbiguous(ctx, lease, attempt.Number, "RPC returned a different transaction hash")
			return job, errors.Join(ErrBroadcastAmbiguous, markErr)
		}
		return s.store.MarkSubmitted(ctx, lease, attempt.Number, hash)
	}
	if errors.Is(err, ErrBroadcastRejected) || errors.Is(err, ErrBroadcastUnderpriced) {
		target := StateDeadLetter
		if errors.Is(err, ErrBroadcastUnderpriced) {
			target = StateTimedOut
		}
		job, markErr := s.store.MarkRejected(ctx, lease, attempt.Number, target, err.Error())
		return job, errors.Join(err, markErr)
	}
	job, markErr := s.store.MarkAmbiguous(ctx, lease, attempt.Number, err.Error())
	return job, errors.Join(ErrBroadcastAmbiguous, markErr)
}
