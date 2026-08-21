package mcp

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

const testBearerToken = "valid_token_000000000000000000000001"

func testAuthorization() string { return strings.Join([]string{"Bearer", testBearerToken}, " ") }

func TestLoadToolsRejectsSchemaManifestDrift(t *testing.T) {
	tools := []byte(`{"tools":[{"name":"ascp.intent.get","description":"read","inputSchema":{"type":"object"}}]}`)
	digest := sha256.Sum256([]byte("different"))
	files := fstest.MapFS{
		"schemas/tools.json":      &fstest.MapFile{Data: tools},
		"schemas/manifest.sha256": &fstest.MapFile{Data: []byte(hex.EncodeToString(digest[:]) + "  schemas/tools.json\n")},
	}
	if _, err := loadTools(files); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("schema drift error = %v", err)
	}
	if _, err := NewServer(Config{Delegate: http.NotFoundHandler()}); err != nil {
		t.Fatalf("embedded schemas rejected: %v", err)
	}
}

func TestServerEnforcesTransportOriginAndAuthentication(t *testing.T) {
	server := newTestServer(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/session" || request.Header.Get("Authorization") != testAuthorization() {
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		writeTestJSON(writer, http.StatusOK, map[string]string{"principalId": "agent_a"})
	}))

	request := mcpRequest(t, 1, "initialize", map[string]any{}, testAuthorization())
	request.Header.Set("Origin", "https://attacker.example")
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("untrusted origin status = %d", recorder.Code)
	}

	request = mcpRequest(t, 2, "initialize", map[string]any{}, "")
	recorder = httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || rpcErrorCode(t, recorder) != -32001 {
		t.Fatalf("unauthenticated MCP response = %d %s", recorder.Code, recorder.Body.String())
	}

	request = mcpRequest(t, 3, "initialize", map[string]any{}, testAuthorization())
	request.Header.Set("Accept", "application/json")
	recorder = httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNotAcceptable {
		t.Fatalf("invalid Accept status = %d", recorder.Code)
	}

	request = httptest.NewRequest(http.MethodGet, "/mcp", nil)
	recorder = httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusMethodNotAllowed || recorder.Header().Get("Allow") != http.MethodPost {
		t.Fatalf("GET response = %d allow=%q", recorder.Code, recorder.Header().Get("Allow"))
	}
}

func TestServerListsToolsAndPreservesIntentBoundary(t *testing.T) {
	var calls []capturedCall
	server := newTestServer(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		calls = append(calls, capturedCall{method: request.Method, path: request.URL.Path, authorization: request.Header.Get("Authorization"), idempotency: request.Header.Get("Idempotency-Key"), body: body})
		if request.Header.Get("Authorization") != testAuthorization() {
			writeTestJSON(writer, http.StatusUnauthorized, map[string]any{"error": map[string]any{"code": "UNAUTHENTICATED"}})
			return
		}
		switch request.URL.Path {
		case "/v1/session":
			writeTestJSON(writer, http.StatusOK, map[string]any{"principalId": "agent_a"})
		case "/v1/intents":
			writeTestJSON(writer, http.StatusCreated, map[string]any{"result": map[string]any{"requestId": "req_1", "authorization": map[string]any{"signature": "do-not-leak", "keyId": "control_1"}}})
		default:
			writeTestJSON(writer, http.StatusNotFound, map[string]any{"error": map[string]any{"code": "NOT_FOUND"}})
		}
	}))

	list := mcpRequest(t, 1, "tools/list", map[string]any{}, testAuthorization())
	listRecorder := httptest.NewRecorder()
	server.ServeHTTP(listRecorder, list)
	listResponse := decodeRPC(t, listRecorder)
	tools := listResponse["result"].(map[string]any)["tools"].([]any)
	if len(tools) != 7 {
		t.Fatalf("tool count = %d", len(tools))
	}

	intent := map[string]any{
		"intentId": "intent_mcp_1", "organizationId": "org_attacker", "customerId": "customer_a", "agentId": "agent_a",
		"taskId": "task_a", "actionId": "fetch", "rail": "X402", "chainId": 84532,
		"recipient": "0x1111111111111111111111111111111111111111", "asset": "0x036cbd53842c5426634e7929541ec2318f3dcf7e",
		"amountAtomic": "1", "resource": "https://seller.example/resource", "category": "research", "purpose": "test",
	}
	call := mcpRequest(t, 2, "tools/call", map[string]any{"name": "ascp.intent.create", "arguments": map[string]any{"intent": intent, "idempotencyKey": "intent_mcp_1"}}, testAuthorization())
	callRecorder := httptest.NewRecorder()
	server.ServeHTTP(callRecorder, call)
	response := decodeRPC(t, callRecorder)
	result := response["result"].(map[string]any)
	if result["isError"] != false || strings.Contains(callRecorder.Body.String(), "do-not-leak") {
		t.Fatalf("MCP tool result leaked or failed: %s", callRecorder.Body.String())
	}
	if len(calls) != 3 || calls[1].path != "/v1/session" || calls[2].path != "/v1/intents" {
		t.Fatalf("backend call sequence = %+v", calls)
	}
	if calls[2].idempotency != "intent_mcp_1" || calls[2].authorization != testAuthorization() {
		t.Fatalf("MCP did not preserve backend credentials/idempotency: %+v", calls[2])
	}
	var forwarded map[string]any
	if err := json.Unmarshal(calls[2].body, &forwarded); err != nil || forwarded["organizationId"] != "org_attacker" {
		t.Fatalf("MCP altered intent body: %s err=%v", calls[2].body, err)
	}

	unknown := mcpRequest(t, 3, "tools/call", map[string]any{"name": "ascp.intent.create", "arguments": map[string]any{"intent": intent, "idempotencyKey": "intent_mcp_2", "unexpected": true}}, testAuthorization())
	unknownRecorder := httptest.NewRecorder()
	server.ServeHTTP(unknownRecorder, unknown)
	if rpcErrorCode(t, unknownRecorder) != -32602 || len(calls) != 4 {
		t.Fatalf("unknown MCP arguments reached the backend: calls=%d body=%s", len(calls), unknownRecorder.Body.String())
	}
}

