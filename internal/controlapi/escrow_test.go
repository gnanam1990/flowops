package controlapi

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gnanam1990/flowops/internal/controlplane"
	"github.com/gnanam1990/flowops/internal/policy"
	"github.com/gnanam1990/flowops/internal/reconciliation"
	"github.com/gnanam1990/flowops/pkg/envelope"
	"github.com/gnanam1990/flowops/pkg/pilotlimits"
)

type captureEscrowRegistry struct {
	intent          reconciliation.EscrowIntent
	call            reconciliation.EscrowCall
	candidate       reconciliation.EscrowTransitionCandidate
	intentCalls     int
	transitionCalls int
	transitionErr   error
}

func (r *captureEscrowRegistry) RegisterEscrowIntent(_ context.Context, intent reconciliation.EscrowIntent) (reconciliation.EscrowCall, error) {
	r.intentCalls++
	r.intent = intent
	r.call = reconciliation.EscrowCall{Intent: intent, State: reconciliation.EscrowPositionRegistered, RegisteredAt: time.Unix(1, 0)}
	return r.call, nil
}

func (r *captureEscrowRegistry) RegisterEscrowTransition(_ context.Context, organizationID, callID string, candidate reconciliation.EscrowTransitionCandidate) (reconciliation.EscrowCall, error) {
	r.transitionCalls++
	r.candidate = candidate
	if r.transitionErr != nil {
		return reconciliation.EscrowCall{}, r.transitionErr
	}
	if r.call.Intent.OrganizationID != organizationID || r.call.Intent.CallID != callID {
		return reconciliation.EscrowCall{}, reconciliation.ErrUnknownExecution
	}
	return r.call, nil
}

func (r *captureEscrowRegistry) EscrowCall(organizationID, callID string) (reconciliation.EscrowCall, bool) {
	return r.call, r.call.Intent.OrganizationID == organizationID && r.call.Intent.CallID == callID
}

func TestEscrowRegistrarDerivesImmutableIntentOnlyFromIssuedAuthorization(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 14, 17, 0, 0, 0, time.UTC)
	lifecycle, journal, authorization := issuedEscrowLifecycle(t, now)
	defer journal.Close()
	registry := &captureEscrowRegistry{}
	registrar, err := NewEscrowRegistrar(lifecycle, registry, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	call, err := registrar.RegisterIntent(context.Background(), authorization.Authorization.OrganizationID, authorization.Authorization.AuthorizationID)
	if err != nil {
		t.Fatal(err)
	}
	terms := authorization.Authorization.Escrow
	got := registry.intent
	if registry.intentCalls != 1 || got.AuthorizationID != authorization.Authorization.AuthorizationID || got.IntentDigest == "" || got.OrganizationID != authorization.Authorization.OrganizationID || got.CustomerID != authorization.Authorization.CustomerID || got.AgentID != authorization.Authorization.AgentID || got.TaskID != authorization.Authorization.TaskID || got.ChainID != authorization.Authorization.ChainID || got.Asset != authorization.Authorization.Asset || got.AmountAtomic != authorization.Authorization.AmountAtomic || got.Contract != terms.Contract || got.Buyer != terms.Buyer || got.Provider != terms.Provider || got.CallID != terms.CallID || call.Intent.CallID != terms.CallID {
		t.Fatalf("derived escrow intent = %+v calls=%d call=%+v", got, registry.intentCalls, call)
	}
	if _, err := registrar.RegisterIntent(context.Background(), "org_other", authorization.Authorization.AuthorizationID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-tenant registration error = %v", err)
	}
	expiredRegistry := &captureEscrowRegistry{}
	expiredRegistrar, err := NewEscrowRegistrar(lifecycle, expiredRegistry, func() time.Time { return now.Add(6 * time.Minute) })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := expiredRegistrar.RegisterIntent(context.Background(), authorization.Authorization.OrganizationID, authorization.Authorization.AuthorizationID); !errors.Is(err, ErrEscrowBinding) || expiredRegistry.intentCalls != 0 {
		t.Fatalf("expired registration error=%v calls=%d", err, expiredRegistry.intentCalls)
	}
	candidate := reconciliation.EscrowTransitionCandidate{Action: reconciliation.EscrowFund, TransactionHash: "0x" + strings.Repeat("a", 64)}
	if _, err := registrar.RegisterTransition(context.Background(), got.OrganizationID, got.CallID, candidate); err != nil || registry.transitionCalls != 1 || registry.candidate.TransactionHash != candidate.TransactionHash {
		t.Fatalf("transition registration = calls=%d candidate=%+v err=%v", registry.transitionCalls, registry.candidate, err)
	}
	if _, err := registrar.RegisterTransition(context.Background(), "org_other", got.CallID, candidate); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-tenant transition error = %v", err)
	}
}

