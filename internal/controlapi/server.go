package controlapi

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gnanam1990/flowops/internal/ascpactivation"
	"github.com/gnanam1990/flowops/internal/ascpagent"
	"github.com/gnanam1990/flowops/internal/ascpapproval"
	"github.com/gnanam1990/flowops/internal/ascpbearer"
	"github.com/gnanam1990/flowops/internal/ascpintake"
	"github.com/gnanam1990/flowops/internal/ascporchestration"
	"github.com/gnanam1990/flowops/internal/ascpsignerbinding"
	"github.com/gnanam1990/flowops/internal/controlplane"
	"github.com/gnanam1990/flowops/internal/directoryreader"
	"github.com/gnanam1990/flowops/internal/reconciliation"
	"github.com/gnanam1990/flowops/pkg/broadcastreceipt"
	"github.com/gnanam1990/flowops/pkg/sellerquote"
)

const (
	maxRequestBytes                 = 64 * 1024
	maxActivationRequestBytes       = 2 * 1024 * 1024
	defaultCommandCompletionTimeout = 5 * time.Second
)

type ChainController interface {
	Status() reconciliation.ChainStatus
	ForceHalt(context.Context, string, string) (reconciliation.ChainStatus, error)
	Resume(context.Context, string) (reconciliation.ChainStatus, error)
}

type ReconciliationReader interface {
	OrganizationView(string) reconciliation.OrganizationView
}

type ReconciliationOperator interface {
	ReconciliationReader
	QuarantineForOrganization(context.Context, string, string, string, string) (reconciliation.Execution, error)
}

type ASCPAgentService interface {
	Create(context.Context, ascpagent.Identity, string, ascpagent.CreateRequest) (ascpintake.Operation, error)
	Get(context.Context, ascpagent.Identity, string) (ascpintake.Operation, error)
}

type ASCPFlowService interface {
	Evaluate(context.Context, ascporchestration.Identity, string) (ascporchestration.Decision, error)
	Decision(context.Context, ascporchestration.Identity, string) (ascporchestration.Decision, error)
	Approval(context.Context, string, string) (ascpapproval.Approval, error)
	DecideApproval(context.Context, string, string, string, bool, string) (ascpapproval.Approval, error)
	Authorize(context.Context, ascporchestration.Identity, string) (ascporchestration.Authorization, error)
	Authorization(context.Context, ascporchestration.Identity, string) (ascporchestration.Authorization, error)
}

type ASCPActivationService interface {
	Create(context.Context, ascporchestration.Identity, string, ascpactivation.Request) (ascpactivation.Status, error)
	Get(context.Context, ascporchestration.Identity, string) (ascpactivation.Status, error)
}

type ASCPSignerBindingService interface {
	Put(context.Context, string, string, string, string, ascpsignerbinding.PutRequest) (ascpsignerbinding.Result, error)
	Current(context.Context, string, string) (ascpsignerbinding.Binding, error)
}

type ASCPSettlementAttemptRequest struct {
	OperationID     string `json:"operationId"`
	Action          string `json:"action"`
	TransactionHash string `json:"transactionHash"`
	DeliveryHash    string `json:"deliveryHash,omitempty"`
	EvidenceHash    string `json:"evidenceHash,omitempty"`
}

type ASCPSettlementAttempt struct {
	ASCPSettlementAttemptRequest
	State        string    `json:"state"`
	RegisteredAt time.Time `json:"registeredAt"`
	Replayed     bool      `json:"replayed"`
}

var (
	ErrASCPSettlementAttemptInvalid  = errors.New("ASCP settlement attempt is invalid")
	ErrASCPSettlementAttemptConflict = errors.New("ASCP settlement attempt conflicts with durable state")
)

// ASCPSettlementRegistrar records keeper-submitted transaction identity only.
// It cannot declare success, finality, or accounting outcomes.
type ASCPSettlementRegistrar interface {
	Register(context.Context, ASCPSettlementAttemptRequest) (ASCPSettlementAttempt, error)
}

type ServerConfig struct {
	Store                    Store
	Lifecycle                *controlplane.Lifecycle
	Chain                    ChainController
	Clock                    func() time.Time
	IDSource                 func(prefix string) (string, error)
	CommandCompletionTimeout time.Duration
	SiteSessions             *SiteSessionCodec
	OperatorControlKey       []byte
	SignerBroadcasts         BroadcastRegistrar
	SignerEscrowBroadcasts   EscrowBroadcastRegistrar
	Escrow                   *EscrowRegistrar
	Reconciliation           ReconciliationReader
	ASCPAgent                ASCPAgentService
	ASCPFlow                 ASCPFlowService
	ASCPActivation           ASCPActivationService
	ASCPSignerBindings       ASCPSignerBindingService
	ASCPSettlement           ASCPSettlementRegistrar
	KeeperCallbackKey        []byte
}

