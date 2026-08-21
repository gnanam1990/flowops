package controlapi

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gnanam1990/flowops/internal/ascpactivation"
	"github.com/gnanam1990/flowops/internal/ascpbearer"
	"github.com/gnanam1990/flowops/internal/ascporchestration"
	"github.com/gnanam1990/flowops/internal/ascpsignerbinding"
)

type stubASCPActivation struct {
	status      ascpactivation.Status
	err         error
	identity    ascporchestration.Identity
	operationID string
	request     ascpactivation.Request
	creates     int
	reads       int
}

func (s *stubASCPActivation) Create(_ context.Context, identity ascporchestration.Identity, operationID string, request ascpactivation.Request) (ascpactivation.Status, error) {
	s.creates++
	s.identity, s.operationID, s.request = identity, operationID, request
	return s.status, s.err
}

func (s *stubASCPActivation) Get(_ context.Context, identity ascporchestration.Identity, operationID string) (ascpactivation.Status, error) {
	s.reads++
	s.identity, s.operationID = identity, operationID
	return s.status, s.err
}

func TestASCPActivationRoutesDeriveAgentScopeAndNeverExposeSignerMaterial(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	server, activation, token, operationID := activationRouteServer(t, now)
	payload := `{"actionId":"lock-action-1","canonicalPayload":"Y2Fub25pY2Fs","canonicalPayloadHash":"0x` + repeatHex("1", 64) + `","evidenceBundle":"ZXZpZGVuY2U=","evidenceBundleHash":"0x` + repeatHex("2", 64) + `","digest":"0x` + repeatHex("3", 64) + `","nonce":"0x` + repeatHex("4", 64) + `","instrumentType":"LOCK_AUTHORIZATION","validAfter":"2033-01-01T00:00:00Z","validUntil":"2033-01-01T00:09:00Z"}`
	request := httptest.NewRequest(http.MethodPost, "/agent/v1/intents/"+operationID+"/activation", bytes.NewBufferString(payload))
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusCreated || activation.creates != 1 ||
		activation.identity != (ascporchestration.Identity{OrganizationID: "org_a", AgentID: "agent_a"}) ||
		activation.operationID != operationID || string(activation.request.CanonicalPayload) != "canonical" {
		t.Fatalf("create status=%d calls=%d identity=%+v operation=%s request=%+v body=%s", recorder.Code, activation.creates, activation.identity, activation.operationID, activation.request, recorder.Body.String())
	}
	for _, forbidden := range []string{"canonicalPayload", "evidenceBundle", "preparedHandle", "signature"} {
		if bytes.Contains(recorder.Body.Bytes(), []byte(forbidden)) {
			t.Fatalf("activation response leaked %s: %s", forbidden, recorder.Body.String())
		}
	}

	request = httptest.NewRequest(http.MethodGet, "/agent/v1/intents/"+operationID+"/activation", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	recorder = httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || activation.reads != 1 || activation.operationID != operationID {
		t.Fatalf("get status=%d reads=%d operation=%s body=%s", recorder.Code, activation.reads, activation.operationID, recorder.Body.String())
	}

	activation.status.Replayed = true
	request = httptest.NewRequest(http.MethodPost, "/agent/v1/intents/"+operationID+"/activation", bytes.NewBufferString(payload))
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	recorder = httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("replay status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestASCPActivationRoutesRejectUnknownFieldsAndNonAgentPrincipal(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	server, activation, token, operationID := activationRouteServer(t, now)
	request := httptest.NewRequest(http.MethodPost, "/agent/v1/intents/"+operationID+"/activation", bytes.NewBufferString(`{"unexpected":true}`))
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest || activation.creates != 0 {
		t.Fatalf("unknown field status=%d calls=%d body=%s", recorder.Code, activation.creates, recorder.Body.String())
	}
	request = httptest.NewRequest(http.MethodPost, "/agent/v1/intents/"+operationID+"/activation", bytes.NewBufferString(`{"signerKeyId":"attacker-selected-key"}`))
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	recorder = httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest || activation.creates != 0 {
		t.Fatalf("caller-controlled signer route status=%d calls=%d body=%s", recorder.Code, activation.creates, recorder.Body.String())
	}
	oversized := append([]byte(`{"actionId":"`), bytes.Repeat([]byte{'a'}, maxActivationRequestBytes)...)
	oversized = append(oversized, []byte(`"}`)...)
	request = httptest.NewRequest(http.MethodPost, "/agent/v1/intents/"+operationID+"/activation", bytes.NewReader(oversized))
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	recorder = httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest || activation.creates != 0 {
		t.Fatalf("oversized activation status=%d calls=%d body=%s", recorder.Code, activation.creates, recorder.Body.String())
	}
	missingScopeToken := "activation_missing_scope_token_123456"
	store := server.store.(*memoryStore)
	store.principals[TokenDigest(missingScopeToken)] = Principal{
		ID: "credential_without_activation", OrganizationID: "org_a", Kind: PrincipalAgent, Role: RoleAgent,
		AgentID: "agent_a", Scopes: []string{"intents:read", "authorizations:issue"},
	}
	request = httptest.NewRequest(http.MethodPost, "/agent/v1/intents/"+operationID+"/activation", bytes.NewBufferString(`{}`))
	request.Header.Set("Authorization", "Bearer "+missingScopeToken)
	request.Header.Set("Content-Type", "application/json")
	recorder = httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden || activation.creates != 0 {
		t.Fatalf("missing activation scope status=%d calls=%d body=%s", recorder.Code, activation.creates, recorder.Body.String())
	}

	humanToken := "activation_human_token_123456"
	store.principals[TokenDigest(humanToken)] = Principal{ID: "developer_a", OrganizationID: "org_a", Kind: PrincipalHuman, Role: RoleDeveloper}
	request = httptest.NewRequest(http.MethodGet, "/agent/v1/intents/"+operationID+"/activation", nil)
	request.Header.Set("Authorization", "Bearer "+humanToken)
	recorder = httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden || activation.reads != 0 {
		t.Fatalf("human read status=%d reads=%d body=%s", recorder.Code, activation.reads, recorder.Body.String())
	}
}

func TestASCPActivationRouteMapsBindingAndNotFoundErrors(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	server, activation, token, operationID := activationRouteServer(t, now)
	activation.err = ascpbearer.ErrActivationBinding
	request := httptest.NewRequest(http.MethodGet, "/agent/v1/intents/"+operationID+"/activation", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("binding status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	activation.err = ascpbearer.ErrActivationNotFound
	request = httptest.NewRequest(http.MethodGet, "/agent/v1/intents/"+operationID+"/activation", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	recorder = httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("not-found status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	activation.err = ascpsignerbinding.ErrNotFound
	request = httptest.NewRequest(http.MethodPost, "/agent/v1/intents/"+operationID+"/activation", bytes.NewBufferString(`{}`))
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	recorder = httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusConflict || !bytes.Contains(recorder.Body.Bytes(), []byte("SIGNER_BINDING_REQUIRED")) {
		t.Fatalf("missing binding status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func activationRouteServer(t *testing.T, now time.Time) (*Server, *stubASCPActivation, string, string) {
	t.Helper()
	store := newMemoryStore(func() time.Time { return now })
	token := "activation_agent_token_123456"
	store.principals[TokenDigest(token)] = Principal{
		ID: "credential_activation", OrganizationID: "org_a", Kind: PrincipalAgent, Role: RoleAgent,
		AgentID: "agent_a", Scopes: []string{"intents:read", "activations:create"},
	}
	store.agents[agentKey("org_a", "agent_a")] = Agent{OrganizationID: "org_a", ID: "agent_a", CustomerID: "customer_a", Name: "Agent A", Status: AgentActive, UpdatedAt: now}
	chain := newHealthyChain(now)
	lifecycle, journal := testLifecycleWithClock(t, store, chain, func() time.Time { return now })
	t.Cleanup(func() { _ = journal.Close() })
	operationID := "0x" + repeatHex("a", 64)
	activation := &stubASCPActivation{status: ascpactivation.Status{
		RequestID: "0x" + repeatHex("b", 64), AuthorizationID: "0x" + repeatHex("c", 64),
		OperationID: operationID, InputHash: "0x" + repeatHex("d", 64), Digest: "0x" + repeatHex("e", 64),
		State: ascpbearer.SignRequested, SignerKeyID: "signer-key-1", KeyEpoch: 1,
		ModuleAddress: "0x1111111111111111111111111111111111111111", SafeAddress: "0x2222222222222222222222222222222222222222",
		KeeperID: "keeper-primary", ValidAfter: now, ValidUntil: now.Add(9 * time.Minute), CreatedAt: now,
	}}
	server, err := NewServer(ServerConfig{Store: store, Lifecycle: lifecycle, Chain: chain, ASCPActivation: activation, Clock: func() time.Time { return now }, IDSource: func(prefix string) (string, error) { return prefix + "_activation", nil }})
	if err != nil {
		t.Fatal(err)
	}
	return server, activation, token, operationID
}
