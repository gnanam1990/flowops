package controlapi

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gnanam1990/flowops/internal/controlplane"
	"github.com/gnanam1990/flowops/internal/reconciliation"
)

const (
	maxRequestBytes                 = 64 * 1024
	defaultCommandCompletionTimeout = 5 * time.Second
)

type ChainStatusSource interface {
	Status() reconciliation.ChainStatus
}

type ServerConfig struct {
	Store                    Store
	Lifecycle                *controlplane.Lifecycle
	Chain                    ChainStatusSource
	Clock                    func() time.Time
	IDSource                 func(prefix string) (string, error)
	CommandCompletionTimeout time.Duration
}

type Server struct {
	store                    Store
	lifecycle                *controlplane.Lifecycle
	chain                    ChainStatusSource
	clock                    func() time.Time
	idSource                 func(string) (string, error)
	commandCompletionTimeout time.Duration
	handler                  http.Handler
}

type principalContextKey struct{}

func NewServer(cfg ServerConfig) (*Server, error) {
	if cfg.Store == nil || cfg.Lifecycle == nil || cfg.Chain == nil {
		return nil, errors.New("store, lifecycle, and chain status are required")
	}
	clock := cfg.Clock
	if clock == nil {
		clock = time.Now
	}
	idSource := cfg.IDSource
	if idSource == nil {
		idSource = randomID
	}
	completionTimeout := cfg.CommandCompletionTimeout
	if completionTimeout <= 0 {
		completionTimeout = defaultCommandCompletionTimeout
	}
	s := &Server{
		store: cfg.Store, lifecycle: cfg.Lifecycle, chain: cfg.Chain, clock: clock, idSource: idSource,
		commandCompletionTimeout: completionTimeout,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", s.handleHealth)
	mux.Handle("POST /v1/intents", s.authenticate(http.HandlerFunc(s.handleCreateIntent)))
	mux.Handle("POST /v1/intents/{requestID}/authorization", s.authenticate(http.HandlerFunc(s.handleIssueAuthorization)))
	mux.Handle("GET /v1/approvals", s.authenticate(http.HandlerFunc(s.handleApprovals)))
	mux.Handle("POST /v1/approvals/{requestID}/decision", s.authenticate(http.HandlerFunc(s.handleApprovalDecision)))
	mux.Handle("POST /v1/agents/{agentID}/pause", s.authenticate(http.HandlerFunc(s.handlePauseAgent)))
	mux.Handle("GET /v1/commands/{commandID}", s.authenticate(http.HandlerFunc(s.handleCommand)))
	mux.Handle("GET /v1/dashboard/snapshot", s.authenticate(http.HandlerFunc(s.handleDashboardSnapshot)))
	s.handler = securityHeaders(mux)
	return s, nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.handler.ServeHTTP(w, r) }

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := r.Header.Get("Authorization")
		if !strings.HasPrefix(header, "Bearer ") || strings.Contains(header[7:], " ") {
			writeError(w, http.StatusUnauthorized, "UNAUTHENTICATED", ErrUnauthenticated, false, "")
			return
		}
		token := header[7:]
		if len(token) < 24 || len(token) > 512 {
			writeError(w, http.StatusUnauthorized, "UNAUTHENTICATED", ErrUnauthenticated, false, "")
			return
		}
		principal, err := s.store.Authenticate(r.Context(), TokenDigest(token))
		if err != nil || !principal.Valid() {
			writeError(w, http.StatusUnauthorized, "UNAUTHENTICATED", ErrUnauthenticated, false, "")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalContextKey{}, principal)))
	})
}

func principalFrom(ctx context.Context) Principal {
	principal, _ := ctx.Value(principalContextKey{}).(Principal)
	return principal
}

func (s *Server) authorize(w http.ResponseWriter, principal Principal, permission Permission, scope string) bool {
	if !principal.Can(permission) {
		writeError(w, http.StatusForbidden, "FORBIDDEN", ErrForbidden, false, "")
		return false
	}
	if principal.Kind == PrincipalAgent && !contains(principal.Scopes, scope) {
		writeError(w, http.StatusForbidden, "SCOPE_REQUIRED", ErrForbidden, false, "")
		return false
	}
	return true
}

