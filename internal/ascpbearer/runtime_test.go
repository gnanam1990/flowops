package ascpbearer

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type runtimeMemoryStore struct {
	mu          sync.Mutex
	request     ActivationRequest
	entry       RegistryEntry
	claimed     bool
	retries     int
	completions int
	expirations int
	proof       UnactivatedProof
}

func (s *runtimeMemoryStore) Get(context.Context, string) (ActivationRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.request, nil
}

func (s *runtimeMemoryStore) RecordPrepared(_ context.Context, _ string, handle string) (ActivationRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.request.PreparedHandle, s.request.State = handle, HandlePrepared
	return s.request, nil
}

func (s *runtimeMemoryStore) Activate(context.Context, string) (RegistryEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.request.State, s.request.ActivatedAt = ActivePendingMirror, s.request.CreatedAt
	s.entry = RegistryEntry{Digest: s.request.Digest}
	return s.entry, nil
}

func (s *runtimeMemoryStore) Registry(context.Context, string) (RegistryEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.entry, nil
}

func (s *runtimeMemoryStore) MarkPrimaryMirrored(_ context.Context, _ string, digest string) (ActivationRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.request.State, s.request.PrimaryMirrorDigest = ActiveMirrored, digest
	return s.request, nil
}

func (s *runtimeMemoryStore) MarkAcknowledged(context.Context, string, string) (ActivationRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.request.State = ActivationAcknowledged
	return s.request, nil
}

func (s *runtimeMemoryStore) Claim(_ context.Context, claim RuntimeClaim) (RuntimeLease, bool, error) {
	return s.claim(claim, false)
}

func (s *runtimeMemoryStore) ClaimExpired(_ context.Context, claim RuntimeClaim) (RuntimeLease, bool, error) {
	return s.claim(claim, true)
}

func (s *runtimeMemoryStore) claim(claim RuntimeClaim, expired bool) (RuntimeLease, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	isExpired := !time.Now().Before(s.request.ValidUntil)
	if s.claimed || expired != isExpired || s.request.State == ActivationAcknowledged || s.request.State == ExpiredUnactivated {
		return RuntimeLease{}, false, nil
	}
	s.claimed = true
	return RuntimeLease{Request: s.request, WorkerID: claim.WorkerID, Token: bearerHash(90), ExpiresAt: time.Now().Add(claim.LeaseDuration)}, true, nil
}

func (s *runtimeMemoryStore) CompleteLease(context.Context, RuntimeLease) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.claimed = false
	s.completions++
	return nil
}

func (s *runtimeMemoryStore) RetryLease(context.Context, RuntimeLease, string, time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.claimed = false
	s.retries++
	return nil
}

func (s *runtimeMemoryStore) ExpireUnactivated(_ context.Context, _ RuntimeLease, proof UnactivatedProof) (ActivationRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := validateUnactivatedProof(s.request, proof, time.Now().UTC()); err != nil {
		return ActivationRequest{}, err
	}
	s.request.State = ExpiredUnactivated
	s.claimed = false
	s.expirations++
	s.proof = proof
	return s.request, nil
}

type runtimeTestSigner struct {
	prepareErr error
	proof      UnactivatedProof
}

type slowExpiryStore struct{ *runtimeMemoryStore }

func (s *slowExpiryStore) ClaimExpired(ctx context.Context, _ RuntimeClaim) (RuntimeLease, bool, error) {
	<-ctx.Done()
	return RuntimeLease{}, false, ctx.Err()
}

func (s *runtimeTestSigner) Prepare(context.Context, ActivationInput) (string, error) {
	return "opaque-runtime-handle-0123456789abcdef", s.prepareErr
}

func (*runtimeTestSigner) AcknowledgeActivation(context.Context, ActivationProof) error { return nil }

func (s *runtimeTestSigner) ProveUnactivated(context.Context, ActivationRequest) (UnactivatedProof, error) {
	return s.proof, nil
}

