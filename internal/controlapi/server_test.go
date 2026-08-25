package controlapi

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gnanam1990/flowops/internal/controlplane"
	"github.com/gnanam1990/flowops/internal/policy"
	"github.com/gnanam1990/flowops/internal/reconciliation"
	"github.com/gnanam1990/flowops/pkg/envelope"
	"github.com/gnanam1990/flowops/pkg/pilotlimits"
	"github.com/jackc/pgx/v5/pgconn"
)

const (
	testUSDC      = "0x036cbd53842c5426634e7929541ec2318f3dcf7e"
	testRecipient = "0x1111111111111111111111111111111111111111"
	agentTokenA   = "fo_sbx_agent_a_000000000000000000000001"
	ownerTokenA   = "fo_sbx_owner_a_000000000000000000000001"
	approverToken = "fo_sbx_approver_000000000000000000000001"
	weakTokenA    = "fo_sbx_weak_a_000000000000000000000001"
	viewerTokenB  = "fo_sbx_viewer_b_000000000000000000000001"
)

type memoryStore struct {
	mu                   sync.Mutex
	principals           map[[32]byte]Principal
	agents               map[string]Agent
	commands             map[string]Command
	commandKeys          map[string]string
	now                  func() time.Time
	completionContextErr error
	memberships          map[string]SiteMembership
	membershipEmails     map[string][32]byte
	organizationPaused   map[string]bool
	exchangeToken        string
	siteSessions         *SiteSessionCodec
}

type dashboardErrorStore struct {
	*memoryStore
	err error
}

func (s dashboardErrorStore) DashboardProjection(context.Context, string, time.Time) (DashboardProjection, error) {
	return DashboardProjection{}, s.err
}

func newMemoryStore(now func() time.Time) *memoryStore {
	return &memoryStore{
		principals: make(map[[32]byte]Principal), agents: make(map[string]Agent),
		commands: make(map[string]Command), commandKeys: make(map[string]string), memberships: make(map[string]SiteMembership),
		membershipEmails: make(map[string][32]byte), organizationPaused: make(map[string]bool), now: now,
	}
}

func agentKey(organizationID, agentID string) string { return organizationID + "\x00" + agentID }

func (s *memoryStore) Authenticate(_ context.Context, token string) (Principal, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.HasPrefix(token, siteSessionPrefix) {
		if s.siteSessions == nil {
			return Principal{}, ErrUnauthenticated
		}
		membership, err := s.siteSessions.Verify(token)
		stored, ok := s.memberships[membership.ID]
		if err != nil || !ok || stored != membership {
			return Principal{}, ErrUnauthenticated
		}
		return Principal{ID: membership.PrincipalID, OrganizationID: membership.OrganizationID, Kind: PrincipalHuman, Role: membership.Role, ReadOnly: true}, nil
	}
	digest := TokenDigest(token)
	principal, ok := s.principals[digest]
	if !ok {
		return Principal{}, ErrUnauthenticated
	}
	if principal.Kind == PrincipalAgent {
		agent, exists := s.agents[agentKey(principal.OrganizationID, principal.AgentID)]
		if !exists || agent.Status == AgentRevoked || agent.Status == AgentArchived {
			return Principal{}, ErrUnauthenticated
		}
	}
	return principal, nil
}

func (s *memoryStore) ExchangeSiteIdentity(_ context.Context, siteProjectID, siteUserKey, email, exchangeToken string) (SiteMembership, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if exchangeToken != s.exchangeToken {
		return SiteMembership{}, ErrUnauthenticated
	}
	emailDigest, err := normalizedEmailDigest(email)
	if err != nil {
		return SiteMembership{}, ErrUnauthenticated
	}
	for _, membership := range s.memberships {
		if membership.SiteProjectID == siteProjectID && membership.SiteUserKey == siteUserKey && s.membershipEmails[membership.ID] == emailDigest {
			return membership, nil
		}
	}
	return SiteMembership{}, ErrUnauthenticated
}

func (s *memoryStore) Organization(_ context.Context, organizationID string) (Organization, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, agent := range s.agents {
		if agent.OrganizationID == organizationID {
			return Organization{ID: organizationID, Name: "Northstar Labs", AuthorizationsPaused: s.organizationPaused[organizationID]}, nil
		}
	}
	return Organization{}, ErrNotFound
}

func (s *memoryStore) PauseOrganization(_ context.Context, organizationID, _, _ string) (Organization, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	found := false
	for _, agent := range s.agents {
		if agent.OrganizationID == organizationID {
			found = true
			break
		}
	}
	if !found {
		return Organization{}, ErrNotFound
	}
	s.organizationPaused[organizationID] = true
	return Organization{ID: organizationID, Name: "Northstar Labs", AuthorizationsPaused: true}, nil
}

func (s *memoryStore) Agent(_ context.Context, organizationID, agentID string) (Agent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	agent, ok := s.agents[agentKey(organizationID, agentID)]
	if !ok {
		return Agent{}, ErrNotFound
	}
	return agent, nil
}

func (s *memoryStore) ListAgents(_ context.Context, organizationID string) ([]Agent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	agents := make([]Agent, 0)
	for _, agent := range s.agents {
		if agent.OrganizationID == organizationID {
			agents = append(agents, agent)
		}
	}
	return agents, nil
}

func (s *memoryStore) SetAgentStatus(_ context.Context, organizationID, agentID string, status AgentStatus, _, _ string) (Agent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := agentKey(organizationID, agentID)
	agent, ok := s.agents[key]
	if !ok {
		return Agent{}, ErrNotFound
	}
	if agent.Status != AgentPaused && agent.Status != AgentActive {
		return Agent{}, ErrForbidden
	}
	agent.Status, agent.UpdatedAt = status, s.now().UTC()
	s.agents[key] = agent
	return agent, nil
}

func (s *memoryStore) WithActiveAgentLock(_ context.Context, organizationID, agentID string, operation func() error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.organizationPaused[organizationID] {
		return fmt.Errorf("%w while organization authorizations are paused", controlplane.ErrFrozen)
	}
	agent, ok := s.agents[agentKey(organizationID, agentID)]
	if !ok {
		return ErrNotFound
	}
	if agent.Status != AgentActive {
		return fmt.Errorf("%w while status is %s", controlplane.ErrFrozen, agent.Status)
	}
	return operation()
}

func (s *memoryStore) BeginCommand(_ context.Context, command Command) (Command, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := command.OrganizationID + "\x00" + command.Kind + "\x00" + command.IdempotencyKey
	if commandID, ok := s.commandKeys[key]; ok {
		existing := s.commands[commandID]
		if existing.InputDigest != command.InputDigest {
			return Command{}, false, ErrIdempotencyConflict
		}
		return cloneCommand(existing), false, nil
	}
	s.commands[command.ID] = cloneCommand(command)
	s.commandKeys[key] = command.ID
	return cloneCommand(command), true, nil
}

