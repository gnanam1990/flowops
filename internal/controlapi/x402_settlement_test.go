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

	"github.com/gnanam1990/flowops/internal/controlplane"
	"github.com/gnanam1990/flowops/internal/reconciliation"
	"github.com/gnanam1990/flowops/internal/x402adapter"
	"github.com/gnanam1990/flowops/pkg/broadcastreceipt"
	"github.com/gnanam1990/flowops/pkg/envelope"
	x402 "github.com/x402-foundation/x402/go/v2"
)

type captureX402Reconciler struct {
	expected reconciliation.ExpectedExecution
	claim    reconciliation.X402SettlementClaim
	calls    int
	err      error
}

func (r *captureX402Reconciler) RegisterX402Settlement(_ context.Context, expected reconciliation.ExpectedExecution, claim reconciliation.X402SettlementClaim) (reconciliation.Execution, error) {
	r.calls++
	r.expected, r.claim = expected, claim
	if r.err != nil {
		return reconciliation.Execution{}, r.err
	}
	return reconciliation.Execution{Expected: expected, State: reconciliation.ExecutionBroadcast, X402SettlementClaim: &claim}, nil
}

func issuedX402Fixture(t *testing.T, now time.Time) (*controlplane.Lifecycle, *X402SettlementRegistrar, envelope.Authorization, ed25519.PrivateKey, *captureX402Reconciler, func()) {
	t.Helper()
	store := newMemoryStore(func() time.Time { return now })
	store.agents[agentKey("org_a", "agent_a")] = Agent{OrganizationID: "org_a", ID: "agent_a", CustomerID: "customer_a", Status: AgentActive}
	chain := newHealthyChain(now)
	lifecycle, journal := testLifecycle(t, store, chain, now)
	paymentIntent := intent("intent_x402_settlement", "org_a", "agent_a", "100")
	paymentIntent.Rail = envelope.RailX402
	record, err := lifecycle.Submit(context.Background(), paymentIntent)
	if err != nil {
		t.Fatal(err)
	}
	signed, err := lifecycle.Issue(context.Background(), record.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	reconciler := &captureX402Reconciler{}
	publicKey, privateKey := broadcastKeypair(t)
	keys, err := NewStaticBroadcastKeys([]BroadcastKey{{OrganizationID: "org_a", CustomerID: "customer_a", KeyID: "customer_signer_1", PublicKey: publicKey}})
	if err != nil {
		t.Fatal(err)
	}
	registrar, err := NewX402SettlementRegistrar(lifecycle, keys, reconciler, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	return lifecycle, registrar, signed.Authorization, privateKey, reconciler, func() { _ = journal.Close() }
}

func validX402Settlement(authorization envelope.Authorization) x402.SettleResponse {
	network := x402adapter.BaseMainnetNetwork
	if authorization.ChainID == x402adapter.BaseSepoliaChainID {
		network = x402adapter.BaseSepoliaNetwork
	}
	return x402.SettleResponse{
		Success: true, Payer: "0x2222222222222222222222222222222222222222",
		Transaction: "0x" + strings.Repeat("a", 64), Network: x402.Network(network), Amount: authorization.AmountAtomic,
	}
}

func validX402Request(t *testing.T, authorization envelope.Authorization, privateKey ed25519.PrivateKey, broadcastAt int64) X402SettlementRequest {
	t.Helper()
	settled := validX402Settlement(authorization)
	digest, err := authorization.Digest()
	if err != nil {
		t.Fatal(err)
	}
	receipt := broadcastreceipt.Receipt{
		Version: broadcastreceipt.Version, OrganizationID: authorization.OrganizationID, CustomerID: authorization.CustomerID,
		AuthorizationID: authorization.AuthorizationID, AuthorizationDigest: "0x" + hex.EncodeToString(digest[:]),
		TransactionHash: settled.Transaction, Sender: settled.Payer, Outcome: broadcastreceipt.OutcomeAmbiguous, BroadcastAt: broadcastAt,
	}
	return X402SettlementRequest{Settlement: settled, SignedReceipt: signBroadcast(t, receipt, privateKey)}
}

func TestX402SettlementDerivesCandidateOnlyFromIssuedAuthorization(t *testing.T) {
	now := time.Date(2026, 8, 28, 8, 0, 0, 0, time.UTC)
	_, registrar, authorization, privateKey, reconciler, closeFixture := issuedX402Fixture(t, now)
	defer closeFixture()
	request := validX402Request(t, authorization, privateKey, now.Unix())
	settled := request.Settlement
	execution, err := registrar.Register(context.Background(), "org_a", "agent_a", authorization.AuthorizationID, request)
	if err != nil {
		t.Fatal(err)
	}
	got := reconciler.expected
	if reconciler.calls != 1 || got.OrganizationID != authorization.OrganizationID || got.AgentID != authorization.AgentID ||
		got.TaskID != authorization.TaskID || got.ChainID != authorization.ChainID || got.Asset != authorization.Asset ||
		got.Recipient != authorization.Recipient || got.AmountAtomic != authorization.AmountAtomic || got.Sender != settled.Payer ||
		got.TransactionHash != settled.Transaction || got.ExecutionID == "" {
		t.Fatalf("derived execution = %+v calls=%d", got, reconciler.calls)
	}
	if execution.X402SettlementClaim == nil || reconciler.claim.Authorization.AuthorizationID != authorization.AuthorizationID ||
		reconciler.claim.Transaction != settled.Transaction || reconciler.claim.Network != string(settled.Network) {
		t.Fatalf("durable x402 claim = %+v", execution.X402SettlementClaim)
	}
}

func TestX402SettlementRejectsSubstitutionAndUnknownOutcome(t *testing.T) {
	now := time.Date(2026, 8, 28, 8, 0, 0, 0, time.UTC)
	tests := map[string]struct {
		organizationID string
		agentID        string
		mutate         func(*x402.SettleResponse)
		want           error
	}{
		"cross tenant":           {organizationID: "org_other", agentID: "agent_a", want: ErrX402SettlementBinding},
		"cross agent":            {organizationID: "org_a", agentID: "agent_other", want: ErrX402SettlementBinding},
		"failed":                 {organizationID: "org_a", agentID: "agent_a", mutate: func(s *x402.SettleResponse) { s.Success = false }, want: ErrX402SettlementResult},
		"network":                {organizationID: "org_a", agentID: "agent_a", mutate: func(s *x402.SettleResponse) { s.Network = x402.Network("eip155:1") }, want: ErrX402SettlementResult},
		"payer case":             {organizationID: "org_a", agentID: "agent_a", mutate: func(s *x402.SettleResponse) { s.Payer = "0x222222222222222222222222222222222222222A" }, want: ErrX402SettlementResult},
		"hash":                   {organizationID: "org_a", agentID: "agent_a", mutate: func(s *x402.SettleResponse) { s.Transaction = "0x1234" }, want: ErrX402SettlementResult},
		"unattested transaction": {organizationID: "org_a", agentID: "agent_a", mutate: func(s *x402.SettleResponse) { s.Transaction = "0x" + strings.Repeat("f", 64) }, want: ErrX402SettlementBinding},
		"amount":                 {organizationID: "org_a", agentID: "agent_a", mutate: func(s *x402.SettleResponse) { s.Amount = "99" }, want: ErrX402SettlementResult},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			_, registrar, authorization, privateKey, reconciler, closeFixture := issuedX402Fixture(t, now)
			defer closeFixture()
			request := validX402Request(t, authorization, privateKey, now.Unix())
			if tc.mutate != nil {
				tc.mutate(&request.Settlement)
			}
			if _, err := registrar.Register(context.Background(), tc.organizationID, tc.agentID, authorization.AuthorizationID, request); !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
			if reconciler.calls != 0 {
				t.Fatalf("invalid result reached reconciler %d times", reconciler.calls)
			}
		})
	}
}

