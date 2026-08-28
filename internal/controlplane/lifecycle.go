package controlplane

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gnanam1990/flowops/internal/policy"
	"github.com/gnanam1990/flowops/pkg/envelope"
	"github.com/gnanam1990/flowops/pkg/pilotlimits"
)

const (
	eventIntentSubmitted     = "intent.submitted"
	eventApprovalDecided     = "approval.decided"
	eventAuthorizationIssued = "authorization.issued"
	eventIntentExpired       = "intent.expired"
)

var (
	ErrUnknownRequest      = errors.New("unknown request")
	ErrIdempotencyConflict = errors.New("intent ID already names different content")
	ErrNotPendingApproval  = errors.New("request is not pending approval")
	ErrNotApproved         = errors.New("request is not approved")
	ErrApprovalDigest      = errors.New("approval request digest mismatch")
	ErrApprovalExpired     = errors.New("approval window expired")
	ErrPolicyChanged       = errors.New("active policy version changed")
	ErrPolicyUnavailable   = errors.New("active policy is unavailable")
	ErrFrozen              = errors.New("agent execution is frozen")
)

type PolicyProvider interface {
	Evaluate(ctx context.Context, intent PaymentIntent, spend policy.SpendSnapshot) (policy.Decision, error)
	ActiveVersion(ctx context.Context, organizationID, agentID string) (string, error)
}

type staticPolicyProvider struct {
	engine        *policy.Engine
	activeVersion func() string
}

func (p staticPolicyProvider) Evaluate(_ context.Context, intent PaymentIntent, spend policy.SpendSnapshot) (policy.Decision, error) {
	return p.engine.Evaluate(toPolicyIntent(intent), spend), nil
}

func (p staticPolicyProvider) ActiveVersion(_ context.Context, _, _ string) (string, error) {
	return p.activeVersion(), nil
}

type FreezeGate interface {
	Check(ctx context.Context, organizationID, taskID, agentID string) error
}

// authorizationFreezeGate serializes the final freeze check and durable
// authorization append against administrative agent-state transitions.
type authorizationFreezeGate interface {
	WithAuthorizationLock(ctx context.Context, organizationID, taskID, agentID string, issue func() error) error
}

type ChainGate interface {
	CheckChain(ctx context.Context, chainID uint64) error
}

type CanonicalExecutionState string

const (
	CanonicalExecutionSettled  CanonicalExecutionState = "SETTLED"
	CanonicalExecutionReverted CanonicalExecutionState = "REVERTED"
)

type FinalizedExecution struct {
	ExecutionID    string
	OrganizationID string
	AgentID        string
	TaskID         string
	ChainID        uint64
	Asset          string
	Recipient      string
	AmountAtomic   string
	State          CanonicalExecutionState
	FinalizedAt    int64
}

// ExecutionOutcomeSource is a read-only projection over the canonical
// reconciliation journal. An unavailable or invalid projection must be treated
// as unresolved reservation exposure.
type ExecutionOutcomeSource interface {
	FinalizedExecution(executionID string) (FinalizedExecution, bool)
}

type authorizationChainGate interface {
	CheckAuthorizationChain(ctx context.Context, authorization envelope.Authorization) error
}

type Config struct {
	PolicyProvider        PolicyProvider
	Policy                *policy.Engine
	ActivePolicyVersion   func() string
	Journal               EventJournal
	FreezeGate            FreezeGate
	ChainGate             ChainGate
	OutcomeSource         ExecutionOutcomeSource
	Clock                 func() time.Time
	ApprovalTTL           time.Duration
	AuthorizationTTL      time.Duration
	RequestIDSource       func() (string, error)
	AuthorizationIDSource func() (string, error)
	NonceSource           func() (string, error)
	EnvelopeKeyID         string
	EnvelopePrivateKey    ed25519.PrivateKey
	PilotLimits           *pilotlimits.Limits
}