func (s *Server) requireStepUp(w http.ResponseWriter, principal Principal) bool {
	if !principal.HasStepUp(s.clock()) {
		writeError(w, http.StatusForbidden, "STEP_UP_REQUIRED", errors.New("fresh step-up authentication is required"), false, "")
		return false
	}
	return true
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	status := s.chain.Status()
	writeJSON(w, http.StatusOK, map[string]any{
		"controlPlane":         "AVAILABLE",
		"chainState":           status.State,
		"authorizationsPaused": status.AuthorizationsPaused,
		"lastTrusted":          status.LastTrusted,
	})
}

func (s *Server) handleCreateIntent(w http.ResponseWriter, r *http.Request) {
	principal := principalFrom(r.Context())
	if !s.authorize(w, principal, PermissionCreateIntent, "intents:create") {
		return
	}
	idempotencyKey, ok := requireIdempotencyKey(w, r)
	if !ok {
		return
	}
	if !s.sweepExpired(w, r) {
		return
	}
	var intent controlplane.PaymentIntent
	if err := decodeJSON(w, r, &intent); err != nil {
		return
	}
	if intent.OrganizationID == "" {
		intent.OrganizationID = principal.OrganizationID
	}
	if intent.OrganizationID != principal.OrganizationID {
		writeError(w, http.StatusNotFound, "NOT_FOUND", ErrNotFound, false, "")
		return
	}
	if intent.IntentID != idempotencyKey {
		writeError(w, http.StatusConflict, "IDEMPOTENCY_MISMATCH", errors.New("intentId must equal Idempotency-Key"), false, "")
		return
	}
	if principal.Kind == PrincipalAgent && intent.AgentID != principal.AgentID {
		writeError(w, http.StatusNotFound, "NOT_FOUND", ErrNotFound, false, "")
		return
	}
	agent, err := s.store.Agent(r.Context(), principal.OrganizationID, intent.AgentID)
	if err != nil || intent.CustomerID != agent.CustomerID {
		writeError(w, http.StatusNotFound, "NOT_FOUND", ErrNotFound, false, "")
		return
	}
	digest, err := intent.Digest()
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_INTENT", err, false, "")
		return
	}
	command, created, ok := s.beginCommand(w, r, principal, "intent.create", intent.IntentID, idempotencyKey, digest)
	if !ok || !created {
		if ok {
			writeStoredCommand(w, command)
		}
		return
	}
	record, err := s.lifecycle.Submit(r.Context(), intent)
	if err != nil {
		s.failCommand(w, r, command, err)
		return
	}
	s.succeedCommand(w, r, command, record, http.StatusCreated)
}

func (s *Server) handleIssueAuthorization(w http.ResponseWriter, r *http.Request) {
	principal := principalFrom(r.Context())
	if !s.authorize(w, principal, PermissionIssue, "authorizations:issue") {
		return
	}
	idempotencyKey, ok := requireIdempotencyKey(w, r)
	if !ok {
		return
	}
	requestID := r.PathValue("requestID")
	record, exists := s.lifecycle.Get(requestID)
	if !exists || record.Intent.OrganizationID != principal.OrganizationID ||
		(principal.Kind == PrincipalAgent && record.Intent.AgentID != principal.AgentID) {
		writeError(w, http.StatusNotFound, "NOT_FOUND", ErrNotFound, false, "")
		return
	}
	digest := digestJSON(struct {
		RequestID     string `json:"requestId"`
		RequestDigest string `json:"requestDigest"`
	}{requestID, record.RequestDigest})
	command, created, ok := s.beginCommand(w, r, principal, "authorization.issue", requestID, idempotencyKey, digest)
	if !ok || !created {
		if ok {
			writeStoredCommand(w, command)
		}
		return
	}
	signed, err := s.lifecycle.Issue(r.Context(), requestID)
	if err != nil {
		s.failCommand(w, r, command, err)
		return
	}
	s.succeedCommand(w, r, command, signed, http.StatusOK)
}

func (s *Server) handleApprovals(w http.ResponseWriter, r *http.Request) {
	principal := principalFrom(r.Context())
	if principal.Kind != PrincipalHuman {
		writeError(w, http.StatusForbidden, "FORBIDDEN", ErrForbidden, false, "")
		return
	}
	if !s.authorize(w, principal, PermissionRead, "records:read") {
		return
	}
	if !s.sweepExpired(w, r) {
		return
	}
	records := s.lifecycle.PendingApprovals()
	filtered := make([]controlplane.Record, 0, len(records))
	for _, record := range records {
		if record.Intent.OrganizationID == principal.OrganizationID {
			filtered = append(filtered, record)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"approvals": filtered})
}

