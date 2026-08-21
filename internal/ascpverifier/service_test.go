package ascpverifier

import (
	"context"
	"crypto/ecdsa"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/gnanam1990/flowops/pkg/executioncommitment"
)

type testEngine struct {
	result EngineResult
	err    error
	calls  atomic.Int32
}

func (e *testEngine) Verify(context.Context, EngineInput) (EngineResult, error) {
	e.calls.Add(1)
	return e.result, e.err
}

type testSigner struct {
	key   *ecdsa.PrivateKey
	calls atomic.Int32
	bad   bool
}

func (s *testSigner) Address() common.Address { return crypto.PubkeyToAddress(s.key.PublicKey) }
func (s *testSigner) SignDigest(_ context.Context, digest common.Hash) ([]byte, error) {
	s.calls.Add(1)
	signature, err := crypto.Sign(digest[:], s.key)
	if s.bad && err == nil {
		signature[10] ^= 0xff
	}
	return signature, err
}

type testNotesAuthorizer struct {
	decision NotesDecision
	err      error
	review   NotesReview
}

type testKeyGate struct {
	mu    sync.RWMutex
	err   error
	calls atomic.Int32
}

func (g *testKeyGate) CheckActive(context.Context, string, string, common.Address, uint64) error {
	g.calls.Add(1)
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.err
}

func (g *testKeyGate) set(err error) { g.mu.Lock(); g.err = err; g.mu.Unlock() }

func (a *testNotesAuthorizer) Decide(_ context.Context, review NotesReview) (NotesDecision, error) {
	a.review = review
	return a.decision, a.err
}