type Lifecycle struct {
	mu                     sync.Mutex
	policyProvider         PolicyProvider
	journal                EventJournal
	freezeGate             FreezeGate
	chainGate              ChainGate
	outcomeSource          ExecutionOutcomeSource
	clock                  func() time.Time
	approvalTTL            time.Duration
	authorizationTTL       time.Duration
	requestIDSource        func() (string, error)
	authorizationIDSource  func() (string, error)
	nonceSource            func() (string, error)
	envelopeKeyID          string
	envelopePrivateKey     ed25519.PrivateKey
	pilotLimits            *pilotlimits.Limits
	records                map[string]Record
	requestByIntent        map[string]string
	requestByAuthorization map[string]string
	requestByNonce         map[string]string
}

func New(cfg Config) (*Lifecycle, error) {
	provider := cfg.PolicyProvider
	if provider == nil && cfg.Policy != nil && cfg.ActivePolicyVersion != nil {
		provider = staticPolicyProvider{engine: cfg.Policy, activeVersion: cfg.ActivePolicyVersion}
	}
	if provider == nil || cfg.Journal == nil || cfg.FreezeGate == nil || cfg.ChainGate == nil || cfg.PilotLimits == nil {
		return nil, errors.New("policy provider, journal, freeze gate, chain gate, and pilot limits are required")
	}
	if cfg.RequestIDSource == nil || cfg.AuthorizationIDSource == nil || cfg.NonceSource == nil {
		return nil, errors.New("request ID, authorization ID, and nonce sources are required")
	}
	if cfg.ApprovalTTL <= 0 || cfg.AuthorizationTTL <= 0 || cfg.AuthorizationTTL > cfg.ApprovalTTL {
		return nil, errors.New("authorization TTL must be positive and no longer than approval TTL")
	}
	if !idPattern.MatchString(cfg.EnvelopeKeyID) || len(cfg.EnvelopePrivateKey) != ed25519.PrivateKeySize {
		return nil, errors.New("envelope signing identity is invalid")
	}
	clock := cfg.Clock
	if clock == nil {
		clock = time.Now
	}
	l := &Lifecycle{
		policyProvider: provider, journal: cfg.Journal,
		freezeGate: cfg.FreezeGate, chainGate: cfg.ChainGate, outcomeSource: cfg.OutcomeSource, clock: clock, approvalTTL: cfg.ApprovalTTL,
		authorizationTTL: cfg.AuthorizationTTL, requestIDSource: cfg.RequestIDSource,
		authorizationIDSource: cfg.AuthorizationIDSource, nonceSource: cfg.NonceSource,
		envelopeKeyID: cfg.EnvelopeKeyID, envelopePrivateKey: append(ed25519.PrivateKey(nil), cfg.EnvelopePrivateKey...),
		pilotLimits: cfg.PilotLimits,
		records:     make(map[string]Record), requestByIntent: make(map[string]string),
		requestByAuthorization: make(map[string]string), requestByNonce: make(map[string]string),
	}
	for _, event := range cfg.Journal.Events() {
		if err := l.applyEvent(event); err != nil {
			return nil, fmt.Errorf("replay event %d (%s): %w", event.Sequence, event.Kind, err)
		}
	}
	return l, nil
}

