package ascpkeeper

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
)

func TestServiceReleasesActivatedBearerAndPersistsBeforeBroadcast(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	store := newMemoryStore(now)
	input := signedInput(now)
	if _, replay, err := store.Enqueue(context.Background(), input); err != nil || replay {
		t.Fatal(err)
	}
	fx := newFixture(store, now)
	fx.broadcast.inspect = func() {
		attempt := store.attempts[input.JobID][0]
		if attempt.State != AttemptBroadcasting || len(attempt.SealedRawTransaction) == 0 || store.jobs[input.JobID].State != StateBroadcasting {
			t.Fatal("transaction was broadcast before durable BROADCASTING transition")
		}
	}
	job, err := fx.service(t).RunOnce(context.Background())
	if err != nil || job.State != StateSubmitted {
		t.Fatalf("RunOnce = %+v, %v", job, err)
	}
	if fx.artifacts.calls != 1 || fx.artifacts.keeper != "keeper-primary" || fx.nonces.calls != 1 || fx.wallet.calls != 1 {
		t.Fatalf("calls artifact=%d nonce=%d wallet=%d keeper=%q", fx.artifacts.calls, fx.nonces.calls, fx.wallet.calls, fx.artifacts.keeper)
	}
	attempt := store.attempts[input.JobID][0]
	if attempt.Nonce != 17 || attempt.GasPayer != input.GasPayer || attempt.State != AttemptSubmitted {
		t.Fatalf("attempt=%+v", attempt)
	}
}

func TestServiceRejectsTransactionSubstitutionBeforeWallet(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	store := newMemoryStore(now)
	input := signedInput(now)
	_, _, _ = store.Enqueue(context.Background(), input)
	fx := newFixture(store, now)
	fx.assembler.mutateTarget = true
	if _, err := fx.service(t).RunOnce(context.Background()); !errors.Is(err, ErrInvalidTransaction) {
		t.Fatalf("error=%v", err)
	}
	if fx.nonces.calls != 1 || store.nextNonce != 0 || fx.wallet.calls != 0 || fx.broadcast.calls != 0 {
		t.Fatal("substituted transaction reserved a nonce or crossed the wallet/RPC boundary")
	}
}

func TestServiceRejectsWalletMutationBeforeSealingOrBroadcast(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	store := newMemoryStore(now)
	_, _, _ = store.Enqueue(context.Background(), signedInput(now))
	fx := newFixture(store, now)
	fx.wallet.mutateData = true
	if _, err := fx.service(t).RunOnce(context.Background()); !errors.Is(err, ErrInvalidTransaction) {
		t.Fatalf("error=%v", err)
	}
	if fx.broadcast.calls != 0 || len(store.attempts[testHash(1)]) != 0 {
		t.Fatal("wallet-mutated transaction was sealed or broadcast")
	}
}

func TestServiceRejectsFeeOrGasAboveRelayerCapBeforeNonceReservation(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	store := newMemoryStore(now)
	_, _, _ = store.Enqueue(context.Background(), signedInput(now))
	fx := newFixture(store, now)
	fx.fees.initial = Fee{"1001", "2"}
	if _, err := fx.service(t).RunOnce(context.Background()); !errors.Is(err, ErrInvalidTransaction) {
		t.Fatalf("fee error=%v", err)
	}
	if store.nextNonce != 0 || fx.wallet.calls != 0 {
		t.Fatal("over-cap fee reserved nonce or reached wallet")
	}

	store = newMemoryStore(now)
	_, _, _ = store.Enqueue(context.Background(), signedInput(now))
	fx = newFixture(store, now)
	fx.assembler.gasLimit = 200001
	if _, err := fx.service(t).RunOnce(context.Background()); !errors.Is(err, ErrInvalidTransaction) {
		t.Fatalf("gas error=%v", err)
	}
	if store.nextNonce != 0 || fx.wallet.calls != 0 {
		t.Fatal("over-cap gas reserved nonce or reached wallet")
	}
}

