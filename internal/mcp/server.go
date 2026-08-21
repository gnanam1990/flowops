// Package mcp implements the FlowOps agent-facing MCP boundary. It deliberately
// delegates every permitted tool to the existing REST application boundary;
// MCP has no signer, database, policy, or chain-write capability of its own.
package mcp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

const (
	ProtocolVersion = "2025-11-25"
	defaultMaxBytes = 64 * 1024
)

var identifierPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._:-]{0,127}$`)

//go:embed schemas/tools.json schemas/manifest.sha256
var schemaFiles embed.FS

// fileReader is the small subset of embed.FS used here. It makes the manifest
// parser independently testable with fstest.MapFS.
type fileReader interface {
	ReadFile(string) ([]byte, error)
}

type Config struct {
	Delegate        http.Handler
	AllowedOrigins  []string
	MaxRequestBytes int64
	ServerName      string
	ServerVersion   string
}

type Tool struct {
	Name        string          `json:"name"`
	Title       string          `json:"title,omitempty"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

type Server struct {
	delegate        http.Handler
	allowedOrigins  map[string]struct{}
	maxRequestBytes int64
	serverName      string
	serverVersion   string
	tools           []Tool
}

type toolsDocument struct {
	Schema string `json:"$schema"`
	Tools  []Tool `json:"tools"`
}

func NewServer(cfg Config) (*Server, error) {
	if cfg.Delegate == nil {
		return nil, errors.New("MCP delegate is required")
	}
	tools, err := loadTools(schemaFiles)
	if err != nil {
		return nil, err
	}
	origins := make(map[string]struct{}, len(cfg.AllowedOrigins))
	for _, origin := range cfg.AllowedOrigins {
		canonical, err := canonicalOrigin(origin)
		if err != nil {
			return nil, fmt.Errorf("MCP allowed origin: %w", err)
		}
		origins[canonical] = struct{}{}
	}
	maxBytes := cfg.MaxRequestBytes
	if maxBytes == 0 {
		maxBytes = defaultMaxBytes
	}
	if maxBytes < 1024 || maxBytes > 1024*1024 {
		return nil, errors.New("MCP request size must be between 1 KiB and 1 MiB")
	}
	name := strings.TrimSpace(cfg.ServerName)
	if name == "" {
		name = "flowops-ascp"
	}
	version := strings.TrimSpace(cfg.ServerVersion)
	if version == "" {
		version = "0.1.0"
	}
	return &Server{delegate: cfg.Delegate, allowedOrigins: origins, maxRequestBytes: maxBytes, serverName: name, serverVersion: version, tools: tools}, nil
}