func (l *Lifecycle) Submit(ctx context.Context, intent PaymentIntent) (Record, error) {
	if err := intent.Validate(); err != nil {
		return Record{}, err
	}
	intentDigest, err := intent.Digest()
	if err != nil {
		return Record{}, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	intentKey := scopedIntentKey(intent.OrganizationID, intent.IntentID)
	if requestID, exists := l.requestByIntent[intentKey]; exists {
		existing := l.records[requestID]
		if existing.IntentDigest != intentDigest {
			return Record{}, ErrIdempotencyConflict
		}
		return cloneRecord(existing), nil
	}
	if err := l.freezeGate.Check(ctx, intent.OrganizationID, intent.TaskID, intent.AgentID); err != nil {
		return Record{}, fmt.Errorf("frozen: %w", err)
	}
	now := l.clock().UTC()
	decision, err := l.policyProvider.Evaluate(ctx, intent, l.spendSnapshot(intent, now))
	if err != nil {
		return Record{}, fmt.Errorf("evaluate active policy: %w", err)
	}
	if !idPattern.MatchString(decision.PolicyVersion) {
		return Record{}, errors.New("policy returned an invalid version identifier")
	}
	if decision.Outcome != policy.Deny {
		if limitErr := l.pilotLimits.Check(intent.AmountAtomic, l.pilotOutstanding(intent)); limitErr != nil {
			switch {
			case errors.Is(limitErr, pilotlimits.ErrPerActionExceeded):
				decision = policy.Decision{Outcome: policy.Deny, Reason: policy.ReasonPilotPerActionLimit, PolicyVersion: decision.PolicyVersion}
			case errors.Is(limitErr, pilotlimits.ErrOutstandingExceeded):
				decision = policy.Decision{Outcome: policy.Deny, Reason: policy.ReasonPilotOutstandingLimit, PolicyVersion: decision.PolicyVersion}
			default:
				return Record{}, fmt.Errorf("evaluate pilot limits: %w", limitErr)
			}
		}
	}
	state, err := decisionState(decision)
	if err != nil {
		return Record{}, err
	}
	requestID, err := l.requestIDSource()
	if err != nil {
		return Record{}, fmt.Errorf("generate request ID: %w", err)
	}
	if !idPattern.MatchString(requestID) {
		return Record{}, errors.New("generated request ID is invalid")
	}
	if _, exists := l.records[requestID]; exists {
		return Record{}, errors.New("generated request ID already exists")
	}
	expiresAt := now.Add(l.approvalTTL).Unix()
	reqDigest, err := requestDigest(requestID, intentDigest, decision, expiresAt)
	if err != nil {
		return Record{}, err
	}
	record := Record{
		RequestID: requestID, Intent: intent, IntentDigest: intentDigest, RequestDigest: reqDigest,
		Decision: decision, State: state, SubmittedAt: now.Unix(), ApprovalExpiresAt: expiresAt,
	}
	event, err := l.journal.Append(ctx, now, eventIntentSubmitted, requestID, submittedPayload{Record: record})
	if err != nil {
		return Record{}, err
	}
	if err := l.applyEvent(event); err != nil {
		return Record{}, fmt.Errorf("apply durable submit event: %w", err)
	}
	return cloneRecord(l.records[requestID]), nil
}

func (l *Lifecycle) Decide(ctx context.Context, requestID, requestDigest string, action ApprovalAction, actor, note string) (Record, error) {
	if action != Approve && action != Reject {
		return Record{}, errors.New("approval action is invalid")
	}
	if !idPattern.MatchString(actor) || len(note) > 2048 {
		return Record{}, errors.New("approval actor or note is invalid")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	record, ok := l.records[requestID]
	if !ok {
		return Record{}, ErrUnknownRequest
	}
	if record.State != StatePendingApproval {
		return Record{}, ErrNotPendingApproval
	}
	if requestDigest != record.RequestDigest {
		return Record{}, ErrApprovalDigest
	}
	now := l.clock().UTC()
	if !now.Before(time.Unix(record.ApprovalExpiresAt, 0)) {
		if err := l.expireLocked(ctx, record, now, "approval window elapsed before decision"); err != nil {
			return Record{}, err
		}
		return Record{}, ErrApprovalExpired
	}
	approval := Approval{Action: action, Actor: actor, Note: note, RequestDigest: requestDigest, DecidedAt: now.Unix()}
	event, err := l.journal.Append(ctx, now, eventApprovalDecided, requestID, approvalPayload{Approval: approval})
	if err != nil {
		return Record{}, err
	}
	if err := l.applyEvent(event); err != nil {
		return Record{}, fmt.Errorf("apply durable approval event: %w", err)
	}
	return cloneRecord(l.records[requestID]), nil
}

func (l *Lifecycle) Issue(ctx context.Context, requestID string) (envelope.SignedAuthorization, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	record, ok := l.records[requestID]
	if !ok {
		return envelope.SignedAuthorization{}, ErrUnknownRequest
	}
	if record.State == StateIssued && record.Authorization != nil {
		if err := l.checkChain(ctx, record); err != nil {
			return envelope.SignedAuthorization{}, fmt.Errorf("chain unavailable: %w", err)
		}
		return envelope.Sign(*record.Authorization, l.envelopeKeyID, l.envelopePrivateKey)
	}
	if record.State != StateApproved {
		return envelope.SignedAuthorization{}, ErrNotApproved
	}
	now := l.clock().UTC()
	if !now.Before(time.Unix(record.ApprovalExpiresAt, 0)) {
		if err := l.expireLocked(ctx, record, now, "approval window elapsed before issuance"); err != nil {
			return envelope.SignedAuthorization{}, err
		}
		return envelope.SignedAuthorization{}, ErrApprovalExpired
	}
	var signed envelope.SignedAuthorization
	issue := func() error {
		var err error
		signed, err = l.issueLocked(ctx, record, now)
		return err
	}
	if gate, ok := l.freezeGate.(authorizationFreezeGate); ok {
		if err := gate.WithAuthorizationLock(ctx, record.Intent.OrganizationID, record.Intent.TaskID, record.Intent.AgentID, issue); err != nil {
			return envelope.SignedAuthorization{}, err
		}
		return signed, nil
	}
	if err := l.freezeGate.Check(ctx, record.Intent.OrganizationID, record.Intent.TaskID, record.Intent.AgentID); err != nil {
		return envelope.SignedAuthorization{}, fmt.Errorf("frozen: %w", err)
	}
	if err := issue(); err != nil {
		return envelope.SignedAuthorization{}, err
	}
	return signed, nil
}

func (l *Lifecycle) issueLocked(ctx context.Context, record Record, now time.Time) (envelope.SignedAuthorization, error) {
	active, err := l.policyProvider.ActiveVersion(ctx, record.Intent.OrganizationID, record.Intent.AgentID)
	if err != nil {
		return envelope.SignedAuthorization{}, fmt.Errorf("resolve active policy: %w", err)
	}
	if active != record.Decision.PolicyVersion {
		return envelope.SignedAuthorization{}, fmt.Errorf("%w: approved under %s, active is %s", ErrPolicyChanged, record.Decision.PolicyVersion, active)
	}
	if err := l.checkChain(ctx, record); err != nil {
		return envelope.SignedAuthorization{}, fmt.Errorf("chain unavailable: %w", err)
	}
	authorizationID, err := l.authorizationIDSource()
	if err != nil {
		return envelope.SignedAuthorization{}, fmt.Errorf("generate authorization ID: %w", err)
	}
	nonce, err := l.nonceSource()
	if err != nil {
		return envelope.SignedAuthorization{}, fmt.Errorf("generate nonce: %w", err)
	}
	expiresAt := now.Add(l.authorizationTTL)
	if approvalExpiry := time.Unix(record.ApprovalExpiresAt, 0); expiresAt.After(approvalExpiry) {
		expiresAt = approvalExpiry
	}
	if record.Intent.Escrow != nil {
		acknowledgeBy := time.Unix(int64(record.Intent.Escrow.AcknowledgeBy), 0).UTC()
		if !now.Before(acknowledgeBy) {
			return envelope.SignedAuthorization{}, errors.New("escrow acknowledgement deadline elapsed before authorization issuance")
		}
		if expiresAt.After(acknowledgeBy) {
			expiresAt = acknowledgeBy
		}
	}
	authorization := envelope.Authorization{
		Version: envelope.Version, AuthorizationID: authorizationID,
		OrganizationID: record.Intent.OrganizationID, CustomerID: record.Intent.CustomerID,
		AgentID: record.Intent.AgentID, TaskID: record.Intent.TaskID, ActionID: record.Intent.ActionID,
		Rail: record.Intent.Rail, ChainID: record.Intent.ChainID, Recipient: record.Intent.Recipient,
		Asset: record.Intent.Asset, AmountAtomic: record.Intent.AmountAtomic, Resource: record.Intent.Resource,
		PolicyVersion: record.Decision.PolicyVersion, Nonce: nonce, IssuedAt: now.Unix(), ExpiresAt: expiresAt.Unix(),
	}
	if record.Intent.Escrow != nil {
		escrow := *record.Intent.Escrow
		authorization.Escrow = &escrow
	}
	signed, err := envelope.Sign(authorization, l.envelopeKeyID, l.envelopePrivateKey)
	if err != nil {
		return envelope.SignedAuthorization{}, fmt.Errorf("sign authorization: %w", err)
	}
	if _, exists := l.requestByAuthorization[authorization.AuthorizationID]; exists {
		return envelope.SignedAuthorization{}, errors.New("generated authorization ID already exists")
	}
	if _, exists := l.requestByNonce[authorization.Nonce]; exists {
		return envelope.SignedAuthorization{}, errors.New("generated nonce already exists")
	}
	event, err := l.journal.Append(ctx, now, eventAuthorizationIssued, record.RequestID, issuedPayload{Authorization: authorization})
	if err != nil {
		return envelope.SignedAuthorization{}, err
	}
	if err := l.applyEvent(event); err != nil {
		return envelope.SignedAuthorization{}, fmt.Errorf("apply durable issuance event: %w", err)
	}
	return signed, nil
}

func (l *Lifecycle) checkChain(ctx context.Context, record Record) error {
	if record.Authorization != nil {
		if strictGate, ok := l.chainGate.(authorizationChainGate); ok {
			return strictGate.CheckAuthorizationChain(ctx, *record.Authorization)
		}
	}
	return l.chainGate.CheckChain(ctx, record.Intent.ChainID)
}

func (l *Lifecycle) SweepExpired(ctx context.Context) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.clock().UTC()
	requestIDs := make([]string, 0, len(l.records))
	for requestID, record := range l.records {
		if (record.State == StatePendingApproval || record.State == StateApproved) && !now.Before(time.Unix(record.ApprovalExpiresAt, 0)) {
			requestIDs = append(requestIDs, requestID)
		}
	}
	sort.Strings(requestIDs)
	completed := 0
	for _, requestID := range requestIDs {
		if err := l.expireLocked(ctx, l.records[requestID], now, "approval window elapsed during sweep"); err != nil {
			return completed, err
		}
		completed++
	}
	return completed, nil
}

func (l *Lifecycle) expireLocked(ctx context.Context, record Record, now time.Time, reason string) error {
	event, err := l.journal.Append(ctx, now, eventIntentExpired, record.RequestID, expiredPayload{Reason: reason})
	if err != nil {
		return err
	}
	if err := l.applyEvent(event); err != nil {
		return fmt.Errorf("apply durable expiry event: %w", err)
	}
	return nil
}

func (l *Lifecycle) Get(requestID string) (Record, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	record, ok := l.records[requestID]
	return cloneRecord(record), ok
}

// GetByAuthorization resolves an issued authorization without trusting any
// request metadata supplied by a customer signer callback.
func (l *Lifecycle) GetByAuthorization(authorizationID string) (Record, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	requestID, ok := l.requestByAuthorization[authorizationID]
	if !ok {
		return Record{}, false
	}
	record, ok := l.records[requestID]
	return cloneRecord(record), ok
}

func (l *Lifecycle) PendingApprovals() []Record {
	l.mu.Lock()
	defer l.mu.Unlock()
	result := make([]Record, 0)
	for _, record := range l.records {
		if record.State == StatePendingApproval {
			result = append(result, cloneRecord(record))
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].RequestID < result[j].RequestID })
	return result
}