func TestServiceChecksLeadershipBeforeSignatureRelease(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	store := newMemoryStore(now)
	_, _, _ = store.Enqueue(context.Background(), signedInput(now))
	fx := newFixture(store, now)
	fx.leadership.epoch = 8
	if _, err := fx.service(t).RunOnce(context.Background()); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("error=%v", err)
	}
	if fx.artifacts.calls != 0 || fx.wallet.calls != 0 {
		t.Fatal("stale leader obtained bearer or used wallet")
	}
}

func TestPermissionlessClaimNeverContactsSigner(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	store := newMemoryStore(now)
	input := claimInput(now)
	_, _, _ = store.Enqueue(context.Background(), input)
	fx := newFixture(store, now)
	job, err := fx.service(t).RunOnce(context.Background())
	if err != nil || job.State != StateSubmitted {
		t.Fatalf("RunOnce = %+v, %v", job, err)
	}
	if fx.artifacts.calls != 0 || fx.leadership.calls != 0 {
		t.Fatal("claimExpired incorrectly crossed a signing boundary")
	}
}

func TestAmbiguousBroadcastIsQuarantinedAndNeverBlindlyRetried(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	store := newMemoryStore(now)
	input := signedInput(now)
	_, _, _ = store.Enqueue(context.Background(), input)
	fx := newFixture(store, now)
	fx.broadcast.err = context.DeadlineExceeded
	job, err := fx.service(t).RunOnce(context.Background())
	if !errors.Is(err, ErrBroadcastAmbiguous) || job.State != StateAmbiguous {
		t.Fatalf("RunOnce = %+v, %v", job, err)
	}
	fx.broadcast.err = nil
	if _, err := fx.service(t).RunOnce(context.Background()); !errors.Is(err, ErrNoWork) {
		t.Fatalf("ambiguous attempt was claimable: %v", err)
	}
	if fx.broadcast.calls != 1 || fx.wallet.calls != 1 {
		t.Fatal("ambiguous attempt was blindly rebuilt or rebroadcast")
	}
	fx.outcomes.state = StateSubmitted
	recovered, err := fx.service(t).ObserveOnce(context.Background())
	if err != nil || recovered.State != StateSubmitted {
		t.Fatalf("recovered=%+v err=%v", recovered, err)
	}
	if fx.broadcast.calls != 1 || fx.wallet.calls != 1 {
		t.Fatal("evidence recovery retransmitted the transaction")
	}
}

func TestRestartRebroadcastsExactSealedTransactionWithoutNewNonce(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	store := newMemoryStore(now)
	input := signedInput(now)
	_, _, _ = store.Enqueue(context.Background(), input)
	lease, _ := store.Claim(context.Background(), input.KeeperID, input.GasPayer, input.ChainID, 20*time.Second)
	nonce, _ := store.AllocateNonce(context.Background(), lease, 17)
	unsigned := UnsignedTransaction{ChainID: input.ChainID, From: input.GasPayer, To: input.Target, ValueWei: "0",
		Nonce: nonce, GasLimit: 100000, Data: []byte{1, 2, 3, 4}, Fee: Fee{"100", "2"}}
	signed := signTestTransaction(t, unsigned)
	raw := signed.Raw
	attempt := Attempt{JobID: input.JobID, Number: 1, Nonce: nonce, GasPayer: input.GasPayer,
		Fee: Fee{"100", "2"}, TransactionHash: signed.Hash, SealedRawTransaction: append([]byte("sealed:"), raw...),
		SealingKeyID: "test-seal-key", State: AttemptPrepared, PreparedAt: now}
	_, _ = store.RecordPrepared(context.Background(), lease, attempt)
	_ = store.ReleaseLease(context.Background(), lease)
	fx := newFixture(store, now)
	fx.broadcast.hash = attempt.TransactionHash
	job, err := fx.service(t).RunOnce(context.Background())
	if err != nil || job.State != StateSubmitted || !bytes.Equal(fx.broadcast.raw, raw) {
		t.Fatalf("RunOnce=%+v err=%v raw=%q", job, err, fx.broadcast.raw)
	}
	if fx.nonces.calls != 0 || fx.artifacts.calls != 0 || fx.wallet.calls != 0 {
		t.Fatal("restart allocated a new nonce, released bearer, or signed again")
	}
}