type Server struct {
	store                    Store
	lifecycle                *controlplane.Lifecycle
	chain                    ChainController
	clock                    func() time.Time
	idSource                 func(string) (string, error)
	commandCompletionTimeout time.Duration
	siteSessions             *SiteSessionCodec
	operatorControlKey       []byte
	signerBroadcasts         BroadcastRegistrar
	signerEscrowBroadcasts   EscrowBroadcastRegistrar
	escrow                   *EscrowRegistrar
	reconciliation           ReconciliationReader
	ascpAgent                ASCPAgentService
	ascpFlow                 ASCPFlowService
	ascpActivation           ASCPActivationService
	ascpSignerBindings       ASCPSignerBindingService
	ascpSettlement           ASCPSettlementRegistrar
	keeperCallbackKey        []byte
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
		commandCompletionTimeout: completionTimeout, siteSessions: cfg.SiteSessions,
		operatorControlKey:     append([]byte(nil), cfg.OperatorControlKey...),
		signerBroadcasts:       cfg.SignerBroadcasts,
		signerEscrowBroadcasts: cfg.SignerEscrowBroadcasts,
		escrow:                 cfg.Escrow,
		reconciliation:         cfg.Reconciliation,
		ascpAgent:              cfg.ASCPAgent,
		ascpFlow:               cfg.ASCPFlow,
		ascpActivation:         cfg.ASCPActivation,
		ascpSignerBindings:     cfg.ASCPSignerBindings,
		ascpSettlement:         cfg.ASCPSettlement,
		keeperCallbackKey:      append([]byte(nil), cfg.KeeperCallbackKey...),
	}
	if len(s.operatorControlKey) != 0 && len(s.operatorControlKey) != 32 {
		return nil, errors.New("operator control key must contain exactly 32 bytes")
	}
	if len(s.keeperCallbackKey) != 0 && len(s.keeperCallbackKey) != 32 {
		return nil, errors.New("keeper callback key must contain exactly 32 bytes")
	}
	if len(s.operatorControlKey) == 32 && len(s.keeperCallbackKey) == 32 && subtle.ConstantTimeCompare(s.operatorControlKey, s.keeperCallbackKey) == 1 {
		return nil, errors.New("keeper callback key must be distinct from operator control key")
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", s.handleHealth)
	mux.HandleFunc("POST /v1/sites/session", s.handleSiteSessionExchange)
	mux.Handle("GET /v1/session", s.authenticate(http.HandlerFunc(s.handleSession)))
	mux.Handle("POST /v1/intents", s.authenticate(http.HandlerFunc(s.handleCreateIntent)))
	mux.Handle("GET /v1/intents/{requestID}", s.authenticate(http.HandlerFunc(s.handleIntent)))
	mux.Handle("POST /v1/intents/{requestID}/authorization", s.authenticate(http.HandlerFunc(s.handleIssueAuthorization)))
	mux.Handle("POST /agent/v1/intents", s.authenticate(http.HandlerFunc(s.handleCreateASCPIntent)))
	mux.Handle("GET /agent/v1/intents/{operationID}", s.authenticate(http.HandlerFunc(s.handleASCPIntent)))
	mux.Handle("POST /agent/v1/intents/{operationID}/evaluate", s.authenticate(http.HandlerFunc(s.handleEvaluateASCPIntent)))
	mux.Handle("GET /agent/v1/intents/{operationID}/decision", s.authenticate(http.HandlerFunc(s.handleASCPDecision)))
	mux.Handle("POST /agent/v1/intents/{operationID}/authorization", s.authenticate(http.HandlerFunc(s.handleAuthorizeASCPIntent)))
	mux.Handle("GET /agent/v1/intents/{operationID}/authorization", s.authenticate(http.HandlerFunc(s.handleASCPAuthorization)))
	mux.Handle("POST /agent/v1/intents/{operationID}/activation", s.authenticate(http.HandlerFunc(s.handleCreateASCPActivation)))
	mux.Handle("GET /agent/v1/intents/{operationID}/activation", s.authenticate(http.HandlerFunc(s.handleASCPActivation)))
	mux.Handle("GET /v1/ascp/approvals/{approvalID}", s.authenticate(http.HandlerFunc(s.handleASCPApproval)))
	mux.Handle("POST /v1/ascp/approvals/{approvalID}/decision", s.authenticate(http.HandlerFunc(s.handleASCPApprovalDecision)))
	mux.Handle("GET /v1/approvals", s.authenticate(http.HandlerFunc(s.handleApprovals)))
	mux.Handle("GET /v1/approvals/{requestID}", s.authenticate(http.HandlerFunc(s.handleApproval)))
	mux.Handle("POST /v1/approvals/{requestID}/decision", s.authenticate(http.HandlerFunc(s.handleApprovalDecision)))
	mux.Handle("POST /v1/agents/{agentID}/pause", s.authenticate(http.HandlerFunc(s.handlePauseAgent)))
	mux.Handle("PUT /v1/agents/{agentID}/signer-binding", s.authenticate(http.HandlerFunc(s.handlePutASCPSignerBinding)))
	mux.Handle("GET /v1/agents/{agentID}/signer-binding", s.authenticate(http.HandlerFunc(s.handleGetASCPSignerBinding)))
	mux.Handle("POST /v1/organization/pause", s.authenticate(http.HandlerFunc(s.handlePauseOrganization)))
	mux.Handle("GET /v1/commands/{commandID}", s.authenticate(http.HandlerFunc(s.handleCommand)))
	mux.Handle("GET /v1/dashboard/snapshot", s.authenticate(http.HandlerFunc(s.handleDashboardSnapshot)))
	mux.HandleFunc("POST /v1/signer/broadcasts", s.handleSignerBroadcast)
	mux.HandleFunc("POST /v1/signer/escrow-broadcasts", s.handleSignerEscrowBroadcast)
	if s.escrow != nil {
		mux.Handle("POST /v1/escrow/intents/{authorizationID}", s.authenticate(http.HandlerFunc(s.handleEscrowIntent)))
		mux.Handle("POST /v1/escrow/calls/{callID}/transitions", s.authenticate(http.HandlerFunc(s.handleEscrowTransition)))
		mux.Handle("GET /v1/escrow/calls/{callID}", s.authenticate(http.HandlerFunc(s.handleEscrowCall)))
	}
	if len(s.operatorControlKey) == 32 {
		mux.Handle("POST /v1/operator/chain/halt", s.authenticateOperator(http.HandlerFunc(s.handleOperatorHalt)))
		mux.Handle("POST /v1/operator/chain/resume", s.authenticateOperator(http.HandlerFunc(s.handleOperatorResume)))
		mux.Handle("GET /v1/operator/reconciliation", s.authenticateOperator(http.HandlerFunc(s.handleOperatorReconciliation)))
		mux.Handle("POST /v1/operator/executions/{executionID}/quarantine", s.authenticateOperator(http.HandlerFunc(s.handleOperatorQuarantine)))
	}
	if s.ascpSettlement != nil && len(s.keeperCallbackKey) == 32 {
		mux.Handle("POST /v1/ascp/settlement-attempts", s.authenticateStaticKey(s.keeperCallbackKey, http.HandlerFunc(s.handleASCPSettlementAttempt)))
	}
	s.handler = securityHeaders(s.withAgentCorrelation(mux))
	return s, nil
}

func (s *Server) authenticateOperator(next http.Handler) http.Handler {
	return s.authenticateStaticKey(s.operatorControlKey, next)
}

func (s *Server) authenticateStaticKey(key []byte, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := bearerToken(r.Header.Get("Authorization"))
		decoded, err := base64.StdEncoding.DecodeString(token)
		if !ok || err != nil || len(decoded) != len(key) || subtle.ConstantTimeCompare(decoded, key) != 1 {
			writeError(w, http.StatusUnauthorized, "UNAUTHENTICATED", ErrUnauthenticated, false, "")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleASCPSettlementAttempt(w http.ResponseWriter, r *http.Request) {
	var request ASCPSettlementAttemptRequest
	if decodeJSON(w, r, &request) != nil {
		return
	}
	attempt, err := s.ascpSettlement.Register(r.Context(), request)
	if err != nil {
		status, code := http.StatusInternalServerError, "ASCP_SETTLEMENT_ATTEMPT_FAILED"
		if errors.Is(err, ErrASCPSettlementAttemptInvalid) {
			status, code = http.StatusBadRequest, "INVALID_ASCP_SETTLEMENT_ATTEMPT"
		} else if errors.Is(err, ErrASCPSettlementAttemptConflict) {
			status, code = http.StatusConflict, "ASCP_SETTLEMENT_ATTEMPT_CONFLICT"
		}
		writeError(w, status, code, err, false, "")
		return
	}
	status := http.StatusCreated
	if attempt.Replayed {
		status = http.StatusOK
	}
	writeJSON(w, status, attempt)
}

func (s *Server) withAgentCorrelation(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/agent/v1/") {
			next.ServeHTTP(w, r)
			return
		}
		correlationID, err := s.idSource("corr")
		if err != nil {
			correlationID, err = randomID("corr")
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "CORRELATION_ID_FAILED", errors.New("correlation ID generation failed"), true, "")
			return
		}
		w.Header().Set("X-Correlation-ID", correlationID)
		next.ServeHTTP(w, r)
	})
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
		token, ok := bearerToken(r.Header.Get("Authorization"))
		if !ok || (len(token) > 512 && !strings.HasPrefix(token, siteSessionPrefix)) {
			writeError(w, http.StatusUnauthorized, "UNAUTHENTICATED", ErrUnauthenticated, false, "")
			return
		}
		principal, err := s.store.Authenticate(r.Context(), token)
		if err != nil || !principal.Valid() {
			writeError(w, http.StatusUnauthorized, "UNAUTHENTICATED", ErrUnauthenticated, false, "")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalContextKey{}, principal)))
	})
}