func loadTools(files fileReader) ([]Tool, error) {
	manifest, err := files.ReadFile("schemas/manifest.sha256")
	if err != nil {
		return nil, fmt.Errorf("read MCP schema manifest: %w", err)
	}
	fields := strings.Fields(string(manifest))
	if len(fields) != 2 || fields[1] != "schemas/tools.json" || len(fields[0]) != 64 {
		return nil, errors.New("MCP schema manifest is invalid")
	}
	expected, err := hex.DecodeString(fields[0])
	if err != nil || len(expected) != sha256.Size {
		return nil, errors.New("MCP schema manifest digest is invalid")
	}
	raw, err := files.ReadFile(fields[1])
	if err != nil {
		return nil, fmt.Errorf("read MCP tool schemas: %w", err)
	}
	actual := sha256.Sum256(raw)
	if !bytes.Equal(expected, actual[:]) {
		return nil, errors.New("MCP schema manifest does not match tool schemas")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var document toolsDocument
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("decode MCP tool schemas: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, errors.New("MCP tool schemas contain trailing data")
	}
	if document.Schema == "" || len(document.Tools) == 0 {
		return nil, errors.New("MCP tool schemas are empty")
	}
	seen := make(map[string]struct{}, len(document.Tools))
	for _, tool := range document.Tools {
		if !validTool(tool) {
			return nil, errors.New("MCP tool schema is invalid")
		}
		if _, exists := seen[tool.Name]; exists {
			return nil, errors.New("MCP tool schema has duplicate names")
		}
		seen[tool.Name] = struct{}{}
	}
	return document.Tools, nil
}

func validTool(tool Tool) bool {
	if len(tool.Name) == 0 || len(tool.Name) > 128 || strings.TrimSpace(tool.Description) == "" || !json.Valid(tool.InputSchema) {
		return false
	}
	for _, character := range tool.Name {
		if !(character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '_' || character == '-' || character == '.') {
			return false
		}
	}
	var schema map[string]any
	return json.Unmarshal(tool.InputSchema, &schema) == nil && schema["type"] == "object"
}

func (s *Server) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	writer.Header().Set("X-Frame-Options", "DENY")
	if !s.originAllowed(request.Header.Get("Origin")) {
		s.writeHTTPError(writer, http.StatusForbidden, nil, -32003, "origin is not allowed")
		return
	}
	if request.Method == http.MethodGet || request.Method == http.MethodDelete {
		writer.Header().Set("Allow", http.MethodPost)
		writer.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", http.MethodPost)
		writer.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if !acceptsMCP(request.Header.Get("Accept")) {
		s.writeHTTPError(writer, http.StatusNotAcceptable, nil, -32000, "Accept must include application/json and text/event-stream")
		return
	}
	if !isJSONContentType(request.Header.Get("Content-Type")) {
		s.writeHTTPError(writer, http.StatusUnsupportedMediaType, nil, -32000, "Content-Type must be application/json")
		return
	}
	if version := request.Header.Get("MCP-Protocol-Version"); version != "" && version != ProtocolVersion {
		s.writeHTTPError(writer, http.StatusBadRequest, nil, -32000, "unsupported MCP protocol version")
		return
	}

	message, id, notification, err := decodeRequest(writer, request, s.maxRequestBytes)
	if err != nil {
		s.writeHTTPError(writer, http.StatusBadRequest, id, -32600, "invalid JSON-RPC request")
		return
	}
	if notification {
		writer.WriteHeader(http.StatusAccepted)
		return
	}
	if !s.authenticated(request.Context(), request.Header.Get("Authorization")) {
		s.writeRPC(writer, id, nil, &rpcError{Code: -32001, Message: "authentication failed"})
		return
	}

	result, rpcErr := s.dispatch(request.Context(), request.Header.Get("Authorization"), message)
	s.writeRPC(writer, id, result, rpcErr)
}

func (s *Server) originAllowed(value string) bool {
	if value == "" {
		return true
	}
	canonical, err := canonicalOrigin(value)
	if err != nil {
		return false
	}
	_, allowed := s.allowedOrigins[canonical]
	return allowed
}

func canonicalOrigin(value string) (string, error) {
	parsed, err := url.ParseRequestURI(strings.TrimSpace(value))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return "", errors.New("must be an http(s) origin without path")
	}
	return strings.ToLower(parsed.Scheme) + "://" + strings.ToLower(parsed.Host), nil
}

func acceptsMCP(value string) bool {
	hasJSON, hasSSE := false, false
	for _, part := range strings.Split(value, ",") {
		mediaType := strings.TrimSpace(strings.SplitN(part, ";", 2)[0])
		hasJSON = hasJSON || mediaType == "application/json"
		hasSSE = hasSSE || mediaType == "text/event-stream"
	}
	return hasJSON && hasSSE
}

