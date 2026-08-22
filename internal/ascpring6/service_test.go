package ascpring6

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/gnanam1990/flowops/internal/ascpbearer"
)

type testVerifier struct {
	mu    sync.Mutex
	calls int
	err   error
}

func (v *testVerifier) Verify(_ context.Context, _ ascpbearer.ActivationInput, _ string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.calls++
	return v.err
}
func (v *testVerifier) count() int { v.mu.Lock(); defer v.mu.Unlock(); return v.calls }

type deterministicHSM struct {
	mu         sync.Mutex
	key        []byte
	operations map[string]HSMResult
	calls      int
	wrong      bool
}

type ambiguousHSM struct {
	delegate *deterministicHSM
	first    bool
}

func (h *ambiguousHSM) Sign(ctx context.Context, request HSMRequest) (HSMResult, error) {
	result, err := h.delegate.Sign(ctx, request)
	if err != nil {
		return HSMResult{}, err
	}
	if h.first {
		h.first = false
		clear(result.Signature)
		return HSMResult{}, errors.New("HSM response was lost")
	}
	return result, nil
}

type refusalFailureStore struct{ BindingStore }

func (refusalFailureStore) MarkRefused(context.Context, ActionBinding, string) (ActionBinding, error) {
	return ActionBinding{}, errors.New("journal unavailable")
}

func (h *deterministicHSM) Sign(_ context.Context, request HSMRequest) (HSMResult, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.calls++
	if result, ok := h.operations[request.IdempotencyKey]; ok {
		return cloneResult(result), nil
	}
	key, err := crypto.ToECDSA(h.key)
	if err != nil {
		return HSMResult{}, err
	}
	digest := request.Digest
	if h.wrong {
		digest = ringHash(99)
	}
	signature, err := crypto.Sign(commonDigest(digest), key)
	if err != nil {
		return HSMResult{}, err
	}
	result := HSMResult{OperationHandle: "hsm-op-" + request.IdempotencyKey[2:18], Digest: request.Digest, Signature: signature}
	h.operations[request.IdempotencyKey] = cloneResult(result)
	return result, nil
}
func (h *deterministicHSM) counts() (int, int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.calls, len(h.operations)
}