type siteSessionExchangeRequest struct {
	SiteProjectID string `json:"siteProjectId"`
	SiteUserKey   string `json:"siteUserKey"`
	Email         string `json:"email"`
}

func (s *Server) handleSiteSessionExchange(w http.ResponseWriter, r *http.Request) {
	if s.siteSessions == nil {
		writeError(w, http.StatusServiceUnavailable, "SITE_SESSION_UNAVAILABLE", errors.New("site sessions are unavailable"), true, "")
		return
	}
	exchangeToken, ok := bearerToken(r.Header.Get("Authorization"))
	if !ok || len(exchangeToken) < 32 || len(exchangeToken) > 512 || strings.HasPrefix(exchangeToken, siteSessionPrefix) {
		writeError(w, http.StatusUnauthorized, "UNAUTHENTICATED", ErrUnauthenticated, false, "")
		return
	}
	var request siteSessionExchangeRequest
	if err := decodeJSON(w, r, &request); err != nil {
		return
	}
	membership, err := s.store.ExchangeSiteIdentity(r.Context(), request.SiteProjectID, request.SiteUserKey, request.Email, exchangeToken)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "UNAUTHENTICATED", ErrUnauthenticated, false, "")
		return
	}
	token, expiresAt, err := s.siteSessions.Mint(membership)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "SITE_SESSION_FAILED", err, true, "")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"accessToken": token, "expiresAt": expiresAt, "organizationId": membership.OrganizationID,
		"principalId": membership.PrincipalID, "role": membership.Role,
	})
}

func principalFrom(ctx context.Context) Principal {
	principal, _ := ctx.Value(principalContextKey{}).(Principal)
	return principal
}

func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	principal := principalFrom(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{
		"principalId": principal.ID, "organizationId": principal.OrganizationID,
		"kind": principal.Kind, "role": principal.Role, "readOnly": principal.ReadOnly,
		"stepUpUntil": principal.StepUpUntil,
	})
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
		"requiredObservers":    status.RequiredObserverQuorum,
		"respondingObservers":  status.RespondingObservers,
		"lastObservationAt":    status.LastObservationAt,
		"readyForManualResume": status.ReadyForManualResume,
		"lastTrusted":          status.LastTrusted,
	})
}

func (s *Server) handleSignerBroadcast(w http.ResponseWriter, r *http.Request) {
	if s.signerBroadcasts == nil {
		writeError(w, http.StatusServiceUnavailable, "SIGNER_BROADCASTS_UNAVAILABLE", errors.New("customer signer broadcast registration is unavailable"), true, "")
		return
	}
	var signed broadcastreceipt.SignedReceipt
	if err := decodeJSON(w, r, &signed); err != nil {
		return
	}
	execution, err := s.signerBroadcasts.Register(r.Context(), signed)
	if err != nil {
		switch {
		case errors.Is(err, ErrBroadcastKeyUnknown), errors.Is(err, ErrBroadcastSignature):
			writeError(w, http.StatusUnauthorized, "INVALID_SIGNER_RECEIPT", errors.New("signer receipt authentication failed"), false, "")
		case errors.Is(err, ErrBroadcastBinding), errors.Is(err, ErrBroadcastTime), errors.Is(err, ErrBroadcastRail), errors.Is(err, reconciliation.ErrConflict):
			writeError(w, http.StatusConflict, "BROADCAST_BINDING_REJECTED", err, false, "")
		default:
			writeError(w, http.StatusServiceUnavailable, "BROADCAST_REGISTRATION_UNRESOLVED", err, true, "")
		}
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"execution": execution})
}

func (s *Server) handleSignerEscrowBroadcast(w http.ResponseWriter, r *http.Request) {
	if s.signerEscrowBroadcasts == nil {
		writeError(w, http.StatusServiceUnavailable, "SIGNER_ESCROW_BROADCASTS_UNAVAILABLE", errors.New("customer signer escrow registration is unavailable"), true, "")
		return
	}
	var signed broadcastreceipt.SignedReceipt
	if err := decodeJSON(w, r, &signed); err != nil {
		return
	}
	call, err := s.signerEscrowBroadcasts.Register(r.Context(), signed)
	if err != nil {
		switch {
		case errors.Is(err, ErrBroadcastKeyUnknown), errors.Is(err, ErrBroadcastSignature):
			writeError(w, http.StatusUnauthorized, "INVALID_SIGNER_RECEIPT", errors.New("signer receipt authentication failed"), false, "")
		case errors.Is(err, ErrBroadcastBinding), errors.Is(err, ErrBroadcastTime), errors.Is(err, ErrBroadcastRail), errors.Is(err, reconciliation.ErrConflict), errors.Is(err, reconciliation.ErrEscrowDeployment), errors.Is(err, reconciliation.ErrEscrowTransition):
			writeError(w, http.StatusConflict, "BROADCAST_BINDING_REJECTED", err, false, "")
		default:
			writeError(w, http.StatusServiceUnavailable, "BROADCAST_REGISTRATION_UNRESOLVED", err, true, "")
		}
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"call": call})
}

func (s *Server) handleEscrowIntent(w http.ResponseWriter, r *http.Request) {
	principal := principalFrom(r.Context())
	if !s.authorize(w, principal, PermissionIssue, "escrow:register") {
		return
	}
	idempotencyKey, ok := requireIdempotencyKey(w, r)
	if !ok {
		return
	}
	authorizationID := r.PathValue("authorizationID")
	if principal.Kind == PrincipalAgent {
		record, exists := s.lifecycle.GetByAuthorization(authorizationID)
		if !exists || record.Intent.OrganizationID != principal.OrganizationID || record.Intent.AgentID != principal.AgentID {
			writeError(w, http.StatusNotFound, "NOT_FOUND", ErrNotFound, false, "")
			return
		}
	}
	if idempotencyKey != authorizationID {
		writeError(w, http.StatusConflict, "IDEMPOTENCY_MISMATCH", errors.New("Idempotency-Key must equal authorizationId"), false, "")
		return
	}
	command, created, ok := s.beginCommand(w, r, principal, "escrow.intent.register", authorizationID, idempotencyKey, digestJSON(map[string]string{"authorizationId": authorizationID}))
	if !ok || !created {
		if ok {
			writeStoredCommand(w, command)
		}
		return
	}
	call, err := s.escrow.RegisterIntent(r.Context(), principal.OrganizationID, authorizationID)
	if err != nil {
		s.failCommand(w, r, command, err)
		return
	}
	s.succeedCommand(w, r, command, call, http.StatusCreated)
}

