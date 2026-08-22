package controlapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gnanam1990/flowops/internal/ascpadaptation"
	"github.com/gnanam1990/flowops/internal/ascpagent"
	"github.com/gnanam1990/flowops/internal/ascpintake"
	"github.com/gnanam1990/flowops/internal/directoryreader"
)

func TestASCPAgentRoutesDeriveIdentityRejectTrustedFieldsAndConcealReads(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	store := newMemoryStore(func() time.Time { return now })
	store.principals[TokenDigest(agentTokenA)] = Principal{ID: "credential_a", OrganizationID: "org_a", Kind: PrincipalAgent, Role: RoleAgent, AgentID: "agent_a", Scopes: []string{"intents:create", "intents:read"}}
	store.principals[TokenDigest(ownerTokenA)] = Principal{ID: "owner_a", OrganizationID: "org_a", Kind: PrincipalHuman, Role: RoleOwner}
	store.agents[agentKey("org_a", "agent_a")] = Agent{OrganizationID: "org_a", ID: "agent_a", CustomerID: "customer_a", Name: "Agent A", Status: AgentActive, UpdatedAt: now}
	chain := newHealthyChain(now)
	lifecycle, journal := testLifecycleWithClock(t, store, chain, func() time.Time { return now })
	defer journal.Close()
	operation := ascpintake.Operation{OperationID: "0x" + repeatHex("1", 64), OrganizationID: "org_a", ActorID: "agent_a", QuoteHash: "0x" + repeatHex("2", 64), PurchaseSpecHash: "0x" + repeatHex("3", 64), QuoteNonce: "0x" + repeatHex("4", 64), DirectoryVersion: 9, DirectoryContract: "0x1111111111111111111111111111111111111111", SellerSigner: "0x2222222222222222222222222222222222222222", CreatedAt: now.Unix()}
	ascp := &stubASCPAgent{operation: operation}
	server, err := NewServer(ServerConfig{Store: store, Lifecycle: lifecycle, Chain: chain, Clock: func() time.Time { return now }, IDSource: func(prefix string) (string, error) { return prefix + "_test", nil }, ASCPAgent: ascp})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/agent/v1/intents", bytes.NewBufferString(`{}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "idem_unauthenticated")
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized || recorder.Header().Get("X-Correlation-ID") != "corr_test" || !bytes.Contains(recorder.Body.Bytes(), []byte(`"correlationId":"corr_test"`)) || ascp.createCalls != 0 {
		t.Fatalf("unauthenticated status=%d headers=%v calls=%d body=%s", recorder.Code, recorder.Header(), ascp.createCalls, recorder.Body.String())
	}
	request = httptest.NewRequest(http.MethodPut, "/agent/v1/intents", bytes.NewBufferString(`{}`))
	recorder = httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusMethodNotAllowed || recorder.Header().Get("X-Correlation-ID") != "corr_test" {
		t.Fatalf("method mismatch status=%d headers=%v body=%s", recorder.Code, recorder.Header(), recorder.Body.String())
	}

	request = httptest.NewRequest(http.MethodPost, "/agent/v1/intents", bytes.NewBufferString(`{}`))
	request.Header.Set("Authorization", "Bearer "+agentTokenA)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "idem_1")
	recorder = httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusCreated || recorder.Header().Get("X-Correlation-ID") != "corr_test" || ascp.createCalls != 1 || ascp.identity != (ascpagent.Identity{OrganizationID: "org_a", AgentID: "agent_a"}) || ascp.idempotencyKey != "idem_1" {
		t.Fatalf("create status=%d headers=%v calls=%d identity=%+v key=%s body=%s", recorder.Code, recorder.Header(), ascp.createCalls, ascp.identity, ascp.idempotencyKey, recorder.Body.String())
	}
	var created map[string]any
	if json.Unmarshal(recorder.Body.Bytes(), &created) != nil || created["correlationId"] != "corr_test" {
		t.Fatalf("create response=%s", recorder.Body.String())
	}

	request = httptest.NewRequest(http.MethodPost, "/agent/v1/intents", bytes.NewBufferString(`{"organizationId":"org_attacker","directoryEvidence":{}}`))
	request.Header.Set("Authorization", "Bearer "+agentTokenA)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "idem_2")
	recorder = httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest || ascp.createCalls != 1 || !bytes.Contains(recorder.Body.Bytes(), []byte(`"correlationId":"corr_test"`)) {
		t.Fatalf("trusted-field injection status=%d calls=%d body=%s", recorder.Code, ascp.createCalls, recorder.Body.String())
	}
	request = httptest.NewRequest(http.MethodPost, "/agent/v1/intents", bytes.NewBufferString(`{"adaptationGrant":{"grant":{},"signature":"attacker"}}`))
	request.Header.Set("Authorization", "Bearer "+agentTokenA)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "idem_untrusted_grant")
	recorder = httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest || ascp.createCalls != 1 || !bytes.Contains(recorder.Body.Bytes(), []byte(`"code":"INVALID_JSON"`)) {
		t.Fatalf("untrusted grant artifact status=%d calls=%d body=%s", recorder.Code, ascp.createCalls, recorder.Body.String())
	}

	ascp.createErr = directoryreader.ErrCurrentVersionMismatch
	request = httptest.NewRequest(http.MethodPost, "/agent/v1/intents", bytes.NewBufferString(`{}`))
	request.Header.Set("Authorization", "Bearer "+agentTokenA)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "idem_stale")
	recorder = httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusConflict || !bytes.Contains(recorder.Body.Bytes(), []byte(`"code":"DIRECTORY_VERSION_STALE"`)) || !bytes.Contains(recorder.Body.Bytes(), []byte(`"retriable":false`)) {
		t.Fatalf("stale directory status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	ascp.createErr = nil
	for _, test := range []struct {
		name      string
		err       error
		want      int
		code      string
		retriable bool
	}{
		{name: "invalid adaptation grant", err: ascpadaptation.ErrInvalidGrant, want: http.StatusBadRequest, code: "INVALID_ASCP_INTENT"},
		{name: "adaptation scope", err: ascpadaptation.ErrGrantScope, want: http.StatusBadRequest, code: "INVALID_ASCP_INTENT"},
		{name: "adaptation consumed", err: ascpadaptation.ErrGrantConsumed, want: http.StatusConflict, code: "ADAPTATION_GRANT_CONSUMED"},
		{name: "adaptation unavailable", err: ascpagent.ErrAdaptationUnavailable, want: http.StatusServiceUnavailable, code: "ADAPTATION_GRANT_UNAVAILABLE", retriable: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ascp.createErr = test.err
			request := httptest.NewRequest(http.MethodPost, "/agent/v1/intents", bytes.NewBufferString(`{}`))
			request.Header.Set("Authorization", "Bearer "+agentTokenA)
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Idempotency-Key", "idem_adaptation_error")
			recorder := httptest.NewRecorder()
			server.ServeHTTP(recorder, request)
			retriable := fmt.Sprintf(`"retriable":%t`, test.retriable)
			if recorder.Code != test.want || !bytes.Contains(recorder.Body.Bytes(), []byte(`"code":"`+test.code+`"`)) || !bytes.Contains(recorder.Body.Bytes(), []byte(retriable)) {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
		})
	}
	ascp.createErr = nil

	request = httptest.NewRequest(http.MethodPost, "/agent/v1/intents", bytes.NewBufferString(`{}`))
	request.Header.Set("Authorization", "Bearer "+ownerTokenA)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "idem_3")
	recorder = httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden || ascp.createCalls != 6 {
		t.Fatalf("human create status=%d calls=%d", recorder.Code, ascp.createCalls)
	}

	request = httptest.NewRequest(http.MethodGet, "/agent/v1/intents/"+operation.OperationID, nil)
	request.Header.Set("Authorization", "Bearer "+agentTokenA)
	recorder = httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || ascp.getCalls != 1 || ascp.operationID != operation.OperationID || ascp.identity.AgentID != "agent_a" {
		t.Fatalf("read status=%d calls=%d identity=%+v operation=%s body=%s", recorder.Code, ascp.getCalls, ascp.identity, ascp.operationID, recorder.Body.String())
	}

	ascp.getErr = ascpintake.ErrNotFound
	request = httptest.NewRequest(http.MethodGet, "/agent/v1/intents/"+operation.OperationID, nil)
	request.Header.Set("Authorization", "Bearer "+agentTokenA)
	recorder = httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNotFound || !bytes.Contains(recorder.Body.Bytes(), []byte(`"code":"NOT_FOUND"`)) {
		t.Fatalf("concealed read status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestASCPAgentRouteFailsClosedWhenRuntimeIsNotConfigured(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	store := newMemoryStore(func() time.Time { return now })
	store.principals[TokenDigest(agentTokenA)] = Principal{ID: "credential_a", OrganizationID: "org_a", Kind: PrincipalAgent, Role: RoleAgent, AgentID: "agent_a", Scopes: []string{"intents:create"}}
	store.agents[agentKey("org_a", "agent_a")] = Agent{OrganizationID: "org_a", ID: "agent_a", CustomerID: "customer_a", Name: "Agent A", Status: AgentActive, UpdatedAt: now}
	chain := newHealthyChain(now)
	lifecycle, journal := testLifecycleWithClock(t, store, chain, func() time.Time { return now })
	defer journal.Close()
	server, err := NewServer(ServerConfig{Store: store, Lifecycle: lifecycle, Chain: chain})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/agent/v1/intents", bytes.NewBufferString(`{}`))
	request.Header.Set("Authorization", "Bearer "+agentTokenA)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "idem_1")
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusServiceUnavailable || !bytes.Contains(recorder.Body.Bytes(), []byte(`"code":"ASCP_INTAKE_UNAVAILABLE"`)) {
		t.Fatalf("unconfigured status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

type stubASCPAgent struct {
	operation      ascpintake.Operation
	createErr      error
	getErr         error
	identity       ascpagent.Identity
	idempotencyKey string
	operationID    string
	createCalls    int
	getCalls       int
}

func (s *stubASCPAgent) Create(_ context.Context, identity ascpagent.Identity, idempotencyKey string, _ ascpagent.CreateRequest) (ascpintake.Operation, error) {
	s.createCalls++
	s.identity, s.idempotencyKey = identity, idempotencyKey
	return s.operation, s.createErr
}

func (s *stubASCPAgent) Get(_ context.Context, identity ascpagent.Identity, operationID string) (ascpintake.Operation, error) {
	s.getCalls++
	s.identity, s.operationID = identity, operationID
	return s.operation, s.getErr
}

func repeatHex(value string, count int) string {
	result := ""
	for range count {
		result += value
	}
	return result
}
