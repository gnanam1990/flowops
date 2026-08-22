package controlapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gnanam1990/flowops/internal/ascpworkflow"
)

func TestASCPWorkflowAPIDualControlStepUpAndTenantBoundary(t *testing.T) {
	now := time.Date(2026, 8, 22, 8, 0, 0, 0, time.UTC)
	store := newMemoryStore(func() time.Time { return now })
	chain := newHealthyChain(now)
	lifecycle, journal := testLifecycle(t, store, chain, now)
	defer journal.Close()
	tokens := map[string]Principal{
		"seller": {ID: "seller_a", OrganizationID: "org_a", Kind: PrincipalHuman, Role: RoleSellerAdmin, StepUpAt: now.Add(-time.Minute), StepUpUntil: now.Add(time.Minute)},
		"owner":  {ID: "owner_a", OrganizationID: "org_a", Kind: PrincipalHuman, Role: RoleOrgAdmin, StepUpAt: now.Add(-time.Minute), StepUpUntil: now.Add(time.Minute)},
		"stale":  {ID: "stale_a", OrganizationID: "org_a", Kind: PrincipalHuman, Role: RoleOrgAdmin, StepUpAt: now.Add(-6 * time.Minute), StepUpUntil: now.Add(time.Minute)},
		"other":  {ID: "owner_b", OrganizationID: "org_b", Kind: PrincipalHuman, Role: RoleOrgAdmin, StepUpAt: now.Add(-time.Minute), StepUpUntil: now.Add(time.Minute)},
		"agent":  {ID: "cred_agent", OrganizationID: "org_a", Kind: PrincipalAgent, Role: RoleAgent, AgentID: "agent_a"},
	}
	for token, principal := range tokens {
		store.principals[TokenDigest(workflowToken(token))] = principal
	}
	store.agents[agentKey("org_a", "agent_a")] = Agent{OrganizationID: "org_a", ID: "agent_a", CustomerID: "customer_a", Name: "Agent", Status: AgentActive, UpdatedAt: now}
	workflowService, err := ascpworkflow.New(ascpworkflow.NewMemoryStore(), nil, func() time.Time { return now }, bytes.NewReader(bytes.Repeat([]byte{4}, 64)))
	if err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(ServerConfig{Store: store, Lifecycle: lifecycle, Chain: chain, Clock: func() time.Time { return now }, ASCPWorkflows: workflowService})
	if err != nil {
		t.Fatal(err)
	}

	create := workflowRequest(t, server, http.MethodPost, "/v1/workflows", "seller", "create_1", `{"kind":"PAYOUT_CHANGE","payloadHash":"0x`+strings.Repeat("1", 64)+`"}`)
	if create.Code != http.StatusCreated {
		t.Fatalf("create=%d %s", create.Code, create.Body.String())
	}
	if create.Header().Get("X-Correlation-ID") == "" || !strings.Contains(create.Body.String(), `"correlationId":`) {
		t.Fatalf("create response lacks correlation: headers=%v body=%s", create.Header(), create.Body.String())
	}
	var created struct {
		Workflow ascpworkflow.Workflow `json:"workflow"`
	}
	if err := json.Unmarshal(create.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}

	self := workflowRequest(t, server, http.MethodPost, "/v1/workflows/"+created.Workflow.WorkflowID+"/approve", "seller", "self_1", `{}`)
	if self.Code != http.StatusForbidden || !strings.Contains(self.Body.String(), "WORKFLOW_DUAL_CONTROL_REQUIRED") {
		t.Fatalf("self=%d %s", self.Code, self.Body.String())
	}
	stale := workflowRequest(t, server, http.MethodPost, "/v1/workflows/"+created.Workflow.WorkflowID+"/approve", "stale", "stale_1", `{}`)
	if stale.Code != http.StatusForbidden || !strings.Contains(stale.Body.String(), "FRESH_STEP_UP_REQUIRED") {
		t.Fatalf("stale=%d %s", stale.Code, stale.Body.String())
	}
	agent := workflowRequest(t, server, http.MethodGet, "/v1/workflows/"+created.Workflow.WorkflowID, "agent", "", "")
	if agent.Code != http.StatusForbidden || !strings.Contains(agent.Body.String(), "HUMAN_GOVERNANCE_REQUIRED") {
		t.Fatalf("agent=%d %s", agent.Code, agent.Body.String())
	}
	legacyRead := workflowRequest(t, server, http.MethodGet, "/v1/approvals", "seller", "", "")
	if legacyRead.Code != http.StatusForbidden {
		t.Fatalf("new governance role inherited broad legacy read=%d %s", legacyRead.Code, legacyRead.Body.String())
	}
	other := workflowRequest(t, server, http.MethodGet, "/v1/workflows/"+created.Workflow.WorkflowID, "other", "", "")
	if other.Code != http.StatusNotFound {
		t.Fatalf("cross tenant=%d %s", other.Code, other.Body.String())
	}
	approved := workflowRequest(t, server, http.MethodPost, "/v1/workflows/"+created.Workflow.WorkflowID+"/approve", "owner", "approve_1", `{}`)
	if approved.Code != http.StatusOK || !strings.Contains(approved.Body.String(), `"state":"APPROVED_PENDING_CHAIN"`) {
		t.Fatalf("approve=%d %s", approved.Code, approved.Body.String())
	}
	unknown := workflowRequest(t, server, http.MethodPost, "/v1/workflows", "seller", "unknown_1", `{"kind":"PAYOUT_CHANGE","payloadHash":"0x`+strings.Repeat("1", 64)+`","mutablePayload":{}}`)
	if unknown.Code != http.StatusBadRequest {
		t.Fatalf("unknown JSON=%d %s", unknown.Code, unknown.Body.String())
	}
}

func workflowRequest(t *testing.T, handler http.Handler, method, path, token, key, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+workflowToken(token))
	if key != "" {
		request.Header.Set("Idempotency-Key", key)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func workflowToken(name string) string { return "workflow_test_" + name + "_000000000000000000000000" }