func TestX402SettlementRequiresSignerAttestationInsideAuthorizationWindow(t *testing.T) {
	now := time.Date(2026, 8, 28, 8, 0, 0, 0, time.UTC)
	lifecycle, _, authorization, privateKey, reconciler, closeFixture := issuedX402Fixture(t, now)
	defer closeFixture()
	publicKey := privateKey.Public().(ed25519.PublicKey)
	keys, err := NewStaticBroadcastKeys([]BroadcastKey{{OrganizationID: "org_a", CustomerID: "customer_a", KeyID: "customer_signer_1", PublicKey: publicKey}})
	if err != nil {
		t.Fatal(err)
	}
	late, err := NewX402SettlementRegistrar(lifecycle, keys, reconciler, func() time.Time { return time.Unix(authorization.ExpiresAt, 0) })
	if err != nil {
		t.Fatal(err)
	}
	request := validX402Request(t, authorization, privateKey, authorization.ExpiresAt)
	if _, err := late.Register(context.Background(), "org_a", "agent_a", authorization.AuthorizationID, request); !errors.Is(err, ErrBroadcastTime) {
		t.Fatalf("late settlement error = %v", err)
	}
	if reconciler.calls != 0 {
		t.Fatalf("late result reached reconciler %d times", reconciler.calls)
	}
}

func TestX402SettlementRejectsAgentTransactionRebindingWithoutSignerKey(t *testing.T) {
	now := time.Date(2026, 8, 28, 8, 0, 0, 0, time.UTC)
	_, registrar, authorization, privateKey, reconciler, closeFixture := issuedX402Fixture(t, now)
	defer closeFixture()
	request := validX402Request(t, authorization, privateKey, now.Unix())
	substituted := "0x" + strings.Repeat("f", 64)
	request.Settlement.Transaction = substituted
	request.SignedReceipt.Receipt.TransactionHash = substituted
	if _, err := registrar.Register(context.Background(), "org_a", "agent_a", authorization.AuthorizationID, request); !errors.Is(err, ErrBroadcastSignature) {
		t.Fatalf("unsigned transaction rebinding error = %v", err)
	}
	if reconciler.calls != 0 {
		t.Fatalf("unsigned rebinding reached reconciler %d times", reconciler.calls)
	}
}