func (s *Server) handleEscrowTransition(w http.ResponseWriter, r *http.Request) {
	principal := principalFrom(r.Context())
	if !s.authorize(w, principal, PermissionRegisterEscrowTransition, "escrow:transitions") || !s.requireStepUp(w, principal) {
		return
	}
	idempotencyKey, ok := requireIdempotencyKey(w, r)
	if !ok {
		return
	}
	var candidate reconciliation.EscrowTransitionCandidate
	if err := decodeJSON(w, r, &candidate); err != nil {
		return
	}
	if candidate.Action == reconciliation.EscrowFund {
		writeError(w, http.StatusConflict, "ATTESTED_FUND_REQUIRED", errors.New("escrow FUND must arrive through the customer signer receipt endpoint"), false, "")
		return
	}
	if idempotencyKey != candidate.TransactionHash {
		writeError(w, http.StatusConflict, "IDEMPOTENCY_MISMATCH", errors.New("Idempotency-Key must equal transactionHash"), false, "")
		return
	}
	callID := r.PathValue("callID")
	command, created, ok := s.beginCommand(w, r, principal, "escrow.transition.register", callID, idempotencyKey, digestJSON(candidate))
	if !ok || !created {
		if ok {
			writeStoredCommand(w, command)
		}
		return
	}
	call, err := s.escrow.RegisterTransition(r.Context(), principal.OrganizationID, callID, candidate)
	if err != nil {
		s.failCommand(w, r, command, err)
		return
	}
	s.succeedCommand(w, r, command, call, http.StatusCreated)
}

func (s *Server) handleEscrowCall(w http.ResponseWriter, r *http.Request) {
	principal := principalFrom(r.Context())
	if !s.authorize(w, principal, PermissionRead, "records:read") {
		return
	}
	call, ok := s.escrow.Call(principal.OrganizationID, r.PathValue("callID"))
	if !ok || principal.Kind == PrincipalAgent && call.Intent.AgentID != principal.AgentID {
		writeError(w, http.StatusNotFound, "NOT_FOUND", ErrNotFound, false, "")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"call": call})
}

type operatorHaltRequest struct {
	Operator string `json:"operator"`
	Reason   string `json:"reason"`
}

type operatorResumeRequest struct {
	Operator string `json:"operator"`
}

type operatorQuarantineRequest struct {
	OrganizationID string `json:"organizationId"`
	Operator       string `json:"operator"`
	Disposition    string `json:"disposition"`
	Reason         string `json:"reason"`
}

func (s *Server) handleOperatorHalt(w http.ResponseWriter, r *http.Request) {
	var request operatorHaltRequest
	if err := decodeJSON(w, r, &request); err != nil {
		return
	}
	status, err := s.chain.ForceHalt(r.Context(), strings.TrimSpace(request.Operator), strings.TrimSpace(request.Reason))
	if err != nil {
		if errors.Is(err, reconciliation.ErrInvalidOperator) || errors.Is(err, reconciliation.ErrInvalidHaltReason) {
			writeError(w, http.StatusBadRequest, "INVALID_HALT", err, false, "")
		} else {
			writeError(w, http.StatusServiceUnavailable, "CONTROL_EVENT_NOT_COMMITTED", err, true, "")
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"chain": status})
}

func (s *Server) handleOperatorResume(w http.ResponseWriter, r *http.Request) {
	var request operatorResumeRequest
	if err := decodeJSON(w, r, &request); err != nil {
		return
	}
	status, err := s.chain.Resume(r.Context(), strings.TrimSpace(request.Operator))
	if errors.Is(err, reconciliation.ErrResumeBlocked) {
		writeError(w, http.StatusConflict, "RESUME_BLOCKED", err, false, "")
		return
	}
	if err != nil {
		if errors.Is(err, reconciliation.ErrInvalidOperator) {
			writeError(w, http.StatusBadRequest, "INVALID_RESUME", err, false, "")
		} else {
			writeError(w, http.StatusServiceUnavailable, "CONTROL_EVENT_NOT_COMMITTED", err, true, "")
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"chain": status})
}

func (s *Server) handleOperatorReconciliation(w http.ResponseWriter, r *http.Request) {
	organizationID := strings.TrimSpace(r.URL.Query().Get("organizationId"))
	if !identifierPattern.MatchString(organizationID) || s.reconciliation == nil {
		writeError(w, http.StatusBadRequest, "INVALID_RECONCILIATION_QUERY", errors.New("a valid organizationId and reconciliation reader are required"), false, "")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"reconciliation": s.reconciliation.OrganizationView(organizationID)})
}

func (s *Server) handleOperatorQuarantine(w http.ResponseWriter, r *http.Request) {
	controller, ok := s.reconciliation.(ReconciliationOperator)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "RECONCILIATION_CONTROL_UNAVAILABLE", errors.New("reconciliation operator control is unavailable"), true, "")
		return
	}
	executionID := r.PathValue("executionID")
	var request operatorQuarantineRequest
	if err := decodeJSON(w, r, &request); err != nil {
		return
	}
	request.OrganizationID = strings.TrimSpace(request.OrganizationID)
	request.Operator = strings.TrimSpace(request.Operator)
	request.Disposition = strings.TrimSpace(request.Disposition)
	request.Reason = strings.TrimSpace(request.Reason)
	if !identifierPattern.MatchString(executionID) || !identifierPattern.MatchString(request.OrganizationID) || !identifierPattern.MatchString(request.Operator) || len(request.Reason) == 0 || len(request.Reason) > 1024 {
		writeError(w, http.StatusBadRequest, "INVALID_QUARANTINE", errors.New("quarantine identifiers or reason are invalid"), false, "")
		return
	}
	switch request.Disposition {
	case "DROPPED_UNPROVEN", "REPLACED_UNPROVEN", "MANUAL_INVESTIGATION":
	default:
		writeError(w, http.StatusBadRequest, "INVALID_QUARANTINE", errors.New("disposition must preserve an unproven external outcome"), false, "")
		return
	}
	view := controller.OrganizationView(request.OrganizationID)
	found := false
	for _, exception := range view.Exceptions {
		if exception.Kind == "DIRECT_EXECUTION" && exception.ID == executionID {
			found = true
			break
		}
	}
	if !found {
		writeError(w, http.StatusNotFound, "NOT_FOUND", ErrNotFound, false, "")
		return
	}
	execution, err := controller.QuarantineForOrganization(r.Context(), request.OrganizationID, executionID, request.Operator, request.Disposition+": "+request.Reason)
	if err != nil {
		if errors.Is(err, reconciliation.ErrUnknownExecution) {
			writeError(w, http.StatusNotFound, "NOT_FOUND", ErrNotFound, false, "")
		} else if errors.Is(err, reconciliation.ErrConflict) || strings.Contains(err.Error(), "only unresolved") {
			writeError(w, http.StatusConflict, "STATE_CONFLICT", err, false, "")
		} else {
			writeError(w, http.StatusServiceUnavailable, "CONTROL_EVENT_NOT_COMMITTED", err, true, "")
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"execution":      execution,
		"reconciliation": controller.OrganizationView(request.OrganizationID),
	})
}