func TestUnderpricedAttemptUsesProvedSameNonceFeeBump(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	store := newMemoryStore(now)
	input := signedInput(now)
	_, _, _ = store.Enqueue(context.Background(), input)
	fx := newFixture(store, now)
	fx.broadcast.err = ErrBroadcastUnderpriced
	first, err := fx.service(t).RunOnce(context.Background())
	if !errors.Is(err, ErrBroadcastUnderpriced) || first.State != StateTimedOut {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	fx.broadcast.err = nil
	fx.broadcast.hash = ""
	second, err := fx.service(t).RunOnce(context.Background())
	if err != nil || second.State != StateSubmitted {
		t.Fatalf("second=%+v err=%v", second, err)
	}
	attempts := store.attempts[input.JobID]
	if len(attempts) != 2 || attempts[0].State != AttemptReplaced || attempts[1].Nonce != attempts[0].Nonce ||
		attempts[1].Fee.MaxFeePerGasWei != "120" || fx.replacements.calls != 1 || fx.nonces.calls != 1 {
		t.Fatalf("attempts=%+v replacementChecks=%d nonceCalls=%d", attempts, fx.replacements.calls, fx.nonces.calls)
	}
}

func TestObserveOnceAdvancesOnlyExactIndependentEvidence(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	store := newMemoryStore(now)
	input := signedInput(now)
	_, _, _ = store.Enqueue(context.Background(), input)
	fx := newFixture(store, now)
	if _, err := fx.service(t).RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	fx.outcomes.state = StateConfirmed
	confirmed, err := fx.service(t).ObserveOnce(context.Background())
	if err != nil || confirmed.State != StateConfirmed {
		t.Fatalf("confirmed=%+v err=%v", confirmed, err)
	}
	fx.outcomes.state = StateFinalized
	finalized, err := fx.service(t).ObserveOnce(context.Background())
	if err != nil || finalized.State != StateFinalized {
		t.Fatalf("finalized=%+v err=%v", finalized, err)
	}
	attempt := store.attempts[input.JobID][0]
	if attempt.State != AttemptFinalized || attempt.EvidenceDigest != testHash(70) {
		t.Fatalf("attempt=%+v", attempt)
	}
}

func TestObserveOnceRejectsCrossTransactionEvidence(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	store := newMemoryStore(now)
	input := signedInput(now)
	_, _, _ = store.Enqueue(context.Background(), input)
	fx := newFixture(store, now)
	_, _ = fx.service(t).RunOnce(context.Background())
	fx.outcomes.state, fx.outcomes.overrideHash = StateFinalized, testHash(999)
	if _, err := fx.service(t).ObserveOnce(context.Background()); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("error=%v", err)
	}
	if store.jobs[input.JobID].State != StateSubmitted {
		t.Fatal("substituted evidence changed keeper state")
	}
}

func TestReplacementFailsClosedWithoutQuorumSafety(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	store := newMemoryStore(now)
	input := signedInput(now)
	_, _, _ = store.Enqueue(context.Background(), input)
	fx := newFixture(store, now)
	fx.broadcast.err = ErrBroadcastUnderpriced
	_, _ = fx.service(t).RunOnce(context.Background())
	fx.replacements.err = errors.New("providers disagree")
	if _, err := fx.service(t).RunOnce(context.Background()); !errors.Is(err, ErrUnsafeReplacement) {
		t.Fatalf("error=%v", err)
	}
	if fx.wallet.calls != 1 || fx.broadcast.calls != 1 {
		t.Fatal("unsafe replacement reached signing or broadcast")
	}
}