func TestPassPreparesExactReleaseAttestationAndIdempotentRetry(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	engine := &testEngine{result: EngineResult{Verdict: VerdictPass, Code: "all-checks-pass"}}
	service, signer := newTestService(t, now, engine, nil)
	input := testInput(t, now)

	decision, err := service.VerifyAndSign(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Verdict != VerdictPass || decision.Outcome != OutcomeRelease || decision.Attestation.Verdict != ContractVerdictRelease {
		t.Fatalf("unexpected outcome: %+v", decision)
	}
	if decision.Attestation.VerificationSpecHash != decision.SpecHash || decision.Attestation.DeliveryHash != decision.DeliveryHash ||
		decision.Attestation.EvidenceHash != decision.EvidenceHash || decision.Attestation.DeliveredAt != input.Delivery.CapturedAt {
		t.Fatalf("attestation lost an exact binding: %+v", decision.Attestation)
	}
	if decision.Attestation.ValidUntil-decision.Attestation.IssuedAt != 10*60 {
		t.Fatalf("attestation window=%d", decision.Attestation.ValidUntil-decision.Attestation.IssuedAt)
	}
	digest, err := decision.Attestation.Digest(input.Commitment.ChainID)
	if err != nil || digest.Hex() != decision.AttestationHash {
		t.Fatalf("digest=%s err=%v", digest.Hex(), err)
	}
	signature, _ := hex.DecodeString(strings.TrimPrefix(decision.Signature, "0x"))
	signature[64] -= 27
	publicKey, err := crypto.SigToPub(digest[:], signature)
	if err != nil || crypto.PubkeyToAddress(*publicKey) != signer.Address() {
		t.Fatalf("signature recovery err=%v", err)
	}

	retry, err := service.VerifyAndSign(context.Background(), input)
	if err != nil || retry.Signature != decision.Signature || engine.calls.Load() != 1 || signer.calls.Load() != 1 {
		t.Fatalf("retry=%+v err=%v engine=%d signer=%d", retry, err, engine.calls.Load(), signer.calls.Load())
	}
}

func TestSpecMismatchFailsBeforeChecksAndSigning(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	engine := &testEngine{result: EngineResult{Verdict: VerdictPass, Code: "pass"}}
	service, signer := newTestService(t, now, engine, nil)
	input := testInput(t, now)
	input.Commitment.VerificationSpecHash = testHash(99)

	if _, err := service.VerifyAndSign(context.Background(), input); !errors.Is(err, ErrSpecHashMismatch) {
		t.Fatalf("error=%v", err)
	}
	if engine.calls.Load() != 0 || signer.calls.Load() != 0 {
		t.Fatalf("spec mismatch ran checks=%d or signer=%d", engine.calls.Load(), signer.calls.Load())
	}
}

func TestContentDigestMismatchNeverSignsOrRunsSemanticEngine(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	engine := &testEngine{result: EngineResult{Verdict: VerdictPass, Code: "pass"}}
	service, signer := newTestService(t, now, engine, nil)
	input := testInput(t, now)
	input.Delivery.ContentDigest = testHash(88)

	if _, err := service.VerifyAndSign(context.Background(), input); !errors.Is(err, ErrInvalidDelivery) {
		t.Fatalf("error=%v", err)
	}
	if engine.calls.Load() != 0 || signer.calls.Load() != 0 {
		t.Fatalf("engine=%d signer=%d", engine.calls.Load(), signer.calls.Load())
	}
}

func TestLateDeliveryCanOnlyProduceRefund(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	engine := &testEngine{result: EngineResult{Verdict: VerdictPass, Code: "pass"}}
	service, _ := newTestService(t, now, engine, nil)
	input := testInput(t, now)
	input.Delivery.CapturedAt = input.Commitment.DeliverBy + 1
	now = time.Unix(int64(input.Delivery.CapturedAt), 0).UTC()
	service.config.Clock = func() time.Time { return now }

	decision, err := service.VerifyAndSign(context.Background(), input)
	if err != nil || decision.Outcome != OutcomeRefund || decision.Code != "LATE_DELIVERY" || engine.calls.Load() != 0 {
		t.Fatalf("decision=%+v err=%v calls=%d", decision, err, engine.calls.Load())
	}
}

func TestFutureCapturedTimestampNeverProducesAnAttestation(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	engine := &testEngine{result: EngineResult{Verdict: VerdictPass, Code: "pass"}}
	service, signer := newTestService(t, now, engine, nil)
	input := testInput(t, now)
	input.Delivery.CapturedAt = input.Commitment.DeliverBy + 1
	if _, err := service.VerifyAndSign(context.Background(), input); !errors.Is(err, ErrInvalidDelivery) {
		t.Fatalf("future capture error=%v", err)
	}
	if signer.calls.Load() != 0 {
		t.Fatal("future capture reached signer")
	}
}

func TestPassWithNotesRequiresBoundApprovalDecision(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	engine := &testEngine{result: EngineResult{Verdict: VerdictPassWithNotes, Code: "minor-drift", Notes: []string{"second note", "first note"}}}
	withoutApproval, signer := newTestService(t, now, engine, nil)
	input := testInput(t, now)
	if _, err := withoutApproval.VerifyAndSign(context.Background(), input); !errors.Is(err, ErrApprovalRequired) {
		t.Fatalf("error=%v", err)
	}
	if signer.calls.Load() != 0 {
		t.Fatal("notes path signed without approval")
	}

	authorizer := &testNotesAuthorizer{decision: NotesDecision{DecisionID: "workflow_approved_1", Outcome: OutcomeRefund}}
	withApproval, _ := newTestService(t, now, engine, authorizer)
	decision, err := withApproval.VerifyAndSign(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Outcome != OutcomeRefund || decision.DecisionID != "workflow_approved_1" || decision.Notes[0] != "first note" ||
		authorizer.review.CallID != decision.Attestation.CallID || authorizer.review.DeliveryHash != decision.DeliveryHash {
		t.Fatalf("decision=%+v review=%+v", decision, authorizer.review)
	}
}

func TestConcurrentRetrySignsOnceAndConflictingDeliveryIsRejected(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	engine := &testEngine{result: EngineResult{Verdict: VerdictPass, Code: "pass"}}
	service, signer := newTestService(t, now, engine, nil)
	input := testInput(t, now)
	var wg sync.WaitGroup
	results := make(chan SignedDecision, 32)
	errorsSeen := make(chan error, 32)
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := service.VerifyAndSign(context.Background(), input)
			results <- result
			errorsSeen <- err
		}()
	}
	wg.Wait()
	close(results)
	close(errorsSeen)
	var signature string
	for err := range errorsSeen {
		if err != nil {
			t.Fatal(err)
		}
	}
	for result := range results {
		if signature == "" {
			signature = result.Signature
		} else if result.Signature != signature {
			t.Fatal("concurrent retry returned another signature")
		}
	}
	if engine.calls.Load() != 1 || signer.calls.Load() != 1 {
		t.Fatalf("engine=%d signer=%d", engine.calls.Load(), signer.calls.Load())
	}
	conflict := input
	conflict.Delivery.Content = []byte(`{"ok":false}`)
	conflict.Delivery.ContentDigest = sha256Hex(conflict.Delivery.Content)
	if _, err := service.VerifyAndSign(context.Background(), conflict); !errors.Is(err, ErrDecisionConflict) {
		t.Fatalf("conflict error=%v", err)
	}
}

