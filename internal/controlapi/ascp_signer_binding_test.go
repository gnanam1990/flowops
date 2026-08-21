package controlapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gnanam1990/flowops/internal/ascpsignerbinding"
)

type signerBindingStub struct {
	result         ascpsignerbinding.Result
	binding        ascpsignerbinding.Binding
	err            error
	organizationID string
	agentID        string
	actorID        string
	idempotencyKey string
	request        ascpsignerbinding.PutRequest
	puts           int
	reads          int
}

func (s *signerBindingStub) Put(_ context.Context, organizationID, agentID, actorID, idempotencyKey string, request ascpsignerbinding.PutRequest) (ascpsignerbinding.Result, error) {
	s.puts++
	s.organizationID, s.agentID, s.actorID, s.idempotencyKey, s.request = organizationID, agentID, actorID, idempotencyKey, request
	return s.result, s.err
}

func (s *signerBindingStub) Current(_ context.Context, organizationID, agentID string) (ascpsignerbinding.Binding, error) {
	s.reads++
	s.organizationID, s.agentID = organizationID, agentID
	return s.binding, s.err
}

func TestASCPSignerBindingRoutesRequirePrivilegedHumanAndFreshStepUp(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	server, service, tokens := signerBindingRouteServer(t, now)
	body := `{"expectedVersion":0,"signerKeyId":"signer-key-1","keyEpoch":1,"moduleAddress":"0x1111111111111111111111111111111111111111","safeAddress":"0x2222222222222222222222222222222222222222","keeperId":"keeper-primary","reason":"Initial customer signer binding"}`

	for name, token := range map[string]string{"owner without step-up": tokens["owner_stale"], "developer": tokens["developer"], "agent": tokens["agent"]} {
		t.Run(name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPut, "/v1/agents/agent_a/signer-binding", strings.NewReader(body))
			request.Header.Set("Authorization", "Bearer "+token)
			request.Header.Set("Idempotency-Key", "bind_agent_a_v1")
			request.Header.Set("Content-Type", "application/json")
			recorder := httptest.NewRecorder()
			server.ServeHTTP(recorder, request)
			if recorder.Code != http.StatusForbidden {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
		})
	}
	if service.puts != 0 {
		t.Fatalf("unauthorized requests reached signer binding store: %d", service.puts)
	}

	request := httptest.NewRequest(http.MethodPut, "/v1/agents/agent_a/signer-binding", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+tokens["owner"])
	request.Header.Set("Idempotency-Key", "bind_agent_a_v1")
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusCreated || service.puts != 1 || service.organizationID != "org_a" || service.agentID != "agent_a" ||
		service.actorID != "owner_a" || service.idempotencyKey != "bind_agent_a_v1" || service.request.SignerKeyID != "signer-key-1" {
		t.Fatalf("status=%d service=%+v body=%s", recorder.Code, service, recorder.Body.String())
	}

	request = httptest.NewRequest(http.MethodGet, "/v1/agents/agent_a/signer-binding", nil)
	request.Header.Set("Authorization", "Bearer "+tokens["admin"])
	recorder = httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || service.reads != 1 || service.organizationID != "org_a" || service.agentID != "agent_a" {
		t.Fatalf("get status=%d service=%+v body=%s", recorder.Code, service, recorder.Body.String())
	}
}