func TestEnqueueIsExactReplayAndConcurrentClaimIsExclusive(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	store := newMemoryStore(now)
	input := signedInput(now)
	if _, replay, err := store.Enqueue(context.Background(), input); err != nil || replay {
		t.Fatal(err)
	}
	if _, replay, err := store.Enqueue(context.Background(), input); err != nil || !replay {
		t.Fatalf("exact replay=%t err=%v", replay, err)
	}
	changed := input
	changed.Target = "0x4444444444444444444444444444444444444444"
	if _, _, err := store.Enqueue(context.Background(), changed); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("substitution error=%v", err)
	}
	var success atomic.Int32
	var group sync.WaitGroup
	for range 20 {
		group.Add(1)
		go func() {
			defer group.Done()
			if _, err := store.Claim(context.Background(), input.KeeperID, input.GasPayer, input.ChainID, 20*time.Second); err == nil {
				success.Add(1)
			}
		}()
	}
	group.Wait()
	if success.Load() != 1 {
		t.Fatalf("successful concurrent claims=%d", success.Load())
	}
}

func TestNonceReservationSurvivesCrashBeforePreparedAttempt(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	store := newMemoryStore(now)
	input := signedInput(now)
	_, _, _ = store.Enqueue(context.Background(), input)
	lease, _ := store.Claim(context.Background(), input.KeeperID, input.GasPayer, input.ChainID, 20*time.Second)
	first, err := store.AllocateNonce(context.Background(), lease, 17)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.AllocateNonce(context.Background(), lease, 99)
	if err != nil || first != 17 || second != first {
		t.Fatalf("nonce reservation first=%d second=%d err=%v", first, second, err)
	}
}

type memoryStore struct {
	mu        sync.Mutex
	now       time.Time
	jobs      map[string]Job
	attempts  map[string][]Attempt
	reserved  map[string]uint64
	nextNonce uint64
}

func newMemoryStore(now time.Time) *memoryStore {
	return &memoryStore{now: now, jobs: map[string]Job{}, attempts: map[string][]Attempt{}, reserved: map[string]uint64{}}
}

func (s *memoryStore) Enqueue(_ context.Context, input EnqueueInput) (Job, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := validateInput(input, s.now); err != nil {
		return Job{}, false, err
	}
	if existing, ok := s.jobs[input.JobID]; ok {
		if !sameInput(existing, input) {
			return Job{}, false, ErrStateConflict
		}
		return existing, true, nil
	}
	job := Job{JobID: input.JobID, OperationID: input.OperationID, OrganizationID: input.OrganizationID,
		Action: input.Action, ChainID: input.ChainID, KeeperID: input.KeeperID, GasPayer: input.GasPayer,
		Target: input.Target, ValueWei: input.ValueWei, CanonicalPayload: append([]byte(nil), input.CanonicalPayload...),
		CanonicalPayloadHash: input.CanonicalPayloadHash, AuthorizationDigest: input.AuthorizationDigest,
		SignerHandle: input.SignerHandle, SignerAddress: input.SignerAddress, ValidAfter: input.ValidAfter,
		ValidBefore: input.ValidBefore, EligibleAfter: input.EligibleAfter,
		EligibilityEvidenceDigest: input.EligibilityEvidenceDigest, EligibilityObservedAt: input.EligibilityObservedAt, LeadershipEpoch: input.LeadershipEpoch,
		State: StateQueued, CreatedAt: s.now, UpdatedAt: s.now}
	s.jobs[input.JobID] = job
	return job, false, nil
}

func (s *memoryStore) Claim(_ context.Context, keeper, gasPayer string, chainID uint64, duration time.Duration) (Lease, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, job := range s.jobs {
		if job.KeeperID != keeper || job.GasPayer != gasPayer || job.ChainID != chainID || s.now.Before(job.EligibleAfter) || (!job.LeaseExpiresAt.IsZero() && s.now.Before(job.LeaseExpiresAt)) ||
			(job.State != StateQueued && job.State != StatePrepared && job.State != StateBroadcasting && job.State != StateTimedOut && job.State != StateReorged) {
			continue
		}
		token := fmt.Sprintf("lease_%048d", len(id)+job.AttemptCount)
		job.LeaseOwner, job.LeaseToken, job.LeaseExpiresAt = keeper, token, s.now.Add(duration)
		s.jobs[id] = job
		return Lease{Job: job, Token: token}, nil
	}
	return Lease{}, ErrNoWork
}