func (s *memoryStore) CompleteCommand(ctx context.Context, organizationID, commandID string, state CommandState, result json.RawMessage, errorCode string) (Command, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.completionContextErr = ctx.Err()
	command, ok := s.commands[commandID]
	if !ok || command.OrganizationID != organizationID {
		return Command{}, ErrNotFound
	}
	if command.State != CommandPending {
		return cloneCommand(command), ErrCommandAlreadyClosed
	}
	completed := s.now().UTC()
	command.State, command.Result, command.ErrorCode, command.CompletedAt = state, append(json.RawMessage(nil), result...), errorCode, &completed
	s.commands[commandID] = cloneCommand(command)
	return command, nil
}

func (s *memoryStore) Command(_ context.Context, organizationID, commandID string) (Command, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	command, ok := s.commands[commandID]
	if !ok || command.OrganizationID != organizationID {
		return Command{}, ErrNotFound
	}
	return cloneCommand(command), nil
}

func cloneCommand(command Command) Command {
	command.Result = append(json.RawMessage(nil), command.Result...)
	if command.CompletedAt != nil {
		completed := *command.CompletedAt
		command.CompletedAt = &completed
	}
	return command
}

type mutableChain struct {
	mu          sync.Mutex
	status      reconciliation.ChainStatus
	haltErr     error
	resumeErr   error
	views       map[string]reconciliation.OrganizationView
	quarantined []string
}

func (c *mutableChain) ForceHalt(_ context.Context, _ string, reason string) (reconciliation.ChainStatus, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.haltErr != nil {
		return reconciliation.ChainStatus{}, c.haltErr
	}
	if strings.TrimSpace(reason) == "" {
		return reconciliation.ChainStatus{}, reconciliation.ErrInvalidHaltReason
	}
	c.status.State = reconciliation.StateHalted
	c.status.Reason = "manual halt: " + reason
	c.status.AuthorizationsPaused = true
	return c.status, nil
}

func (c *mutableChain) Resume(_ context.Context, operator string) (reconciliation.ChainStatus, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.resumeErr != nil {
		return reconciliation.ChainStatus{}, c.resumeErr
	}
	if operator == "" {
		return reconciliation.ChainStatus{}, reconciliation.ErrInvalidOperator
	}
	if c.status.State == reconciliation.StateHealthy && c.status.Reason == "manual recovery release by "+operator {
		return c.status, nil
	}
	if !c.status.ReadyForManualResume {
		return reconciliation.ChainStatus{}, reconciliation.ErrResumeBlocked
	}
	c.status.State = reconciliation.StateHealthy
	c.status.Reason = "manual recovery release by " + operator
	c.status.AuthorizationsPaused = false
	c.status.ReadyForManualResume = false
	return c.status, nil
}

func TestOperatorChainControlRequiresDedicatedKeyAndRecoveryReadiness(t *testing.T) {
	server, _, chain, _, journal, _ := setupServer(t)
	defer server.Close()
	defer journal.Close()
	client := server.Client()
	operatorToken := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("o", 32)))

	for name, token := range map[string]string{"missing": "", "tenant": ownerTokenA, "wrong": base64.StdEncoding.EncodeToString([]byte(strings.Repeat("x", 32)))} {
		status, body := doRequest(t, client, http.MethodPost, server.URL+"/v1/operator/chain/halt", token, "", operatorHaltRequest{Operator: "operator_alice", Reason: "drill"})
		if status != http.StatusUnauthorized || body["error"].(map[string]any)["code"] != "UNAUTHENTICATED" {
			t.Fatalf("%s operator authentication = %d %+v", name, status, body)
		}
	}
	status, body := doRequest(t, client, http.MethodPost, server.URL+"/v1/operator/chain/halt", operatorToken, "", operatorHaltRequest{Operator: "operator_alice", Reason: ""})
	if status != http.StatusBadRequest || body["error"].(map[string]any)["code"] != "INVALID_HALT" {
		t.Fatalf("invalid halt = %d %+v", status, body)
	}
	status, body = doRequest(t, client, http.MethodPost, server.URL+"/v1/operator/chain/resume", operatorToken, "", operatorResumeRequest{Operator: ""})
	if status != http.StatusBadRequest || body["error"].(map[string]any)["code"] != "INVALID_RESUME" {
		t.Fatalf("invalid resume = %d %+v", status, body)
	}

	status, body = doRequest(t, client, http.MethodPost, server.URL+"/v1/operator/chain/halt", operatorToken, "", operatorHaltRequest{Operator: "operator_alice", Reason: "provider disagreement drill"})
	if status != http.StatusOK || body["chain"].(map[string]any)["state"] != string(reconciliation.StateHalted) {
		t.Fatalf("operator halt = %d %+v", status, body)
	}
	status, body = doRequest(t, client, http.MethodPost, server.URL+"/v1/operator/chain/resume", operatorToken, "", operatorResumeRequest{Operator: "operator_alice"})
	if status != http.StatusConflict || body["error"].(map[string]any)["code"] != "RESUME_BLOCKED" {
		t.Fatalf("unsafe resume = %d %+v", status, body)
	}

	chain.mu.Lock()
	chain.status.State = reconciliation.StateRecovering
	chain.status.ReadyForManualResume = true
	chain.mu.Unlock()
	for attempt := 0; attempt < 2; attempt++ {
		status, body = doRequest(t, client, http.MethodPost, server.URL+"/v1/operator/chain/resume", operatorToken, "", operatorResumeRequest{Operator: "operator_alice"})
		if status != http.StatusOK || body["chain"].(map[string]any)["state"] != string(reconciliation.StateHealthy) {
			t.Fatalf("operator resume attempt %d = %d %+v", attempt, status, body)
		}
	}
}

func TestOperatorChainControlClassifiesUncommittedJournalEventsAsRetriable(t *testing.T) {
	server, _, chain, _, journal, _ := setupServer(t)
	defer server.Close()
	defer journal.Close()
	operatorToken := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("o", 32)))
	chain.haltErr = errors.New("journal sync failed")
	status, body := doRequest(t, server.Client(), http.MethodPost, server.URL+"/v1/operator/chain/halt", operatorToken, "", operatorHaltRequest{Operator: "operator_alice", Reason: "drill"})
	apiError := body["error"].(map[string]any)
	if status != http.StatusServiceUnavailable || apiError["code"] != "CONTROL_EVENT_NOT_COMMITTED" || apiError["retriable"] != true || apiError["message"] != "request could not be completed" {
		t.Fatalf("halt journal failure = %d %+v", status, body)
	}
	chain.haltErr = nil
	chain.resumeErr = errors.New("journal sync failed")
	status, body = doRequest(t, server.Client(), http.MethodPost, server.URL+"/v1/operator/chain/resume", operatorToken, "", operatorResumeRequest{Operator: "operator_alice"})
	apiError = body["error"].(map[string]any)
	if status != http.StatusServiceUnavailable || apiError["code"] != "CONTROL_EVENT_NOT_COMMITTED" || apiError["retriable"] != true {
		t.Fatalf("resume journal failure = %d %+v", status, body)
	}
}