func (s *Server) handleCreateASCPIntent(w http.ResponseWriter, r *http.Request) {
	correlationID := s.correlationID(w)
	principal := principalFrom(r.Context())
	if principal.Kind != PrincipalAgent {
		writeCorrelatedError(w, http.StatusForbidden, "AGENT_CREDENTIAL_REQUIRED", ErrForbidden, false, correlationID)
		return
	}
	if !s.authorize(w, principal, PermissionCreateIntent, "intents:create") {
		return
	}
	if s.ascpAgent == nil {
		writeCorrelatedError(w, http.StatusServiceUnavailable, "ASCP_INTAKE_UNAVAILABLE", errors.New("durable ASCP intake is not configured"), true, correlationID)
		return
	}
	idempotencyKey, ok := requireIdempotencyKey(w, r)
	if !ok {
		return
	}
	var request ascpagent.CreateRequest
	if err := decodeJSON(w, r, &request); err != nil {
		return
	}
	operation, err := s.ascpAgent.Create(r.Context(), ascpagent.Identity{OrganizationID: principal.OrganizationID, AgentID: principal.AgentID}, idempotencyKey, request)
	if err != nil {
		s.writeASCPError(w, err, correlationID)
		return
	}
	status := http.StatusCreated
	if operation.Replayed {
		status = http.StatusOK
	}
	writeJSON(w, status, map[string]any{"correlationId": correlationID, "operation": operation})
}

func (s *Server) handleASCPIntent(w http.ResponseWriter, r *http.Request) {
	correlationID := s.correlationID(w)
	principal := principalFrom(r.Context())
	if principal.Kind != PrincipalAgent {
		writeCorrelatedError(w, http.StatusForbidden, "AGENT_CREDENTIAL_REQUIRED", ErrForbidden, false, correlationID)
		return
	}
	if !s.authorize(w, principal, PermissionRead, "intents:read") {
		return
	}
	if s.ascpAgent == nil {
		writeCorrelatedError(w, http.StatusServiceUnavailable, "ASCP_INTAKE_UNAVAILABLE", errors.New("durable ASCP intake is not configured"), true, correlationID)
		return
	}
	operation, err := s.ascpAgent.Get(r.Context(), ascpagent.Identity{OrganizationID: principal.OrganizationID, AgentID: principal.AgentID}, r.PathValue("operationID"))
	if err != nil {
		if errors.Is(err, ascpintake.ErrNotFound) || errors.Is(err, ascpagent.ErrInvalidIdentity) {
			writeCorrelatedError(w, http.StatusNotFound, "NOT_FOUND", ErrNotFound, false, correlationID)
			return
		}
		s.writeASCPError(w, err, correlationID)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"correlationId": correlationID, "operation": operation})
}

func (s *Server) handleEvaluateASCPIntent(w http.ResponseWriter, r *http.Request) {
	correlationID := s.correlationID(w)
	_, identity, ok := s.ascpAgentIdentity(w, r, correlationID, PermissionCreateIntent, "intents:create")
	if !ok {
		return
	}
	if s.ascpFlow == nil {
		writeCorrelatedError(w, http.StatusServiceUnavailable, "ASCP_ORCHESTRATION_UNAVAILABLE", errors.New("durable ASCP orchestration is not configured"), true, correlationID)
		return
	}
	decision, err := s.ascpFlow.Evaluate(r.Context(), identity, r.PathValue("operationID"))
	if err != nil {
		s.writeASCPFlowError(w, err, correlationID)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"correlationId": correlationID, "decision": decision})
}

func (s *Server) handleASCPDecision(w http.ResponseWriter, r *http.Request) {
	correlationID := s.correlationID(w)
	_, identity, ok := s.ascpAgentIdentity(w, r, correlationID, PermissionRead, "intents:read")
	if !ok {
		return
	}
	if s.ascpFlow == nil {
		writeCorrelatedError(w, http.StatusServiceUnavailable, "ASCP_ORCHESTRATION_UNAVAILABLE", errors.New("durable ASCP orchestration is not configured"), true, correlationID)
		return
	}
	decision, err := s.ascpFlow.Decision(r.Context(), identity, r.PathValue("operationID"))
	if err != nil {
		s.writeASCPFlowError(w, err, correlationID)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"correlationId": correlationID, "decision": decision})
}

func (s *Server) handleAuthorizeASCPIntent(w http.ResponseWriter, r *http.Request) {
	correlationID := s.correlationID(w)
	_, identity, ok := s.ascpAgentIdentity(w, r, correlationID, PermissionIssue, "authorizations:issue")
	if !ok {
		return
	}
	if s.ascpFlow == nil {
		writeCorrelatedError(w, http.StatusServiceUnavailable, "ASCP_ORCHESTRATION_UNAVAILABLE", errors.New("durable ASCP orchestration is not configured"), true, correlationID)
		return
	}
	authorization, err := s.ascpFlow.Authorize(r.Context(), identity, r.PathValue("operationID"))
	if err != nil && authorization.AuthorizationID == "" {
		s.writeASCPFlowError(w, err, correlationID)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"correlationId": correlationID, "authorization": authorization})
}

func (s *Server) handleASCPAuthorization(w http.ResponseWriter, r *http.Request) {
	correlationID := s.correlationID(w)
	_, identity, ok := s.ascpAgentIdentity(w, r, correlationID, PermissionRead, "intents:read")
	if !ok {
		return
	}
	if s.ascpFlow == nil {
		writeCorrelatedError(w, http.StatusServiceUnavailable, "ASCP_ORCHESTRATION_UNAVAILABLE", errors.New("durable ASCP orchestration is not configured"), true, correlationID)
		return
	}
	authorization, err := s.ascpFlow.Authorization(r.Context(), identity, r.PathValue("operationID"))
	if err != nil {
		s.writeASCPFlowError(w, err, correlationID)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"correlationId": correlationID, "authorization": authorization})
}

