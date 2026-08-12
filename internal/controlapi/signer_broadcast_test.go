package controlapi

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gnanam1990/flowops/internal/reconciliation"
	"github.com/gnanam1990/flowops/pkg/broadcastreceipt"
)

type stubBroadcastRegistrar struct {
	execution reconciliation.Execution
	err       error
}

func (s stubBroadcastRegistrar) Register(context.Context, broadcastreceipt.SignedReceipt) (reconciliation.Execution, error) {
	return s.execution, s.err
}

type captureBroadcastReconciler struct {
	expected    reconciliation.ExpectedExecution
	broadcastAt time.Time
	calls       int
	err         error
}

func (r *captureBroadcastReconciler) RegisterAttestedBroadcast(_ context.Context, expected reconciliation.ExpectedExecution, broadcastAt time.Time) (reconciliation.Execution, error) {
	r.calls++
	r.expected = expected
	r.broadcastAt = broadcastAt
	if r.err != nil {
		return reconciliation.Execution{}, r.err
	}
	return reconciliation.Execution{Expected: expected, State: reconciliation.ExecutionBroadcast, BroadcastAt: broadcastAt}, nil
}

func broadcastKeypair(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	seed, err := hex.DecodeString(strings.Repeat("23", ed25519.SeedSize))
	if err != nil {
		t.Fatal(err)
	}
	privateKey := ed25519.NewKeyFromSeed(seed)
	return privateKey.Public().(ed25519.PublicKey), privateKey
}

func issuedBroadcastFixture(t *testing.T, now time.Time) (*SignerBroadcastRegistrar, broadcastreceipt.Receipt, ed25519.PrivateKey, *captureBroadcastReconciler, func()) {
	t.Helper()
	store := newMemoryStore(func() time.Time { return now })
	store.agents[agentKey("org_a", "agent_a")] = Agent{OrganizationID: "org_a", ID: "agent_a", CustomerID: "customer_a", Status: AgentActive}
	chain := newHealthyChain(now)
	lifecycle, journal := testLifecycle(t, store, chain, now)
	record, err := lifecycle.Submit(context.Background(), intent("intent_broadcast", "org_a", "agent_a", "100"))
	if err != nil {
		t.Fatal(err)
	}
	signedAuthorization, err := lifecycle.Issue(context.Background(), record.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := signedAuthorization.Authorization.Digest()
	if err != nil {
		t.Fatal(err)
	}
	publicKey, privateKey := broadcastKeypair(t)
	keys, err := NewStaticBroadcastKeys([]BroadcastKey{{OrganizationID: "org_a", CustomerID: "customer_a", KeyID: "customer_signer_1", PublicKey: publicKey}})
	if err != nil {
		t.Fatal(err)
	}
	reconciler := &captureBroadcastReconciler{}
	registrar, err := NewSignerBroadcastRegistrar(lifecycle, keys, reconciler, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	receipt := broadcastreceipt.Receipt{
		Version: broadcastreceipt.Version, OrganizationID: "org_a", CustomerID: "customer_a",
		AuthorizationID:     signedAuthorization.Authorization.AuthorizationID,
		AuthorizationDigest: "0x" + hex.EncodeToString(digest[:]), TransactionHash: "0x" + strings.Repeat("b", 64),
		Sender: "0x2222222222222222222222222222222222222222", Outcome: broadcastreceipt.OutcomeAmbiguous, BroadcastAt: now.Unix(),
	}
	return registrar, receipt, privateKey, reconciler, func() { _ = journal.Close() }
}

func signBroadcast(t *testing.T, receipt broadcastreceipt.Receipt, privateKey ed25519.PrivateKey) broadcastreceipt.SignedReceipt {
	t.Helper()
	signed, err := broadcastreceipt.Sign(receipt, "customer_signer_1", privateKey)
	if err != nil {
		t.Fatal(err)
	}
	return signed
}

func TestSignerBroadcastDerivesExpectedExecutionOnlyFromIssuedAuthorization(t *testing.T) {
	now := time.Date(2026, 8, 12, 16, 0, 0, 0, time.UTC)
	registrar, receipt, privateKey, reconciler, closeFixture := issuedBroadcastFixture(t, now)
	defer closeFixture()
	execution, err := registrar.Register(context.Background(), signBroadcast(t, receipt, privateKey))
	if err != nil {
		t.Fatal(err)
	}
	got := execution.Expected
	if reconciler.calls != 1 || got.OrganizationID != "org_a" || got.AgentID != "agent_a" || got.TaskID != "task_research" || got.Asset != testUSDC || got.Recipient != testRecipient || got.AmountAtomic != "100" || got.TransactionHash != receipt.TransactionHash || got.Sender != receipt.Sender {
		t.Fatalf("derived execution = %+v calls=%d", got, reconciler.calls)
	}
	if got.ExecutionID == "" || !reconciler.broadcastAt.Equal(now) {
		t.Fatalf("execution ID/time = %q %s", got.ExecutionID, reconciler.broadcastAt)
	}
}

func TestSignerBroadcastRejectsCrossTenantDigestTimeAndSignatureSubstitution(t *testing.T) {
	now := time.Date(2026, 8, 12, 16, 0, 0, 0, time.UTC)
	tests := map[string]struct {
		mutate func(*broadcastreceipt.Receipt)
		resign bool
		want   error
	}{
		"cross organization": {func(r *broadcastreceipt.Receipt) { r.OrganizationID = "org_other" }, true, ErrBroadcastKeyUnknown},
		"cross customer":     {func(r *broadcastreceipt.Receipt) { r.CustomerID = "customer_other" }, true, ErrBroadcastKeyUnknown},
		"digest":             {func(r *broadcastreceipt.Receipt) { r.AuthorizationDigest = "0x" + strings.Repeat("c", 64) }, true, ErrBroadcastBinding},
		"after expiry":       {func(r *broadcastreceipt.Receipt) { r.BroadcastAt = now.Add(6 * time.Minute).Unix() }, true, ErrBroadcastTime},
		"future":             {func(r *broadcastreceipt.Receipt) { r.BroadcastAt = now.Add(time.Minute).Unix() }, true, ErrBroadcastTime},
		"unsigned hash swap": {func(r *broadcastreceipt.Receipt) { r.TransactionHash = "0x" + strings.Repeat("d", 64) }, false, ErrBroadcastSignature},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			registrar, receipt, privateKey, reconciler, closeFixture := issuedBroadcastFixture(t, now)
			defer closeFixture()
			signed := signBroadcast(t, receipt, privateKey)
			tc.mutate(&signed.Receipt)
			if tc.resign {
				signed = signBroadcast(t, signed.Receipt, privateKey)
			}
			if _, err := registrar.Register(context.Background(), signed); !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
			if reconciler.calls != 0 {
				t.Fatalf("reconciler called %d times", reconciler.calls)
			}
		})
	}
}

