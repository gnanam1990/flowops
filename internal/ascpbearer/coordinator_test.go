package ascpbearer

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type coordinatorStore struct {
	request ActivationRequest
	entry   RegistryEntry
}

func (s *coordinatorStore) Get(context.Context, string) (ActivationRequest, error) {
	return s.request, nil
}
func (s *coordinatorStore) RecordPrepared(_ context.Context, _ string, handle string) (ActivationRequest, error) {
	s.request.PreparedHandle, s.request.State, s.request.PreparedAt = handle, HandlePrepared, s.request.CreatedAt
	return s.request, nil
}
func (s *coordinatorStore) Activate(context.Context, string) (RegistryEntry, error) {
	s.request.State, s.request.ActivatedAt = ActivePendingMirror, s.request.CreatedAt
	s.entry = RegistryEntry{
		Digest: s.request.Digest, InstrumentType: s.request.InstrumentType, SignatureRef: s.request.PreparedHandle,
		Nonce: s.request.Nonce, IssuedAt: s.request.ValidAfter, ValidUntil: s.request.ValidUntil,
		SignerKeyID: s.request.SignerKeyID, KeyEpoch: s.request.KeyEpoch, OperationID: s.request.OperationID,
		AuthorizationID: s.request.AuthorizationID, ReservationID: s.request.ReservationID,
		ModuleAddress: s.request.ModuleAddress, SafeAddress: s.request.SafeAddress, Outcome: "LIVE", CreatedAt: s.request.CreatedAt,
	}
	return s.entry, nil
}
func (s *coordinatorStore) Registry(context.Context, string) (RegistryEntry, error) {
	return s.entry, nil
}
func (s *coordinatorStore) MarkPrimaryMirrored(_ context.Context, _ string, digest string) (ActivationRequest, error) {
	s.request.State, s.request.PrimaryMirrorDigest = ActiveMirrored, digest
	return s.request, nil
}
func (s *coordinatorStore) MarkAcknowledged(context.Context, string, string) (ActivationRequest, error) {
	s.request.State = ActivationAcknowledged
	return s.request, nil
}

type coordinatorSigner struct {
	prepareErr error
	ackErr     error
	prepares   int
	acks       int
	proof      ActivationProof
}

func (s *coordinatorSigner) Prepare(context.Context, ActivationInput) (string, error) {
	s.prepares++
	return "opaque-prepared-handle-0123456789abcdef", s.prepareErr
}
func (s *coordinatorSigner) AcknowledgeActivation(_ context.Context, proof ActivationProof) error {
	s.acks++
	s.proof = proof
	return s.ackErr
}

type coordinatorMirror struct {
	err    error
	puts   int
	key    string
	digest string
	bytes  []byte
}

func (m *coordinatorMirror) PutPrimary(_ context.Context, key string, value []byte, digest string) error {
	m.puts++
	m.key, m.digest, m.bytes = key, digest, append([]byte(nil), value...)
	return m.err
}