func isJSONContentType(value string) bool {
	return strings.EqualFold(strings.TrimSpace(strings.SplitN(value, ";", 2)[0]), "application/json")
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func decodeRequest(writer http.ResponseWriter, request *http.Request, maxBytes int64) (rpcRequest, json.RawMessage, bool, error) {
	request.Body = http.MaxBytesReader(writer, request.Body, maxBytes)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	var message rpcRequest
	if err := decoder.Decode(&message); err != nil {
		return rpcRequest{}, nil, false, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return rpcRequest{}, message.ID, false, errors.New("trailing JSON data")
	}
	if message.JSONRPC != "2.0" || strings.TrimSpace(message.Method) == "" || (message.ID != nil && !validRequestID(message.ID)) {
		return rpcRequest{}, message.ID, false, errors.New("invalid JSON-RPC fields")
	}
	return message, message.ID, message.ID == nil, nil
}

func validRequestID(id json.RawMessage) bool {
	if bytes.Equal(id, []byte("null")) {
		return false
	}
	var stringID string
	if json.Unmarshal(id, &stringID) == nil {
		return true
	}
	var number json.Number
	return json.Unmarshal(id, &number) == nil
}

func (s *Server) authenticated(ctx context.Context, authorization string) bool {
	response := s.callBackend(ctx, authorization, http.MethodGet, "/v1/session", nil, nil)
	return response.status == http.StatusOK
}

func (s *Server) dispatch(ctx context.Context, authorization string, message rpcRequest) (any, *rpcError) {
	switch message.Method {
	case "initialize":
		return map[string]any{
			"protocolVersion": ProtocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      map[string]string{"name": s.serverName, "version": s.serverVersion},
		}, nil
	case "tools/list":
		if err := requireNoUnknown(message.Params, &struct {
			Cursor string `json:"cursor,omitempty"`
		}{}); err != nil {
			return nil, invalidParams()
		}
		return map[string]any{"tools": s.tools}, nil
	case "tools/call":
		return s.callTool(ctx, authorization, message.Params)
	default:
		return nil, &rpcError{Code: -32601, Message: "method not found"}
	}
}

func (s *Server) callTool(ctx context.Context, authorization string, params json.RawMessage) (any, *rpcError) {
	var call struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := requireNoUnknown(params, &call); err != nil || call.Name == "" || len(call.Arguments) == 0 {
		return nil, invalidParams()
	}
	var response backendResponse
	switch call.Name {
	case "ascp.operation.create":
		var arguments struct {
			Request        json.RawMessage `json:"request"`
			IdempotencyKey string          `json:"idempotencyKey"`
		}
		if err := requireNoUnknown(call.Arguments, &arguments); err != nil {
			return nil, invalidParams()
		}
		trimmedRequest := bytes.TrimSpace(arguments.Request)
		if !json.Valid(trimmedRequest) || len(trimmedRequest) == 0 || trimmedRequest[0] != '{' || !identifierPattern.MatchString(arguments.IdempotencyKey) {
			return nil, invalidParams()
		}
		response = s.callBackend(ctx, authorization, http.MethodPost, "/agent/v1/intents", map[string]string{"Idempotency-Key": arguments.IdempotencyKey}, trimmedRequest)
	case "ascp.operation.get":
		operationID, ok := oneIdentifierArgument(call.Arguments, "operationId")
		if !ok {
			return nil, invalidParams()
		}
		response = s.callBackend(ctx, authorization, http.MethodGet, "/agent/v1/intents/"+url.PathEscape(operationID), nil, nil)
	case "ascp.operation.evaluate":
		operationID, ok := oneIdentifierArgument(call.Arguments, "operationId")
		if !ok {
			return nil, invalidParams()
		}
		response = s.callBackend(ctx, authorization, http.MethodPost, "/agent/v1/intents/"+url.PathEscape(operationID)+"/evaluate", nil, nil)
	case "ascp.operation.decision.get":
		operationID, ok := oneIdentifierArgument(call.Arguments, "operationId")
		if !ok {
			return nil, invalidParams()
		}
		response = s.callBackend(ctx, authorization, http.MethodGet, "/agent/v1/intents/"+url.PathEscape(operationID)+"/decision", nil, nil)
	case "ascp.operation.authorize":
		operationID, ok := oneIdentifierArgument(call.Arguments, "operationId")
		if !ok {
			return nil, invalidParams()
		}
		response = s.callBackend(ctx, authorization, http.MethodPost, "/agent/v1/intents/"+url.PathEscape(operationID)+"/authorization", nil, nil)
	case "ascp.operation.authorization.get":
		operationID, ok := oneIdentifierArgument(call.Arguments, "operationId")
		if !ok {
			return nil, invalidParams()
		}
		response = s.callBackend(ctx, authorization, http.MethodGet, "/agent/v1/intents/"+url.PathEscape(operationID)+"/authorization", nil, nil)
	case "ascp.intent.create":
		var arguments struct {
			Intent         json.RawMessage `json:"intent"`
			IdempotencyKey string          `json:"idempotencyKey"`
		}
		if err := requireNoUnknown(call.Arguments, &arguments); err != nil || !json.Valid(arguments.Intent) || !identifierPattern.MatchString(arguments.IdempotencyKey) {
			return nil, invalidParams()
		}
		response = s.callBackend(ctx, authorization, http.MethodPost, "/v1/intents", map[string]string{"Idempotency-Key": arguments.IdempotencyKey}, arguments.Intent)
	case "ascp.intent.get":
		requestID, ok := oneIdentifierArgument(call.Arguments, "requestId")
		if !ok {
			return nil, invalidParams()
		}
		response = s.callBackend(ctx, authorization, http.MethodGet, "/v1/intents/"+url.PathEscape(requestID), nil, nil)
	case "ascp.approval.list":
		if err := requireNoUnknown(call.Arguments, &struct{}{}); err != nil {
			return nil, invalidParams()
		}
		response = s.callBackend(ctx, authorization, http.MethodGet, "/v1/approvals", nil, nil)
	case "ascp.approval.get":
		requestID, ok := oneIdentifierArgument(call.Arguments, "requestId")
		if !ok {
			return nil, invalidParams()
		}
		response = s.callBackend(ctx, authorization, http.MethodGet, "/v1/approvals/"+url.PathEscape(requestID), nil, nil)
	case "ascp.approval.decide":
		var arguments struct {
			RequestID      string `json:"requestId"`
			RequestDigest  string `json:"requestDigest"`
			Action         string `json:"action"`
			Note           string `json:"note,omitempty"`
			IdempotencyKey string `json:"idempotencyKey"`
		}
		if err := requireNoUnknown(call.Arguments, &arguments); err != nil || !identifierPattern.MatchString(arguments.RequestID) || !identifierPattern.MatchString(arguments.IdempotencyKey) || strings.TrimSpace(arguments.RequestDigest) == "" || len(arguments.RequestDigest) > 128 || (arguments.Action != "APPROVE" && arguments.Action != "REJECT") || len(arguments.Note) > 2048 {
			return nil, invalidParams()
		}
		body, _ := json.Marshal(map[string]string{"requestDigest": arguments.RequestDigest, "action": arguments.Action, "note": arguments.Note})
		response = s.callBackend(ctx, authorization, http.MethodPost, "/v1/approvals/"+url.PathEscape(arguments.RequestID)+"/decision", map[string]string{"Idempotency-Key": arguments.IdempotencyKey}, body)
	default:
		return nil, &rpcError{Code: -32602, Message: "tool is not supported"}
	}
	return toolResult(response), nil
}

func oneIdentifierArgument(raw json.RawMessage, field string) (string, bool) {
	var value map[string]json.RawMessage
	if err := requireNoUnknown(raw, &value); err != nil || len(value) != 1 {
		return "", false
	}
	encoded, exists := value[field]
	if !exists {
		return "", false
	}
	var identifier string
	return identifier, json.Unmarshal(encoded, &identifier) == nil && identifierPattern.MatchString(identifier)
}

func requireNoUnknown(raw json.RawMessage, target any) error {
	if len(raw) == 0 {
		raw = []byte(`{}`)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON data")
	}
	return nil
}

func invalidParams() *rpcError { return &rpcError{Code: -32602, Message: "invalid params"} }

type backendResponse struct {
	status int
	body   json.RawMessage
}

type backendRecorder struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func newBackendRecorder() *backendRecorder     { return &backendRecorder{header: make(http.Header)} }
func (w *backendRecorder) Header() http.Header { return w.header }
func (w *backendRecorder) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}
func (w *backendRecorder) Write(value []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.body.Write(value)
}