func TestOperatorReconciliationIsTenantBoundAndQuarantinePreservesUnprovenOutcome(t *testing.T) {
	server, _, chain, _, journal, now := setupServer(t)
	defer server.Close()
	defer journal.Close()
	operatorToken := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("o", 32)))
	chain.mu.Lock()
	chain.views["org_a"] = reconciliation.OrganizationView{
		Available: true, GeneratedAt: now,
		Recovery: reconciliation.RecoveryProgress{TotalCandidates: 1, UnresolvedOutcomes: 1},
		Exceptions: []reconciliation.Exception{{
			ID: "exec_pending", Kind: "DIRECT_EXECUTION", State: string(reconciliation.ExecutionPendingChainRecovery),
			Asset: testUSDC, AmountAtomic: "100", FirstObservedAt: now, OperatorActionNeeded: true,
		}},
	}
	chain.mu.Unlock()

	status, body := doRequest(t, server.Client(), http.MethodGet, server.URL+"/v1/operator/reconciliation?organizationId=org_a", ownerTokenA, "", nil)
	if status != http.StatusUnauthorized || body["error"].(map[string]any)["code"] != "UNAUTHENTICATED" {
		t.Fatalf("tenant credential reached operator read = %d %+v", status, body)
	}
	status, body = doRequest(t, server.Client(), http.MethodGet, server.URL+"/v1/operator/reconciliation?organizationId=org_a", operatorToken, "", nil)
	if status != http.StatusOK || body["reconciliation"].(map[string]any)["available"] != true {
		t.Fatalf("operator reconciliation = %d %+v", status, body)
	}

	request := operatorQuarantineRequest{OrganizationID: "org_b", Operator: "operator_alice", Disposition: "DROPPED_UNPROVEN", Reason: "nonce outcome not proved"}
	status, body = doRequest(t, server.Client(), http.MethodPost, server.URL+"/v1/operator/executions/exec_pending/quarantine", operatorToken, "", request)
	if status != http.StatusNotFound {
		t.Fatalf("cross-tenant quarantine = %d %+v", status, body)
	}
	request.OrganizationID = "org_a"
	request.Disposition = "DROPPED"
	status, body = doRequest(t, server.Client(), http.MethodPost, server.URL+"/v1/operator/executions/exec_pending/quarantine", operatorToken, "", request)
	if status != http.StatusBadRequest || body["error"].(map[string]any)["code"] != "INVALID_QUARANTINE" {
		t.Fatalf("proved-drop claim accepted = %d %+v", status, body)
	}
	request.Disposition = "DROPPED_UNPROVEN"
	status, body = doRequest(t, server.Client(), http.MethodPost, server.URL+"/v1/operator/executions/exec_pending/quarantine", operatorToken, "", request)
	if status != http.StatusOK || body["execution"].(map[string]any)["state"] != string(reconciliation.ExecutionQuarantined) {
		t.Fatalf("safe quarantine = %d %+v", status, body)
	}
	chain.mu.Lock()
	defer chain.mu.Unlock()
	if len(chain.quarantined) != 1 || chain.quarantined[0] != "exec_pending\x00operator_alice\x00DROPPED_UNPROVEN: nonce outcome not proved" {
		t.Fatalf("quarantine audit input = %#v", chain.quarantined)
	}
}

func newHealthyChain(now time.Time) *mutableChain {
	return &mutableChain{status: reconciliation.ChainStatus{
		ChainID: 84532, State: reconciliation.StateHealthy, StateChangedAt: now.Add(-time.Minute),
		LastTrusted: &reconciliation.Checkpoint{BlockNumber: 100, BlockHash: "0x" + strings.Repeat("1", 64), BlockTime: now.Add(-time.Second), ObservedAt: now},
	}}
}

func (c *mutableChain) CheckChain(context.Context, uint64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.status.State != reconciliation.StateHealthy {
		return reconciliation.ErrChainUnavailable
	}
	return nil
}

func (c *mutableChain) Status() reconciliation.ChainStatus {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.status
}

func (c *mutableChain) OrganizationView(organizationID string) reconciliation.OrganizationView {
	c.mu.Lock()
	defer c.mu.Unlock()
	view := c.views[organizationID]
	view.Chain = c.status
	return view
}

func (c *mutableChain) QuarantineForOrganization(_ context.Context, _, executionID, operator, reason string) (reconciliation.Execution, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.quarantined = append(c.quarantined, executionID+"\x00"+operator+"\x00"+reason)
	return reconciliation.Execution{
		Expected: reconciliation.ExpectedExecution{ExecutionID: executionID},
		State:    reconciliation.ExecutionQuarantined, Resolution: "manual quarantine by " + operator + ": " + reason,
	}, nil
}

func (c *mutableChain) halt(now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.status.State = reconciliation.StateHalted
	c.status.StateChangedAt = now
	c.status.AuthorizationsPaused = true
}

type sequenceIDs struct{ value atomic.Uint64 }

func (s *sequenceIDs) next(prefix string) (string, error) {
	return fmt.Sprintf("%s_%d", prefix, s.value.Add(1)), nil
}

func testLifecycle(t *testing.T, store Store, chain *mutableChain, now time.Time) (*controlplane.Lifecycle, *controlplane.Journal) {
	return testLifecycleWithClock(t, store, chain, func() time.Time { return now })
}