func (l *Lifecycle) spendSnapshot(intent PaymentIntent, now time.Time) policy.SpendSnapshot {
	// Reservations deliberately have no rail dimension. An x402, direct-USDC,
	// or escrow intent for the same customer therefore consumes the same task
	// and daily budget, including after the hash-chained journal is replayed.
	// Issued authorizations remain reserved until a protocol-aware canonical
	// outcome can move them to settled or released accounting; treating them as
	// spent without that evidence would invent settlement.
	taskReserved := new(big.Int)
	dailyReserved := new(big.Int)
	taskSpent := new(big.Int)
	dailySpent := new(big.Int)
	for _, record := range l.records {
		if record.Intent.OrganizationID != intent.OrganizationID || record.Intent.CustomerID != intent.CustomerID {
			continue
		}
		amount, ok := new(big.Int).SetString(record.Intent.AmountAtomic, 10)
		if !ok {
			continue
		}
		finalized, hasFinalized := l.finalizedExecution(record)
		if reservationActive(record.State) && !hasFinalized {
			if record.Intent.TaskID == intent.TaskID {
				taskReserved.Add(taskReserved, amount)
			}
			// An unresolved authorization carries into the current UTC budget
			// window. Dropping yesterday's unknown payment at midnight would
			// create a temporary overspend window before chain finality.
			dailyReserved.Add(dailyReserved, amount)
		} else if hasFinalized && finalized.State == CanonicalExecutionSettled {
			if record.Intent.TaskID == intent.TaskID {
				taskSpent.Add(taskSpent, amount)
			}
			finalizedAt := time.Unix(finalized.FinalizedAt, 0).UTC()
			if finalizedAt.Year() == now.Year() && finalizedAt.YearDay() == now.YearDay() {
				dailySpent.Add(dailySpent, amount)
			}
		}
	}
	return policy.SpendSnapshot{
		TaskSpentAtomic: taskSpent.String(), TaskReservedAtomic: taskReserved.String(),
		DailySpentAtomic: dailySpent.String(), DailyReservedAtomic: dailyReserved.String(),
	}
}

