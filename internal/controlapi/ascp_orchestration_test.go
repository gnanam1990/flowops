package controlapi

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gnanam1990/flowops/internal/ascpadaptation"
	"github.com/gnanam1990/flowops/internal/ascpapproval"
	"github.com/gnanam1990/flowops/internal/ascpexecauth"
	"github.com/gnanam1990/flowops/internal/ascporchestration"
	"github.com/gnanam1990/flowops/internal/policy"
)

func TestASCPOrchestrationRoutesDeriveAgentAndHumanScope(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	store := newMemoryStore(func() time.Time { return now })
	agentToken := "agent_orchestration_token_123456"
	approverToken := "approver_orchestration_token_123456"
	store.principals[TokenDigest(agentToken)] = Principal{
		ID: "credential_orch", OrganizationID: "org_a", Kind: PrincipalAgent, Role: RoleAgent,
		AgentID: "agent_a", Scopes: []string{"intents:create", "intents:read", "authorizations:issue"},
	}
	store.principals[TokenDigest(approverToken)] = Principal{
		ID: "approver_a", OrganizationID: "org_a", Kind: PrincipalHuman, Role: RoleApprover,
		StepUpUntil: now.Add(time.Minute),
	}
	store.agents[agentKey("org_a", "agent_a")] = Agent{OrganizationID: "org_a", ID: "agent_a", CustomerID: "customer_a", Name: "Agent A", Status: AgentActive, UpdatedAt: now}
	chain := newHealthyChain(now)
	lifecycle, journal := testLifecycleWithClock(t, store, chain, func() time.Time { return now })
	defer journal.Close()
	operationID, approvalID := "0x"+repeatHex("a", 64), "0x"+repeatHex("b", 64)
	flow := &stubASCPFlow{
		decision:      ascporchestration.Decision{DecisionID: "0x" + repeatHex("c", 64), OperationID: operationID, Outcome: policy.RequireApproval},
		approval:      ascpapproval.Approval{ApprovalID: approvalID, OrganizationID: "org_a", IntentID: operationID, State: ascpapproval.Requested, ReviewSnapshotHash: "0x" + repeatHex("d", 64), RequestedAt: now.Unix(), ExpiresAt: now.Add(time.Hour).Unix()},
		authorization: ascporchestration.Authorization{AuthorizationID: "0x" + repeatHex("e", 64), OperationID: operationID, State: ascpexecauth.ValidatedAndReserved},
	}
	server, err := NewServer(ServerConfig{
		Store: store, Lifecycle: lifecycle, Chain: chain, ASCPFlow: flow, Clock: func() time.Time { return now },
		IDSource: func(prefix string) (string, error) { return prefix + "_orch", nil },
	})
	if err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodPost, "/agent/v1/intents/"+operationID+"/evaluate", nil)
	request.Header.Set("Authorization", "Bearer "+agentToken)
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || flow.evaluateIdentity != (ascporchestration.Identity{OrganizationID: "org_a", AgentID: "agent_a"}) || flow.operationID != operationID || recorder.Header().Get("X-Correlation-ID") != "corr_orch" {
		t.Fatalf("evaluate status=%d identity=%+v operation=%s headers=%v body=%s", recorder.Code, flow.evaluateIdentity, flow.operationID, recorder.Header(), recorder.Body.String())
	}
	flow.evaluateErr = ascpadaptation.ErrSignerUnavailable
	request = httptest.NewRequest(http.MethodPost, "/agent/v1/intents/"+operationID+"/evaluate", nil)
	request.Header.Set("Authorization", "Bearer "+agentToken)
	recorder = httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusServiceUnavailable || !bytes.Contains(recorder.Body.Bytes(), []byte(`"code":"ADAPTATION_GRANT_UNAVAILABLE"`)) || !bytes.Contains(recorder.Body.Bytes(), []byte(`"retriable":true`)) {
		t.Fatalf("adaptation signer failure status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	flow.evaluateErr = nil

	request = httptest.NewRequest(http.MethodPost, "/agent/v1/intents/"+operationID+"/authorization", nil)
	request.Header.Set("Authorization", "Bearer "+agentToken)
	recorder = httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || flow.authorizeCalls != 1 {
		t.Fatalf("authorize status=%d calls=%d body=%s", recorder.Code, flow.authorizeCalls, recorder.Body.String())
	}

	request = httptest.NewRequest(http.MethodGet, "/v1/ascp/approvals/"+approvalID, nil)
	request.Header.Set("Authorization", "Bearer "+agentToken)
	recorder = httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden || flow.approvalCalls != 0 {
		t.Fatalf("agent approval read status=%d calls=%d", recorder.Code, flow.approvalCalls)
	}

	request = httptest.NewRequest(http.MethodPost, "/v1/ascp/approvals/"+approvalID+"/decision", bytes.NewBufferString(`{"reviewSnapshotHash":"0x`+repeatHex("d", 64)+`","action":"APPROVE"}`))
	request.Header.Set("Authorization", "Bearer "+approverToken)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "approve_orch_1")
	recorder = httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || flow.decideOrganization != "org_a" || flow.decideActor != "approver_a" || !flow.approved {
		t.Fatalf("decision status=%d org=%s actor=%s approved=%t body=%s", recorder.Code, flow.decideOrganization, flow.decideActor, flow.approved, recorder.Body.String())
	}
}

type stubASCPFlow struct {
	decision           ascporchestration.Decision
	approval           ascpapproval.Approval
	authorization      ascporchestration.Authorization
	evaluateIdentity   ascporchestration.Identity
	operationID        string
	decideOrganization string
	decideActor        string
	approved           bool
	approvalCalls      int
	authorizeCalls     int
	evaluateErr        error
}

func (s *stubASCPFlow) Evaluate(_ context.Context, identity ascporchestration.Identity, operationID string) (ascporchestration.Decision, error) {
	s.evaluateIdentity, s.operationID = identity, operationID
	return s.decision, s.evaluateErr
}
func (s *stubASCPFlow) Decision(context.Context, ascporchestration.Identity, string) (ascporchestration.Decision, error) {
	return s.decision, nil
}
func (s *stubASCPFlow) Approval(_ context.Context, _, _ string) (ascpapproval.Approval, error) {
	s.approvalCalls++
	return s.approval, nil
}
func (s *stubASCPFlow) DecideApproval(_ context.Context, organizationID, _, _ string, approved bool, actor string) (ascpapproval.Approval, error) {
	s.decideOrganization, s.decideActor, s.approved = organizationID, actor, approved
	s.approval.State = ascpapproval.Approved
	return s.approval, nil
}
func (s *stubASCPFlow) Authorize(context.Context, ascporchestration.Identity, string) (ascporchestration.Authorization, error) {
	s.authorizeCalls++
	return s.authorization, nil
}
func (s *stubASCPFlow) Authorization(context.Context, ascporchestration.Identity, string) (ascporchestration.Authorization, error) {
	return s.authorization, nil
}