func testLifecycleWithClock(t *testing.T, store Store, chain *mutableChain, clock func() time.Time) (*controlplane.Lifecycle, *controlplane.Journal) {
	t.Helper()
	engine, err := policy.Compile(policy.Config{
		Version: "policy_1", Enabled: true, AllowedChainIDs: []uint64{84532},
		AllowedRails: []envelope.Rail{envelope.RailX402, envelope.RailDirect}, AllowedAssets: []string{testUSDC},
		AllowedRecipients: []string{testRecipient}, PerActionLimitAtomic: "200",
		AutoApproveThresholdAtomic: "100", TaskBudgetAtomic: "1000", DailyBudgetAtomic: "1000",
	})
	if err != nil {
		t.Fatal(err)
	}
	journal, err := controlplane.OpenJournal(filepath.Join(t.TempDir(), "control.log"))
	if err != nil {
		t.Fatal(err)
	}
	seed, _ := hex.DecodeString(strings.Repeat("07", ed25519.SeedSize))
	privateKey := ed25519.NewKeyFromSeed(seed)
	pilot, err := pilotlimits.Compile(pilotlimits.Config{MaxPerActionAtomic: "200", MaxOutstandingAtomic: "1000"})
	if err != nil {
		t.Fatal(err)
	}
	var request, authorization, nonce atomic.Uint64
	lifecycle, err := controlplane.New(controlplane.Config{
		Policy: engine, ActivePolicyVersion: func() string { return "policy_1" }, Journal: journal,
		FreezeGate: AgentFreezeGate{Store: store}, ChainGate: chain, Clock: clock,
		ApprovalTTL: 10 * time.Minute, AuthorizationTTL: 5 * time.Minute,
		RequestIDSource:       func() (string, error) { return fmt.Sprintf("req_%d", request.Add(1)), nil },
		AuthorizationIDSource: func() (string, error) { return fmt.Sprintf("auth_%d", authorization.Add(1)), nil },
		NonceSource:           func() (string, error) { return fmt.Sprintf("0x%064x", nonce.Add(1)), nil },
		EnvelopeKeyID:         "flowops_control_1", EnvelopePrivateKey: privateKey,
		PilotLimits: pilot,
	})
	if err != nil {
		journal.Close()
		t.Fatal(err)
	}
	return lifecycle, journal
}

func TestCommandCompletionSurvivesClientCancellation(t *testing.T) {
	now := time.Date(2026, 8, 12, 9, 0, 0, 0, time.UTC)
	store := newMemoryStore(func() time.Time { return now })
	command := Command{
		ID: "cmd_cancel", OrganizationID: "org_a", ActorID: "owner_a", Kind: "agent.pause",
		TargetID: "agent_a", IdempotencyKey: "pause_1", InputDigest: "0xproof", State: CommandPending, CreatedAt: now,
	}
	store.commands[command.ID] = command
	server := &Server{store: store, commandCompletionTimeout: time.Second}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	request := httptest.NewRequest(http.MethodPost, "/", nil).WithContext(ctx)
	recorder := httptest.NewRecorder()
	server.succeedCommand(recorder, request, command, map[string]string{"status": "PAUSED"}, http.StatusOK)
	if recorder.Code != http.StatusOK || store.completionContextErr != nil {
		t.Fatalf("completion status=%d context=%v", recorder.Code, store.completionContextErr)
	}
}

func TestApprovalReadsSweepExpiredReservations(t *testing.T) {
	current := time.Date(2026, 8, 12, 9, 0, 0, 0, time.UTC)
	store := newMemoryStore(func() time.Time { return current })
	store.principals[TokenDigest(ownerTokenA)] = Principal{ID: "owner_a", OrganizationID: "org_a", Kind: PrincipalHuman, Role: RoleOwner, StepUpUntil: current.Add(time.Hour)}
	store.agents[agentKey("org_a", "agent_a")] = Agent{OrganizationID: "org_a", ID: "agent_a", CustomerID: "customer_a", Name: "Research", Status: AgentActive, UpdatedAt: current}
	chain := newHealthyChain(current)
	lifecycle, journal := testLifecycleWithClock(t, store, chain, func() time.Time { return current })
	defer journal.Close()
	created, err := lifecycle.Submit(context.Background(), intent("intent_expiring", "org_a", "agent_a", "150"))
	if err != nil || created.State != controlplane.StatePendingApproval {
		t.Fatalf("create pending intent = %+v, %v", created, err)
	}
	server, err := NewServer(ServerConfig{Store: store, Lifecycle: lifecycle, Chain: chain, Clock: func() time.Time { return current }})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server)
	defer httpServer.Close()
	current = current.Add(11 * time.Minute)
	status, body := doRequest(t, httpServer.Client(), http.MethodGet, httpServer.URL+"/v1/approvals", ownerTokenA, "", nil)
	if status != http.StatusOK || len(body["approvals"].([]any)) != 0 {
		t.Fatalf("expired approval list = %d %+v", status, body)
	}
	record, ok := lifecycle.Get(created.RequestID)
	if !ok || record.State != controlplane.StateExpired || len(journal.Events()) != 2 {
		t.Fatalf("expired record=%+v exists=%v events=%d", record, ok, len(journal.Events()))
	}
}

func setupServer(t *testing.T) (*httptest.Server, *memoryStore, *mutableChain, *controlplane.Lifecycle, *controlplane.Journal, time.Time) {
	t.Helper()
	server, store, chain, lifecycle, journal, now := setupHandler(t)
	return httptest.NewServer(server), store, chain, lifecycle, journal, now
}

func setupHandler(t *testing.T) (*Server, *memoryStore, *mutableChain, *controlplane.Lifecycle, *controlplane.Journal, time.Time) {
	t.Helper()
	now := time.Date(2026, 8, 12, 9, 0, 0, 0, time.UTC)
	store := newMemoryStore(func() time.Time { return now })
	stepUp := now.Add(10 * time.Minute)
	store.principals[TokenDigest(agentTokenA)] = Principal{
		ID: "credential_agent_a", OrganizationID: "org_a", Kind: PrincipalAgent, Role: RoleAgent, AgentID: "agent_a",
		Scopes: []string{"intents:create", "intents:read", "authorizations:issue", "commands:read"},
	}
	store.principals[TokenDigest(ownerTokenA)] = Principal{ID: "owner_a", OrganizationID: "org_a", Kind: PrincipalHuman, Role: RoleOwner, StepUpUntil: stepUp}
	store.principals[TokenDigest(approverToken)] = Principal{ID: "approver_a", OrganizationID: "org_a", Kind: PrincipalHuman, Role: RoleApprover, StepUpUntil: stepUp}
	store.principals[TokenDigest(weakTokenA)] = Principal{ID: "approver_weak", OrganizationID: "org_a", Kind: PrincipalHuman, Role: RoleApprover}
	store.principals[TokenDigest(viewerTokenB)] = Principal{ID: "viewer_b", OrganizationID: "org_b", Kind: PrincipalHuman, Role: RoleViewer}
	store.agents[agentKey("org_a", "agent_a")] = Agent{OrganizationID: "org_a", ID: "agent_a", CustomerID: "customer_a", Name: "Research", Status: AgentActive, UpdatedAt: now}
	store.agents[agentKey("org_b", "agent_b")] = Agent{OrganizationID: "org_b", ID: "agent_b", CustomerID: "customer_b", Name: "Other", Status: AgentActive, UpdatedAt: now}
	chain := newHealthyChain(now)
	chain.views = make(map[string]reconciliation.OrganizationView)
	lifecycle, journal := testLifecycle(t, store, chain, now)
	siteSessions, err := NewSiteSessionCodec([]byte(strings.Repeat("s", 32)), 2*time.Minute, func() time.Time { return now })
	if err != nil {
		journal.Close()
		t.Fatal(err)
	}
	store.siteSessions = siteSessions
	ids := &sequenceIDs{}
	server, err := NewServer(ServerConfig{
		Store: store, Lifecycle: lifecycle, Chain: chain, Clock: func() time.Time { return now },
		IDSource: ids.next, SiteSessions: siteSessions, OperatorControlKey: []byte(strings.Repeat("o", 32)), Reconciliation: chain,
	})
	if err != nil {
		t.Fatal(err)
	}
	return server, store, chain, lifecycle, journal, now
}