func (s *Server) handleCreateASCPActivation(w http.ResponseWriter, r *http.Request) {
	correlationID := s.correlationID(w)
	_, identity, ok := s.ascpAgentIdentity(w, r, correlationID, PermissionIssue, "activations:create")
	if !ok {
		return
	}
	if s.ascpActivation == nil {
		writeCorrelatedError(w, http.StatusServiceUnavailable, "ASCP_ACTIVATION_UNAVAILABLE", ascpactivation.ErrUnavailable, true, correlationID)
		return
	}
	var request ascpactivation.Request
	if err := decodeJSONLimit(w, r, &request, maxActivationRequestBytes); err != nil {
		return
	}
	status, err := s.ascpActivation.Create(r.Context(), identity, r.PathValue("operationID"), request)
	if err != nil {
		s.writeASCPActivationError(w, err, correlationID)
		return
	}
	httpStatus := http.StatusCreated
	if status.Replayed {
		httpStatus = http.StatusOK
	}
	writeJSON(w, httpStatus, map[string]any{"correlationId": correlationID, "activation": status})
}

func (s *Server) handleASCPActivation(w http.ResponseWriter, r *http.Request) {
	correlationID := s.correlationID(w)
	_, identity, ok := s.ascpAgentIdentity(w, r, correlationID, PermissionRead, "intents:read")
	if !ok {
		return
	}
	if s.ascpActivation == nil {
		writeCorrelatedError(w, http.StatusServiceUnavailable, "ASCP_ACTIVATION_UNAVAILABLE", ascpactivation.ErrUnavailable, true, correlationID)
		return
	}
	status, err := s.ascpActivation.Get(r.Context(), identity, r.PathValue("operationID"))
	if err != nil {
		s.writeASCPActivationError(w, err, correlationID)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"correlationId": correlationID, "activation": status})
}

func (s *Server) ascpAgentIdentity(w http.ResponseWriter, r *http.Request, correlationID string, permission Permission, scope string) (Principal, ascporchestration.Identity, bool) {
	principal := principalFrom(r.Context())
	if principal.Kind != PrincipalAgent {
		writeCorrelatedError(w, http.StatusForbidden, "AGENT_CREDENTIAL_REQUIRED", ErrForbidden, false, correlationID)
		return Principal{}, ascporchestration.Identity{}, false
	}
	if !s.authorize(w, principal, permission, scope) {
		return Principal{}, ascporchestration.Identity{}, false
	}
	return principal, ascporchestration.Identity{OrganizationID: principal.OrganizationID, AgentID: principal.AgentID}, true
}

func (s *Server) handleASCPApproval(w http.ResponseWriter, r *http.Request) {
	principal := principalFrom(r.Context())
	if principal.Kind != PrincipalHuman {
		writeError(w, http.StatusForbidden, "FORBIDDEN", ErrForbidden, false, "")
		return
	}
	if !s.authorize(w, principal, PermissionRead, "records:read") {
		return
	}
	if s.ascpFlow == nil {
		writeError(w, http.StatusServiceUnavailable, "ASCP_ORCHESTRATION_UNAVAILABLE", errors.New("durable ASCP orchestration is not configured"), true, "")
		return
	}
	approval, err := s.ascpFlow.Approval(r.Context(), principal.OrganizationID, r.PathValue("approvalID"))
	if err != nil {
		status, code, retriable := classifyError(err)
		writeError(w, status, code, err, retriable, "")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"approval": approval})
}

type ascpApprovalDecisionRequest struct {
	ReviewSnapshotHash string `json:"reviewSnapshotHash"`
	Action             string `json:"action"`
}

func (s *Server) handleASCPApprovalDecision(w http.ResponseWriter, r *http.Request) {
	principal := principalFrom(r.Context())
	if principal.Kind != PrincipalHuman {
		writeError(w, http.StatusForbidden, "FORBIDDEN", ErrForbidden, false, "")
		return
	}
	if !s.authorize(w, principal, PermissionDecide, "approvals:decide") || !s.requireStepUp(w, principal) {
		return
	}
	if s.ascpFlow == nil {
		writeError(w, http.StatusServiceUnavailable, "ASCP_ORCHESTRATION_UNAVAILABLE", errors.New("durable ASCP orchestration is not configured"), true, "")
		return
	}
	idempotencyKey, ok := requireIdempotencyKey(w, r)
	if !ok {
		return
	}
	var request ascpApprovalDecisionRequest
	if err := decodeJSON(w, r, &request); err != nil {
		return
	}
	if request.Action != "APPROVE" && request.Action != "REJECT" {
		writeError(w, http.StatusBadRequest, "INVALID_DECISION", errors.New("action must be APPROVE or REJECT"), false, "")
		return
	}
	approvalID := r.PathValue("approvalID")
	digest := digestJSON(struct {
		ApprovalID string                      `json:"approvalId"`
		Decision   ascpApprovalDecisionRequest `json:"decision"`
	}{approvalID, request})
	command, created, ok := s.beginCommand(w, r, principal, "ascp.approval.decide", approvalID, idempotencyKey, digest)
	if !ok || !created {
		if ok {
			writeStoredCommand(w, command)
		}
		return
	}
	approval, err := s.ascpFlow.DecideApproval(r.Context(), principal.OrganizationID, approvalID, request.ReviewSnapshotHash, request.Action == "APPROVE", principal.ID)
	if err != nil {
		s.failCommand(w, r, command, err)
		return
	}
	s.succeedCommand(w, r, command, approval, http.StatusOK)
}

func (s *Server) correlationID(w http.ResponseWriter) string {
	if existing := w.Header().Get("X-Correlation-ID"); existing != "" {
		return existing
	}
	correlationID, err := s.idSource("corr")
	if err != nil {
		correlationID = "corr_unavailable"
	}
	w.Header().Set("X-Correlation-ID", correlationID)
	return correlationID
}

func (s *Server) writeASCPError(w http.ResponseWriter, err error, correlationID string) {
	switch {
	case errors.Is(err, ascpintake.ErrIdempotencyConflict):
		writeCorrelatedError(w, http.StatusConflict, "IDEMPOTENCY_CONFLICT", err, false, correlationID)
	case errors.Is(err, ascpintake.ErrQuoteNonceConsumed):
		writeCorrelatedError(w, http.StatusConflict, "QUOTE_NONCE_CONSUMED", err, false, correlationID)
	case errors.Is(err, directoryreader.ErrCurrentVersionMismatch):
		writeCorrelatedError(w, http.StatusConflict, "DIRECTORY_VERSION_STALE", err, false, correlationID)
	case errors.Is(err, directoryreader.ErrCurrentSnapshotUnavailable), errors.Is(err, directoryreader.ErrCurrentSnapshotStale):
		writeCorrelatedError(w, http.StatusServiceUnavailable, "DIRECTORY_EVIDENCE_UNAVAILABLE", err, true, correlationID)
	case errors.Is(err, directoryreader.ErrQuoteEvidenceUnavailable),
		errors.Is(err, ascpagent.ErrInvalidRequest), errors.Is(err, ascpagent.ErrUnsupportedTerms),
		errors.Is(err, sellerquote.ErrInvalidQuote), errors.Is(err, sellerquote.ErrInvalidSignature),
		errors.Is(err, sellerquote.ErrQuoteExpired), errors.Is(err, sellerquote.ErrDirectoryEvidence),
		errors.Is(err, ascpintake.ErrPurchaseSpecBinding):
		writeCorrelatedError(w, http.StatusBadRequest, "INVALID_ASCP_INTENT", err, false, correlationID)
	default:
		writeCorrelatedError(w, http.StatusServiceUnavailable, "ASCP_INTAKE_FAILED", err, true, correlationID)
	}
}