type approvalDecisionRequest struct {
	RequestDigest string                      `json:"requestDigest"`
	Action        controlplane.ApprovalAction `json:"action"`
	Note          string                      `json:"note"`
}

func (s *Server) handleApprovalDecision(w http.ResponseWriter, r *http.Request) {
	principal := principalFrom(r.Context())
	if !s.authorize(w, principal, PermissionDecide, "approvals:decide") || !s.requireStepUp(w, principal) {
		return
	}
	idempotencyKey, ok := requireIdempotencyKey(w, r)
	if !ok {
		return
	}
	requestID := r.PathValue("requestID")
	record, exists := s.lifecycle.Get(requestID)
	if !exists || record.Intent.OrganizationID != principal.OrganizationID {
		writeError(w, http.StatusNotFound, "NOT_FOUND", ErrNotFound, false, "")
		return
	}
	var request approvalDecisionRequest
	if err := decodeJSON(w, r, &request); err != nil {
		return
	}
	if (request.Action != controlplane.Approve && request.Action != controlplane.Reject) || len(request.Note) > 2048 {
		writeError(w, http.StatusBadRequest, "INVALID_DECISION", errors.New("approval action or note is invalid"), false, "")
		return
	}
	digest := digestJSON(struct {
		RequestID string                  `json:"requestId"`
		Decision  approvalDecisionRequest `json:"decision"`
	}{requestID, request})
	command, created, ok := s.beginCommand(w, r, principal, "approval.decide", requestID, idempotencyKey, digest)
	if !ok || !created {
		if ok {
			writeStoredCommand(w, command)
		}
		return
	}
	decided, err := s.lifecycle.Decide(r.Context(), requestID, request.RequestDigest, request.Action, principal.ID, request.Note)
	if err != nil {
		s.failCommand(w, r, command, err)
		return
	}
	s.succeedCommand(w, r, command, decided, http.StatusOK)
}

type pauseRequest struct {
	Reason string `json:"reason"`
}

func (s *Server) handlePauseAgent(w http.ResponseWriter, r *http.Request) {
	principal := principalFrom(r.Context())
	if !s.authorize(w, principal, PermissionPause, "agents:pause") || !s.requireStepUp(w, principal) {
		return
	}
	idempotencyKey, ok := requireIdempotencyKey(w, r)
	if !ok {
		return
	}
	var request pauseRequest
	if err := decodeJSON(w, r, &request); err != nil {
		return
	}
	request.Reason = strings.TrimSpace(request.Reason)
	if request.Reason == "" || len(request.Reason) > 1024 {
		writeError(w, http.StatusBadRequest, "INVALID_REASON", errors.New("reason must contain 1 to 1024 characters"), false, "")
		return
	}
	agentID := r.PathValue("agentID")
	digest := digestJSON(struct {
		AgentID string `json:"agentId"`
		Reason  string `json:"reason"`
	}{agentID, request.Reason})
	command, created, ok := s.beginCommand(w, r, principal, "agent.pause", agentID, idempotencyKey, digest)
	if !ok || !created {
		if ok {
			writeStoredCommand(w, command)
		}
		return
	}
	auditID, err := s.idSource("audit")
	if err != nil {
		s.failCommand(w, r, command, err)
		return
	}
	agent, err := s.store.SetAgentStatus(r.Context(), principal.OrganizationID, agentID, AgentPaused, principal.ID, auditID)
	if err != nil {
		s.failCommand(w, r, command, err)
		return
	}
	s.succeedCommand(w, r, command, agent, http.StatusOK)
}

func (s *Server) handleCommand(w http.ResponseWriter, r *http.Request) {
	principal := principalFrom(r.Context())
	if !s.authorize(w, principal, PermissionReadCommand, "commands:read") {
		return
	}
	command, err := s.store.Command(r.Context(), principal.OrganizationID, r.PathValue("commandID"))
	if err != nil || (principal.Kind == PrincipalAgent && command.ActorID != principal.ID) {
		writeError(w, http.StatusNotFound, "NOT_FOUND", ErrNotFound, false, "")
		return
	}
	writeJSON(w, http.StatusOK, command)
}

type DashboardSnapshot struct {
	Live             bool                       `json:"live"`
	GeneratedAt      time.Time                  `json:"generatedAt"`
	OrganizationID   string                     `json:"organizationId"`
	Chain            reconciliation.ChainStatus `json:"chain"`
	PendingApprovals []controlplane.Record      `json:"pendingApprovals"`
	Agents           []Agent                    `json:"agents"`
}