type readinessStub struct{ err error }

func (r *readinessStub) Ready(context.Context) error { return r.err }

func TestOperationalEndpointsSeparateLivenessReadinessProductHealthAndMetrics(t *testing.T) {
	_, store, chain, lifecycle, journal, _ := setupHandler(t)
	defer journal.Close()
	readiness := &readinessStub{}
	metricsKey := []byte(strings.Repeat("m", 32))
	server, err := NewServer(ServerConfig{Store: store, Lifecycle: lifecycle, Chain: chain, Readiness: readiness, MetricsKey: metricsKey})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server)
	defer httpServer.Close()
	do := func(method, path string) *http.Response {
		t.Helper()
		request, err := http.NewRequestWithContext(t.Context(), method, httpServer.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		response, err := httpServer.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		return response
	}

	for path, want := range map[string]int{"/livez": http.StatusOK, "/readyz": http.StatusOK, "/health": http.StatusOK} {
		response := do(http.MethodGet, path)
		_ = response.Body.Close()
		if response.StatusCode != want {
			t.Fatalf("%s status = %d", path, response.StatusCode)
		}
	}
	readiness.err = errors.New("database unavailable with secret details")
	response := do(http.MethodGet, "/readyz")
	body, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusServiceUnavailable || strings.Contains(string(body), "secret details") || !strings.Contains(string(body), "NOT_READY") {
		t.Fatalf("not-ready response = %d %s", response.StatusCode, body)
	}
	response = do("ATTACKER_METHOD_IGNORED", "/not-a-route")
	_ = response.Body.Close()

	response = do(http.MethodGet, "/metrics")
	_ = response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated metrics status = %d", response.StatusCode)
	}
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, httpServer.URL+"/metrics", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+base64.StdEncoding.EncodeToString(metricsKey))
	response, err = httpServer.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(response.Body)
	_ = response.Body.Close()
	metrics := string(body)
	for _, expected := range []string{
		`flowops_http_requests_total{method="GET",route="GET /health",status_class="2xx"} 1`,
		`flowops_http_requests_total{method="GET",route="GET /readyz",status_class="5xx"} 1`,
		`flowops_http_requests_total{method="OTHER",route="unmatched",status_class="4xx"} 1`,
		`flowops_chain_state{chain_id="84532",state="HEALTHY"} 1`,
		"flowops_authorizations_paused 0",
	} {
		if !strings.Contains(metrics, expected) {
			t.Fatalf("metrics missing %q:\n%s", expected, metrics)
		}
	}
}

type settlementRegistrarStub struct {
	request ASCPSettlementAttemptRequest
	replay  bool
}

func (s *settlementRegistrarStub) Register(_ context.Context, request ASCPSettlementAttemptRequest) (ASCPSettlementAttempt, error) {
	s.request = request
	return ASCPSettlementAttempt{ASCPSettlementAttemptRequest: request, State: "SUBMITTED", RegisteredAt: time.Date(2026, 8, 21, 8, 0, 0, 0, time.UTC), Replayed: s.replay}, nil
}