func (l *Lifecycle) pilotOutstanding(intent PaymentIntent) string {
	total := new(big.Int)
	for _, record := range l.records {
		_, finalized := l.finalizedExecution(record)
		if !reservationActive(record.State) || finalized || record.Intent.OrganizationID != intent.OrganizationID || record.Intent.CustomerID != intent.CustomerID {
			continue
		}
		amount, ok := new(big.Int).SetString(record.Intent.AmountAtomic, 10)
		if ok {
			total.Add(total, amount)
		}
	}
	return total.String()
}

func (l *Lifecycle) finalizedExecution(record Record) (FinalizedExecution, bool) {
	if record.State != StateIssued || record.Authorization == nil || l.outcomeSource == nil ||
		(record.Authorization.Rail != envelope.RailDirect && record.Authorization.Rail != envelope.RailX402) {
		return FinalizedExecution{}, false
	}
	finalized, ok := l.outcomeSource.FinalizedExecution(ExecutionID(record.Authorization.AuthorizationID))
	if !ok || (finalized.State != CanonicalExecutionSettled && finalized.State != CanonicalExecutionReverted) ||
		finalized.ExecutionID != ExecutionID(record.Authorization.AuthorizationID) ||
		finalized.OrganizationID != record.Authorization.OrganizationID || finalized.AgentID != record.Authorization.AgentID ||
		finalized.TaskID != record.Authorization.TaskID || finalized.ChainID != record.Authorization.ChainID ||
		finalized.Asset != record.Authorization.Asset || finalized.Recipient != record.Authorization.Recipient ||
		finalized.AmountAtomic != record.Authorization.AmountAtomic ||
		finalized.FinalizedAt < record.Authorization.IssuedAt || finalized.FinalizedAt > l.clock().UTC().Unix() {
		return FinalizedExecution{}, false
	}
	return finalized, true
}

