package controlapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gnanam1990/flowops/internal/mcp"
)

func TestMCPUsesTheControlPlaneAuthorizationAndLifecyclePaths(t *testing.T) {
	rest, _, _, _, journal, _ := setupHandler(t)
	defer journal.Close()

	server, err := mcp.NewServer(mcp.Config{Delegate: rest})
	if err != nil {
		t.Fatal(err)
	}

	created := callMCP(t, server, agentTokenA, 1, "tools/call", map[string]any{
		"name":      "ascp.intent.create",
		"arguments": map[string]any{"intent": intent("intent_mcp_parity", "org_a", "agent_a", "150"), "idempotencyKey": "intent_mcp_parity"},
	})
	createdResult := created["result"].(map[string]any)
	if createdResult["isError"] != false {
		t.Fatalf("MCP intent create failed: %+v", created)
	}
	createdPayload := createdResult["structuredContent"].(map[string]any)
	requestID := createdPayload["result"].(map[string]any)["requestId"].(string)
	replayed := callMCP(t, server, agentTokenA, 2, "tools/call", map[string]any{
		"name":      "ascp.intent.create",
		"arguments": map[string]any{"intent": intent("intent_mcp_parity", "org_a", "agent_a", "150"), "idempotencyKey": "intent_mcp_parity"},
	})
	if replayed["result"].(map[string]any)["isError"] != false || replayed["result"].(map[string]any)["structuredContent"].(map[string]any)["result"].(map[string]any)["requestId"] != requestID {
		t.Fatalf("MCP idempotent replay changed outcome: %+v", replayed)
	}
	changed := intent("intent_mcp_parity", "org_a", "agent_a", "151")
	conflict := callMCP(t, server, agentTokenA, 3, "tools/call", map[string]any{
		"name":      "ascp.intent.create",
		"arguments": map[string]any{"intent": changed, "idempotencyKey": "intent_mcp_parity"},
	})
	if conflict["result"].(map[string]any)["isError"] != true {
		t.Fatalf("MCP idempotency conflict succeeded: %+v", conflict)
	}

	crossTenantCreate := callMCP(t, server, agentTokenA, 4, "tools/call", map[string]any{
		"name":      "ascp.intent.create",
		"arguments": map[string]any{"intent": intent("intent_mcp_cross_tenant", "org_b", "agent_a", "150"), "idempotencyKey": "intent_mcp_cross_tenant"},
	})
	if crossTenantCreate["result"].(map[string]any)["isError"] != true {
		t.Fatalf("MCP body organization overrode agent credential: %+v", crossTenantCreate)
	}

	read := callMCP(t, server, agentTokenA, 5, "tools/call", map[string]any{
		"name": "ascp.intent.get", "arguments": map[string]any{"requestId": requestID},
	})
	readResult := read["result"].(map[string]any)
	if readResult["isError"] != false {
		t.Fatalf("MCP intent read failed: %+v", read)
	}
	status, restRead := callRESTHandler(t, rest, http.MethodGet, "/v1/intents/"+requestID, agentTokenA, nil)
	if status != http.StatusOK || !sameJSON(readResult["structuredContent"], restRead) {
		t.Fatalf("MCP/REST read parity status=%d mcp=%+v rest=%+v", status, readResult["structuredContent"], restRead)
	}

	crossTenant := callMCP(t, server, viewerTokenB, 6, "tools/call", map[string]any{
		"name": "ascp.intent.get", "arguments": map[string]any{"requestId": requestID},
	})
	if crossTenant["error"] == nil {
		t.Fatalf("cross-tenant MCP read succeeded: %+v", crossTenant)
	}

	agentApprovals := callMCP(t, server, agentTokenA, 7, "tools/call", map[string]any{
		"name": "ascp.approval.list", "arguments": map[string]any{},
	})
	if agentApprovals["error"] == nil {
		t.Fatalf("agent MCP approval list succeeded: %+v", agentApprovals)
	}

	requestDigest := createdPayload["result"].(map[string]any)["requestDigest"].(string)
	approved := callMCP(t, server, approverToken, 8, "tools/call", map[string]any{
		"name":      "ascp.approval.decide",
		"arguments": map[string]any{"requestId": requestID, "requestDigest": requestDigest, "action": "APPROVE", "note": "MCP approval", "idempotencyKey": "approval_mcp_parity"},
	})
	if approved["result"].(map[string]any)["isError"] != false {
		t.Fatalf("MCP approval decision failed: %+v", approved)
	}
}

func callRESTHandler(t *testing.T, handler http.Handler, method, path, token string, body any) (int, map[string]any) {
	t.Helper()
	var encoded []byte
	var err error
	if body != nil {
		encoded, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	request := httptest.NewRequest(method, path, bytes.NewReader(encoded))
	request.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	var response map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode REST response status=%d body=%q: %v", recorder.Code, recorder.Body.String(), err)
	}
	return recorder.Code, response
}

func callMCP(t *testing.T, server http.Handler, token string, id int, method string, params any) map[string]any {
	t.Helper()
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("MCP %s returned HTTP %d: %s", method, recorder.Code, recorder.Body.String())
	}
	var response map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	return response
}

func sameJSON(left, right any) bool {
	leftEncoded, leftErr := json.Marshal(left)
	rightEncoded, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftEncoded, rightEncoded)
}