func TestASCPSettlementAttemptUsesDedicatedKeeperCredentialAndCannotAssertOutcome(t *testing.T) {
	now := time.Date(2026, 8, 21, 8, 0, 0, 0, time.UTC)
	store := newMemoryStore(func() time.Time { return now })
	chain := newHealthyChain(now)
	lifecycle, journal := testLifecycle(t, store, chain, now)
	defer journal.Close()
	registrar := &settlementRegistrarStub{}
	keeperKey := []byte(strings.Repeat("k", 32))
	if _, err := NewServer(ServerConfig{Store: store, Lifecycle: lifecycle, Chain: chain,
		OperatorControlKey: keeperKey, KeeperCallbackKey: keeperKey, ASCPSettlement: registrar}); err == nil {
		t.Fatal("shared operator and keeper credential was accepted")
	}
	server, err := NewServer(ServerConfig{Store: store, Lifecycle: lifecycle, Chain: chain,
		OperatorControlKey: []byte(strings.Repeat("o", 32)), KeeperCallbackKey: keeperKey, ASCPSettlement: registrar})
	if err != nil {
		t.Fatal(err)
	}
	requestBody := ASCPSettlementAttemptRequest{OperationID: "0x" + strings.Repeat("1", 64), Action: "LOCK", TransactionHash: "0x" + strings.Repeat("2", 64)}
	body, _ := json.Marshal(requestBody)
	request := httptest.NewRequest(http.MethodPost, "/v1/ascp/settlement-attempts", bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+base64.StdEncoding.EncodeToString([]byte(strings.Repeat("o", 32))))
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("operator credential reached keeper endpoint: %d", recorder.Code)
	}
	request = httptest.NewRequest(http.MethodPost, "/v1/ascp/settlement-attempts", bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+base64.StdEncoding.EncodeToString(keeperKey))
	recorder = httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusCreated || registrar.request != requestBody || strings.Contains(recorder.Body.String(), "success") || strings.Contains(recorder.Body.String(), "finality") {
		t.Fatalf("keeper attempt response=%d body=%s request=%+v", recorder.Code, recorder.Body.String(), registrar.request)
	}
	malformed := []byte(`{"operationId":"0x` + strings.Repeat("1", 64) + `","action":"LOCK","transactionHash":"0x` + strings.Repeat("2", 64) + `","success":true}`)
	request = httptest.NewRequest(http.MethodPost, "/v1/ascp/settlement-attempts", bytes.NewReader(malformed))
	request.Header.Set("Authorization", "Bearer "+base64.StdEncoding.EncodeToString(keeperKey))
	recorder = httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("keeper asserted outcome: %d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestSitesExchangeIsMembershipBoundAndNeverCarriesStepUp(t *testing.T) {
	server, store, _, _, journal, _ := setupServer(t)
	defer server.Close()
	defer journal.Close()
	client := server.Client()
	projectID := "appgprj_flowops_1"
	userKey, err := SiteUserKey(projectID, "opaque_sites_user_a")
	if err != nil {
		t.Fatal(err)
	}
	membership := SiteMembership{
		ID: "membership_sites_owner", SiteProjectID: projectID, SiteUserKey: userKey,
		OrganizationID: "org_a", PrincipalID: "sites_owner_a", Role: RoleOwner,
	}
	emailDigest, err := normalizedEmailDigest("owner@example.com")
	if err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	store.exchangeToken = "flowops_sites_exchange_00000000000000000001"
	store.memberships[membership.ID] = membership
	store.membershipEmails[membership.ID] = emailDigest
	store.mu.Unlock()

	request := siteSessionExchangeRequest{SiteProjectID: projectID, SiteUserKey: userKey, Email: "Owner@Example.com"}
	status, exchanged := doRequest(t, client, http.MethodPost, server.URL+"/v1/sites/session", store.exchangeToken, "", request)
	if status != http.StatusCreated {
		t.Fatalf("site exchange = %d %+v", status, exchanged)
	}
	siteToken, ok := exchanged["accessToken"].(string)
	if !ok || !strings.HasPrefix(siteToken, siteSessionPrefix) {
		t.Fatalf("site exchange token = %#v", exchanged["accessToken"])
	}
	status, snapshot := doRequest(t, client, http.MethodGet, server.URL+"/v1/dashboard/snapshot", siteToken, "", nil)
	if status != http.StatusOK || snapshot["organizationId"] != "org_a" || snapshot["live"] != true {
		t.Fatalf("site dashboard = %d %+v", status, snapshot)
	}
	status, session := doRequest(t, client, http.MethodGet, server.URL+"/v1/session", siteToken, "", nil)
	if status != http.StatusOK || session["principalId"] != membership.PrincipalID || session["organizationId"] != "org_a" || session["readOnly"] != true {
		t.Fatalf("site session claims = %d %+v", status, session)
	}

	status, denied := doRequest(t, client, http.MethodPost, server.URL+"/v1/agents/agent_a/pause", siteToken, "pause_sites", pauseRequest{Reason: "test"})
	if status != http.StatusForbidden || denied["error"].(map[string]any)["code"] != "FORBIDDEN" {
		t.Fatalf("site session read-only boundary = %d %+v", status, denied)
	}
	status, denied = doRequest(t, client, http.MethodPost, server.URL+"/v1/intents", siteToken, "site_intent", intent("site_intent", "org_a", "agent_a", "50"))
	if status != http.StatusForbidden || denied["error"].(map[string]any)["code"] != "FORBIDDEN" {
		t.Fatalf("site session created intent = %d %+v", status, denied)
	}

	for name, substitution := range map[string]siteSessionExchangeRequest{
		"project": {SiteProjectID: "appgprj_other", SiteUserKey: userKey, Email: "owner@example.com"},
		"user":    {SiteProjectID: projectID, SiteUserKey: strings.Repeat("b", 64), Email: "owner@example.com"},
		"email":   {SiteProjectID: projectID, SiteUserKey: userKey, Email: "attacker@example.com"},
	} {
		t.Run(name, func(t *testing.T) {
			status, _ := doRequest(t, client, http.MethodPost, server.URL+"/v1/sites/session", store.exchangeToken, "", substitution)
			if status != http.StatusUnauthorized {
				t.Fatalf("substitution status = %d", status)
			}
		})
	}

	store.mu.Lock()
	delete(store.memberships, membership.ID)
	store.mu.Unlock()
	status, _ = doRequest(t, client, http.MethodGet, server.URL+"/v1/dashboard/snapshot", siteToken, "", nil)
	if status != http.StatusUnauthorized {
		t.Fatalf("revoked membership status = %d", status)
	}
}

func TestDashboardSnapshotDegradesOnlyMissingASCPDeploymentSchema(t *testing.T) {
	_, store, chain, lifecycle, journal, now := setupHandler(t)
	defer journal.Close()
	server, err := NewServer(ServerConfig{
		Store:     dashboardErrorStore{memoryStore: store, err: &pgconn.PgError{Code: "42P01", Message: "relation does not exist"}},
		Lifecycle: lifecycle, Chain: chain, Clock: func() time.Time { return now }, SiteSessions: store.siteSessions,
	})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server)
	defer httpServer.Close()

	status, snapshot := doRequest(t, httpServer.Client(), http.MethodGet, httpServer.URL+"/v1/dashboard/snapshot", ownerTokenA, "", nil)
	if status != http.StatusOK || snapshot["organizationId"] != "org_a" || snapshot["live"] != true {
		t.Fatalf("dashboard rollout snapshot = %d %+v", status, snapshot)
	}
	ascp, ok := snapshot["ascp"].(map[string]any)
	if !ok || ascp["available"] != false {
		t.Fatalf("dashboard ASCP rollout state = %+v", snapshot["ascp"])
	}
	reconciliationView, ok := snapshot["reconciliation"].(map[string]any)
	if !ok {
		t.Fatalf("dashboard reconciliation state = %+v", snapshot["reconciliation"])
	}
	if assets, ok := reconciliationView["assets"].([]any); !ok || len(assets) != 0 {
		t.Fatalf("dashboard reconciliation assets = %+v", reconciliationView["assets"])
	}
	if exceptions, ok := reconciliationView["exceptions"].([]any); !ok || len(exceptions) != 0 {
		t.Fatalf("dashboard reconciliation exceptions = %+v", reconciliationView["exceptions"])
	}
	if dashboardSchemaUnavailable(errors.New("database unavailable")) {
		t.Fatal("non-schema database errors must not be suppressed")
	}
}

func TestOrganizationPauseRequiresOwnerStepUpAndBlocksNewAuthorizations(t *testing.T) {
	server, store, _, _, journal, _ := setupServer(t)
	defer server.Close()
	defer journal.Close()
	client := server.Client()

	status, claims := doRequest(t, client, http.MethodGet, server.URL+"/v1/session", ownerTokenA, "", nil)
	if status != http.StatusOK || claims["principalId"] != "owner_a" || claims["organizationId"] != "org_a" || claims["readOnly"] != false {
		t.Fatalf("step-up claims = %d %+v", status, claims)
	}
	status, denied := doRequest(t, client, http.MethodPost, server.URL+"/v1/organization/pause", approverToken, "org_pause_denied", pauseRequest{Reason: "containment"})
	if status != http.StatusForbidden || denied["error"].(map[string]any)["code"] != "FORBIDDEN" {
		t.Fatalf("approver organization pause = %d %+v", status, denied)
	}
	status, paused := doRequest(t, client, http.MethodPost, server.URL+"/v1/organization/pause", ownerTokenA, "org_pause_1", pauseRequest{Reason: "suspected signer compromise"})
	if status != http.StatusOK || paused["result"].(map[string]any)["organization"].(map[string]any)["authorizationsPaused"] != true || paused["result"].(map[string]any)["auditId"] == "" {
		t.Fatalf("organization pause = %d %+v", status, paused)
	}
	status, snapshot := doRequest(t, client, http.MethodGet, server.URL+"/v1/dashboard/snapshot", ownerTokenA, "", nil)
	if status != http.StatusOK || snapshot["organization"].(map[string]any)["authorizationsPaused"] != true {
		t.Fatalf("paused organization snapshot = %d %+v", status, snapshot)
	}
	status, replayed := doRequest(t, client, http.MethodPost, server.URL+"/v1/organization/pause", ownerTokenA, "org_pause_1", pauseRequest{Reason: "suspected signer compromise"})
	if status != http.StatusOK || replayed["command"].(map[string]any)["id"] != paused["command"].(map[string]any)["id"] {
		t.Fatalf("organization pause replay = %d %+v", status, replayed)
	}
	status, blocked := doRequest(t, client, http.MethodPost, server.URL+"/v1/intents", agentTokenA, "intent_org_paused", intent("intent_org_paused", "org_a", "agent_a", "50"))
	if status != http.StatusConflict || blocked["error"].(map[string]any)["code"] != "AGENT_FROZEN" {
		t.Fatalf("post-organization-pause intent = %d %+v", status, blocked)
	}
	store.mu.Lock()
	otherPaused := store.organizationPaused["org_b"]
	store.mu.Unlock()
	if otherPaused {
		t.Fatal("organization pause crossed tenant boundary")
	}
}