func (s *memoryStore) ClaimObservation(_ context.Context, keeper, gasPayer string, chainID uint64, duration time.Duration) (Lease, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, job := range s.jobs {
		if job.KeeperID != keeper || job.GasPayer != gasPayer || job.ChainID != chainID || (job.State != StateAmbiguous && job.State != StateSubmitted && job.State != StateConfirmed) ||
			(!job.LeaseExpiresAt.IsZero() && s.now.Before(job.LeaseExpiresAt)) {
			continue
		}
		token := fmt.Sprintf("observe_lease_%048d", len(id)+job.AttemptCount)
		job.LeaseOwner, job.LeaseToken, job.LeaseExpiresAt = keeper, token, s.now.Add(duration)
		s.jobs[id] = job
		return Lease{Job: job, Token: token}, nil
	}
	return Lease{}, ErrNoWork
}

func (s *memoryStore) AllocateNonce(_ context.Context, lease Lease, observed uint64) (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, err := s.leased(lease)
	if err != nil {
		return 0, err
	}
	if job.State != StateQueued || job.AttemptCount != 0 {
		return 0, ErrStateConflict
	}
	if nonce, ok := s.reserved[job.JobID]; ok {
		return nonce, nil
	}
	if observed > s.nextNonce {
		s.nextNonce = observed
	}
	nonce := s.nextNonce
	s.nextNonce++
	s.reserved[job.JobID] = nonce
	return nonce, nil
}

func (s *memoryStore) RecordPrepared(_ context.Context, lease Lease, attempt Attempt) (Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, err := s.leased(lease)
	if err != nil {
		return Job{}, err
	}
	if job.State != StateQueued || attempt.Number != 1 {
		return Job{}, ErrStateConflict
	}
	job.State, job.AttemptCount, job.CurrentAttempt = StatePrepared, 1, 1
	s.attempts[job.JobID] = []Attempt{cloneAttempt(attempt)}
	s.jobs[job.JobID] = job
	return job, nil
}

func (s *memoryStore) RecordReplacement(_ context.Context, lease Lease, previous, attempt Attempt) (Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, err := s.leased(lease)
	if err != nil {
		return Job{}, err
	}
	if (job.State != StateTimedOut && job.State != StateReorged) || attempt.Number != previous.Number+1 {
		return Job{}, ErrStateConflict
	}
	items := s.attempts[job.JobID]
	items[len(items)-1].State = AttemptReplaced
	items = append(items, cloneAttempt(attempt))
	s.attempts[job.JobID] = items
	job.State, job.AttemptCount, job.CurrentAttempt = StatePrepared, attempt.Number, attempt.Number
	s.jobs[job.JobID] = job
	return job, nil
}

func (s *memoryStore) MarkBroadcasting(_ context.Context, lease Lease, number int) (Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, err := s.leased(lease)
	if err != nil {
		return Job{}, err
	}
	if (job.State != StatePrepared && job.State != StateBroadcasting) || job.CurrentAttempt != number {
		return Job{}, ErrStateConflict
	}
	job.State = StateBroadcasting
	items := s.attempts[job.JobID]
	items[len(items)-1].State = AttemptBroadcasting
	items[len(items)-1].BroadcastAt = s.now
	s.attempts[job.JobID], s.jobs[job.JobID] = items, job
	return job, nil
}

