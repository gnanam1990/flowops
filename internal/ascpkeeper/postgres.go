package ascpkeeper

import (
	"context"
	cryptorand "crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgconn"
)

type PostgresStore struct {
	db    *sql.DB
	clock func() time.Time
}

func NewPostgresStore(db *sql.DB, clocks ...func() time.Time) (*PostgresStore, error) {
	if db == nil || len(clocks) > 1 || len(clocks) == 1 && clocks[0] == nil {
		return nil, ErrInvalidConfig
	}
	clock := time.Now
	if len(clocks) == 1 {
		clock = clocks[0]
	}
	return &PostgresStore{db: db, clock: clock}, nil
}

func (s *PostgresStore) Enqueue(ctx context.Context, input EnqueueInput) (Job, bool, error) {
	now := s.clock().UTC()
	input.CanonicalPayload = append([]byte(nil), input.CanonicalPayload...)
	if err := validateInput(input, now); err != nil {
		return Job{}, false, err
	}
	for retry := 0; retry < 3; retry++ {
		job, replay, err := s.enqueueOnce(ctx, input, now)
		if !serializationFailure(err) {
			return job, replay, err
		}
		if ctx.Err() != nil {
			return Job{}, false, ctx.Err()
		}
	}
	return Job{}, false, errors.New("keeper enqueue serialization retries exhausted")
}

func (s *PostgresStore) enqueueOnce(ctx context.Context, input EnqueueInput, now time.Time) (Job, bool, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return Job{}, false, fmt.Errorf("begin keeper enqueue: %w", err)
	}
	defer tx.Rollback()
	if existing, err := loadJob(ctx, tx, input.JobID, false); err == nil {
		if !sameInput(existing, input) {
			return Job{}, false, ErrStateConflict
		}
		if err := tx.Commit(); err != nil {
			return Job{}, false, err
		}
		return existing, true, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return Job{}, false, err
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO ascp_keeper_jobs
			(job_id,operation_id,organization_id,action,chain_id,keeper_id,gas_payer,target,value_wei,
			 canonical_payload,canonical_payload_hash,authorization_digest,signer_handle,signer_address,
			 valid_after,valid_before,eligible_after,eligibility_evidence_digest,eligibility_observed_at,
			 leadership_epoch,state,created_at,updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,NULLIF($12,''),NULLIF($13,''),NULLIF($14,''),
		        $15,$16,$17,NULLIF($18,''),$19,$20,'QUEUED',$21,$21)`,
		input.JobID, input.OperationID, input.OrganizationID, input.Action, input.ChainID, input.KeeperID,
		input.GasPayer, input.Target, input.ValueWei, input.CanonicalPayload, input.CanonicalPayloadHash,
		input.AuthorizationDigest, input.SignerHandle, input.SignerAddress, nullTime(input.ValidAfter),
		nullTime(input.ValidBefore), input.EligibleAfter, input.EligibilityEvidenceDigest,
		nullTime(input.EligibilityObservedAt), nullUint64(input.LeadershipEpoch), now)
	if err != nil {
		return Job{}, false, classifyWrite(err)
	}
	job, err := loadJob(ctx, tx, input.JobID, false)
	if err != nil {
		return Job{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return Job{}, false, err
	}
	return job, false, nil
}

func (s *PostgresStore) Claim(ctx context.Context, keeperID, gasPayer string, chainID uint64, duration time.Duration) (Lease, error) {
	if !identifier(keeperID) || !address(gasPayer) || (chainID != 8453 && chainID != 84532) || duration < time.Second || duration > time.Minute {
		return Lease{}, ErrInvalidConfig
	}
	now := s.clock().UTC()
	token, err := randomToken()
	if err != nil {
		return Lease{}, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return Lease{}, err
	}
	defer tx.Rollback()
	var jobID string
	err = tx.QueryRowContext(ctx, `
		SELECT job_id FROM ascp_keeper_jobs
		 WHERE keeper_id=$1 AND gas_payer=$2 AND chain_id=$3 AND state IN ('QUEUED','PREPARED','BROADCASTING','TIMED_OUT','REORGED')
		   AND eligible_after <= $4 AND (lease_expires_at IS NULL OR lease_expires_at <= $4)
		 ORDER BY CASE action WHEN 'CLAIM_EXPIRED' THEN 0 ELSE 1 END, eligible_after, created_at, job_id
		 FOR UPDATE SKIP LOCKED LIMIT 1`, keeperID, gasPayer, chainID, now).Scan(&jobID)
	if errors.Is(err, sql.ErrNoRows) {
		return Lease{}, ErrNoWork
	}
	if err != nil {
		return Lease{}, fmt.Errorf("claim keeper job: %w", err)
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE ascp_keeper_jobs SET lease_owner=$2,lease_token=$3,lease_expires_at=$4,updated_at=$5
		 WHERE job_id=$1`, jobID, keeperID, token, now.Add(duration), now)
	if err != nil {
		return Lease{}, err
	}
	if affected(result) != 1 {
		return Lease{}, ErrLeaseLost
	}
	job, err := loadJob(ctx, tx, jobID, false)
	if err != nil {
		return Lease{}, err
	}
	if err := tx.Commit(); err != nil {
		return Lease{}, err
	}
	return Lease{Job: job, Token: token}, nil
}