func doRequest(t *testing.T, client *http.Client, method, url, token, idempotency string, body any) (int, map[string]any) {
	t.Helper()
	var reader *bytes.Reader
	if body == nil {
		reader = bytes.NewReader(nil)
	} else {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(raw)
	}
	request, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	if idempotency != "" {
		request.Header.Set("Idempotency-Key", idempotency)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var decoded map[string]any
	if err := json.NewDecoder(response.Body).Decode(&decoded); err != nil {
		t.Fatalf("decode status %d: %v", response.StatusCode, err)
	}
	return response.StatusCode, decoded
}

func intent(intentID, organizationID, agentID, amount string) controlplane.PaymentIntent {
	return controlplane.PaymentIntent{
		IntentID: intentID, OrganizationID: organizationID, CustomerID: "customer_a", AgentID: agentID,
		TaskID: "task_research", ActionID: "action_fetch", Rail: envelope.RailX402, ChainID: 84532,
		Recipient: testRecipient, Asset: testUSDC, AmountAtomic: amount,
		Resource: "https://evidence.flowops.example/v1/fetch", Category: "research", Purpose: "retrieve evidence",
	}
}

func TestServerExactIntentIsolationIdempotencyApprovalPauseAndHalt(t *testing.T) {
	server, store, chain, lifecycle, journal, now := setupServer(t)
	defer server.Close()
	defer journal.Close()
	client := server.Client()

	status, _ := doRequest(t, client, http.MethodGet, server.URL+"/v1/approvals", "", "", nil)
	if status != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d", status)
	}
	status, _ = doRequest(t, client, http.MethodGet, server.URL+"/v1/approvals", agentTokenA, "", nil)
	if status != http.StatusForbidden {
		t.Fatalf("agent approval-inbox status = %d", status)
	}

	crossTenant := intent("intent_cross", "org_b", "agent_b", "150")
	status, _ = doRequest(t, client, http.MethodPost, server.URL+"/v1/intents", agentTokenA, "intent_cross", crossTenant)
	if status != http.StatusNotFound {
		t.Fatalf("cross-tenant status = %d", status)
	}
	if _, ok := lifecycle.Get("req_1"); ok {
		t.Fatal("cross-tenant request reached the lifecycle")
	}
	wrongCustomer := intent("intent_customer_substitution", "org_a", "agent_a", "150")
	wrongCustomer.CustomerID = "customer_other"
	status, _ = doRequest(t, client, http.MethodPost, server.URL+"/v1/intents", agentTokenA, "intent_customer_substitution", wrongCustomer)
	if status != http.StatusNotFound || len(journal.Events()) != 0 {
		t.Fatalf("customer substitution status=%d events=%d", status, len(journal.Events()))
	}

	pendingIntent := intent("intent_pending", "org_a", "agent_a", "150")
	status, created := doRequest(t, client, http.MethodPost, server.URL+"/v1/intents", agentTokenA, "intent_pending", pendingIntent)
	if status != http.StatusCreated {
		t.Fatalf("create status = %d, response = %+v", status, created)
	}
	result := created["result"].(map[string]any)
	requestID, requestDigest := result["requestId"].(string), result["requestDigest"].(string)
	if result["state"] != string(controlplane.StatePendingApproval) {
		t.Fatalf("created state = %v", result["state"])
	}
	status, replayed := doRequest(t, client, http.MethodPost, server.URL+"/v1/intents", agentTokenA, "intent_pending", pendingIntent)
	if status != http.StatusOK || replayed["result"].(map[string]any)["requestId"] != requestID || len(journal.Events()) != 1 {
		t.Fatalf("idempotent replay = %d %+v, events=%d", status, replayed, len(journal.Events()))
	}
	changed := pendingIntent
	changed.AmountAtomic = "151"
	status, _ = doRequest(t, client, http.MethodPost, server.URL+"/v1/intents", agentTokenA, "intent_pending", changed)
	if status != http.StatusConflict || len(journal.Events()) != 1 {
		t.Fatalf("idempotency conflict status=%d events=%d", status, len(journal.Events()))
	}

	status, otherApprovals := doRequest(t, client, http.MethodGet, server.URL+"/v1/approvals", viewerTokenB, "", nil)
	if status != http.StatusOK || len(otherApprovals["approvals"].([]any)) != 0 {
		t.Fatalf("other org approvals = %d %+v", status, otherApprovals)
	}
	decision := approvalDecisionRequest{RequestDigest: requestDigest, Action: controlplane.Approve, Note: "approved"}
	status, _ = doRequest(t, client, http.MethodPost, server.URL+"/v1/approvals/"+requestID+"/decision", weakTokenA, "approval_weak", decision)
	if status != http.StatusForbidden || len(journal.Events()) != 1 {
		t.Fatalf("weak approval status=%d events=%d", status, len(journal.Events()))
	}
	wrong := decision
	wrong.RequestDigest = "0x" + strings.Repeat("0", 64)
	status, wrongResponse := doRequest(t, client, http.MethodPost, server.URL+"/v1/approvals/"+requestID+"/decision", approverToken, "approval_wrong", wrong)
	if status != http.StatusConflict || wrongResponse["commandId"] == "" || len(journal.Events()) != 1 {
		t.Fatalf("digest substitution = %d %+v events=%d", status, wrongResponse, len(journal.Events()))
	}
	status, approved := doRequest(t, client, http.MethodPost, server.URL+"/v1/approvals/"+requestID+"/decision", approverToken, "approval_exact", decision)
	if status != http.StatusOK || approved["result"].(map[string]any)["state"] != string(controlplane.StateApproved) {
		t.Fatalf("approval = %d %+v", status, approved)
	}
	status, issued := doRequest(t, client, http.MethodPost, server.URL+"/v1/intents/"+requestID+"/authorization", agentTokenA, "issue_pending", nil)
	if status != http.StatusOK || issued["result"].(map[string]any)["authorization"] == nil {
		t.Fatalf("issuance = %d %+v", status, issued)
	}

	status, pause := doRequest(t, client, http.MethodPost, server.URL+"/v1/agents/agent_a/pause", ownerTokenA, "pause_agent_a", pauseRequest{Reason: "operator containment"})
	if status != http.StatusOK || pause["result"].(map[string]any)["status"] != string(AgentPaused) {
		t.Fatalf("pause = %d %+v", status, pause)
	}
	status, pauseReplay := doRequest(t, client, http.MethodPost, server.URL+"/v1/agents/agent_a/pause", ownerTokenA, "pause_agent_a", pauseRequest{Reason: "operator containment"})
	if status != http.StatusOK || pauseReplay["result"].(map[string]any)["status"] != string(AgentPaused) {
		t.Fatalf("pause replay = %d %+v", status, pauseReplay)
	}
	status, _ = doRequest(t, client, http.MethodPost, server.URL+"/v1/intents", agentTokenA, "intent_after_pause", intent("intent_after_pause", "org_a", "agent_a", "50"))
	if status != http.StatusConflict {
		t.Fatalf("post-pause create status = %d", status)
	}

	store.mu.Lock()
	agent := store.agents[agentKey("org_a", "agent_a")]
	agent.Status = AgentActive
	store.agents[agentKey("org_a", "agent_a")] = agent
	store.mu.Unlock()
	status, auto := doRequest(t, client, http.MethodPost, server.URL+"/v1/intents", agentTokenA, "intent_halt", intent("intent_halt", "org_a", "agent_a", "50"))
	if status != http.StatusCreated {
		t.Fatalf("auto create = %d %+v", status, auto)
	}
	autoRequestID := auto["result"].(map[string]any)["requestId"].(string)
	chain.halt(now)
	status, halted := doRequest(t, client, http.MethodPost, server.URL+"/v1/intents/"+autoRequestID+"/authorization", agentTokenA, "issue_halted", nil)
	if status != http.StatusServiceUnavailable || halted["error"].(map[string]any)["code"] != "CHAIN_UNAVAILABLE" {
		t.Fatalf("halted issuance = %d %+v", status, halted)
	}
	status, haltedReplay := doRequest(t, client, http.MethodPost, server.URL+"/v1/intents/"+autoRequestID+"/authorization", agentTokenA, "issue_halted", nil)
	if status != http.StatusServiceUnavailable || haltedReplay["error"].(map[string]any)["code"] != "CHAIN_UNAVAILABLE" {
		t.Fatalf("halted retry changed the durable failure = %d %+v", status, haltedReplay)
	}

	commandID := created["command"].(map[string]any)["id"].(string)
	status, _ = doRequest(t, client, http.MethodGet, server.URL+"/v1/commands/"+commandID, viewerTokenB, "", nil)
	if status != http.StatusNotFound {
		t.Fatalf("cross-tenant command status = %d", status)
	}
}