func (s *memoryStore) MarkSubmitted(_ context.Context, lease Lease, number int, txHash string) (Job, error) {
	return s.finish(lease, number, StateSubmitted, AttemptSubmitted, txHash, "")
}
func (s *memoryStore) MarkAmbiguous(_ context.Context, lease Lease, number int, reason string) (Job, error) {
	return s.finish(lease, number, StateAmbiguous, AttemptAmbiguous, "", reason)
}
func (s *memoryStore) MarkRejected(_ context.Context, lease Lease, number int, target State, reason string) (Job, error) {
	return s.finish(lease, number, target, AttemptRejected, "", reason)
}
func (s *memoryStore) finish(lease Lease, number int, target State, attemptState AttemptState, hash, reason string) (Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, err := s.leased(lease)
	if err != nil {
		return Job{}, err
	}
	if job.State != StateBroadcasting || job.CurrentAttempt != number {
		return Job{}, ErrStateConflict
	}
	items := s.attempts[job.JobID]
	current := &items[len(items)-1]
	if hash != "" && current.TransactionHash != hash {
		return Job{}, ErrStateConflict
	}
	current.State, current.LastError = attemptState, reason
	job.State, job.LastError = target, reason
	s.attempts[job.JobID], s.jobs[job.JobID] = items, job
	return job, nil
}
func (s *memoryStore) MarkRecoveryDeadLetter(_ context.Context, lease Lease, reason string) (Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, err := s.leased(lease)
	if err != nil {
		return Job{}, err
	}
	job.State, job.LastError = StateDeadLetter, reason
	s.jobs[job.JobID] = job
	return job, nil
}
func (s *memoryStore) ApplyOutcome(_ context.Context, lease Lease, outcome Outcome) (Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, err := s.leased(lease)
	if err != nil {
		return Job{}, err
	}
	items := s.attempts[job.JobID]
	if len(items) == 0 {
		return Job{}, ErrNotFound
	}
	attempt := &items[len(items)-1]
	if err := validateOutcome(job, *attempt, outcome, s.now); err != nil {
		return Job{}, err
	}
	switch outcome.State {
	case StateSubmitted:
		attempt.State = AttemptSubmitted
	case StateConfirmed:
		attempt.State = AttemptConfirmed
	case StateFinalized:
		attempt.State = AttemptFinalized
	case StateReverted:
		attempt.State = AttemptReverted
	case StateReorged:
		attempt.State = AttemptReorged
	case StateTimedOut:
		attempt.State = AttemptRejected
	default:
		return Job{}, ErrStateConflict
	}
	attempt.EvidenceDigest, attempt.ObservedAt = outcome.EvidenceDigest, outcome.ObservedAt
	job.State = outcome.State
	s.attempts[job.JobID], s.jobs[job.JobID] = items, job
	return job, nil
}
func (s *memoryStore) CurrentAttempt(_ context.Context, jobID string) (Attempt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	items := s.attempts[jobID]
	if len(items) == 0 {
		return Attempt{}, ErrNotFound
	}
	return cloneAttempt(items[len(items)-1]), nil
}
func (s *memoryStore) ReleaseLease(_ context.Context, lease Lease) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.jobs[lease.Job.JobID]
	if !ok || job.LeaseToken != lease.Token {
		return ErrLeaseLost
	}
	job.LeaseOwner, job.LeaseToken, job.LeaseExpiresAt = "", "", time.Time{}
	s.jobs[job.JobID] = job
	return nil
}
func (s *memoryStore) leased(lease Lease) (Job, error) {
	job, ok := s.jobs[lease.Job.JobID]
	if !ok || job.LeaseToken != lease.Token || !s.now.Before(job.LeaseExpiresAt) {
		return Job{}, ErrLeaseLost
	}
	return job, nil
}
func cloneAttempt(input Attempt) Attempt {
	input.SealedRawTransaction = append([]byte(nil), input.SealedRawTransaction...)
	return input
}

type fixture struct {
	store        *memoryStore
	now          time.Time
	artifacts    *artifactFixture
	assembler    *assemblerFixture
	verifier     *verifierFixture
	wallet       *walletFixture
	sealer       *sealerFixture
	broadcast    *broadcastFixture
	fees         *feeFixture
	nonces       *nonceFixture
	replacements *replacementFixture
	outcomes     *outcomeFixture
	leadership   *leadershipFixture
}