func reservationActive(state State) bool {
	return state == StatePendingApproval || state == StateApproved || state == StateIssued
}

func toPolicyIntent(intent PaymentIntent) policy.Intent {
	return policy.Intent{
		OrganizationID: intent.OrganizationID, CustomerID: intent.CustomerID, AgentID: intent.AgentID,
		TaskID: intent.TaskID, ActionID: intent.ActionID, Rail: intent.Rail, ChainID: intent.ChainID,
		Recipient: intent.Recipient, Asset: intent.Asset, AmountAtomic: intent.AmountAtomic,
		Resource: intent.Resource, Category: intent.Category,
	}
}

func (l *Lifecycle) applyEvent(event Event) error {
	switch event.Kind {
	case eventIntentSubmitted:
		var payload submittedPayload
		if err := decodePayload(event, &payload); err != nil {
			return err
		}
		record := payload.Record
		if event.RequestID != record.RequestID || event.At != record.SubmittedAt {
			return errors.New("submitted event identity or time mismatch")
		}
		if _, exists := l.records[record.RequestID]; exists {
			return errors.New("duplicate request ID")
		}
		intentKey := scopedIntentKey(record.Intent.OrganizationID, record.Intent.IntentID)
		if _, exists := l.requestByIntent[intentKey]; exists {
			return errors.New("duplicate intent ID")
		}
		intentDigest, err := record.Intent.Digest()
		if err != nil || intentDigest != record.IntentDigest {
			return errors.New("intent digest mismatch")
		}
		reqDigest, err := requestDigest(record.RequestID, record.IntentDigest, record.Decision, record.ApprovalExpiresAt)
		if err != nil || reqDigest != record.RequestDigest {
			return errors.New("request digest mismatch")
		}
		state, err := decisionState(record.Decision)
		if err != nil || state != record.State || record.Approval != nil || record.Authorization != nil {
			return errors.New("submitted state does not match decision")
		}
		l.records[record.RequestID] = cloneRecord(record)
		l.requestByIntent[intentKey] = record.RequestID
		return nil
	case eventApprovalDecided:
		var payload approvalPayload
		if err := decodePayload(event, &payload); err != nil {
			return err
		}
		record, ok := l.records[event.RequestID]
		if !ok || record.State != StatePendingApproval || payload.Approval.RequestDigest != record.RequestDigest || payload.Approval.DecidedAt != event.At ||
			!eventBefore(event.At, record.ApprovalExpiresAt) || !idPattern.MatchString(payload.Approval.Actor) || len(payload.Approval.Note) > 2048 {
			return errors.New("approval event is invalid for current request")
		}
		switch payload.Approval.Action {
		case Approve:
			record.State = StateApproved
		case Reject:
			record.State = StateRejected
		default:
			return errors.New("approval action is invalid")
		}
		record.Approval = &payload.Approval
		l.records[event.RequestID] = record
		return nil
	case eventAuthorizationIssued:
		var payload issuedPayload
		if err := decodePayload(event, &payload); err != nil {
			return err
		}
		record, ok := l.records[event.RequestID]
		if !ok || record.State != StateApproved || payload.Authorization.IssuedAt != event.At || !eventBefore(event.At, record.ApprovalExpiresAt) {
			return errors.New("issuance event is invalid for current request")
		}
		if err := authorizationMatches(record, payload.Authorization); err != nil {
			return err
		}
		if _, exists := l.requestByAuthorization[payload.Authorization.AuthorizationID]; exists {
			return errors.New("duplicate authorization ID")
		}
		if _, exists := l.requestByNonce[payload.Authorization.Nonce]; exists {
			return errors.New("duplicate authorization nonce")
		}
		record.State = StateIssued
		record.Authorization = &payload.Authorization
		l.records[event.RequestID] = record
		l.requestByAuthorization[payload.Authorization.AuthorizationID] = event.RequestID
		l.requestByNonce[payload.Authorization.Nonce] = event.RequestID
		return nil
	case eventIntentExpired:
		var payload expiredPayload
		if err := decodePayload(event, &payload); err != nil || strings.TrimSpace(payload.Reason) == "" {
			return errors.New("expiry event is invalid")
		}
		record, ok := l.records[event.RequestID]
		if !ok || (record.State != StatePendingApproval && record.State != StateApproved) || event.At < record.ApprovalExpiresAt {
			return errors.New("expiry event is invalid for current request")
		}
		record.State = StateExpired
		l.records[event.RequestID] = record
		return nil
	default:
		return fmt.Errorf("unknown event kind %q", event.Kind)
	}
}