func (s *Server) writeASCPFlowError(w http.ResponseWriter, err error, correlationID string) {
	switch {
	case errors.Is(err, ascporchestration.ErrNotFound), errors.Is(err, ascporchestration.ErrInvalidScope):
		writeCorrelatedError(w, http.StatusNotFound, "NOT_FOUND", ErrNotFound, false, correlationID)
	case errors.Is(err, ascporchestration.ErrPolicyUnavailable):
		writeCorrelatedError(w, http.StatusConflict, "POLICY_UNAVAILABLE", err, false, correlationID)
	case errors.Is(err, ascporchestration.ErrDecisionDenied):
		writeCorrelatedError(w, http.StatusConflict, "POLICY_DENIED", err, false, correlationID)
	case errors.Is(err, ascporchestration.ErrApprovalPending):
		writeCorrelatedError(w, http.StatusConflict, "APPROVAL_PENDING", err, true, correlationID)
	case errors.Is(err, ascporchestration.ErrApprovalUnavailable), errors.Is(err, ascporchestration.ErrStateConflict),
		errors.Is(err, ascpapproval.ErrSnapshotMismatch), errors.Is(err, ascpapproval.ErrNotRequested):
		writeCorrelatedError(w, http.StatusConflict, "ORCHESTRATION_STATE_CONFLICT", err, false, correlationID)
	case errors.Is(err, ascporchestration.ErrOperationExpired):
		writeCorrelatedError(w, http.StatusGone, "ASCP_OPERATION_EXPIRED", err, false, correlationID)
	default:
		writeCorrelatedError(w, http.StatusServiceUnavailable, "ASCP_ORCHESTRATION_FAILED", err, true, correlationID)
	}
}

func (s *Server) writeASCPActivationError(w http.ResponseWriter, err error, correlationID string) {
	switch {
	case errors.Is(err, ascporchestration.ErrNotFound), errors.Is(err, ascporchestration.ErrInvalidScope),
		errors.Is(err, ascpbearer.ErrActivationNotFound):
		writeCorrelatedError(w, http.StatusNotFound, "NOT_FOUND", ErrNotFound, false, correlationID)
	case errors.Is(err, ascpbearer.ErrActivationInput):
		writeCorrelatedError(w, http.StatusBadRequest, "INVALID_ASCP_ACTIVATION", err, false, correlationID)
	case errors.Is(err, ascpsignerbinding.ErrNotFound):
		writeCorrelatedError(w, http.StatusConflict, "SIGNER_BINDING_REQUIRED", err, false, correlationID)
	case errors.Is(err, ascpactivation.ErrStateConflict), errors.Is(err, ascpbearer.ErrActivationBinding),
		errors.Is(err, ascpbearer.ErrActivationState):
		writeCorrelatedError(w, http.StatusConflict, "ASCP_ACTIVATION_STATE_CONFLICT", err, false, correlationID)
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		writeCorrelatedError(w, http.StatusServiceUnavailable, "ASCP_ACTIVATION_UNRESOLVED", err, true, correlationID)
	default:
		writeCorrelatedError(w, http.StatusServiceUnavailable, "ASCP_ACTIVATION_FAILED", err, true, correlationID)
	}
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

func (s *Server) handleIntent(w http.ResponseWriter, r *http.Request) {
	principal := principalFrom(r.Context())
	if !s.authorize(w, principal, PermissionRead, "intents:read") || !s.sweepExpired(w, r) {
		return
	}
	record, exists := s.lifecycle.Get(r.PathValue("requestID"))
	if !exists || record.Intent.OrganizationID != principal.OrganizationID ||
		(principal.Kind == PrincipalAgent && record.Intent.AgentID != principal.AgentID) {
		writeError(w, http.StatusNotFound, "NOT_FOUND", ErrNotFound, false, "")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"intent": record})
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

func (s *Server) handleApproval(w http.ResponseWriter, r *http.Request) {
	principal := principalFrom(r.Context())
	if principal.Kind != PrincipalHuman {
		writeError(w, http.StatusForbidden, "FORBIDDEN", ErrForbidden, false, "")
		return
	}
	if !s.authorize(w, principal, PermissionRead, "records:read") || !s.sweepExpired(w, r) {
		return
	}
	record, exists := s.lifecycle.Get(r.PathValue("requestID"))
	if !exists || record.Intent.OrganizationID != principal.OrganizationID {
		writeError(w, http.StatusNotFound, "NOT_FOUND", ErrNotFound, false, "")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"approval": record})
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

func (s *Server) handlePutASCPSignerBinding(w http.ResponseWriter, r *http.Request) {
	principal := principalFrom(r.Context())
	if !s.authorize(w, principal, PermissionManageSignerBinding, "signer-bindings:write") || !s.requireStepUp(w, principal) {
		return
	}
	if s.ascpSignerBindings == nil {
		writeError(w, http.StatusServiceUnavailable, "ASCP_SIGNER_BINDING_UNAVAILABLE", ascpsignerbinding.ErrUnavailable, true, "")
		return
	}
	idempotencyKey, ok := requireIdempotencyKey(w, r)
	if !ok {
		return
	}
	var request ascpsignerbinding.PutRequest
	if err := decodeJSON(w, r, &request); err != nil {
		return
	}
	result, err := s.ascpSignerBindings.Put(r.Context(), principal.OrganizationID, r.PathValue("agentID"), principal.ID, idempotencyKey, request)
	if err != nil {
		s.writeASCPSignerBindingError(w, err)
		return
	}
	status := http.StatusOK
	if !result.Replayed && request.ExpectedVersion == 0 && result.Binding.Version == 1 {
		status = http.StatusCreated
	}
	writeJSON(w, status, result)
}

func (s *Server) handleGetASCPSignerBinding(w http.ResponseWriter, r *http.Request) {
	principal := principalFrom(r.Context())
	if !s.authorize(w, principal, PermissionManageSignerBinding, "signer-bindings:read") {
		return
	}
	if s.ascpSignerBindings == nil {
		writeError(w, http.StatusServiceUnavailable, "ASCP_SIGNER_BINDING_UNAVAILABLE", ascpsignerbinding.ErrUnavailable, true, "")
		return
	}
	binding, err := s.ascpSignerBindings.Current(r.Context(), principal.OrganizationID, r.PathValue("agentID"))
	if err != nil {
		s.writeASCPSignerBindingError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"binding": binding})
}