func TestEscrowAPIRequiresTenantAuthorizationAndTransactionHashIdempotency(t *testing.T) {
	now := time.Date(2026, 8, 14, 17, 30, 0, 0, time.UTC)
	const otherOwnerToken = "owner_token_b_12345678901234567890"
	const ownerWithoutStepUpToken = "owner_no_step_up_123456789012345"
	const agentToken = "agent_token_a_12345678901234567890"
	const otherAgentToken = "agent_token_b_12345678901234567890"
	lifecycle, journal, authorization := issuedEscrowLifecycle(t, now)
	defer journal.Close()
	registry := &captureEscrowRegistry{}
	registrar, err := NewEscrowRegistrar(lifecycle, registry, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	store := newMemoryStore(func() time.Time { return now })
	store.principals[TokenDigest(ownerTokenA)] = Principal{ID: "owner_a", OrganizationID: "org_a", Kind: PrincipalHuman, Role: RoleOwner, StepUpUntil: now.Add(time.Hour)}
	store.principals[TokenDigest(otherOwnerToken)] = Principal{ID: "owner_b", OrganizationID: "org_b", Kind: PrincipalHuman, Role: RoleOwner, StepUpUntil: now.Add(time.Hour)}
	store.principals[TokenDigest(ownerWithoutStepUpToken)] = Principal{ID: "owner_no_step", OrganizationID: "org_a", Kind: PrincipalHuman, Role: RoleOwner}
	store.principals[TokenDigest(agentToken)] = Principal{ID: "agent_a", OrganizationID: "org_a", Kind: PrincipalAgent, Role: RoleAgent, AgentID: "agent_a", Scopes: []string{"escrow:transitions"}}
	store.principals[TokenDigest(otherAgentToken)] = Principal{ID: "agent_b", OrganizationID: "org_a", Kind: PrincipalAgent, Role: RoleAgent, AgentID: "agent_b", Scopes: []string{"escrow:register", "records:read"}}
	store.agents[agentKey("org_a", "agent_a")] = Agent{OrganizationID: "org_a", ID: "agent_a", CustomerID: "customer_a", Name: "Escrow agent", Status: AgentActive, UpdatedAt: now}
	store.agents[agentKey("org_a", "agent_b")] = Agent{OrganizationID: "org_a", ID: "agent_b", CustomerID: "customer_a", Name: "Other agent", Status: AgentActive, UpdatedAt: now}
	chain := newHealthyChain(now)
	server, err := NewServer(ServerConfig{Store: store, Lifecycle: lifecycle, Chain: chain, Clock: func() time.Time { return now }, Escrow: registrar})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server)
	defer httpServer.Close()
	authorizationID := authorization.Authorization.AuthorizationID
	path := httpServer.URL + "/v1/escrow/intents/" + authorizationID
	status, _ := doRequest(t, httpServer.Client(), http.MethodPost, path, otherOwnerToken, authorizationID, nil)
	if status != http.StatusNotFound || registry.intentCalls != 0 {
		t.Fatalf("cross-tenant intent status=%d calls=%d", status, registry.intentCalls)
	}
	status, _ = doRequest(t, httpServer.Client(), http.MethodPost, path, otherAgentToken, authorizationID, nil)
	if status != http.StatusNotFound || registry.intentCalls != 0 {
		t.Fatalf("cross-agent intent status=%d calls=%d", status, registry.intentCalls)
	}
	status, _ = doRequest(t, httpServer.Client(), http.MethodPost, path, ownerTokenA, authorizationID, nil)
	if status != http.StatusCreated || registry.intentCalls != 1 {
		t.Fatalf("intent status=%d calls=%d", status, registry.intentCalls)
	}
	candidate := reconciliation.EscrowTransitionCandidate{Action: reconciliation.EscrowFund, TransactionHash: "0x" + strings.Repeat("a", 64)}
	transitionPath := httpServer.URL + "/v1/escrow/calls/" + authorization.Authorization.Escrow.CallID + "/transitions"
	status, _ = doRequest(t, httpServer.Client(), http.MethodPost, transitionPath, ownerWithoutStepUpToken, candidate.TransactionHash, candidate)
	if status != http.StatusForbidden || registry.transitionCalls != 0 {
		t.Fatalf("missing step-up status=%d calls=%d", status, registry.transitionCalls)
	}
	status, _ = doRequest(t, httpServer.Client(), http.MethodPost, transitionPath, agentToken, candidate.TransactionHash, candidate)
	if status != http.StatusForbidden || registry.transitionCalls != 0 {
		t.Fatalf("agent transition status=%d calls=%d", status, registry.transitionCalls)
	}
	status, _ = doRequest(t, httpServer.Client(), http.MethodPost, transitionPath, ownerTokenA, "wrong_key", candidate)
	if status != http.StatusConflict || registry.transitionCalls != 0 {
		t.Fatalf("mismatched idempotency status=%d calls=%d", status, registry.transitionCalls)
	}
	status, body := doRequest(t, httpServer.Client(), http.MethodPost, transitionPath, ownerTokenA, candidate.TransactionHash, candidate)
	if status != http.StatusConflict || registry.transitionCalls != 0 || !strings.Contains(fmt.Sprint(body), "ATTESTED_FUND_REQUIRED") {
		t.Fatalf("manual FUND status=%d calls=%d body=%v", status, registry.transitionCalls, body)
	}
	invalid := reconciliation.EscrowTransitionCandidate{Action: "MINT", TransactionHash: "0x" + strings.Repeat("b", 64)}
	registry.transitionErr = fmt.Errorf("%w: unsupported action", reconciliation.ErrEscrowTransition)
	status, _ = doRequest(t, httpServer.Client(), http.MethodPost, transitionPath, ownerTokenA, invalid.TransactionHash, invalid)
	if status != http.StatusBadRequest || registry.transitionCalls != 1 {
		t.Fatalf("invalid transition status=%d calls=%d", status, registry.transitionCalls)
	}
	status, _ = doRequest(t, httpServer.Client(), http.MethodGet, httpServer.URL+"/v1/escrow/calls/"+authorization.Authorization.Escrow.CallID, ownerTokenA, "", nil)
	if status != http.StatusOK {
		t.Fatalf("escrow read status=%d", status)
	}
	status, _ = doRequest(t, httpServer.Client(), http.MethodGet, httpServer.URL+"/v1/escrow/calls/"+authorization.Authorization.Escrow.CallID, otherAgentToken, "", nil)
	if status != http.StatusNotFound {
		t.Fatalf("cross-agent escrow read status=%d", status)
	}
}