func TestServiceBindsActionAndReplaysOneHSMOperationAcrossConcurrencyAndRestart(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	key, _ := crypto.GenerateKey()
	hsm := &deterministicHSM{key: crypto.FromECDSA(key), operations: map[string]HSMResult{}}
	verifier := &testVerifier{}
	path := filepath.Join(ringTempDir(t), "ring6.jsonl")
	journal, err := OpenJournal(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	service := ringService(t, journal, verifier, hsm, key, now)
	input := ringInput(now)
	const workers = 8
	results := make(chan []byte, workers)
	failures := make(chan error, workers)
	var group sync.WaitGroup
	for range workers {
		group.Add(1)
		go func() {
			defer group.Done()
			signature, err := service.VerifyAndSign(context.Background(), input)
			if err != nil {
				failures <- err
				return
			}
			results <- signature
		}()
	}
	group.Wait()
	close(results)
	close(failures)
	for err := range failures {
		t.Fatal(err)
	}
	var first []byte
	for signature := range results {
		if first == nil {
			first = signature
		} else if !bytes.Equal(first, signature) {
			t.Fatal("concurrent retry changed signature")
		}
	}
	if calls, operations := hsm.counts(); calls != workers || operations != 1 || verifier.count() != 1 {
		t.Fatalf("hsm calls=%d operations=%d verifier=%d", calls, operations, verifier.count())
	}
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := OpenJournal(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = restarted.Close() }()
	replayed, err := ringService(t, restarted, verifier, hsm, key, now).VerifyAndSign(context.Background(), input)
	if err != nil || !bytes.Equal(replayed, first) {
		t.Fatalf("restart replay changed: err=%v", err)
	}
	changed := input
	changed.EvidenceBundle = []byte("different-evidence")
	changed.EvidenceBundleHash = ascpbearer.EvidenceBundleHash(changed.EvidenceBundle)
	if _, err := ringService(t, restarted, verifier, hsm, key, now).VerifyAndSign(context.Background(), changed); !errors.Is(err, ErrBinding) {
		t.Fatalf("changed action error=%v", err)
	}
}

func TestServicePersistsOnlyExplicitVerifierRefusal(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	key, _ := crypto.GenerateKey()
	journal, err := OpenJournal(context.Background(), filepath.Join(ringTempDir(t), "ring6.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = journal.Close() }()
	verifier := &testVerifier{err: &PermanentRefusal{Code: "EVIDENCE_INVALID"}}
	hsm := &deterministicHSM{key: crypto.FromECDSA(key), operations: map[string]HSMResult{}}
	service := ringService(t, journal, verifier, hsm, key, now)
	input := ringInput(now)
	if _, err := service.VerifyAndSign(context.Background(), input); !errors.Is(err, ErrRefused) {
		t.Fatalf("refusal=%v", err)
	}
	verifier.err = nil
	if _, err := service.VerifyAndSign(context.Background(), input); !errors.Is(err, ErrRefused) || verifier.count() != 1 {
		t.Fatalf("persisted refusal=%v calls=%d", err, verifier.count())
	}
	if calls, operations := hsm.counts(); calls != 0 || operations != 0 {
		t.Fatalf("refusal reached HSM calls=%d operations=%d", calls, operations)
	}
}

func TestServiceRejectsWrongRecoveredSignerWithoutTerminalRefusal(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	key, _ := crypto.GenerateKey()
	journal, err := OpenJournal(context.Background(), filepath.Join(ringTempDir(t), "ring6.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = journal.Close() }()
	hsm := &deterministicHSM{key: crypto.FromECDSA(key), operations: map[string]HSMResult{}, wrong: true}
	service := ringService(t, journal, &testVerifier{}, hsm, key, now)
	if _, err := service.VerifyAndSign(context.Background(), ringInput(now)); !errors.Is(err, ErrBinding) {
		t.Fatalf("wrong signer error=%v", err)
	}
}

func TestServiceNeverPersistsPermanentRefusalAfterAmbiguousHSMRequest(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	key, _ := crypto.GenerateKey()
	journal, err := OpenJournal(context.Background(), filepath.Join(ringTempDir(t), "ring6.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = journal.Close() }()
	verifier := &testVerifier{}
	delegate := &deterministicHSM{key: crypto.FromECDSA(key), operations: map[string]HSMResult{}}
	hsm := &ambiguousHSM{delegate: delegate, first: true}
	service := ringService(t, journal, verifier, hsm, key, now)
	input := ringInput(now)
	if _, err := service.VerifyAndSign(context.Background(), input); err == nil || errors.Is(err, ErrRefused) {
		t.Fatalf("ambiguous HSM error=%v", err)
	}
	verifier.err = &PermanentRefusal{Code: "EVIDENCE_INVALID"}
	if signature, err := service.VerifyAndSign(context.Background(), input); err != nil || len(signature) != 65 {
		t.Fatalf("idempotent HSM recovery signature=%x err=%v", signature, err)
	}
	if calls, operations := delegate.counts(); calls != 2 || operations != 1 || verifier.count() != 1 {
		t.Fatalf("HSM calls=%d operations=%d verifier=%d", calls, operations, verifier.count())
	}
}

func TestServiceAllowsOnlyPersistedHSMReplayAfterFreshnessWindow(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	clock := now
	key, _ := crypto.GenerateKey()
	journal, err := OpenJournal(context.Background(), filepath.Join(ringTempDir(t), "ring6.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = journal.Close() }()
	verifier := &testVerifier{}
	delegate := &deterministicHSM{key: crypto.FromECDSA(key), operations: map[string]HSMResult{}}
	hsm := &ambiguousHSM{delegate: delegate, first: true}
	service, err := New(Config{Store: journal, Verifier: verifier, HSM: hsm, Clock: func() time.Time { return clock },
		KeyID: "key-1", KeyEpoch: 1, KeeperID: "keeper-1", SignerAddress: strings.ToLower(crypto.PubkeyToAddress(key.PublicKey).Hex())})
	if err != nil {
		t.Fatal(err)
	}
	input := ringInput(now)
	if _, err := service.VerifyAndSign(context.Background(), input); err == nil {
		t.Fatal("ambiguous HSM response was accepted")
	}
	clock = now.Add(2 * time.Minute)
	if signature, err := service.VerifyAndSign(context.Background(), input); err != nil || len(signature) != 65 {
		t.Fatalf("late persisted replay signature=%x err=%v", signature, err)
	}
	clock = now.Add(-2 * time.Minute)
	if signature, err := service.VerifyAndSign(context.Background(), input); err != nil || len(signature) != 65 {
		t.Fatalf("backward-clock persisted replay signature=%x err=%v", signature, err)
	}
	futureOnly := input
	futureOnly.ActionID = "new-future-action"
	if _, err := service.VerifyAndSign(context.Background(), futureOnly); !errors.Is(err, ascpbearer.ErrActivationInput) {
		t.Fatalf("new future binding error=%v", err)
	}
	clock = now.Add(2 * time.Minute)
	freshnessOnly := input
	freshnessOnly.ActionID = "new-stale-action"
	if _, err := service.VerifyAndSign(context.Background(), freshnessOnly); !errors.Is(err, ascpbearer.ErrActivationInput) {
		t.Fatalf("new stale binding error=%v", err)
	}
	clock = now.Add(6 * time.Minute)
	if _, err := service.VerifyAndSign(context.Background(), input); !errors.Is(err, ascpbearer.ErrActivationInput) {
		t.Fatalf("expired persisted replay error=%v", err)
	}
}

func TestServiceDoesNotBypassFreshnessForPersistedBoundAction(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	clock := now
	key, _ := crypto.GenerateKey()
	journal, err := OpenJournal(context.Background(), filepath.Join(ringTempDir(t), "ring6.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = journal.Close() }()
	verifier := &testVerifier{err: errors.New("verifier unavailable")}
	service, err := New(Config{Store: journal, Verifier: verifier,
		HSM: &deterministicHSM{key: crypto.FromECDSA(key), operations: map[string]HSMResult{}}, Clock: func() time.Time { return clock },
		KeyID: "key-1", KeyEpoch: 1, KeeperID: "keeper-1", SignerAddress: strings.ToLower(crypto.PubkeyToAddress(key.PublicKey).Hex())})
	if err != nil {
		t.Fatal(err)
	}
	input := ringInput(now)
	if _, err := service.VerifyAndSign(context.Background(), input); err == nil {
		t.Fatal("transient verifier error was swallowed")
	}
	clock = now.Add(2 * time.Minute)
	verifier.err = nil
	if _, err := service.VerifyAndSign(context.Background(), input); !errors.Is(err, ascpbearer.ErrActivationInput) {
		t.Fatalf("stale BOUND replay error=%v", err)
	}
}

func TestServiceReturnsNoPermanentRefusalWhenRefusalCannotBePersisted(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	key, _ := crypto.GenerateKey()
	journal, err := OpenJournal(context.Background(), filepath.Join(ringTempDir(t), "ring6.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = journal.Close() }()
	service := ringService(t, refusalFailureStore{BindingStore: journal}, &testVerifier{err: &PermanentRefusal{Code: "EVIDENCE_INVALID"}},
		&deterministicHSM{key: crypto.FromECDSA(key), operations: map[string]HSMResult{}}, key, now)
	if _, err := service.VerifyAndSign(context.Background(), ringInput(now)); err == nil || errors.Is(err, ErrRefused) {
		t.Fatalf("unpersisted refusal was exposed as permanent: %v", err)
	}
}

func ringService(t *testing.T, store BindingStore, verifier IndependentVerifier, hsm HSM, key *ecdsa.PrivateKey, now time.Time) *Service {
	t.Helper()
	service, err := New(Config{Store: store, Verifier: verifier, HSM: hsm, Clock: func() time.Time { return now }, KeyID: "key-1", KeyEpoch: 1, KeeperID: "keeper-1", SignerAddress: strings.ToLower(crypto.PubkeyToAddress(key.PublicKey).Hex())})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func ringInput(now time.Time) ascpbearer.ActivationInput {
	payload := []byte("canonical-payload")
	evidence := []byte("independent-evidence")
	return ascpbearer.ActivationInput{RequestID: ringHash(1), AuthorizationID: ringHash(2), OperationID: ringHash(3), ReservationID: ringHash(4), ActionID: "lock-1", CanonicalPayload: payload, CanonicalPayloadHash: ascpbearer.CanonicalPayloadHash(payload), EvidenceBundle: evidence, EvidenceBundleHash: ascpbearer.EvidenceBundleHash(evidence), Digest: ringHash(5), Nonce: ringHash(6), InstrumentType: ascpbearer.InstrumentLockAuthorization, SignerBindingVersion: 1, SignerKeyID: "key-1", KeyEpoch: 1, ModuleAddress: "0x1111111111111111111111111111111111111111", SafeAddress: "0x2222222222222222222222222222222222222222", KeeperID: "keeper-1", ValidAfter: now, ValidUntil: now.Add(5 * time.Minute)}
}

func ringHash(value byte) string       { return "0x" + strings.Repeat(hex.EncodeToString([]byte{value}), 32) }
func commonDigest(value string) []byte { return common.HexToHash(value).Bytes() }
func cloneResult(result HSMResult) HSMResult {
	result.Signature = append([]byte(nil), result.Signature...)
	return result
}
func ringTempDir(t *testing.T) string {
	t.Helper()
	base, err := filepath.EvalSymlinks(os.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp(base, "flowops-ring6-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}