func (s *Server) writeASCPSignerBindingError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ascpsignerbinding.ErrInvalid):
		writeError(w, http.StatusBadRequest, "INVALID_SIGNER_BINDING", err, false, "")
	case errors.Is(err, ascpsignerbinding.ErrNotFound):
		writeError(w, http.StatusNotFound, "NOT_FOUND", ErrNotFound, false, "")
	case errors.Is(err, ascpsignerbinding.ErrIdempotencyConflict):
		writeError(w, http.StatusConflict, "IDEMPOTENCY_CONFLICT", err, false, "")
	case errors.Is(err, ascpsignerbinding.ErrVersionConflict):
		writeError(w, http.StatusConflict, "SIGNER_BINDING_VERSION_CONFLICT", err, false, "")
	case errors.Is(err, ascpsignerbinding.ErrInUse):
		writeError(w, http.StatusConflict, "SIGNER_BINDING_IN_USE", err, false, "")
	case errors.Is(err, ascpsignerbinding.ErrKeyEpochReuse):
		writeError(w, http.StatusConflict, "SIGNER_KEY_EPOCH_REUSED", err, false, "")
	case errors.Is(err, ascpsignerbinding.ErrAgentUnavailable):
		writeError(w, http.StatusConflict, "AGENT_FROZEN", err, false, "")
	default:
		writeError(w, http.StatusServiceUnavailable, "ASCP_SIGNER_BINDING_UNAVAILABLE", err, true, "")
	}
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

func (s *Server) handlePauseOrganization(w http.ResponseWriter, r *http.Request) {
	principal := principalFrom(r.Context())
	if !s.authorize(w, principal, PermissionPause, "organization:pause") || !s.requireStepUp(w, principal) {
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
	digest := digestJSON(struct {
		OrganizationID string `json:"organizationId"`
		Reason         string `json:"reason"`
	}{principal.OrganizationID, request.Reason})
	command, created, ok := s.beginCommand(w, r, principal, "organization.pause", principal.OrganizationID, idempotencyKey, digest)
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
	organization, err := s.store.PauseOrganization(r.Context(), principal.OrganizationID, principal.ID, auditID)
	if err != nil {
		s.failCommand(w, r, command, err)
		return
	}
	s.succeedCommand(w, r, command, map[string]any{"organization": organization, "auditId": auditID}, http.StatusOK)
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
	Live             bool                            `json:"live"`
	GeneratedAt      time.Time                       `json:"generatedAt"`
	OrganizationID   string                          `json:"organizationId"`
	Chain            reconciliation.ChainStatus      `json:"chain"`
	PendingApprovals []controlplane.Record           `json:"pendingApprovals"`
	Agents           []Agent                         `json:"agents"`
	Organization     Organization                    `json:"organization"`
	Reconciliation   reconciliation.OrganizationView `json:"reconciliation"`
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
	organization, err := s.store.Organization(r.Context(), principal.OrganizationID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "STORE_UNAVAILABLE", err, true, "")
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
	reconciliationView := reconciliation.OrganizationView{Available: false, Chain: s.chain.Status(), GeneratedAt: s.clock().UTC()}
	if s.reconciliation != nil {
		reconciliationView = s.reconciliation.OrganizationView(principal.OrganizationID)
	}
	writeJSON(w, http.StatusOK, DashboardSnapshot{
		Live: true, GeneratedAt: s.clock().UTC(), OrganizationID: principal.OrganizationID,
		Chain: s.chain.Status(), PendingApprovals: approvals, Agents: agents, Organization: organization,
		Reconciliation: reconciliationView,
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
		errors.Is(err, controlplane.ErrPolicyChanged), errors.Is(err, reconciliation.ErrConflict), errors.Is(err, reconciliation.ErrEscrowDeployment),
		errors.Is(err, reconciliation.ErrEscrowFinality), errors.Is(err, ErrEscrowBinding),
		errors.Is(err, ascpapproval.ErrSnapshotMismatch), errors.Is(err, ascpapproval.ErrNotRequested),
		errors.Is(err, ascporchestration.ErrStateConflict), errors.Is(err, ascporchestration.ErrApprovalUnavailable):
		return http.StatusConflict, "STATE_CONFLICT", false
	case errors.Is(err, reconciliation.ErrEscrowTransition):
		return http.StatusBadRequest, "INVALID_ESCROW_TRANSITION", false
	case errors.Is(err, controlplane.ErrApprovalExpired):
		return http.StatusGone, "APPROVAL_EXPIRED", false
	case errors.Is(err, controlplane.ErrPolicyUnavailable), errors.Is(err, ascporchestration.ErrPolicyUnavailable):
		return http.StatusConflict, "POLICY_UNAVAILABLE", false
	case errors.Is(err, controlplane.ErrFrozen):
		return http.StatusConflict, "AGENT_FROZEN", false
	case errors.Is(err, controlplane.ErrUnknownRequest), errors.Is(err, reconciliation.ErrUnknownExecution), errors.Is(err, ErrNotFound),
		errors.Is(err, ascporchestration.ErrNotFound), errors.Is(err, ascporchestration.ErrInvalidScope):
		return http.StatusNotFound, "NOT_FOUND", false
	case errors.Is(err, reconciliation.ErrChainUnavailable):
		return http.StatusServiceUnavailable, "CHAIN_UNAVAILABLE", true
	case errors.Is(err, reconciliation.ErrUnsafeFinality):
		return http.StatusServiceUnavailable, "CANONICAL_EVIDENCE_UNSAFE", true
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

func bearerToken(header string) (string, bool) {
	if !strings.HasPrefix(header, "Bearer ") || strings.Contains(header[7:], " ") {
		return "", false
	}
	token := header[7:]
	return token, len(token) >= 24 && len(token) <= 2048
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target any) error {
	return decodeJSONLimit(w, r, target, maxRequestBytes)
}

func decodeJSONLimit(w http.ResponseWriter, r *http.Request, target any, limit int64) error {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
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
	payload := map[string]any{
		"error":     map[string]any{"code": code, "message": message, "retriable": retriable},
		"commandId": commandID,
	}
	if correlationID := w.Header().Get("X-Correlation-ID"); correlationID != "" {
		payload["correlationId"] = correlationID
	}
	writeJSON(w, status, payload)
}

func writeCorrelatedError(w http.ResponseWriter, status int, code string, err error, retriable bool, correlationID string) {
	message := err.Error()
	if status >= 500 {
		message = "request could not be completed"
	}
	writeJSON(w, status, map[string]any{
		"correlationId": correlationID,
		"error":         map[string]any{"code": code, "message": message, "retriable": retriable},
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