func TestBroadcastKeyRegistryRejectsDuplicatesAndCopiesKeys(t *testing.T) {
	publicKey, _ := broadcastKeypair(t)
	entry := BroadcastKey{OrganizationID: "org_a", CustomerID: "customer_a", KeyID: "signer_1", PublicKey: publicKey}
	if _, err := NewStaticBroadcastKeys([]BroadcastKey{entry, entry}); err == nil {
		t.Fatal("duplicate key accepted")
	}
	registry, err := NewStaticBroadcastKeys([]BroadcastKey{entry})
	if err != nil {
		t.Fatal(err)
	}
	publicKey[0] ^= 0xff
	resolved, ok := registry.Resolve("org_a", "customer_a", "signer_1")
	if !ok || resolved[0] == publicKey[0] {
		t.Fatal("registry did not isolate public key storage")
	}
}

func TestSignerBroadcastHTTPBoundaryIsSignatureAuthenticatedAndFailClosed(t *testing.T) {
	now := time.Date(2026, 8, 12, 16, 0, 0, 0, time.UTC)
	store := newMemoryStore(func() time.Time { return now })
	chain := newHealthyChain(now)
	lifecycle, journal := testLifecycle(t, store, chain, now)
	defer journal.Close()
	expected := reconciliation.ExpectedExecution{ExecutionID: "exec_1", OrganizationID: "org_a", AgentID: "agent_a", TaskID: "task_a", IntentDigest: "0x" + strings.Repeat("a", 64), TransactionHash: "0x" + strings.Repeat("b", 64), ChainID: 84532, Sender: "0x2222222222222222222222222222222222222222", Asset: testUSDC, Recipient: testRecipient, AmountAtomic: "100"}
	execution := reconciliation.Execution{Expected: expected, State: reconciliation.ExecutionBroadcast, BroadcastAt: now}
	server, err := NewServer(ServerConfig{Store: store, Lifecycle: lifecycle, Chain: chain, SignerBroadcasts: stubBroadcastRegistrar{execution: execution}})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(broadcastreceipt.SignedReceipt{})
	request := httptest.NewRequest(http.MethodPost, "/v1/signer/broadcasts", bytes.NewReader(body))
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("signature-authenticated endpoint status=%d body=%s", recorder.Code, recorder.Body.String())
	}

	unavailable, err := NewServer(ServerConfig{Store: store, Lifecycle: lifecycle, Chain: chain})
	if err != nil {
		t.Fatal(err)
	}
	recorder = httptest.NewRecorder()
	unavailable.ServeHTTP(recorder, request.Clone(context.Background()))
	if recorder.Code != http.StatusServiceUnavailable || !strings.Contains(recorder.Body.String(), "SIGNER_BROADCASTS_UNAVAILABLE") {
		t.Fatalf("unconfigured endpoint status=%d body=%s", recorder.Code, recorder.Body.String())
	}

	serverWithError, err := NewServer(ServerConfig{Store: store, Lifecycle: lifecycle, Chain: chain, SignerBroadcasts: stubBroadcastRegistrar{err: ErrBroadcastSignature}})
	if err != nil {
		t.Fatal(err)
	}
	recorder = httptest.NewRecorder()
	serverWithError.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/signer/broadcasts", bytes.NewReader(body)))
	if recorder.Code != http.StatusUnauthorized || !strings.Contains(recorder.Body.String(), "INVALID_SIGNER_RECEIPT") {
		t.Fatalf("invalid signature status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}