func newFixture(store *memoryStore, now time.Time) *fixture {
	return &fixture{store: store, now: now, artifacts: &artifactFixture{artifact: bytes.Repeat([]byte{0x55}, 65)},
		assembler: &assemblerFixture{}, verifier: &verifierFixture{}, wallet: &walletFixture{},
		sealer: &sealerFixture{}, broadcast: &broadcastFixture{}, fees: &feeFixture{}, nonces: &nonceFixture{nonce: 17},
		replacements: &replacementFixture{}, outcomes: &outcomeFixture{state: StateConfirmed, now: now}, leadership: &leadershipFixture{epoch: 7}}
}
func (f *fixture) service(t *testing.T) *Service {
	t.Helper()
	service, err := NewService(f.store, f.artifacts, f.assembler, f.verifier, f.wallet, f.sealer, f.broadcast, f.fees, f.nonces, f.replacements, f.outcomes, f.leadership, Config{KeeperID: "keeper-primary", GasPayer: testGasPayer(), ChainID: 84532, LeaseDuration: 20 * time.Second, MaxFeeBumps: 3, MaxGasLimit: 200000, FeeCap: Fee{"1000", "100"}, Clock: func() time.Time { return f.now }})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

type artifactFixture struct {
	calls    int
	keeper   string
	artifact []byte
}

func (f *artifactFixture) Release(_ context.Context, _ string, keeper string) ([]byte, error) {
	f.calls++
	f.keeper = keeper
	return append([]byte(nil), f.artifact...), nil
}

type assemblerFixture struct {
	mutateTarget bool
	gasLimit     uint64
}

func (f *assemblerFixture) Assemble(_ context.Context, j Job, artifact []byte, nonce uint64, fee Fee) (UnsignedTransaction, error) {
	to := j.Target
	if f.mutateTarget {
		to = "0x4444444444444444444444444444444444444444"
	}
	data := append([]byte{1, 2, 3, 4}, artifact...)
	gas := f.gasLimit
	if gas == 0 {
		gas = 100000
	}
	return UnsignedTransaction{ChainID: j.ChainID, From: j.GasPayer, To: to, ValueWei: j.ValueWei, Nonce: nonce, GasLimit: gas, Data: data, Fee: fee}, nil
}

type verifierFixture struct{ err error }

func (f *verifierFixture) Verify(context.Context, Job, UnsignedTransaction, []byte) error {
	return f.err
}

type walletFixture struct {
	calls      int
	mutateData bool
}

func (f *walletFixture) Sign(_ context.Context, unsigned UnsignedTransaction) (SignedTransaction, error) {
	f.calls++
	if f.mutateData {
		unsigned.Data = append([]byte(nil), unsigned.Data...)
		unsigned.Data[len(unsigned.Data)-1] ^= 0xff
	}
	return signTestTransaction(nil, unsigned), nil
}

type sealerFixture struct{}

func (*sealerFixture) Seal(_ context.Context, raw, aad []byte) ([]byte, string, error) {
	return append([]byte("sealed:"), raw...), "test-seal-key", nil
}
func (*sealerFixture) Open(_ context.Context, sealed []byte, _ string, _ []byte) ([]byte, error) {
	return append([]byte(nil), sealed[len("sealed:"):]...), nil
}

type broadcastFixture struct {
	calls   int
	hash    string
	err     error
	raw     []byte
	inspect func()
}

func (f *broadcastFixture) Broadcast(_ context.Context, raw []byte) (string, error) {
	f.calls++
	f.raw = append([]byte(nil), raw...)
	if f.inspect != nil {
		f.inspect()
	}
	hash := f.hash
	if hash == "" {
		hash = crypto.Keccak256Hash(raw).Hex()
	}
	return hash, f.err
}

type feeFixture struct{ initial Fee }

func (f *feeFixture) Initial(context.Context, Job) (Fee, error) {
	if f.initial.MaxFeePerGasWei != "" {
		return f.initial, nil
	}
	return Fee{"100", "2"}, nil
}
func (*feeFixture) Bump(_ context.Context, _ Job, previous Attempt) (Fee, error) {
	return Fee{"120", previous.Fee.MaxPriorityFeePerGasWei}, nil
}

type nonceFixture struct {
	calls int
	nonce uint64
}

func (f *nonceFixture) PendingNonce(context.Context, uint64, string) (uint64, error) {
	f.calls++
	return f.nonce, nil
}

type replacementFixture struct {
	calls int
	err   error
}

type outcomeFixture struct {
	state        State
	now          time.Time
	overrideHash string
}

func (f *outcomeFixture) Observe(_ context.Context, job Job, attempt Attempt) (Outcome, error) {
	hash := attempt.TransactionHash
	if f.overrideHash != "" {
		hash = f.overrideHash
	}
	return Outcome{JobID: job.JobID, AttemptNumber: attempt.Number, TransactionHash: hash, State: f.state, EvidenceDigest: testHash(70), ObservedAt: f.now}, nil
}

func (f *replacementFixture) SafeToReplace(context.Context, Job, Attempt) error {
	f.calls++
	return f.err
}

type leadershipFixture struct {
	calls int
	epoch uint64
}

func (f *leadershipFixture) Current(context.Context, string) (uint64, error) {
	f.calls++
	return f.epoch, nil
}

func signedInput(now time.Time) EnqueueInput {
	payload := []byte(`{"action":"lock","operationId":"exact"}`)
	return EnqueueInput{JobID: testHash(1), OperationID: testHash(2), OrganizationID: "org-test", Action: ActionLock, ChainID: 84532,
		KeeperID: "keeper-primary", GasPayer: testGasPayer(), Target: "0x2222222222222222222222222222222222222222", ValueWei: "0",
		CanonicalPayload: payload, CanonicalPayloadHash: canonicalPayloadHash(payload), AuthorizationDigest: testHash(3), SignerHandle: "signer_handle_1234567890", SignerAddress: "0x3333333333333333333333333333333333333333",
		ValidAfter: now.Add(-time.Minute), ValidBefore: now.Add(8 * time.Minute), EligibleAfter: now.Add(-time.Minute), LeadershipEpoch: 7}
}
func claimInput(now time.Time) EnqueueInput {
	input := signedInput(now)
	input.JobID = testHash(4)
	input.OperationID = testHash(5)
	input.Action = ActionClaimExpired
	input.AuthorizationDigest = ""
	input.SignerHandle = ""
	input.SignerAddress = ""
	input.ValidAfter = time.Time{}
	input.ValidBefore = time.Time{}
	input.LeadershipEpoch = 0
	input.EligibilityEvidenceDigest = testHash(6)
	input.EligibilityObservedAt = now
	return input
}
func testHash(value uint64) string { return fmt.Sprintf("0x%064x", value) }

func testGasPayer() string {
	key, _ := crypto.HexToECDSA("1111111111111111111111111111111111111111111111111111111111111111")
	return strings.ToLower(crypto.PubkeyToAddress(key.PublicKey).Hex())
}

func signTestTransaction(t *testing.T, unsigned UnsignedTransaction) SignedTransaction {
	key, err := crypto.HexToECDSA("1111111111111111111111111111111111111111111111111111111111111111")
	if err != nil {
		if t != nil {
			t.Fatal(err)
		}
		panic(err)
	}
	fee, _ := new(big.Int).SetString(unsigned.Fee.MaxFeePerGasWei, 10)
	tip, _ := new(big.Int).SetString(unsigned.Fee.MaxPriorityFeePerGasWei, 10)
	value, _ := new(big.Int).SetString(unsigned.ValueWei, 10)
	tx := types.NewTx(&types.DynamicFeeTx{ChainID: new(big.Int).SetUint64(unsigned.ChainID), Nonce: unsigned.Nonce,
		GasTipCap: tip, GasFeeCap: fee, Gas: unsigned.GasLimit, To: ptrAddress(common.HexToAddress(unsigned.To)),
		Value: value, Data: append([]byte(nil), unsigned.Data...)})
	signed, err := types.SignTx(tx, types.LatestSignerForChainID(new(big.Int).SetUint64(unsigned.ChainID)), key)
	if err != nil {
		if t != nil {
			t.Fatal(err)
		}
		panic(err)
	}
	raw, err := signed.MarshalBinary()
	if err != nil {
		if t != nil {
			t.Fatal(err)
		}
		panic(err)
	}
	return SignedTransaction{Hash: signed.Hash().Hex(), Raw: raw}
}

func ptrAddress(value common.Address) *common.Address { return &value }