func (s *Server) callBackend(ctx context.Context, authorization, method, path string, extraHeaders map[string]string, body []byte) backendResponse {
	request, err := http.NewRequestWithContext(ctx, method, path, bytes.NewReader(body))
	if err != nil {
		return backendResponse{status: http.StatusInternalServerError, body: []byte(`{"error":{"code":"MCP_BACKEND_BUILD_FAILED","retriable":true}}`)}
	}
	request.Header.Set("Authorization", authorization)
	request.Header.Set("Accept", "application/json")
	if len(body) > 0 {
		request.Header.Set("Content-Type", "application/json")
	}
	for key, value := range extraHeaders {
		request.Header.Set(key, value)
	}
	recorder := newBackendRecorder()
	s.delegate.ServeHTTP(recorder, request)
	if recorder.status == 0 {
		recorder.status = http.StatusOK
	}
	return backendResponse{status: recorder.status, body: append(json.RawMessage(nil), recorder.body.Bytes()...)}
}

func toolResult(response backendResponse) map[string]any {
	var output any
	if json.Unmarshal(response.body, &output) != nil {
		output = map[string]any{"error": map[string]any{"code": "MCP_BACKEND_INVALID_RESPONSE", "retriable": true}}
	}
	output = redact(output)
	encoded, _ := json.Marshal(output)
	return map[string]any{
		"content":           []map[string]string{{"type": "text", "text": string(encoded)}},
		"structuredContent": output,
		"isError":           response.status < 200 || response.status >= 300,
	}
}

func redact(value any) any {
	switch current := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(current))
		for key, nested := range current {
			switch strings.ToLower(key) {
			case "signature", "privatekey", "private_key", "seed", "mnemonic", "accesstoken", "access_token", "rawapprovaltoken", "raw_approval_token", "calldata":
				continue
			}
			result[key] = redact(nested)
		}
		return result
	case []any:
		result := make([]any, len(current))
		for index, nested := range current {
			result[index] = redact(nested)
		}
		return result
	default:
		return value
	}
}

func (s *Server) writeHTTPError(writer http.ResponseWriter, status int, id json.RawMessage, code int, message string) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: message}})
}

func (s *Server) writeRPC(writer http.ResponseWriter, id json.RawMessage, result any, rpcErr *rpcError) {
	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(rpcResponse{JSONRPC: "2.0", ID: id, Result: result, Error: rpcErr})
}