func (s *Server) handleDashboardSnapshot(w http.ResponseWriter, r *http.Request) {
	principal := principalFrom(r.Context())
	if principal.Kind != PrincipalHuman {
		writeError(w, http.StatusForbidden, "FORBIDDEN", ErrForbidden, false, "")
		return
	}
	if !s.authorize(w, principal, PermissionRead, "records:read") {
		return
	}
	if !s.sweepExpired(w, r) {
		return
	}
	agents, err := s.store.ListAgents(r.Context(), principal.OrganizationID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "STORE_UNAVAILABLE", err, true, "")
		return
	}
	pending := s.lifecycle.PendingApprovals()
	approvals := make([]controlplane.Record, 0, len(pending))
	for _, record := range pending {
		if record.Intent.OrganizationID == principal.OrganizationID {
			approvals = append(approvals, record)
		}
	}
	writeJSON(w, http.StatusOK, DashboardSnapshot{
		Live: true, GeneratedAt: s.clock().UTC(), OrganizationID: principal.OrganizationID,
		Chain: s.chain.Status(), PendingApprovals: approvals, Agents: agents,
	})
}

func (s *Server) beginCommand(w http.ResponseWriter, r *http.Request, principal Principal, kind, targetID, idempotencyKey, inputDigest string) (Command, bool, bool) {
	commandID, err := s.idSource("cmd")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "ID_GENERATION_FAILED", err, true, "")
		return Command{}, false, false
	}
	command := Command{
		ID: commandID, OrganizationID: principal.OrganizationID, ActorID: principal.ID,
		Kind: kind, TargetID: targetID, IdempotencyKey: idempotencyKey, InputDigest: inputDigest,
		State: CommandPending, CreatedAt: s.clock().UTC(),
	}
	stored, created, err := s.store.BeginCommand(r.Context(), command)
	if errors.Is(err, ErrIdempotencyConflict) {
		writeError(w, http.StatusConflict, "IDEMPOTENCY_CONFLICT", err, false, "")
		return Command{}, false, false
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "STORE_UNAVAILABLE", err, true, "")
		return Command{}, false, false
	}
	return stored, created, true
}

func (s *Server) succeedCommand(w http.ResponseWriter, r *http.Request, command Command, result any, status int) {
	raw, err := json.Marshal(result)
	if err != nil {
		s.failCommand(w, r, command, err)
		return
	}
	completionCtx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), s.commandCompletionTimeout)
	defer cancel()
	completed, err := s.store.CompleteCommand(completionCtx, command.OrganizationID, command.ID, CommandSucceeded, raw, "")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "COMMAND_COMMIT_FAILED", err, true, command.ID)
		return
	}
	writeJSON(w, status, map[string]any{"command": completed, "result": result})
}

func (s *Server) failCommand(w http.ResponseWriter, r *http.Request, command Command, operationErr error) {
	status, code, retriable := classifyError(operationErr)
	errorResult, _ := json.Marshal(map[string]string{"code": code})
	completionCtx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), s.commandCompletionTimeout)
	defer cancel()
	completed, completeErr := s.store.CompleteCommand(completionCtx, command.OrganizationID, command.ID, CommandFailed, errorResult, code)
	if completeErr != nil {
		writeError(w, http.StatusInternalServerError, "COMMAND_COMMIT_FAILED", completeErr, true, command.ID)
		return
	}
	writeError(w, status, code, operationErr, retriable, completed.ID)
}

func (s *Server) sweepExpired(w http.ResponseWriter, r *http.Request) bool {
	if _, err := s.lifecycle.SweepExpired(r.Context()); err != nil {
		status, code, retriable := classifyError(err)
		writeError(w, status, code, err, retriable, "")
		return false
	}
	return true
}

func writeStoredCommand(w http.ResponseWriter, command Command) {
	status := commandHTTPStatus(command)
	var result any
	if len(command.Result) > 0 {
		_ = json.Unmarshal(command.Result, &result)
	}
	if command.State == CommandFailed {
		writeJSON(w, status, map[string]any{
			"command": command,
			"error": map[string]any{
				"code": command.ErrorCode, "message": "the original command failed",
				"retriable": command.ErrorCode == "CHAIN_UNAVAILABLE" || command.ErrorCode == "OPERATION_UNRESOLVED" || command.ErrorCode == "CONTROL_JOURNAL_STALE" || command.ErrorCode == "INTERNAL_ERROR",
			},
		})
		return
	}
	writeJSON(w, status, map[string]any{"command": command, "result": result})
}