func TestASCPSignerBindingRoutesRejectMalformedAndMapConflicts(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	server, service, tokens := signerBindingRouteServer(t, now)
	request := httptest.NewRequest(http.MethodPut, "/v1/agents/agent_a/signer-binding", strings.NewReader(`{"unexpected":true}`))
	request.Header.Set("Authorization", "Bearer "+tokens["owner"])
	request.Header.Set("Idempotency-Key", "bind_invalid")
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest || service.puts != 0 {
		t.Fatalf("malformed status=%d puts=%d body=%s", recorder.Code, service.puts, recorder.Body.String())
	}

	for _, test := range []struct {
		err  error
		code int
	}{
		{ascpsignerbinding.ErrVersionConflict, http.StatusConflict},
		{ascpsignerbinding.ErrInUse, http.StatusConflict},
		{ascpsignerbinding.ErrKeyEpochReuse, http.StatusConflict},
		{ascpsignerbinding.ErrIdempotencyConflict, http.StatusConflict},
		{ascpsignerbinding.ErrNotFound, http.StatusNotFound},
		{errors.New("database offline"), http.StatusServiceUnavailable},
	} {
		service.err = test.err
		request = httptest.NewRequest(http.MethodGet, "/v1/agents/agent_a/signer-binding", nil)
		request.Header.Set("Authorization", "Bearer "+tokens["owner"])
		recorder = httptest.NewRecorder()
		server.ServeHTTP(recorder, request)
		if recorder.Code != test.code {
			t.Fatalf("error=%v status=%d body=%s", test.err, recorder.Code, recorder.Body.String())
		}
	}
}

func signerBindingRouteServer(t *testing.T, now time.Time) (*Server, *signerBindingStub, map[string]string) {
	t.Helper()
	store := newMemoryStore(func() time.Time { return now })
	tokens := map[string]string{
		"owner":       "signer_binding_owner_token_123456",
		"owner_stale": "signer_binding_owner_stale_123456",
		"admin":       "signer_binding_admin_token_123456",
		"developer":   "signer_binding_developer_token_123456",
		"agent":       "signer_binding_agent_token_123456",
	}
	store.principals[TokenDigest(tokens["owner"])] = Principal{ID: "owner_a", OrganizationID: "org_a", Kind: PrincipalHuman, Role: RoleOwner, StepUpUntil: now.Add(time.Minute)}
	store.principals[TokenDigest(tokens["owner_stale"])] = Principal{ID: "owner_a", OrganizationID: "org_a", Kind: PrincipalHuman, Role: RoleOwner, StepUpUntil: now}
	store.principals[TokenDigest(tokens["admin"])] = Principal{ID: "admin_a", OrganizationID: "org_a", Kind: PrincipalHuman, Role: RoleAdmin}
	store.principals[TokenDigest(tokens["developer"])] = Principal{ID: "developer_a", OrganizationID: "org_a", Kind: PrincipalHuman, Role: RoleDeveloper, StepUpUntil: now.Add(time.Minute)}
	store.principals[TokenDigest(tokens["agent"])] = Principal{ID: "credential_a", OrganizationID: "org_a", Kind: PrincipalAgent, Role: RoleAgent, AgentID: "agent_a", Scopes: []string{"signer-bindings:write"}}
	store.agents[agentKey("org_a", "agent_a")] = Agent{OrganizationID: "org_a", ID: "agent_a", CustomerID: "customer_a", Name: "Agent A", Status: AgentActive, UpdatedAt: now}
	chain := newHealthyChain(now)
	lifecycle, journal := testLifecycleWithClock(t, store, chain, func() time.Time { return now })
	t.Cleanup(func() { _ = journal.Close() })
	binding := ascpsignerbinding.Binding{
		OrganizationID: "org_a", AgentID: "agent_a", Version: 1, ChainID: 84532,
		SignerKeyID: "signer-key-1", KeyEpoch: 1,
		ModuleAddress: "0x1111111111111111111111111111111111111111",
		SafeAddress:   "0x2222222222222222222222222222222222222222", KeeperID: "keeper-primary",
		UpdatedBy: "owner_a", UpdatedAt: now,
	}
	service := &signerBindingStub{binding: binding, result: ascpsignerbinding.Result{Binding: binding, ChangeID: "0x" + strings.Repeat("a", 64)}}
	server, err := NewServer(ServerConfig{Store: store, Lifecycle: lifecycle, Chain: chain, ASCPSignerBindings: service, Clock: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	return server, service, tokens
}