func authorizationMatches(record Record, a envelope.Authorization) error {
	if err := a.Validate(); err != nil {
		return err
	}
	i := record.Intent
	if a.OrganizationID != i.OrganizationID || a.CustomerID != i.CustomerID || a.AgentID != i.AgentID ||
		a.TaskID != i.TaskID || a.ActionID != i.ActionID || a.Rail != i.Rail || a.ChainID != i.ChainID ||
		a.Recipient != i.Recipient || a.Asset != i.Asset || a.AmountAtomic != i.AmountAtomic || a.Resource != i.Resource ||
		!equalEscrowTerms(a.Escrow, i.Escrow) ||
		a.PolicyVersion != record.Decision.PolicyVersion || a.IssuedAt < record.SubmittedAt || a.ExpiresAt > record.ApprovalExpiresAt {
		return errors.New("authorization does not match approved intent")
	}
	return nil
}

func equalEscrowTerms(left, right *envelope.EscrowTerms) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func decodePayload(event Event, target any) error {
	if err := json.Unmarshal(event.Payload, target); err != nil {
		return fmt.Errorf("decode %s payload: %w", event.Kind, err)
	}
	return nil
}

func eventBefore(at, boundary int64) bool { return at < boundary }

func scopedIntentKey(organizationID, intentID string) string {
	return organizationID + "\x00" + intentID
}