func TestX402SettlementHTTPRouteIsTenantScopedAndCommandIdempotent(t *testing.T) {
	now := time.Date(2026, 8, 28, 8, 0, 0, 0, time.UTC)
	store := newMemoryStore(func() time.Time { return now })
	store.principals[TokenDigest(agentTokenA)] = Principal{
		ID: "credential_agent_a", OrganizationID: "org_a", Kind: PrincipalAgent, Role: RoleAgent, AgentID: "agent_a",
		Scopes: []string{"x402:settlements"},
	}
	store.principals[TokenDigest(viewerTokenB)] = Principal{
		ID: "credential_agent_b", OrganizationID: "org_b", Kind: PrincipalAgent, Role: RoleAgent, AgentID: "agent_b",
		Scopes: []string{"x402:settlements"},
	}
	store.agents[agentKey("org_a", "agent_a")] = Agent{OrganizationID: "org_a", ID: "agent_a", CustomerID: "customer_a", Status: AgentActive}
	store.agents[agentKey("org_b", "agent_b")] = Agent{OrganizationID: "org_b", ID: "agent_b", CustomerID: "customer_b", Status: AgentActive}
	chain := newHealthyChain(now)
	lifecycle, journal := testLifecycle(t, store, chain, now)
	defer journal.Close()
	paymentIntent := intent("intent_x402_http", "org_a", "agent_a", "100")
	paymentIntent.Rail = envelope.RailX402
	record, err := lifecycle.Submit(context.Background(), paymentIntent)
	if err != nil {
		t.Fatal(err)
	}
	signed, err := lifecycle.Issue(context.Background(), record.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	reconciler := &captureX402Reconciler{}
	publicKey, privateKey := broadcastKeypair(t)
	keys, err := NewStaticBroadcastKeys([]BroadcastKey{{OrganizationID: "org_a", CustomerID: "customer_a", KeyID: "customer_signer_1", PublicKey: publicKey}})
	if err != nil {
		t.Fatal(err)
	}
	registrar, err := NewX402SettlementRegistrar(lifecycle, keys, reconciler, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(ServerConfig{
		Store: store, Lifecycle: lifecycle, Chain: chain, Clock: func() time.Time { return now },
		IDSource: (&sequenceIDs{}).next, X402Settlements: registrar,
	})
	if err != nil {
		t.Fatal(err)
	}
	settlementRequest := validX402Request(t, signed.Authorization, privateKey, now.Unix())
	body, _ := json.Marshal(settlementRequest)
	path := "/v1/x402/authorizations/" + signed.Authorization.AuthorizationID + "/settlements"
	invalid := settlementRequest
	invalid.SignedReceipt.Signature = "0x" + strings.Repeat("0", ed25519.SignatureSize*2)
	invalidBody, _ := json.Marshal(invalid)
	invalidRequest := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(invalidBody))
	invalidRequest.Header.Set("Authorization", "Bearer "+agentTokenA)
	invalidRequest.Header.Set("Idempotency-Key", settlementRequest.Settlement.Transaction)
	invalidRecorder := httptest.NewRecorder()
	server.ServeHTTP(invalidRecorder, invalidRequest)
	if invalidRecorder.Code != http.StatusUnauthorized || reconciler.calls != 0 || len(store.commands) != 0 {
		t.Fatalf("invalid signer receipt status=%d calls=%d commands=%d body=%s", invalidRecorder.Code, reconciler.calls, len(store.commands), invalidRecorder.Body.String())
	}

	request := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+agentTokenA)
	request.Header.Set("Idempotency-Key", settlementRequest.Settlement.Transaction)
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusCreated || reconciler.calls != 1 || !strings.Contains(recorder.Body.String(), `"x402SettlementClaim"`) {
		t.Fatalf("x402 route status=%d calls=%d body=%s", recorder.Code, reconciler.calls, recorder.Body.String())
	}

	replay := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	replay.Header.Set("Authorization", "Bearer "+agentTokenA)
	replay.Header.Set("Idempotency-Key", settlementRequest.Settlement.Transaction)
	replayRecorder := httptest.NewRecorder()
	server.ServeHTTP(replayRecorder, replay)
	if replayRecorder.Code != http.StatusOK || reconciler.calls != 1 {
		t.Fatalf("x402 command replay status=%d calls=%d body=%s", replayRecorder.Code, reconciler.calls, replayRecorder.Body.String())
	}

	crossTenant := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	crossTenant.Header.Set("Authorization", "Bearer "+viewerTokenB)
	crossTenant.Header.Set("Idempotency-Key", settlementRequest.Settlement.Transaction)
	crossTenantRecorder := httptest.NewRecorder()
	server.ServeHTTP(crossTenantRecorder, crossTenant)
	if crossTenantRecorder.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant status=%d body=%s", crossTenantRecorder.Code, crossTenantRecorder.Body.String())
	}
}