func TestCoordinatorRecoversEveryPrepareActivateMirrorAcknowledgeBoundary(t *testing.T) {
	now := time.Unix(1800000000, 0).UTC()
	payload := []byte(`{"safe":"0x2222222222222222222222222222222222222222","amount":"10"}`)
	evidence := []byte(`{"directoryVersion":9,"policyVersion":3,"chainHead":"0xabc"}`)
	store := &coordinatorStore{request: ActivationRequest{ActivationInput: ActivationInput{
		RequestID: bearerHash(20), AuthorizationID: bearerHash(21), OperationID: bearerHash(22),
		ReservationID: bearerHash(23), ActionID: "lock-action-23", CanonicalPayload: payload,
		CanonicalPayloadHash: CanonicalPayloadHash(payload), EvidenceBundle: evidence,
		EvidenceBundleHash: EvidenceBundleHash(evidence), Digest: bearerHash(25),
		Nonce: bearerHash(26), InstrumentType: InstrumentLockAuthorization, SignerKeyID: "signer-key-1",
		KeyEpoch: 1, ModuleAddress: "0x1111111111111111111111111111111111111111",
		SafeAddress: "0x2222222222222222222222222222222222222222", KeeperID: "keeper-primary",
		ValidAfter: now, ValidUntil: now.Add(9 * time.Minute),
	}, State: SignRequested, CreatedAt: now}}
	signer := &coordinatorSigner{prepareErr: errors.New("timeout after signer durably prepared")}
	mirror := &coordinatorMirror{}
	coordinator, err := NewCoordinator(store, signer, mirror)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.Advance(context.Background(), store.request.RequestID); err == nil || store.request.State != SignRequested {
		t.Fatalf("prepare crash err=%v state=%s", err, store.request.State)
	}
	signer.prepareErr = nil
	if request, err := coordinator.Advance(context.Background(), store.request.RequestID); err != nil || request.State != HandlePrepared {
		t.Fatalf("prepared request=%+v err=%v", request, err)
	}
	if request, err := coordinator.Advance(context.Background(), store.request.RequestID); err != nil || request.State != ActivePendingMirror {
		t.Fatalf("active request=%+v err=%v", request, err)
	}
	mirror.err = errors.New("primary WORM unavailable")
	if _, err := coordinator.Advance(context.Background(), store.request.RequestID); err == nil || store.request.State != ActivePendingMirror {
		t.Fatalf("mirror crash err=%v state=%s", err, store.request.State)
	}
	mirror.err = nil
	if request, err := coordinator.Advance(context.Background(), store.request.RequestID); err != nil || request.State != ActiveMirrored || len(mirror.bytes) == 0 {
		t.Fatalf("mirrored request=%+v err=%v", request, err)
	}
	signer.ackErr = errors.New("ack response lost")
	if _, err := coordinator.Advance(context.Background(), store.request.RequestID); err == nil || store.request.State != ActiveMirrored {
		t.Fatalf("ack crash err=%v state=%s", err, store.request.State)
	}
	signer.ackErr = nil
	request, err := coordinator.Advance(context.Background(), store.request.RequestID)
	if err != nil || request.State != ActivationAcknowledged || signer.proof.PrimaryMirrorDigest != mirror.digest {
		t.Fatalf("acknowledged request=%+v proof=%+v err=%v", request, signer.proof, err)
	}
	if replay, err := coordinator.Advance(context.Background(), store.request.RequestID); err != nil || replay.State != ActivationAcknowledged {
		t.Fatalf("terminal replay=%+v err=%v", replay, err)
	}
}

type testIndependentSigningEngine struct {
	signature []byte
	err       error
	seen      ActivationInput
	calls     int
}

func (e *testIndependentSigningEngine) VerifyAndSign(_ context.Context, input ActivationInput) ([]byte, error) {
	e.calls++
	e.seen = input
	if e.err != nil {
		return nil, e.err
	}
	return append([]byte(nil), e.signature...), nil
}

func TestLedgerPreparedSignerRequiresIndependentExactByteVerification(t *testing.T) {
	now := time.Unix(1800000000, 0).UTC()
	ledger := signerStore(t, now, testActivationVerifier{})
	payload := []byte("exact-canonical-module-calldata")
	evidence := []byte("exact-policy-directory-and-chain-evidence")
	input := ActivationInput{
		RequestID: bearerHash(40), AuthorizationID: bearerHash(41), OperationID: bearerHash(42),
		ReservationID: bearerHash(43), ActionID: "lock-action-43", CanonicalPayload: payload,
		CanonicalPayloadHash: CanonicalPayloadHash(payload), EvidenceBundle: evidence,
		EvidenceBundleHash: EvidenceBundleHash(evidence), Digest: bearerHash(44), Nonce: bearerHash(45),
		InstrumentType: InstrumentLockAuthorization, SignerKeyID: "signer-key-1", KeyEpoch: 1,
		ModuleAddress: "0x1111111111111111111111111111111111111111",
		SafeAddress:   "0x2222222222222222222222222222222222222222", KeeperID: "keeper-primary",
		ValidAfter: now, ValidUntil: now.Add(9 * time.Minute),
	}
	engine := &testIndependentSigningEngine{signature: bytes.Repeat([]byte{0x61}, 65)}
	signer, err := NewLedgerPreparedSigner(ledger, engine)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := signer.Prepare(context.Background(), input)
	if err != nil || handle == "" || !bytes.Equal(engine.seen.CanonicalPayload, payload) || !bytes.Equal(engine.seen.EvidenceBundle, evidence) {
		t.Fatalf("handle=%q seen=%+v err=%v", handle, engine.seen, err)
	}
	replayed, err := signer.Prepare(context.Background(), input)
	if err != nil || replayed != handle || engine.calls != 1 {
		t.Fatalf("replayed=%q initial=%q engine calls=%d err=%v", replayed, handle, engine.calls, err)
	}
	newAttempt := input
	newAttempt.RequestID = bearerHash(46)
	replayed, err = signer.Prepare(context.Background(), newAttempt)
	if err != nil || replayed != handle || engine.calls != 1 {
		t.Fatalf("new-attempt replay=%q initial=%q engine calls=%d err=%v", replayed, handle, engine.calls, err)
	}
	changedEvidence := input
	changedEvidence.EvidenceBundle = []byte("different-but-internally-consistent-evidence")
	changedEvidence.EvidenceBundleHash = EvidenceBundleHash(changedEvidence.EvidenceBundle)
	if _, err := signer.Prepare(context.Background(), changedEvidence); !errors.Is(err, ErrMismatch) || engine.calls != 1 {
		t.Fatalf("changed evidence err=%v engine calls=%d", err, engine.calls)
	}

	refusingLedger := signerStore(t, now, testActivationVerifier{})
	refusingEngine := &testIndependentSigningEngine{err: errors.New("evidence cannot be independently verified")}
	refusingSigner, _ := NewLedgerPreparedSigner(refusingLedger, refusingEngine)
	if _, err := refusingSigner.Prepare(context.Background(), input); err == nil {
		t.Fatal("signer prepared an artifact after independent verification refusal")
	}
	if len(refusingLedger.byID) != 0 {
		t.Fatal("verification refusal mutated the signer ledger")
	}

	mutated := input
	mutated.CanonicalPayload = []byte("attacker-changed-payload")
	if validateActivationInput(mutated, now) == nil {
		t.Fatal("payload bytes can diverge from their bound hash")
	}
	mutated = input
	mutated.EvidenceBundle = []byte("attacker-changed-evidence")
	if validateActivationInput(mutated, now) == nil {
		t.Fatal("evidence bytes can diverge from their bound hash")
	}
	mutated = input
	mutated.ValidUntil = now
	if validateActivationInput(mutated, now) == nil {
		t.Fatal("already-expired signer request was accepted")
	}
}