func TestInvalidSignerOutputAndDuplicateNonceFailClosed(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	engine := &testEngine{result: EngineResult{Verdict: VerdictPass, Code: "pass"}}
	service, signer := newTestService(t, now, engine, nil)
	signer.bad = true
	if _, err := service.VerifyAndSign(context.Background(), testInput(t, now)); !errors.Is(err, ErrSigning) {
		t.Fatalf("bad signer error=%v", err)
	}
}

func TestRevocationAndExpiryStopCachedBearerPublication(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	engine := &testEngine{result: EngineResult{Verdict: VerdictPass, Code: "pass"}}
	gate := &testKeyGate{}
	service, _ := newTestServiceWithGate(t, now, engine, nil, gate)
	input := testInput(t, now)
	decision, err := service.VerifyAndSign(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	gate.set(errors.New("revoked at finalized block"))
	if _, err := service.VerifyAndSign(context.Background(), input); !errors.Is(err, ErrVerifierInactive) {
		t.Fatalf("revoked cache error=%v", err)
	}
	gate.set(nil)
	service.config.Clock = func() time.Time { return time.Unix(int64(decision.Attestation.ValidUntil+1), 0) }
	if _, err := service.VerifyAndSign(context.Background(), input); !errors.Is(err, ErrAttestationExpired) {
		t.Fatalf("expired cache error=%v", err)
	}
}

func TestSpecParsingCanonicalizesSetsAndRejectsUnknownOrWeakSpecs(t *testing.T) {
	raw := testSpecJSON(t)
	first, err := ParseSpec(raw)
	if err != nil {
		t.Fatal(err)
	}
	var spec VerificationSpec
	if err := json.Unmarshal(raw, &spec); err != nil {
		t.Fatal(err)
	}
	for left, right := 0, len(spec.RequiredChecks)-1; left < right; left, right = left+1, right-1 {
		spec.RequiredChecks[left], spec.RequiredChecks[right] = spec.RequiredChecks[right], spec.RequiredChecks[left]
	}
	reordered, _ := json.Marshal(spec)
	second, err := ParseSpec(reordered)
	if err != nil || first.Hash != second.Hash || string(first.CanonicalJSON) != string(second.CanonicalJSON) {
		t.Fatalf("canonicalization mismatch err=%v", err)
	}
	unknown := append(raw[:len(raw)-1], []byte(`,"owner":"attacker"}`)...)
	if _, err := ParseSpec(unknown); err == nil {
		t.Fatal("unknown spec field was accepted")
	}
	spec.RequiredChecks = spec.RequiredChecks[:2]
	weak, _ := json.Marshal(spec)
	if _, err := ParseSpec(weak); err == nil {
		t.Fatal("spec without format floor was accepted")
	}
}

func TestStructuredDataEngineChecksExactJSONAndFailsClosed(t *testing.T) {
	input := EngineInput{
		Spec:     VerificationSpec{Class: ClassStructuredData, ReferenceSource: "captured-delivery", Tolerance: "0", SemanticPredicates: []Predicate{{ID: "ok", Operator: "json-equals", Expected: "true"}}},
		Delivery: Delivery{Content: []byte(`{"ok":true,"count":2}`)},
	}
	result, err := (StructuredDataEngine{}).Verify(context.Background(), input)
	if err != nil || result.Verdict != VerdictPass {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	input.Delivery.Content = []byte(`{"ok":false}`)
	result, err = (StructuredDataEngine{}).Verify(context.Background(), input)
	if err != nil || result.Verdict != VerdictFail || result.Code != "semantic-predicate-failed" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	input.Spec.SemanticPredicates[0].Operator = "execute-code"
	if _, err := (StructuredDataEngine{}).Verify(context.Background(), input); !errors.Is(err, ErrInvalidEngineResult) {
		t.Fatalf("unsupported operator error=%v", err)
	}
}

func TestVerdictAttestationGoldenVector(t *testing.T) {
	attestation := testAttestationVector()
	digest, err := attestation.Digest("8453")
	if err != nil {
		t.Fatal(err)
	}
	if digest.Hex() != "0xb5bd196d91f7d0069c355204391ebf4929c51064bb1fbac9213ea810ccbe56dc" {
		t.Fatalf("digest=%s", digest.Hex())
	}
}

func TestPublishedVerdictAttestationVector(t *testing.T) {
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate test source")
	}
	contents, err := os.ReadFile(filepath.Join(filepath.Dir(sourceFile), "..", "..", "vectors", "verdict-attestation-v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var vector struct {
		TypeString string `json:"typeString"`
		Domain     struct {
			ChainID string `json:"chainId"`
		} `json:"domain"`
		Attestation Attestation `json:"attestation"`
		Digest      string      `json:"digest"`
	}
	if err := json.Unmarshal(contents, &vector); err != nil {
		t.Fatal(err)
	}
	digest, err := vector.Attestation.Digest(vector.Domain.ChainID)
	if err != nil || vector.TypeString != VerdictAttestationTypeString || digest.Hex() != vector.Digest {
		t.Fatalf("type=%q digest=%s err=%v", vector.TypeString, digest.Hex(), err)
	}
}

func TestEveryVerdictAttestationBindingChangesDigest(t *testing.T) {
	base := testAttestationVector()
	want, err := base.Digest("8453")
	if err != nil {
		t.Fatal(err)
	}
	mutations := []func(*Attestation){
		func(a *Attestation) { a.CallID = testHash(11) },
		func(a *Attestation) { a.CommitmentHash = testHash(12) },
		func(a *Attestation) { a.EscrowContract = "0x2222222222222222222222222222222222222222" },
		func(a *Attestation) { a.VerifierEpoch++ },
		func(a *Attestation) { a.VerificationSpecHash = testHash(13) },
		func(a *Attestation) { a.VerifierSoftwareHash = testHash(14) },
		func(a *Attestation) { a.DeliveryHash = testHash(15) },
		func(a *Attestation) { a.DeliveredAt-- },
		func(a *Attestation) { a.EvidenceHash = testHash(16) },
		func(a *Attestation) { a.Verdict = ContractVerdictEarlyRefund },
		func(a *Attestation) { a.VerdictNonce = "43" },
		func(a *Attestation) { a.IssuedAt++ },
		func(a *Attestation) { a.ValidUntil-- },
	}
	for index, mutate := range mutations {
		candidate := base
		mutate(&candidate)
		got, err := candidate.Digest("8453")
		if err != nil || got == want {
			t.Fatalf("mutation %d digest=%s err=%v", index, got.Hex(), err)
		}
	}
	otherChain, err := base.Digest("84532")
	if err != nil || otherChain == want {
		t.Fatalf("chain mutation digest=%s err=%v", otherChain.Hex(), err)
	}
}

func newTestService(t *testing.T, now time.Time, engine Engine, authorizer NotesAuthorizer) (*Service, *testSigner) {
	return newTestServiceWithGate(t, now, engine, authorizer, &testKeyGate{})
}

func newTestServiceWithGate(t *testing.T, now time.Time, engine Engine, authorizer NotesAuthorizer, gate VerifierKeyGate) (*Service, *testSigner) {
	t.Helper()
	key, err := crypto.HexToECDSA(strings.Repeat("11", 32))
	if err != nil {
		t.Fatal(err)
	}
	nonces, err := NewMemoryNonceSource(big.NewInt(42))
	if err != nil {
		t.Fatal(err)
	}
	signer := &testSigner{key: key}
	service, err := New(Config{
		VerifierEpoch: 7, VerifierSoftwareHash: testHash(77), AttestationTTL: 10 * time.Minute,
		Clock: func() time.Time { return now }, Engines: map[Class]Engine{ClassStructuredData: engine},
		NotesAuthorizer: authorizer, Signer: signer, Nonces: nonces, VerifierKeyGate: gate,
	})
	if err != nil {
		t.Fatal(err)
	}
	return service, signer
}

func testInput(t *testing.T, now time.Time) Input {
	t.Helper()
	specJSON := testSpecJSON(t)
	canonical, err := ParseSpec(specJSON)
	if err != nil {
		t.Fatal(err)
	}
	content := []byte(`{"ok":true}`)
	return Input{
		Commitment: executioncommitment.Commitment{
			OrgDomain: testHash(1), OperationID: testHash(2), Rail: executioncommitment.RailEscrow,
			SchemeVersion: executioncommitment.SchemeVersionV1, Protection: executioncommitment.ProtectionEscrow,
			EscrowContract: "0x1111111111111111111111111111111111111111", PurchaseSpecHash: testHash(3),
			QuoteHash: testHash(4), VerificationSpecHash: canonical.Hash, DeclaredWorkTime: 30,
			VerificationBudgetSeconds: 20, DirectoryVersion: 9, SellerID: testHash(5), ResourceID: testHash(6),
			PayTo: "0x2222222222222222222222222222222222222222", AckAuthority: "0x3333333333333333333333333333333333333333",
			Amount: "42", ChainID: "8453", Asset: "0x4444444444444444444444444444444444444444",
			QuoteExpiresAt: uint64(now.Add(30 * time.Minute).Unix()), AcceptBy: uint64(now.Add(10 * time.Minute).Unix()),
			DeliverBy: uint64(now.Add(20 * time.Minute).Unix()), SettleBy: uint64(now.Add(40 * time.Minute).Unix()),
		},
		SpecJSON: specJSON,
		Delivery: Delivery{Reference: []byte("ipfs://delivery"), Content: content, ContentDigest: sha256Hex(content), HTTPStatus: 200, ContentType: "application/json", CapturedAt: uint64(now.Add(-time.Minute).Unix())},
	}
}

func testSpecJSON(t *testing.T) []byte {
	t.Helper()
	raw, err := json.Marshal(VerificationSpec{
		Version: SpecVersion, Class: ClassStructuredData,
		RequiredChecks:  []FormatCheck{{Kind: CheckNonEmpty}, {Kind: CheckContentType, Expected: "application/json"}, {Kind: CheckHTTPStatus, Expected: "200-299"}, {Kind: CheckContentDigest}},
		ReferenceSource: "captured-delivery", FreshnessWindowSeconds: 3600,
		SemanticPredicates: []Predicate{{ID: "result-ok", Operator: "json-equals", Expected: "true"}},
		Tolerance:          "0", TimeoutSeconds: 5, EvidenceArtifact: "captured-delivery-v1", NotesPolicy: NotesRequireApproval,
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func sha256Hex(value []byte) string {
	digest := sha256.Sum256(value)
	return "0x" + hex.EncodeToString(digest[:])
}

func testHash(value byte) string {
	return "0x" + strings.Repeat("00", 31) + hex.EncodeToString([]byte{value})
}

func testAttestationVector() Attestation {
	return Attestation{
		CallID: testHash(1), CommitmentHash: testHash(2), EscrowContract: "0x1111111111111111111111111111111111111111",
		VerifierEpoch: 7, VerificationSpecHash: testHash(3), VerifierSoftwareHash: testHash(4),
		DeliveryHash: testHash(5), DeliveredAt: 1_800_000_000, EvidenceHash: testHash(6),
		Verdict: ContractVerdictRelease, VerdictNonce: "42", IssuedAt: 1_800_000_010, ValidUntil: 1_800_000_610,
	}
}