func issuedEscrowLifecycle(t *testing.T, now time.Time) (*controlplane.Lifecycle, *controlplane.Journal, envelope.SignedAuthorization) {
	t.Helper()
	provider := "0xc2f0967c4df966636e4ac1dad40abda65536cbb6"
	contract := "0x86e145397f58e71c134c0e054320db929483227a"
	buyer := "0x079bdde909e28e437768a06d7001eb40896668d4"
	asset := "0x036cbd53842c5426634e7929541ec2318f3dcf7e"
	taskDigest := "0x" + strings.Repeat("31", 32)
	requestDigest := "0x" + strings.Repeat("42", 32)
	callID, err := envelope.DeriveEscrowCallID(84532, contract, buyer, taskDigest, requestDigest)
	if err != nil {
		t.Fatal(err)
	}
	terms := &envelope.EscrowTerms{
		Contract: contract, Buyer: buyer, Provider: provider, CallID: callID,
		TaskDigest: taskDigest, RequestDigest: requestDigest,
		AcknowledgeBy: uint64(now.Add(time.Hour).Unix()), DeliverBy: uint64(now.Add(2 * time.Hour).Unix()), ReleaseWindow: 3600,
	}
	policyEngine, err := policy.Compile(policy.Config{
		Version: "policy_escrow_1", Enabled: true, AllowedChainIDs: []uint64{84532}, AllowedRails: []envelope.Rail{envelope.RailEscrow},
		AllowedAssets: []string{asset}, AllowedRecipients: []string{provider}, PerActionLimitAtomic: "100", AutoApproveThresholdAtomic: "100",
		TaskBudgetAtomic: "100", DailyBudgetAtomic: "100",
	})
	if err != nil {
		t.Fatal(err)
	}
	journal, err := controlplane.OpenJournal(filepath.Join(t.TempDir(), "escrow-control.log"))
	if err != nil {
		t.Fatal(err)
	}
	seed, _ := hex.DecodeString(strings.Repeat("17", ed25519.SeedSize))
	privateKey := ed25519.NewKeyFromSeed(seed)
	pilot, err := pilotlimits.Compile(pilotlimits.Config{MaxPerActionAtomic: "100", MaxOutstandingAtomic: "100"})
	if err != nil {
		t.Fatal(err)
	}
	var ids atomic.Uint64
	lifecycle, err := controlplane.New(controlplane.Config{
		Policy: policyEngine, ActivePolicyVersion: func() string { return "policy_escrow_1" }, Journal: journal,
		FreezeGate: &testAllowGate{}, ChainGate: &testAllowGate{}, Clock: func() time.Time { return now },
		ApprovalTTL: 10 * time.Minute, AuthorizationTTL: 5 * time.Minute,
		RequestIDSource:       func() (string, error) { return fmt.Sprintf("req_%d", ids.Add(1)), nil },
		AuthorizationIDSource: func() (string, error) { return fmt.Sprintf("auth_%d", ids.Add(1)), nil },
		NonceSource:           func() (string, error) { return fmt.Sprintf("0x%064x", ids.Add(1)), nil },
		EnvelopeKeyID:         "flowops_control_escrow", EnvelopePrivateKey: privateKey, PilotLimits: pilot,
	})
	if err != nil {
		journal.Close()
		t.Fatal(err)
	}
	record, err := lifecycle.Submit(context.Background(), controlplane.PaymentIntent{
		IntentID: "intent_escrow_1", OrganizationID: "org_a", CustomerID: "customer_a", AgentID: "agent_a", TaskID: "task_a", ActionID: "action_a",
		Rail: envelope.RailEscrow, ChainID: 84532, Recipient: provider, Asset: asset, AmountAtomic: "100",
		Resource: "https://evidence.flowops.example/v1/fetch", Category: "research", Purpose: "delivery assured fetch", Escrow: terms,
	})
	if err != nil {
		journal.Close()
		t.Fatal(err)
	}
	authorization, err := lifecycle.Issue(context.Background(), record.RequestID)
	if err != nil {
		journal.Close()
		t.Fatal(err)
	}
	return lifecycle, journal, authorization
}

type testAllowGate struct{}

func (*testAllowGate) Check(context.Context, string, string, string) error { return nil }
func (*testAllowGate) CheckChain(context.Context, uint64) error            { return nil }