func runtimeServiceForTest(t *testing.T, store runtimeRepository, signer RuntimeSigner) *RuntimeService {
	t.Helper()
	service, err := NewRuntimeService(store, signer, &coordinatorMirror{}, RuntimeConfig{
		Claim: RuntimeClaim{
			WorkerID: "bearer-worker-1", SignerKeyID: "signer-key-1", KeyEpoch: 1,
			KeeperID: "keeper-primary", LeaseDuration: 10 * time.Second,
		},
		RetryDelay: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func runtimeRequest(now time.Time) ActivationRequest {
	return ActivationRequest{ActivationInput: ActivationInput{
		RequestID: bearerHash(70), AuthorizationID: bearerHash(71), OperationID: bearerHash(72),
		ReservationID: bearerHash(73), ActionID: "runtime-action-1", Digest: bearerHash(74),
		SignerKeyID: "signer-key-1", KeyEpoch: 1, KeeperID: "keeper-primary",
		ValidAfter: now.Add(-time.Minute), ValidUntil: now.Add(time.Minute),
	}, InputHash: bearerHash(75), State: SignRequested, CreatedAt: now.Add(-time.Minute)}
}

func TestRuntimeServiceContainsRetryableBoundaryFailure(t *testing.T) {
	now := time.Now().UTC()
	store := &runtimeMemoryStore{request: runtimeRequest(now)}
	signer := &runtimeTestSigner{prepareErr: errors.Join(ErrRuntimeBoundary, context.DeadlineExceeded)}
	step, ok, err := runtimeServiceForTest(t, store, signer).AdvanceOnce(context.Background())
	if err != nil || !ok || !step.Retried || store.retries != 1 || store.completions != 0 || store.request.State != SignRequested {
		t.Fatalf("step=%+v ok=%t retries=%d completions=%d state=%s err=%v", step, ok, store.retries, store.completions, store.request.State, err)
	}
}

func TestRuntimeServiceExpiresOnlyWithExactSignerProof(t *testing.T) {
	now := time.Now().UTC()
	request := runtimeRequest(now)
	request.ValidUntil = now.Add(-time.Second)
	proof := UnactivatedProof{
		RequestID: request.RequestID, ActionID: request.ActionID, InputHash: request.InputHash,
		Status: "EXPIRED_UNACTIVATED", ProvenAt: now,
	}
	proof.ProofDigest, _ = UnactivatedProofDigest(proof)
	store := &runtimeMemoryStore{request: request}
	step, ok, err := runtimeServiceForTest(t, store, &runtimeTestSigner{proof: proof}).ExpireOnce(context.Background())
	if err != nil || !ok || !step.Expired || store.expirations != 1 || store.request.State != ExpiredUnactivated {
		t.Fatalf("step=%+v ok=%t expirations=%d state=%s err=%v", step, ok, store.expirations, store.request.State, err)
	}

	badStore := &runtimeMemoryStore{request: request}
	badProof := proof
	badProof.ActionID = "attacker-action"
	badProof.ProofDigest, _ = UnactivatedProofDigest(badProof)
	_, _, err = runtimeServiceForTest(t, badStore, &runtimeTestSigner{proof: badProof}).ExpireOnce(context.Background())
	if !errors.Is(err, ErrActivationBinding) || badStore.expirations != 0 || badStore.request.State != SignRequested {
		t.Fatalf("mismatched proof mutated state: expirations=%d state=%s err=%v", badStore.expirations, badStore.request.State, err)
	}
}

func TestRuntimeWorkerReservesCapacityForExpiryAndAdvancement(t *testing.T) {
	now := time.Now().UTC()
	store := &runtimeMemoryStore{request: runtimeRequest(now)}
	worker, err := NewRuntimeWorker(runtimeServiceForTest(t, store, &runtimeTestSigner{}), RuntimeWorkerConfig{
		Interval: 3 * time.Second, CycleTimeout: 2 * time.Second, ExpiryPhaseTimeout: time.Second,
		ExpiryBatchSize: 1, AdvanceBatchSize: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	cycle, err := worker.RunOnce(context.Background())
	if err != nil || cycle.Prepared != 1 || cycle.Advanced != 1 || store.request.State != HandlePrepared {
		t.Fatalf("cycle=%+v state=%s err=%v", cycle, store.request.State, err)
	}
}

func TestRuntimeServiceRecoversWholeActivationLifecycleAcrossClaims(t *testing.T) {
	now := time.Now().UTC()
	store := &runtimeMemoryStore{request: runtimeRequest(now)}
	service := runtimeServiceForTest(t, store, &runtimeTestSigner{})
	want := []ActivationState{HandlePrepared, ActivePendingMirror, ActiveMirrored, ActivationAcknowledged}
	for _, expected := range want {
		step, ok, err := service.AdvanceOnce(context.Background())
		if err != nil || !ok || step.State != expected || store.request.State != expected {
			t.Fatalf("want=%s step=%+v ok=%t state=%s err=%v", expected, step, ok, store.request.State, err)
		}
	}
	if store.completions != len(want) || store.retries != 0 {
		t.Fatalf("completions=%d retries=%d", store.completions, store.retries)
	}
	if _, ok, err := service.AdvanceOnce(context.Background()); err != nil || ok {
		t.Fatalf("terminal request was reclaimed: ok=%t err=%v", ok, err)
	}
}

func TestRuntimeWorkerExpiryPhaseCannotStarveActivation(t *testing.T) {
	now := time.Now().UTC()
	store := &slowExpiryStore{runtimeMemoryStore: &runtimeMemoryStore{request: runtimeRequest(now)}}
	worker, err := NewRuntimeWorker(runtimeServiceForTest(t, store, &runtimeTestSigner{}), RuntimeWorkerConfig{
		Interval: 3 * time.Second, CycleTimeout: 2 * time.Second, ExpiryPhaseTimeout: time.Second,
		ExpiryBatchSize: 100, AdvanceBatchSize: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	cycle, err := worker.RunOnce(context.Background())
	if err != nil || cycle.Advanced != 1 || cycle.Prepared != 1 || store.request.State != HandlePrepared {
		t.Fatalf("cycle=%+v state=%s err=%v", cycle, store.request.State, err)
	}
	if elapsed := time.Since(started); elapsed < 900*time.Millisecond || elapsed >= 2500*time.Millisecond {
		t.Fatalf("expiry phase did not preserve the configured activation window: %s", elapsed)
	}
}