func commandHTTPStatus(command Command) int {
	if command.State == CommandPending {
		return http.StatusAccepted
	}
	if command.State == CommandSucceeded {
		return http.StatusOK
	}
	switch command.ErrorCode {
	case "NOT_FOUND":
		return http.StatusNotFound
	case "APPROVAL_EXPIRED":
		return http.StatusGone
	case "STATE_CONFLICT", "AGENT_FROZEN", "POLICY_UNAVAILABLE":
		return http.StatusConflict
	case "CHAIN_UNAVAILABLE", "OPERATION_UNRESOLVED", "STORE_UNAVAILABLE", "COMMAND_COMMIT_FAILED", "CONTROL_JOURNAL_STALE":
		return http.StatusServiceUnavailable
	case "INTERNAL_ERROR":
		return http.StatusInternalServerError
	case "FORBIDDEN":
		return http.StatusForbidden
	default:
		return http.StatusBadRequest
	}
}

func classifyError(err error) (int, string, bool) {
	switch {
	case errors.Is(err, controlplane.ErrIdempotencyConflict), errors.Is(err, controlplane.ErrApprovalDigest),
		errors.Is(err, controlplane.ErrNotPendingApproval), errors.Is(err, controlplane.ErrNotApproved),
		errors.Is(err, controlplane.ErrPolicyChanged):
		return http.StatusConflict, "STATE_CONFLICT", false
	case errors.Is(err, controlplane.ErrApprovalExpired):
		return http.StatusGone, "APPROVAL_EXPIRED", false
	case errors.Is(err, controlplane.ErrPolicyUnavailable):
		return http.StatusConflict, "POLICY_UNAVAILABLE", false
	case errors.Is(err, controlplane.ErrFrozen):
		return http.StatusConflict, "AGENT_FROZEN", false
	case errors.Is(err, controlplane.ErrUnknownRequest), errors.Is(err, ErrNotFound):
		return http.StatusNotFound, "NOT_FOUND", false
	case errors.Is(err, reconciliation.ErrChainUnavailable):
		return http.StatusServiceUnavailable, "CHAIN_UNAVAILABLE", true
	case errors.Is(err, controlplane.ErrJournalStale):
		return http.StatusServiceUnavailable, "CONTROL_JOURNAL_STALE", true
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return http.StatusServiceUnavailable, "OPERATION_UNRESOLVED", true
	case errors.Is(err, ErrForbidden):
		return http.StatusForbidden, "FORBIDDEN", false
	default:
		message := err.Error()
		if strings.Contains(message, "chain unavailable") {
			return http.StatusServiceUnavailable, "CHAIN_UNAVAILABLE", true
		}
		if strings.Contains(message, "frozen") || strings.Contains(message, "execution is blocked") {
			return http.StatusConflict, "AGENT_FROZEN", false
		}
		return http.StatusInternalServerError, "INTERNAL_ERROR", true
	}
}

func requireIdempotencyKey(w http.ResponseWriter, r *http.Request) (string, bool) {
	key := r.Header.Get("Idempotency-Key")
	if !identifierPattern.MatchString(key) {
		writeError(w, http.StatusBadRequest, "INVALID_IDEMPOTENCY_KEY", errors.New("a valid Idempotency-Key is required"), false, "")
		return "", false
	}
	return key, true
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", errors.New("request body is invalid"), false, "")
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", errors.New("request body must contain one JSON value"), false, "")
		return errors.New("trailing JSON content")
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, code string, err error, retriable bool, commandID string) {
	message := err.Error()
	if status >= 500 {
		message = "request could not be completed"
	}
	writeJSON(w, status, map[string]any{
		"error":     map[string]any{"code": code, "message": message, "retriable": retriable},
		"commandId": commandID,
	})
}

func digestJSON(value any) string {
	raw, _ := json.Marshal(value)
	digest := sha256.Sum256(raw)
	return "0x" + hex.EncodeToString(digest[:])
}

func randomID(prefix string) (string, error) {
	if !identifierPattern.MatchString(prefix) {
		return "", errors.New("ID prefix is invalid")
	}
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return "", fmt.Errorf("read cryptographic randomness: %w", err)
	}
	return prefix + "_" + hex.EncodeToString(random), nil
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