func (s *PostgresStore) ClaimObservation(ctx context.Context, keeperID, gasPayer string, chainID uint64, duration time.Duration) (Lease, error) {
	if !identifier(keeperID) || !address(gasPayer) || (chainID != 8453 && chainID != 84532) || duration < time.Second || duration > time.Minute {
		return Lease{}, ErrInvalidConfig
	}
	now := s.clock().UTC()
	token, err := randomToken()
	if err != nil {
		return Lease{}, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return Lease{}, err
	}
	defer tx.Rollback()
	var jobID string
	err = tx.QueryRowContext(ctx, `SELECT job_id FROM ascp_keeper_jobs
		WHERE keeper_id=$1 AND gas_payer=$2 AND chain_id=$3 AND state IN ('AMBIGUOUS','SUBMITTED','CONFIRMED')
		  AND (lease_expires_at IS NULL OR lease_expires_at <= $4)
		ORDER BY updated_at,job_id FOR UPDATE SKIP LOCKED LIMIT 1`, keeperID, gasPayer, chainID, now).Scan(&jobID)
	if errors.Is(err, sql.ErrNoRows) {
		return Lease{}, ErrNoWork
	}
	if err != nil {
		return Lease{}, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE ascp_keeper_jobs SET lease_owner=$2,lease_token=$3,
		lease_expires_at=$4,updated_at=$5 WHERE job_id=$1`, jobID, keeperID, token, now.Add(duration), now)
	if err != nil || affected(result) != 1 {
		return Lease{}, errors.Join(err, ErrLeaseLost)
	}
	job, err := loadJob(ctx, tx, jobID, false)
	if err != nil {
		return Lease{}, err
	}
	if err := tx.Commit(); err != nil {
		return Lease{}, err
	}
	return Lease{Job: job, Token: token}, nil
}

func (s *PostgresStore) AllocateNonce(ctx context.Context, lease Lease, observed uint64) (uint64, error) {
	if err := validateLease(lease); err != nil {
		return 0, err
	}
	now := s.clock().UTC()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	job, err := loadLeasedJob(ctx, tx, lease, now)
	if err != nil {
		return 0, err
	}
	if job.State != StateQueued || job.AttemptCount != 0 {
		return 0, ErrStateConflict
	}
	var reserved sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT nonce::text FROM ascp_keeper_jobs WHERE job_id=$1`, job.JobID).Scan(&reserved); err != nil {
		return 0, err
	}
	if reserved.Valid {
		allocated, err := parseNonce(reserved.String)
		if err != nil {
			return 0, err
		}
		if err := tx.Commit(); err != nil {
			return 0, err
		}
		return allocated, nil
	}
	var next string
	err = tx.QueryRowContext(ctx, `SELECT next_nonce::text FROM ascp_keeper_nonce_sequences
		WHERE chain_id=$1 AND gas_payer=$2 FOR UPDATE`, job.ChainID, job.GasPayer).Scan(&next)
	allocated := observed
	if err == nil {
		persisted, parseErr := parseNonce(next)
		if parseErr != nil {
			return 0, parseErr
		}
		if persisted > allocated {
			allocated = persisted
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	if allocated == math.MaxUint64 {
		return 0, ErrStateConflict
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO ascp_keeper_nonce_sequences (chain_id,gas_payer,next_nonce,updated_at)
		VALUES ($1,$2,$3,$4)
		ON CONFLICT (chain_id,gas_payer) DO UPDATE SET next_nonce=$3,updated_at=$4`,
		job.ChainID, job.GasPayer, allocated+1, now)
	if err != nil {
		return 0, classifyWrite(err)
	}
	result, err := tx.ExecContext(ctx, `UPDATE ascp_keeper_jobs SET nonce=$2,updated_at=$3
		WHERE job_id=$1 AND lease_token=$4 AND nonce IS NULL`, job.JobID, allocated, now, lease.Token)
	if err != nil {
		return 0, classifyWrite(err)
	}
	if affected(result) != 1 {
		return 0, ErrLeaseLost
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return allocated, nil
}

func (s *PostgresStore) RecordPrepared(ctx context.Context, lease Lease, attempt Attempt) (Job, error) {
	if err := validateAttempt(attempt); err != nil || attempt.Number != 1 || attempt.State != AttemptPrepared {
		return Job{}, ErrInvalidTransaction
	}
	return s.transition(ctx, lease, func(ctx context.Context, tx *sql.Tx, job Job, now time.Time) error {
		if job.State != StateQueued || job.AttemptCount != 0 || job.CurrentAttempt != 0 || job.JobID != attempt.JobID {
			return ErrStateConflict
		}
		var nonce string
		if err := tx.QueryRowContext(ctx, `SELECT nonce::text FROM ascp_keeper_jobs WHERE job_id=$1`, job.JobID).Scan(&nonce); err != nil {
			return err
		}
		allocated, err := parseNonce(nonce)
		if err != nil || allocated != attempt.Nonce || attempt.GasPayer != job.GasPayer {
			return ErrStateConflict
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO ascp_keeper_tx_attempts
			(job_id,attempt_number,nonce,gas_payer,max_fee_per_gas_wei,max_priority_fee_per_gas_wei,
			 transaction_hash,sealed_raw_transaction,sealing_key_id,state,prepared_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,'PREPARED',$10)`, attempt.JobID, attempt.Number,
			attempt.Nonce, attempt.GasPayer, attempt.Fee.MaxFeePerGasWei, attempt.Fee.MaxPriorityFeePerGasWei,
			attempt.TransactionHash, attempt.SealedRawTransaction, attempt.SealingKeyID, attempt.PreparedAt)
		if err != nil {
			return classifyWrite(err)
		}
		_, err = tx.ExecContext(ctx, `UPDATE ascp_keeper_jobs SET state='PREPARED',attempt_count=1,current_attempt=1,updated_at=$2 WHERE job_id=$1`, job.JobID, now)
		return err
	})
}