func TestDurableOperationToolsDelegateOnlyToAgentBoundary(t *testing.T) {
	var calls []capturedCall
	server := newTestServer(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		calls = append(calls, capturedCall{method: request.Method, path: request.URL.Path, authorization: request.Header.Get("Authorization"), idempotency: request.Header.Get("Idempotency-Key"), body: body})
		if request.URL.Path == "/v1/session" {
			writeTestJSON(writer, http.StatusOK, map[string]string{"principalId": "agent_a"})
			return
		}
		writeTestJSON(writer, http.StatusOK, map[string]any{"operation": map[string]string{"operationId": "0x" + strings.Repeat("a", 64)}})
	}))
	requestBody := map[string]any{"taskId": "task_1", "sellerQuote": map[string]any{"purchaseSpecHash": "0x" + strings.Repeat("b", 64)}}
	call := mcpRequest(t, 1, "tools/call", map[string]any{"name": "ascp.operation.create", "arguments": map[string]any{"request": requestBody, "idempotencyKey": "idem_1"}}, testAuthorization())
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, call)
	if rpc := decodeRPC(t, recorder); rpc["error"] != nil {
		t.Fatalf("create RPC error: %s", recorder.Body.String())
	}
	if len(calls) != 2 || calls[1].path != "/agent/v1/intents" || calls[1].method != http.MethodPost || calls[1].idempotency != "idem_1" || calls[1].authorization != testAuthorization() {
		t.Fatalf("create calls=%+v", calls)
	}
	var forwarded map[string]any
	if json.Unmarshal(calls[1].body, &forwarded) != nil || forwarded["taskId"] != "task_1" {
		t.Fatalf("forwarded body=%s", calls[1].body)
	}

	operationID := "0x" + strings.Repeat("c", 64)
	call = mcpRequest(t, 2, "tools/call", map[string]any{"name": "ascp.operation.get", "arguments": map[string]any{"operationId": operationID}}, testAuthorization())
	recorder = httptest.NewRecorder()
	server.ServeHTTP(recorder, call)
	if len(calls) != 4 || calls[3].path != "/agent/v1/intents/"+operationID || calls[3].method != http.MethodGet {
		t.Fatalf("read calls=%+v body=%s", calls, recorder.Body.String())
	}
}

func TestToolBackendErrorsRemainTypedToolErrors(t *testing.T) {
	server := newTestServer(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/v1/session" {
			writeTestJSON(writer, http.StatusOK, map[string]string{"principalId": "owner_a"})
			return
		}
		writeTestJSON(writer, http.StatusConflict, map[string]any{"error": map[string]any{"code": "STATE_CONFLICT", "retriable": false}})
	}))
	request := mcpRequest(t, 1, "tools/call", map[string]any{"name": "ascp.approval.list", "arguments": map[string]any{}}, testAuthorization())
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	result := decodeRPC(t, recorder)["result"].(map[string]any)
	if result["isError"] != true || !strings.Contains(recorder.Body.String(), "STATE_CONFLICT") {
		t.Fatalf("backend error became success: %s", recorder.Body.String())
	}
}

type capturedCall struct {
	method, path, authorization, idempotency string
	body                                     []byte
}

func newTestServer(t *testing.T, delegate http.Handler) *Server {
	t.Helper()
	server, err := NewServer(Config{Delegate: delegate, AllowedOrigins: []string{"https://console.flowops.example"}})
	if err != nil {
		t.Fatal(err)
	}
	return server
}

func mcpRequest(t *testing.T, id int, method string, params any, authorization string) *http.Request {
	t.Helper()
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(body))
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set("Content-Type", "application/json")
	if authorization != "" {
		request.Header.Set("Authorization", authorization)
	}
	return request
}

func decodeRPC(t *testing.T, recorder *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var response map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode MCP response %d: %v (%s)", recorder.Code, err, recorder.Body.String())
	}
	return response
}

func rpcErrorCode(t *testing.T, recorder *httptest.ResponseRecorder) int {
	t.Helper()
	response := decodeRPC(t, recorder)
	return int(response["error"].(map[string]any)["code"].(float64))
}

func writeTestJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}