func TestLifecycleReadEndpointsAreTenantAndPrincipalBound(t *testing.T) {
	server, _, _, _, journal, _ := setupServer(t)
	defer server.Close()
	defer journal.Close()
	client := server.Client()

	pending := intent("intent_readable", "org_a", "agent_a", "150")
	status, created := doRequest(t, client, http.MethodPost, server.URL+"/v1/intents", agentTokenA, "intent_readable", pending)
	if status != http.StatusCreated {
		t.Fatalf("create readable intent = %d %+v", status, created)
	}
	requestID := created["result"].(map[string]any)["requestId"].(string)

	status, got := doRequest(t, client, http.MethodGet, server.URL+"/v1/intents/"+requestID, agentTokenA, "", nil)
	if status != http.StatusOK || got["intent"].(map[string]any)["requestId"] != requestID {
		t.Fatalf("agent intent read = %d %+v", status, got)
	}
	status, _ = doRequest(t, client, http.MethodGet, server.URL+"/v1/intents/"+requestID, viewerTokenB, "", nil)
	if status != http.StatusNotFound {
		t.Fatalf("cross-tenant intent read = %d", status)
	}

	status, approval := doRequest(t, client, http.MethodGet, server.URL+"/v1/approvals/"+requestID, ownerTokenA, "", nil)
	if status != http.StatusOK || approval["approval"].(map[string]any)["requestId"] != requestID {
		t.Fatalf("owner approval read = %d %+v", status, approval)
	}
	status, _ = doRequest(t, client, http.MethodGet, server.URL+"/v1/approvals/"+requestID, agentTokenA, "", nil)
	if status != http.StatusForbidden {
		t.Fatalf("agent approval read = %d", status)
	}
	status, _ = doRequest(t, client, http.MethodGet, server.URL+"/v1/approvals/"+requestID, viewerTokenB, "", nil)
	if status != http.StatusNotFound {
		t.Fatalf("cross-tenant approval read = %d", status)
	}
}

func TestServerRejectsUnknownFieldsAndAgentScopeEscalation(t *testing.T) {
	server, store, _, _, journal, _ := setupServer(t)
	defer server.Close()
	defer journal.Close()
	client := server.Client()

	store.mu.Lock()
	principal := store.principals[TokenDigest(agentTokenA)]
	principal.Scopes = []string{"commands:read"}
	store.principals[TokenDigest(agentTokenA)] = principal
	store.mu.Unlock()
	status, _ := doRequest(t, client, http.MethodPost, server.URL+"/v1/intents", agentTokenA, "intent_scope", intent("intent_scope", "org_a", "agent_a", "50"))
	if status != http.StatusForbidden {
		t.Fatalf("scope escalation status = %d", status)
	}

	raw := []byte(`{"intentId":"intent_unknown","organizationId":"org_a","unknownSecret":"value"}`)
	request, err := http.NewRequest(http.MethodPost, server.URL+"/v1/intents", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+ownerTokenA)
	request.Header.Set("Idempotency-Key", "intent_unknown")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown field status = %d", response.StatusCode)
	}
}

func TestClassifyWrappedChainFailure(t *testing.T) {
	status, code, retriable := classifyError(fmt.Errorf("chain unavailable: %w", reconciliation.ErrChainUnavailable))
	if status != http.StatusServiceUnavailable || code != "CHAIN_UNAVAILABLE" || !retriable {
		t.Fatalf("classification = %d %s %v", status, code, retriable)
	}
	if !errors.Is(fmt.Errorf("wrapped: %w", reconciliation.ErrChainUnavailable), reconciliation.ErrChainUnavailable) {
		t.Fatal("test error wrapping is broken")
	}
}