type concurrentSigningEngine struct {
	mu      sync.Mutex
	calls   int
	entered chan struct{}
	release chan struct{}
}

func (e *concurrentSigningEngine) VerifyAndSign(context.Context, ActivationInput) ([]byte, error) {
	e.mu.Lock()
	e.calls++
	call := e.calls
	if call == 1 {
		close(e.entered)
	}
	e.mu.Unlock()
	<-e.release
	return bytes.Repeat([]byte{byte(0x60 + call)}, 65), nil
}

func (e *concurrentSigningEngine) callCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.calls
}

func TestLedgerPreparedSignerSerializesConcurrentExactPrepare(t *testing.T) {
	now := time.Unix(1800000000, 0).UTC()
	payload := []byte("concurrent-canonical-payload")
	evidence := []byte("concurrent-evidence")
	input := ActivationInput{
		RequestID: bearerHash(50), AuthorizationID: bearerHash(51), OperationID: bearerHash(52),
		ReservationID: bearerHash(53), ActionID: "lock-action-53", CanonicalPayload: payload,
		CanonicalPayloadHash: CanonicalPayloadHash(payload), EvidenceBundle: evidence,
		EvidenceBundleHash: EvidenceBundleHash(evidence), Digest: bearerHash(54), Nonce: bearerHash(55),
		InstrumentType: InstrumentLockAuthorization, SignerKeyID: "signer-key-1", KeyEpoch: 1,
		ModuleAddress: "0x1111111111111111111111111111111111111111",
		SafeAddress:   "0x2222222222222222222222222222222222222222", KeeperID: "keeper-primary",
		ValidAfter: now, ValidUntil: now.Add(9 * time.Minute),
	}
	engine := &concurrentSigningEngine{entered: make(chan struct{}), release: make(chan struct{})}
	signer, err := NewLedgerPreparedSigner(signerStore(t, now, testActivationVerifier{}), engine)
	if err != nil {
		t.Fatal(err)
	}
	type result struct {
		handle string
		err    error
	}
	results := make(chan result, 2)
	go func() {
		handle, err := signer.Prepare(context.Background(), input)
		results <- result{handle: handle, err: err}
	}()
	<-engine.entered
	secondStarted := make(chan struct{})
	go func() {
		close(secondStarted)
		handle, err := signer.Prepare(context.Background(), input)
		results <- result{handle: handle, err: err}
	}()
	<-secondStarted
	close(engine.release)
	first, second := <-results, <-results
	if first.err != nil || second.err != nil || first.handle == "" || first.handle != second.handle || engine.callCount() != 1 {
		t.Fatalf("first=%+v second=%+v engine calls=%d", first, second, engine.callCount())
	}
}