func (s *PostgresStore) RecordReplacement(ctx context.Context, lease Lease, previous, attempt Attempt) (Job, error) {
	if err := validateAttempt(attempt); err != nil || attempt.Number != previous.Number+1 ||
		attempt.State != AttemptPrepared || attempt.Nonce != previous.Nonce || attempt.GasPayer != previous.GasPayer ||
		!strictlyBumped(previous.Fee, attempt.Fee) {
		return Job{}, ErrInvalidTransaction
	}
	return s.transition(ctx, lease, func(ctx context.Context, tx *sql.Tx, job Job, now time.Time) error {
		if (job.State != StateTimedOut && job.State != StateReorged) || job.CurrentAttempt != previous.Number ||
			job.AttemptCount != previous.Number || job.JobID != attempt.JobID || attempt.Number > 4 {
			return ErrStateConflict
		}
		result, err := tx.ExecContext(ctx, `UPDATE ascp_keeper_tx_attempts SET state='REPLACED'
			WHERE job_id=$1 AND attempt_number=$2 AND transaction_hash=$3 AND state IN ('REJECTED','REORGED')`,
			job.JobID, previous.Number, previous.TransactionHash)
		if err != nil || affected(result) != 1 {
			return errors.Join(err, ErrStateConflict)
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO ascp_keeper_tx_attempts
			(job_id,attempt_number,nonce,gas_payer,max_fee_per_gas_wei,max_priority_fee_per_gas_wei,
			 transaction_hash,sealed_raw_transaction,sealing_key_id,state,prepared_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,'PREPARED',$10)`, attempt.JobID, attempt.Number,
			attempt.Nonce, attempt.GasPayer, attempt.Fee.MaxFeePerGasWei, attempt.Fee.MaxPriorityFeePerGasWei,
			attempt.TransactionHash, attempt.SealedRawTransaction, attempt.SealingKeyID, attempt.PreparedAt)
		if err != nil {
			return classifyWrite(err)
		}
		_, err = tx.ExecContext(ctx, `UPDATE ascp_keeper_jobs SET state='PREPARED',attempt_count=$2,current_attempt=$2,
			last_error=NULL,updated_at=$3 WHERE job_id=$1`, job.JobID, attempt.Number, now)
		return err
	})
}

func (s *PostgresStore) MarkBroadcasting(ctx context.Context, lease Lease, number int) (Job, error) {
	return s.transition(ctx, lease, func(ctx context.Context, tx *sql.Tx, job Job, now time.Time) error {
		if job.CurrentAttempt != number || (job.State != StatePrepared && job.State != StateBroadcasting) {
			return ErrStateConflict
		}
		result, err := tx.ExecContext(ctx, `UPDATE ascp_keeper_tx_attempts
			SET state='BROADCASTING',broadcast_at=COALESCE(broadcast_at,$3)
			WHERE job_id=$1 AND attempt_number=$2 AND state IN ('PREPARED','BROADCASTING')`, job.JobID, number, now)
		if err != nil || affected(result) != 1 {
			return errors.Join(err, ErrStateConflict)
		}
		_, err = tx.ExecContext(ctx, `UPDATE ascp_keeper_jobs SET state='BROADCASTING',updated_at=$2 WHERE job_id=$1`, job.JobID, now)
		return err
	})
}

func (s *PostgresStore) MarkSubmitted(ctx context.Context, lease Lease, number int, txHash string) (Job, error) {
	if !hash(txHash) {
		return Job{}, ErrInvalidTransaction
	}
	return s.finishBroadcast(ctx, lease, number, txHash, StateSubmitted, AttemptSubmitted, "")
}

func (s *PostgresStore) MarkAmbiguous(ctx context.Context, lease Lease, number int, reason string) (Job, error) {
	return s.finishBroadcast(ctx, lease, number, "", StateAmbiguous, AttemptAmbiguous, reason)
}

func (s *PostgresStore) MarkRejected(ctx context.Context, lease Lease, number int, target State, reason string) (Job, error) {
	if target != StateTimedOut && target != StateDeadLetter {
		return Job{}, ErrStateConflict
	}
	return s.finishBroadcast(ctx, lease, number, "", target, AttemptRejected, reason)
}

func (s *PostgresStore) MarkRecoveryDeadLetter(ctx context.Context, lease Lease, reason string) (Job, error) {
	reason = boundedError(reason)
	return s.transition(ctx, lease, func(ctx context.Context, tx *sql.Tx, job Job, now time.Time) error {
		if job.State != StateQueued && job.State != StateTimedOut && job.State != StateReorged {
			return ErrStateConflict
		}
		_, err := tx.ExecContext(ctx, `UPDATE ascp_keeper_jobs SET state='DEAD_LETTER',last_error=$2,updated_at=$3
			WHERE job_id=$1`, job.JobID, reason, now)
		return err
	})
}

func (s *PostgresStore) ApplyOutcome(ctx context.Context, lease Lease, outcome Outcome) (Job, error) {
	return s.transition(ctx, lease, func(ctx context.Context, tx *sql.Tx, job Job, now time.Time) error {
		attempt, err := scanAttempt(tx.QueryRowContext(ctx, `SELECT a.job_id,a.attempt_number,a.nonce::text,a.gas_payer,
			a.max_fee_per_gas_wei,a.max_priority_fee_per_gas_wei,a.transaction_hash,a.sealed_raw_transaction,
			a.sealing_key_id,a.state,a.prepared_at,a.broadcast_at,a.last_error,a.evidence_digest,a.observed_at
			FROM ascp_keeper_tx_attempts a WHERE a.job_id=$1 AND a.attempt_number=$2 FOR UPDATE`, job.JobID, job.CurrentAttempt))
		if err != nil {
			return err
		}
		if err := validateOutcome(job, attempt, outcome, now); err != nil {
			return err
		}
		var attemptState AttemptState
		switch outcome.State {
		case StateSubmitted:
			attemptState = AttemptSubmitted
		case StateConfirmed:
			attemptState = AttemptConfirmed
		case StateFinalized:
			attemptState = AttemptFinalized
		case StateReverted:
			attemptState = AttemptReverted
		case StateReorged:
			attemptState = AttemptReorged
		case StateTimedOut:
			attemptState = AttemptRejected
		default:
			return ErrStateConflict
		}
		result, err := tx.ExecContext(ctx, `UPDATE ascp_keeper_tx_attempts SET state=$3,evidence_digest=$4,observed_at=$5
			WHERE job_id=$1 AND attempt_number=$2 AND transaction_hash=$6 AND state IN ('AMBIGUOUS','SUBMITTED','CONFIRMED')`,
			job.JobID, attempt.Number, attemptState, outcome.EvidenceDigest, outcome.ObservedAt.UTC(), outcome.TransactionHash)
		if err != nil || affected(result) != 1 {
			return errors.Join(err, ErrStateConflict)
		}
		_, err = tx.ExecContext(ctx, `UPDATE ascp_keeper_jobs SET state=$2,last_error=NULL,updated_at=$3 WHERE job_id=$1`,
			job.JobID, outcome.State, now)
		return err
	})
}

func (s *PostgresStore) finishBroadcast(ctx context.Context, lease Lease, number int, txHash string, jobState State, attemptState AttemptState, reason string) (Job, error) {
	reason = boundedError(reason)
	return s.transition(ctx, lease, func(ctx context.Context, tx *sql.Tx, job Job, now time.Time) error {
		if job.State != StateBroadcasting || job.CurrentAttempt != number {
			return ErrStateConflict
		}
		query := `UPDATE ascp_keeper_tx_attempts SET state=$3,last_error=NULLIF($4,'')
			WHERE job_id=$1 AND attempt_number=$2 AND state='BROADCASTING'`
		args := []any{job.JobID, number, attemptState, reason}
		if txHash != "" {
			query += ` AND transaction_hash=$5`
			args = append(args, txHash)
		}
		result, err := tx.ExecContext(ctx, query, args...)
		if err != nil || affected(result) != 1 {
			return errors.Join(err, ErrStateConflict)
		}
		_, err = tx.ExecContext(ctx, `UPDATE ascp_keeper_jobs SET state=$2,last_error=NULLIF($3,''),updated_at=$4 WHERE job_id=$1`,
			job.JobID, jobState, reason, now)
		return err
	})
}

func (s *PostgresStore) CurrentAttempt(ctx context.Context, jobID string) (Attempt, error) {
	if !hash(jobID) {
		return Attempt{}, ErrInvalidJob
	}
	return scanAttempt(s.db.QueryRowContext(ctx, `SELECT a.job_id,a.attempt_number,a.nonce::text,a.gas_payer,
		a.max_fee_per_gas_wei,a.max_priority_fee_per_gas_wei,a.transaction_hash,a.sealed_raw_transaction,
		a.sealing_key_id,a.state,a.prepared_at,a.broadcast_at,a.last_error,a.evidence_digest,a.observed_at
		FROM ascp_keeper_tx_attempts a JOIN ascp_keeper_jobs j ON j.job_id=a.job_id
		WHERE a.job_id=$1 AND a.attempt_number=j.current_attempt`, jobID))
}

func (s *PostgresStore) ReleaseLease(ctx context.Context, lease Lease) error {
	if err := validateLease(lease); err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `UPDATE ascp_keeper_jobs
		SET lease_owner=NULL,lease_token=NULL,lease_expires_at=NULL
		WHERE job_id=$1 AND lease_token=$2`, lease.Job.JobID, lease.Token)
	if err != nil {
		return err
	}
	if affected(result) == 0 {
		return ErrLeaseLost
	}
	return nil
}

func (s *PostgresStore) transition(ctx context.Context, lease Lease, change func(context.Context, *sql.Tx, Job, time.Time) error) (Job, error) {
	if err := validateLease(lease); err != nil {
		return Job{}, err
	}
	now := s.clock().UTC()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return Job{}, err
	}
	defer tx.Rollback()
	job, err := loadLeasedJob(ctx, tx, lease, now)
	if err != nil {
		return Job{}, err
	}
	if err := change(ctx, tx, job, now); err != nil {
		return Job{}, err
	}
	job, err = loadJob(ctx, tx, job.JobID, false)
	if err != nil {
		return Job{}, err
	}
	if err := tx.Commit(); err != nil {
		return Job{}, err
	}
	return job, nil
}

type queryRower interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

const jobColumns = `job_id,operation_id,organization_id,action,chain_id,keeper_id,gas_payer,target,value_wei,
	canonical_payload,canonical_payload_hash,authorization_digest,signer_handle,signer_address,
	valid_after,valid_before,eligible_after,eligibility_evidence_digest,eligibility_observed_at,
	leadership_epoch,state,lease_owner,lease_token,lease_expires_at,
	attempt_count,current_attempt,created_at,updated_at,last_error`

func loadJob(ctx context.Context, q queryRower, jobID string, lock bool) (Job, error) {
	suffix := ""
	if lock {
		suffix = " FOR UPDATE"
	}
	return scanJob(q.QueryRowContext(ctx, `SELECT `+jobColumns+` FROM ascp_keeper_jobs WHERE job_id=$1`+suffix, jobID))
}

func loadLeasedJob(ctx context.Context, q queryRower, lease Lease, now time.Time) (Job, error) {
	job, err := scanJob(q.QueryRowContext(ctx, `SELECT `+jobColumns+` FROM ascp_keeper_jobs
		WHERE job_id=$1 AND lease_token=$2 AND lease_owner=$3 AND lease_expires_at>$4 FOR UPDATE`,
		lease.Job.JobID, lease.Token, lease.Job.KeeperID, now))
	if errors.Is(err, sql.ErrNoRows) {
		return Job{}, ErrLeaseLost
	}
	return job, err
}

type rowScanner interface{ Scan(...any) error }

func scanJob(row rowScanner) (Job, error) {
	var job Job
	var auth, handle, signer, owner, token, last, eligibilityEvidence sql.NullString
	var validAfter, validBefore, leaseExpires, eligibilityObserved sql.NullTime
	var epoch, current sql.NullInt64
	err := row.Scan(&job.JobID, &job.OperationID, &job.OrganizationID, &job.Action, &job.ChainID, &job.KeeperID,
		&job.GasPayer, &job.Target, &job.ValueWei, &job.CanonicalPayload, &job.CanonicalPayloadHash,
		&auth, &handle, &signer, &validAfter, &validBefore, &job.EligibleAfter, &eligibilityEvidence, &eligibilityObserved, &epoch, &job.State,
		&owner, &token, &leaseExpires, &job.AttemptCount, &current, &job.CreatedAt, &job.UpdatedAt, &last)
	if err != nil {
		return Job{}, err
	}
	job.AuthorizationDigest, job.SignerHandle, job.SignerAddress = auth.String, handle.String, signer.String
	job.EligibilityEvidenceDigest = eligibilityEvidence.String
	job.LeaseOwner, job.LeaseToken, job.LastError = owner.String, token.String, last.String
	if validAfter.Valid {
		job.ValidAfter = validAfter.Time.UTC()
	}
	if validBefore.Valid {
		job.ValidBefore = validBefore.Time.UTC()
	}
	if leaseExpires.Valid {
		job.LeaseExpiresAt = leaseExpires.Time.UTC()
	}
	if eligibilityObserved.Valid {
		job.EligibilityObservedAt = eligibilityObserved.Time.UTC()
	}
	if epoch.Valid {
		job.LeadershipEpoch = uint64(epoch.Int64)
	}
	if current.Valid {
		job.CurrentAttempt = int(current.Int64)
	}
	job.EligibleAfter, job.CreatedAt, job.UpdatedAt = job.EligibleAfter.UTC(), job.CreatedAt.UTC(), job.UpdatedAt.UTC()
	return job, nil
}

func scanAttempt(row rowScanner) (Attempt, error) {
	var attempt Attempt
	var nonce string
	var broadcast sql.NullTime
	var last sql.NullString
	var evidence sql.NullString
	var observed sql.NullTime
	err := row.Scan(&attempt.JobID, &attempt.Number, &nonce, &attempt.GasPayer,
		&attempt.Fee.MaxFeePerGasWei, &attempt.Fee.MaxPriorityFeePerGasWei, &attempt.TransactionHash,
		&attempt.SealedRawTransaction, &attempt.SealingKeyID, &attempt.State, &attempt.PreparedAt, &broadcast, &last,
		&evidence, &observed)
	if err != nil {
		return Attempt{}, err
	}
	parsed, err := parseNonce(nonce)
	if err != nil {
		return Attempt{}, err
	}
	attempt.Nonce, attempt.LastError = parsed, last.String
	attempt.EvidenceDigest = evidence.String
	attempt.PreparedAt = attempt.PreparedAt.UTC()
	if broadcast.Valid {
		attempt.BroadcastAt = broadcast.Time.UTC()
	}
	if observed.Valid {
		attempt.ObservedAt = observed.Time.UTC()
	}
	return attempt, nil
}

func validateAttempt(attempt Attempt) error {
	if !hash(attempt.JobID) || attempt.Number < 1 || attempt.Number > 4 || !address(attempt.GasPayer) ||
		!validFee(attempt.Fee) || !hash(attempt.TransactionHash) || len(attempt.SealedRawTransaction) == 0 ||
		len(attempt.SealedRawTransaction) > 2*1024*1024 || !identifier(attempt.SealingKeyID) || attempt.PreparedAt.IsZero() {
		return ErrInvalidTransaction
	}
	return nil
}

func validateLease(lease Lease) error {
	if !hash(lease.Job.JobID) || !identifier(lease.Job.KeeperID) || !opaque(lease.Token) || lease.Token != lease.Job.LeaseToken {
		return ErrLeaseLost
	}
	return nil
}

func sameInput(job Job, input EnqueueInput) bool {
	base := job.JobID == input.JobID && job.OperationID == input.OperationID && job.OrganizationID == input.OrganizationID &&
		job.Action == input.Action && job.ChainID == input.ChainID && job.KeeperID == input.KeeperID &&
		job.GasPayer == input.GasPayer && job.Target == input.Target && job.ValueWei == input.ValueWei &&
		string(job.CanonicalPayload) == string(input.CanonicalPayload) && job.CanonicalPayloadHash == input.CanonicalPayloadHash &&
		job.AuthorizationDigest == input.AuthorizationDigest && job.SignerHandle == input.SignerHandle &&
		job.SignerAddress == input.SignerAddress && job.ValidAfter.Equal(input.ValidAfter) &&
		job.ValidBefore.Equal(input.ValidBefore) && job.EligibleAfter.Equal(input.EligibleAfter) && job.LeadershipEpoch == input.LeadershipEpoch
	if !base {
		return false
	}
	if job.Action == ActionClaimExpired {
		return true
	}
	return job.EligibilityEvidenceDigest == input.EligibilityEvidenceDigest && job.EligibilityObservedAt.Equal(input.EligibilityObservedAt)
}

func randomToken() (string, error) {
	value := make([]byte, 24)
	if _, err := cryptorand.Read(value); err != nil {
		return "", err
	}
	return "lease_" + hex.EncodeToString(value), nil
}

func parseNonce(value string) (uint64, error) {
	if !unsignedDecimal(value) {
		return 0, ErrStateConflict
	}
	var result uint64
	for _, digit := range value {
		if result > (math.MaxUint64-uint64(digit-'0'))/10 {
			return 0, ErrStateConflict
		}
		result = result*10 + uint64(digit-'0')
	}
	return result, nil
}

func nullTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value.UTC()
}
func nullUint64(value uint64) any {
	if value == 0 {
		return nil
	}
	return value
}
func boundedError(value string) string {
	value = strings.TrimSpace(strings.ToValidUTF8(value, "�"))
	for len(value) > 2048 {
		_, size := utf8.DecodeLastRuneInString(value)
		value = value[:len(value)-size]
	}
	return value
}
func affected(result sql.Result) int64 {
	if result == nil {
		return 0
	}
	count, _ := result.RowsAffected()
	return count
}
func classifyWrite(err error) error {
	var pg *pgconn.PgError
	if errors.As(err, &pg) && (pg.Code == "23505" || pg.Code == "23514" || pg.Code == "23503") {
		return errors.Join(ErrStateConflict, err)
	}
	return err
}
func serializationFailure(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && (pg.Code == "40001" || pg.Code == "40P01")
}
