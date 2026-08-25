package reconciliation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"sync"
	"time"

	"github.com/gnanam1990/flowops/pkg/envelope"
)

const (
	eventChainStatus         = "chain_status"
	eventExecutionBroadcast  = "execution_broadcast"
	eventExecutionResolved   = "execution_resolved"
	eventExecutionReorged    = "execution_reorged"
	eventExecutionQuarantine = "execution_quarantine"
	eventLedgerPosted        = "ledger_posted"
	eventEscrowIntent        = "escrow_intent_registered"
	eventEscrowPending       = "escrow_transition_pending"
	eventEscrowResolved      = "escrow_transition_resolved"
	eventEscrowReorged       = "escrow_transition_reorged"
)

type chainPayload struct {
	Status                ChainStatus   `json:"status"`
	Observations          []Observation `json:"observations,omitempty"`
	Operator              string        `json:"operator,omitempty"`
	MarkBroadcastsPending bool          `json:"markBroadcastsPending,omitempty"`
}

type executionPayload struct {
	Execution Execution          `json:"execution"`
	Evidence  []ReceiptEvidence  `json:"evidence,omitempty"`
	Ledger    *LedgerTransaction `json:"ledger,omitempty"`
	Reorg     []ReorgEvidence    `json:"reorg,omitempty"`
	Status    *ChainStatus       `json:"status,omitempty"`
}

type ledgerPayload struct {
	Transaction LedgerTransaction `json:"transaction"`
}

type Engine struct {
	mu                 sync.Mutex
	config             Config
	journal            *journal
	status             ChainStatus
	executions         map[string]Execution
	executionByHash    map[string]string
	escrowCalls        map[string]EscrowCall
	escrowByAuth       map[string]string
	escrowByHash       map[string]string
	ledger             map[string]LedgerTransaction
	balances           map[string]map[string]*big.Int
	lastResumeOperator string
}

func Open(path string, config Config) (*Engine, error) {
	if err := config.defaults(); err != nil {
		return nil, err
	}
	journal, err := openJournal(path)
	if err != nil {
		return nil, err
	}
	now := config.Clock().UTC()
	engine := &Engine{
		config: config, journal: journal,
		status: ChainStatus{
			ChainID: config.ChainID,
			State:   StateSuspectedStall, Reason: "startup requires fresh independent Base observations",
			RequiredObserverQuorum: config.ObserverQuorum, StateChangedAt: now,
		},
		executions: make(map[string]Execution), executionByHash: make(map[string]string),
		escrowCalls: make(map[string]EscrowCall), escrowByAuth: make(map[string]string), escrowByHash: make(map[string]string),
		ledger: make(map[string]LedgerTransaction), balances: make(map[string]map[string]*big.Int),
	}
	engine.setPauseFlags(&engine.status)
	for _, event := range journal.Events() {
		if err := engine.apply(event); err != nil {
			_ = journal.Close()
			return nil, fmt.Errorf("replay reconciliation event %d: %w", event.Sequence, err)
		}
	}
	if engine.status.State == StateHealthy || engine.status.State == StateRecovering {
		status := engine.status
		if status.State == StateHealthy {
			status.State = StateSuspectedStall
		}
		status.Reason = "process restart requires a fresh independent Base stability window"
		status.StateChangedAt = now
		status.ConsecutiveUnhealthy = 0
		status.ConsecutiveRecovery = 0
		status.ReadyForManualResume = false
		engine.setPauseFlags(&status)
		event, err := journal.append(context.Background(), now, eventChainStatus, "restart", chainPayload{Status: status, MarkBroadcastsPending: true})
		if err != nil {
			_ = journal.Close()
			return nil, err
		}
		if err := engine.apply(event); err != nil {
			_ = journal.Close()
			return nil, err
		}
	}
	engine.refreshAffected()
	return engine, nil
}

func (e *Engine) Close() error { return e.journal.Close() }

func (e *Engine) CheckChain(_ context.Context, chainID uint64) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	now := e.config.Clock().UTC()
	status := e.statusAt(now)
	if chainID != e.config.ChainID || !e.chainUsable(now) {
		return fmt.Errorf("%w: state=%s", ErrChainUnavailable, status.State)
	}
	return nil
}

func (e *Engine) FinalityDepth() uint64 { return e.config.ReorgLookback }

func (e *Engine) CheckAuthorizationChain(_ context.Context, authorization envelope.Authorization) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	now := e.config.Clock().UTC()
	status := e.statusAt(now)
	if authorization.ChainID != e.config.ChainID || !e.chainUsable(now) {
		return fmt.Errorf("%w: state=%s", ErrChainUnavailable, status.State)
	}
	if authorization.IssuedAt < e.status.StateChangedAt.Unix() {
		return fmt.Errorf("%w: authorization predates the latest healthy recovery epoch", ErrChainUnavailable)
	}
	return nil
}

func (e *Engine) Status() ChainStatus {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.statusAt(e.config.Clock().UTC())
}

func (e *Engine) Observe(ctx context.Context, observations []Observation) (ChainStatus, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	now := e.config.Clock().UTC()
	healthy, checkpoint, reason := e.evaluateSnapshot(now, observations)
	status := cloneStatus(e.status)
	status.RequiredObserverQuorum = e.config.ObserverQuorum
	status.RespondingObservers = len(observations)
	status.LastObservationAt = now
	if status.State == StateHealthy && !e.trustedFresh(now) {
		status.State = StateSuspectedStall
		status.StateChangedAt = now
		status.Reason = "observer heartbeat or trusted Base head expired before this snapshot"
		status.ConsecutiveUnhealthy = 1
	}
	status.ReadyForManualResume = false
	if healthy {
		switch status.State {
		case StateHealthy:
			status.Reason = "independent Base observers agree on canonical progression"
			status.ConsecutiveUnhealthy = 0
		case StateSuspectedStall, StateHalted:
			status.State = StateRecovering
			status.StateChangedAt = now
			status.Reason = "canonical progression resumed; reconciliation and manual release remain required"
			status.ConsecutiveUnhealthy = 0
			status.ConsecutiveRecovery = 1
		case StateRecovering:
			status.ConsecutiveRecovery++
			status.Reason = "recovery observations agree; manual release remains required"
		}
		if status.State == StateRecovering && status.ConsecutiveRecovery >= e.config.RecoveryObservations && e.unresolvedCount() == 0 {
			status.ReadyForManualResume = true
			status.Reason = "recovery stability reached and all ambiguous executions resolved or quarantined"
		}
		if checkpoint != nil && (status.LastTrusted == nil || checkpoint.BlockNumber > status.LastTrusted.BlockNumber || checkpoint.BlockNumber == status.LastTrusted.BlockNumber && checkpoint.BlockHash == status.LastTrusted.BlockHash) {
			status.LastTrusted = checkpoint
		}
	} else {
		switch status.State {
		case StateHealthy:
			status.State = StateSuspectedStall
			status.StateChangedAt = now
			status.ConsecutiveUnhealthy = 1
			status.ConsecutiveRecovery = 0
		case StateSuspectedStall:
			status.ConsecutiveUnhealthy++
			if status.ConsecutiveUnhealthy >= e.config.HaltConfirmations {
				status.State = StateHalted
				status.StateChangedAt = now
			}
		case StateRecovering:
			status.State = StateHalted
			status.StateChangedAt = now
			status.ConsecutiveUnhealthy = 1
			status.ConsecutiveRecovery = 0
		case StateHalted:
			status.ConsecutiveUnhealthy++
		}
		status.Reason = reason
	}
	e.setPauseFlags(&status)
	status.AffectedExecutions = e.unresolvedCount()
	event, err := e.journal.append(ctx, now, eventChainStatus, fmt.Sprintf("snapshot:%d", now.UnixNano()), chainPayload{Status: status, Observations: cloneObservations(observations)})
	if err != nil {
		return ChainStatus{}, err
	}
	if err := e.apply(event); err != nil {
		return ChainStatus{}, err
	}
	e.refreshAffected()
	return cloneStatus(e.status), nil
}

func (e *Engine) ForceHalt(ctx context.Context, operator, reason string) (ChainStatus, error) {
	if !identifierPattern.MatchString(operator) {
		return ChainStatus{}, ErrInvalidOperator
	}
	if reason == "" || len(reason) > 1024 {
		return ChainStatus{}, ErrInvalidHaltReason
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	now := e.config.Clock().UTC()
	status := cloneStatus(e.status)
	status.State = StateHalted
	status.StateChangedAt = now
	status.Reason = "manual halt: " + reason
	status.ConsecutiveRecovery = 0
	status.ReadyForManualResume = false
	e.setPauseFlags(&status)
	status.AffectedExecutions = e.unresolvedCount()
	event, err := e.journal.append(ctx, now, eventChainStatus, "manual-halt:"+operator, chainPayload{Status: status, Operator: operator})
	if err != nil {
		return ChainStatus{}, err
	}
	if err := e.apply(event); err != nil {
		return ChainStatus{}, err
	}
	e.refreshAffected()
	return cloneStatus(e.status), nil
}

func (e *Engine) Resume(ctx context.Context, operator string) (ChainStatus, error) {
	if !identifierPattern.MatchString(operator) {
		return ChainStatus{}, ErrInvalidOperator
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.status.State == StateHealthy && e.lastResumeOperator == operator && e.chainUsable(e.config.Clock().UTC()) {
		return cloneStatus(e.status), nil
	}
	if !e.recoveryReady(e.config.Clock().UTC()) {
		return ChainStatus{}, ErrResumeBlocked
	}
	now := e.config.Clock().UTC()
	status := cloneStatus(e.status)
	status.State = StateHealthy
	status.StateChangedAt = now
	status.Reason = "manual recovery release by " + operator
	status.ConsecutiveUnhealthy = 0
	status.ReadyForManualResume = false
	e.setPauseFlags(&status)
	event, err := e.journal.append(ctx, now, eventChainStatus, "manual-resume:"+operator, chainPayload{Status: status, Operator: operator})
	if err != nil {
		return ChainStatus{}, err
	}
	if err := e.apply(event); err != nil {
		return ChainStatus{}, err
	}
	return cloneStatus(e.status), nil
}

func (e *Engine) RegisterBroadcast(ctx context.Context, expected ExpectedExecution) (Execution, error) {
	if err := expected.validate(e.config.ChainID); err != nil {
		return Execution{}, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if existing, ok := e.executions[expected.ExecutionID]; ok {
		if equalJSON(existing.Expected, expected) {
			return cloneExecution(existing), nil
		}
		return Execution{}, ErrConflict
	}
	if other, ok := e.executionByHash[expected.TransactionHash]; ok && other != expected.ExecutionID {
		return Execution{}, ErrConflict
	}
	if _, ok := e.escrowByHash[expected.TransactionHash]; ok {
		return Execution{}, ErrConflict
	}
	now := e.config.Clock().UTC()
	if !e.chainUsable(now) {
		return Execution{}, fmt.Errorf("%w: state=%s", ErrChainUnavailable, e.statusAt(now).State)
	}
	execution := Execution{Expected: expected, State: ExecutionBroadcast, BroadcastAt: now}
	event, err := e.journal.append(ctx, execution.BroadcastAt, eventExecutionBroadcast, expected.ExecutionID, executionPayload{Execution: execution})
	if err != nil {
		return Execution{}, err
	}
	if err := e.apply(event); err != nil {
		return Execution{}, err
	}
	return cloneExecution(execution), nil
}

// RegisterAttestedBroadcast records a transaction that the customer-controlled
// signer may already have submitted. Unlike RegisterBroadcast, it must remain
// available after Base becomes unhealthy: refusing the callback would discard
// the only durable handle for an ambiguous payment. An unhealthy chain places
// the execution directly into PENDING_CHAIN_RECOVERY.
func (e *Engine) RegisterAttestedBroadcast(ctx context.Context, expected ExpectedExecution, attestation BroadcastAttestation) (Execution, error) {
	if err := expected.validate(e.config.ChainID); err != nil {
		return Execution{}, err
	}
	if err := attestation.validate(expected); err != nil {
		return Execution{}, err
	}
	broadcastAt := time.Unix(attestation.SignedReceipt.Receipt.BroadcastAt, 0).UTC()
	e.mu.Lock()
	defer e.mu.Unlock()
	if existing, ok := e.executions[expected.ExecutionID]; ok {
		if equalJSON(existing.Expected, expected) {
			return cloneExecution(existing), nil
		}
		return Execution{}, ErrConflict
	}
	if other, ok := e.executionByHash[expected.TransactionHash]; ok && other != expected.ExecutionID {
		return Execution{}, ErrConflict
	}
	if _, ok := e.escrowByHash[expected.TransactionHash]; ok {
		return Execution{}, ErrConflict
	}
	now := e.config.Clock().UTC()
	state := ExecutionBroadcast
	if !e.chainUsable(now) {
		state = ExecutionPendingChainRecovery
	}
	attestationCopy := attestation
	execution := Execution{Expected: expected, State: state, BroadcastAt: broadcastAt, BroadcastAttestation: &attestationCopy}
	event, err := e.journal.append(ctx, now, eventExecutionBroadcast, expected.ExecutionID, executionPayload{Execution: execution})
	if err != nil {
		return Execution{}, err
	}
	if err := e.apply(event); err != nil {
		return Execution{}, err
	}
	return cloneExecution(execution), nil
}

func (e *Engine) ReconcileReceipt(ctx context.Context, executionID string, evidence []ReceiptEvidence, settlement *LedgerTransaction) (Execution, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	execution, ok := e.executions[executionID]
	if !ok {
		return Execution{}, ErrUnknownExecution
	}
	if execution.State == ExecutionSettled || execution.State == ExecutionReverted {
		if !e.resolvedRequestMatches(execution, evidence, settlement) {
			return Execution{}, ErrConflict
		}
		return cloneExecution(execution), nil
	}
	if execution.State == ExecutionQuarantined {
		return Execution{}, errors.New("quarantined execution requires an explicit operator resolution workflow")
	}
	now := e.config.Clock().UTC()
	if e.status.State == StateHalted || e.status.State == StateSuspectedStall || !e.trustedFresh(now) {
		return Execution{}, fmt.Errorf("%w: state=%s", ErrChainUnavailable, e.statusAt(now).State)
	}
	canonical, err := e.validateReceiptQuorum(execution.Expected, evidence)
	if err != nil {
		return Execution{}, err
	}
	if execution.CorrectionTransactionID != "" && execution.ReorgEvidenceDigest != "" && canonical.BlockNumber == execution.BlockNumber && canonical.BlockHash == execution.BlockHash {
		return Execution{}, fmt.Errorf("%w: receipt repeats the block removed by canonical reorg evidence", ErrUnsafeFinality)
	}
	resolved := cloneExecution(execution)
	resolved.BlockNumber = canonical.BlockNumber
	resolved.BlockHash = canonical.BlockHash
	resolved.ResolvedAt = &now
	resolved.FinalityCheckedAt = nil
	resolved.FinalityCheckedHead = 0
	var ledgerCopy *LedgerTransaction
	if canonical.Success {
		if settlement == nil {
			return Execution{}, errors.New("settled receipt requires a balanced settlement transaction")
		}
		candidate := cloneLedger(*settlement)
		if candidate.Kind != LedgerSettlement || candidate.ReferenceID != executionID || candidate.OrganizationID != execution.Expected.OrganizationID {
			return Execution{}, errors.New("settlement ledger transaction is not bound to the execution")
		}
		if err := candidate.validate(); err != nil {
			return Execution{}, err
		}
		if existing, exists := e.ledger[candidate.TransactionID]; exists && !equalJSON(existing, candidate) {
			return Execution{}, ErrConflict
		}
		resolved.State = ExecutionSettled
		resolved.Resolution = "canonical receipt success"
		resolved.LedgerTransactionID = candidate.TransactionID
		ledgerCopy = &candidate
	} else {
		if settlement != nil {
			return Execution{}, errors.New("reverted receipt cannot post a settlement transaction")
		}
		resolved.State = ExecutionReverted
		resolved.Resolution = "canonical receipt reverted"
	}
	event, err := e.journal.append(ctx, now, eventExecutionResolved, executionID, executionPayload{Execution: resolved, Evidence: cloneEvidence(evidence), Ledger: ledgerCopy})
	if err != nil {
		return Execution{}, err
	}
	if err := e.apply(event); err != nil {
		return Execution{}, err
	}
	e.refreshAffected()
	return cloneExecution(resolved), nil
}

func (e *Engine) Quarantine(ctx context.Context, executionID, operator, reason string) (Execution, error) {
	return e.quarantine(ctx, "", executionID, operator, reason)
}

// QuarantineForOrganization makes the tenant check and state transition under
// the same engine lock. The unscoped method remains for internal recovery
// callers that already hold an execution identity from the engine itself.
func (e *Engine) QuarantineForOrganization(ctx context.Context, organizationID, executionID, operator, reason string) (Execution, error) {
	if !identifierPattern.MatchString(organizationID) {
		return Execution{}, ErrUnknownExecution
	}
	return e.quarantine(ctx, organizationID, executionID, operator, reason)
}

func (e *Engine) quarantine(ctx context.Context, organizationID, executionID, operator, reason string) (Execution, error) {
	if !identifierPattern.MatchString(operator) || reason == "" || len(reason) > 1024 {
		return Execution{}, errors.New("operator or quarantine reason is invalid")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	execution, ok := e.executions[executionID]
	if !ok || organizationID != "" && execution.Expected.OrganizationID != organizationID {
		return Execution{}, ErrUnknownExecution
	}
	if execution.State != ExecutionPendingChainRecovery && execution.State != ExecutionBroadcast {
		return Execution{}, errors.New("only unresolved executions can be quarantined")
	}
	now := e.config.Clock().UTC()
	execution.State = ExecutionQuarantined
	execution.Resolution = "manual quarantine by " + operator + ": " + reason
	execution.ResolvedAt = &now
	event, err := e.journal.append(ctx, now, eventExecutionQuarantine, executionID, executionPayload{Execution: execution})
	if err != nil {
		return Execution{}, err
	}
	if err := e.apply(event); err != nil {
		return Execution{}, err
	}
	e.refreshAffected()
	return cloneExecution(execution), nil
}

func (e *Engine) ReopenReorg(ctx context.Context, executionID string, evidence []ReorgEvidence, correction LedgerTransaction) (Execution, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	execution, ok := e.executions[executionID]
	if !ok {
		return Execution{}, ErrUnknownExecution
	}
	reorgDigest, canonicalEvidence, err := digestReorgEvidence(evidence)
	if err != nil {
		return Execution{}, err
	}
	if execution.State == ExecutionPendingChainRecovery && execution.CorrectionTransactionID != "" {
		stored, exists := e.ledger[execution.CorrectionTransactionID]
		if exists && equalJSON(stored, correction) && execution.ReorgEvidenceDigest == reorgDigest {
			return cloneExecution(execution), nil
		}
		return Execution{}, ErrConflict
	}
	if execution.State != ExecutionSettled || execution.LedgerTransactionID == "" {
		return Execution{}, errors.New("only a settled execution can be reopened for reorg")
	}
	now := e.config.Clock().UTC()
	if (e.status.State != StateHealthy && e.status.State != StateRecovering) || !e.trustedFresh(now) {
		return Execution{}, fmt.Errorf("%w: state=%s", ErrChainUnavailable, e.statusAt(now).State)
	}
	if err := e.validateReorgQuorum(execution, canonicalEvidence); err != nil {
		return Execution{}, err
	}
	if correction.Kind != LedgerCorrection || correction.ReversesTransactionID != execution.LedgerTransactionID || correction.OrganizationID != execution.Expected.OrganizationID || correction.ReferenceID != executionID {
		return Execution{}, errors.New("reorg correction is not bound to the settled execution")
	}
	if err := correction.validate(); err != nil {
		return Execution{}, err
	}
	if err := e.validateCorrection(correction); err != nil {
		return Execution{}, err
	}
	if _, exists := e.ledger[correction.TransactionID]; exists {
		return Execution{}, ErrConflict
	}
	reopened := cloneExecution(execution)
	reopened.State = ExecutionPendingChainRecovery
	reopened.ResolvedAt = nil
	reopened.Resolution = "canonical reorg removed the prior settlement block; outcome requires reconciliation"
	reopened.CorrectionTransactionID = correction.TransactionID
	reopened.ReorgEvidenceDigest = reorgDigest
	reopened.LedgerTransactionID = ""
	reopened.FinalityCheckedAt = nil
	reopened.FinalityCheckedHead = 0
	status := cloneStatus(e.status)
	status.State = StateRecovering
	status.StateChangedAt = now
	status.Reason = "canonical reorg evidence reopened a settled execution"
	status.ConsecutiveRecovery = 0
	status.ReadyForManualResume = false
	status.AffectedExecutions = e.unresolvedCount() + 1
	e.setPauseFlags(&status)
	ledgerCopy := cloneLedger(correction)
	event, err := e.journal.append(ctx, now, eventExecutionReorged, executionID, executionPayload{
		Execution: reopened, Ledger: &ledgerCopy, Reorg: canonicalEvidence, Status: &status,
	})
	if err != nil {
		return Execution{}, err
	}
	if err := e.apply(event); err != nil {
		return Execution{}, err
	}
	e.refreshAffected()
	return cloneExecution(reopened), nil
}

func (e *Engine) ConfirmFinality(ctx context.Context, executionID string, evidence []ReorgEvidence) (Execution, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	execution, ok := e.executions[executionID]
	if !ok {
		return Execution{}, ErrUnknownExecution
	}
	if execution.State != ExecutionSettled || execution.LedgerTransactionID == "" {
		return Execution{}, errors.New("only a settled execution can complete finality monitoring")
	}
	canonical, err := e.validateCanonicalBlockQuorum(execution, evidence, false)
	if err != nil {
		return Execution{}, err
	}
	if execution.FinalityCheckedAt != nil {
		if execution.FinalityCheckedHead == canonical.ObservedHead {
			return cloneExecution(execution), nil
		}
		return Execution{}, ErrConflict
	}
	now := e.config.Clock().UTC()
	if (e.status.State != StateHealthy && e.status.State != StateRecovering) || !e.trustedFresh(now) {
		return Execution{}, fmt.Errorf("%w: state=%s", ErrChainUnavailable, e.statusAt(now).State)
	}
	confirmed := cloneExecution(execution)
	confirmed.FinalityCheckedAt = &now
	confirmed.FinalityCheckedHead = canonical.ObservedHead
	// Reuse the existing resolved-event envelope so an older binary can safely
	// replay a journal written by this version during an image rollback. Older
	// readers ignore the additive finality fields and retain the settlement.
	event, err := e.journal.append(ctx, now, eventExecutionResolved, executionID, executionPayload{Execution: confirmed, Reorg: cloneReorgEvidence(evidence)})
	if err != nil {
		return Execution{}, err
	}
	if err := e.apply(event); err != nil {
		return Execution{}, err
	}
	return cloneExecution(confirmed), nil
}

func (e *Engine) Post(ctx context.Context, transaction LedgerTransaction) (LedgerTransaction, error) {
	if err := transaction.validate(); err != nil {
		return LedgerTransaction{}, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if existing, ok := e.ledger[transaction.TransactionID]; ok {
		if equalJSON(existing, transaction) {
			return cloneLedger(existing), nil
		}
		return LedgerTransaction{}, ErrConflict
	}
	if chainDependent(transaction.Kind) {
		return LedgerTransaction{}, errors.New("chain-dependent ledger entries require canonical reconciliation evidence")
	}
	if transaction.Kind == LedgerCorrection {
		if err := e.validateCorrection(transaction); err != nil {
			return LedgerTransaction{}, err
		}
	}
	event, err := e.journal.append(ctx, e.config.Clock().UTC(), eventLedgerPosted, transaction.TransactionID, ledgerPayload{Transaction: cloneLedger(transaction)})
	if err != nil {
		return LedgerTransaction{}, err
	}
	if err := e.apply(event); err != nil {
		return LedgerTransaction{}, err
	}
	return cloneLedger(transaction), nil
}

func (e *Engine) Execution(executionID string) (Execution, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	execution, ok := e.executions[executionID]
	return cloneExecution(execution), ok
}

func (e *Engine) Executions() []Execution {
	e.mu.Lock()
	defer e.mu.Unlock()
	result := make([]Execution, 0, len(e.executions))
	for _, execution := range e.executions {
		result = append(result, cloneExecution(execution))
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Expected.ExecutionID < result[j].Expected.ExecutionID })
	return result
}

func (e *Engine) LedgerTransaction(transactionID string) (LedgerTransaction, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	transaction, ok := e.ledger[transactionID]
	return cloneLedger(transaction), ok
}

func (e *Engine) Balance(organizationID, account string) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	if organization, ok := e.balances[organizationID]; ok {
		if balance, ok := organization[account]; ok {
			return balance.String()
		}
	}
	return "0"
}

func (e *Engine) apply(event journalEvent) error {
	switch event.Kind {
	case eventChainStatus:
		var payload chainPayload
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return err
		}
		e.status = cloneStatus(payload.Status)
		e.trackManualRelease(payload.Operator)
		if e.status.State == StateHalted || payload.MarkBroadcastsPending {
			e.markBroadcastsPending(&e.status)
		}
	case eventExecutionBroadcast, eventExecutionQuarantine:
		var payload executionPayload
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return err
		}
		if err := e.preserveBroadcastAttestation(&payload.Execution); err != nil {
			return err
		}
		if _, exists := e.escrowByHash[payload.Execution.Expected.TransactionHash]; exists {
			return errors.New("execution transaction hash is already bound to an escrow transition")
		}
		e.executions[payload.Execution.Expected.ExecutionID] = cloneExecution(payload.Execution)
		e.executionByHash[payload.Execution.Expected.TransactionHash] = payload.Execution.Expected.ExecutionID
	case eventExecutionResolved, eventExecutionReorged:
		var payload executionPayload
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return err
		}
		if err := e.preserveBroadcastAttestation(&payload.Execution); err != nil {
			return err
		}
		e.executions[payload.Execution.Expected.ExecutionID] = cloneExecution(payload.Execution)
		if payload.Ledger != nil {
			e.applyLedger(*payload.Ledger)
		}
		if payload.Status != nil {
			e.status = cloneStatus(*payload.Status)
			e.trackManualRelease("")
		}
	case eventEscrowIntent, eventEscrowPending, eventEscrowResolved, eventEscrowReorged:
		var payload escrowPayload
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return err
		}
		callID := payload.Call.Intent.CallID
		if event.Key != callID {
			return errors.New("escrow event key does not match call identity")
		}
		if e.config.EscrowContract == "" || payload.Call.Intent.Contract != e.config.EscrowContract || payload.Call.Intent.Asset != e.config.EscrowAsset || payload.Call.Intent.ReleaseWindow != e.config.EscrowReleaseWindow {
			return fmt.Errorf("%w: journal call does not match the configured reviewed tuple", ErrEscrowDeployment)
		}
		current, exists := e.escrowCalls[callID]
		if event.Kind == eventEscrowIntent {
			if exists || payload.Call.RegisteredAt != event.At || payload.Call.Pending != nil || len(payload.Call.Transitions) != 0 {
				return errors.New("escrow intent event does not create exactly one new call")
			}
		} else if !exists || !equalJSON(current.Intent, payload.Call.Intent) || current.RegisteredAt != payload.Call.RegisteredAt {
			return errors.New("escrow event changed immutable call identity")
		}
		if other, ok := e.escrowByAuth[payload.Call.Intent.AuthorizationID]; ok && other != callID {
			return errors.New("escrow authorization is already bound to another call")
		}
		for _, transition := range payload.Call.Transitions {
			if other, ok := e.escrowByHash[transition.Expected.TransactionHash]; ok && other != callID {
				return errors.New("escrow transaction hash is already bound to another call")
			}
			if _, ok := e.executionByHash[transition.Expected.TransactionHash]; ok {
				return errors.New("escrow transaction hash is already bound to a direct execution")
			}
		}
		if payload.Call.Pending != nil {
			if other, ok := e.escrowByHash[payload.Call.Pending.Expected.TransactionHash]; ok && other != callID {
				return errors.New("pending escrow transaction hash is already bound to another call")
			}
			if _, ok := e.executionByHash[payload.Call.Pending.Expected.TransactionHash]; ok {
				return errors.New("pending escrow transaction hash is already bound to a direct execution")
			}
		}
		if err := payload.Call.validateSnapshot(e.config.ChainID); err != nil {
			return fmt.Errorf("escrow snapshot: %w", err)
		}
		if err := e.validateEscrowEventEvolution(event.Kind, event.At, current, exists, payload); err != nil {
			return fmt.Errorf("escrow event evolution: %w", err)
		}
		if err := validateEscrowLedgerBindings(payload.Call, payload.Ledgers, e.ledger, event.At); err != nil {
			return fmt.Errorf("escrow ledger bindings: %w", err)
		}
		e.escrowCalls[callID] = cloneEscrowCall(payload.Call)
		e.escrowByAuth[payload.Call.Intent.AuthorizationID] = callID
		for _, transition := range payload.Call.Transitions {
			e.escrowByHash[transition.Expected.TransactionHash] = callID
		}
		if payload.Call.Pending != nil {
			e.escrowByHash[payload.Call.Pending.Expected.TransactionHash] = callID
		}
		for _, transaction := range payload.Ledgers {
			e.applyLedger(transaction)
		}
		if payload.Status != nil {
			e.status = cloneStatus(*payload.Status)
			e.trackManualRelease("")
		}
	case eventLedgerPosted:
		var payload ledgerPayload
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return err
		}
		e.applyLedger(payload.Transaction)
	default:
		return fmt.Errorf("unsupported reconciliation event kind %q", event.Kind)
	}
	// ChainID was added after the original reconciliation journal format. A
	// missing value is normalized for rollback compatibility, while a non-zero
	// mismatch is rejected so an operator can never replay one network's chain
	// state into another network's runtime.
	if e.status.ChainID == 0 {
		e.status.ChainID = e.config.ChainID
	} else if e.status.ChainID != e.config.ChainID {
		return fmt.Errorf("reconciliation chain status %d does not match configured chain %d", e.status.ChainID, e.config.ChainID)
	}
	return nil
}

// preserveBroadcastAttestation makes additive proof fields rollback tolerant.
// An older binary ignores the proof while replaying the original broadcast and
// can later append a resolution without it. The original event remains in the
// journal, so a newer binary must carry that proof through later legacy events.
func (e *Engine) preserveBroadcastAttestation(next *Execution) error {
	if next == nil {
		return nil
	}
	if next.BroadcastAttestation == nil {
		if current, ok := e.executions[next.Expected.ExecutionID]; ok && current.BroadcastAttestation != nil {
			attestation := *current.BroadcastAttestation
			next.BroadcastAttestation = &attestation
		}
	}
	if next.BroadcastAttestation != nil {
		if err := next.BroadcastAttestation.validate(next.Expected); err != nil {
			return fmt.Errorf("execution broadcast attestation: %w", err)
		}
	}
	return nil
}

func (e *Engine) trackManualRelease(operator string) {
	if e.status.RequiredObserverQuorum == 0 {
		e.status.RequiredObserverQuorum = e.config.ObserverQuorum
	}
	if e.status.LastObservationAt.IsZero() && e.status.LastTrusted != nil {
		e.status.LastObservationAt = e.status.LastTrusted.ObservedAt
	}
	if e.status.State != StateHealthy {
		e.lastResumeOperator = ""
	} else if operator != "" {
		e.lastResumeOperator = operator
	}
}

func (e *Engine) applyLedger(transaction LedgerTransaction) {
	transaction = cloneLedger(transaction)
	e.ledger[transaction.TransactionID] = transaction
	if e.balances[transaction.OrganizationID] == nil {
		e.balances[transaction.OrganizationID] = make(map[string]*big.Int)
	}
	for _, posting := range transaction.Postings {
		if e.balances[transaction.OrganizationID][posting.Account] == nil {
			e.balances[transaction.OrganizationID][posting.Account] = new(big.Int)
		}
		amount, _ := new(big.Int).SetString(posting.AmountAtomic, 10)
		e.balances[transaction.OrganizationID][posting.Account].Add(e.balances[transaction.OrganizationID][posting.Account], amount)
	}
}

func (e *Engine) evaluateSnapshot(now time.Time, observations []Observation) (bool, *Checkpoint, string) {
	if len(observations) < e.config.ObserverQuorum {
		return false, nil, "fewer than the required independent Base observers responded"
	}
	seen := make(map[string]struct{}, len(observations))
	first := observations[0]
	minHead, maxHead := first.HeadNumber, first.HeadNumber
	for _, observation := range observations {
		if !identifierPattern.MatchString(observation.Provider) {
			return false, nil, "observer identity is invalid"
		}
		if _, exists := seen[observation.Provider]; exists {
			return false, nil, "duplicate observer cannot contribute to quorum"
		}
		seen[observation.Provider] = struct{}{}
		if observation.ChainID != e.config.ChainID || !hashPattern.MatchString(observation.HeadHash) || !hashPattern.MatchString(observation.AnchorHash) || observation.HeadNumber < observation.AnchorNumber {
			return false, nil, "observer returned invalid Base chain data"
		}
		if observation.ObservedAt.IsZero() || observation.HeadTime.IsZero() || observation.AnchorTime.IsZero() || observation.ObservedAt.After(now.Add(e.config.MaxFutureClockSkew)) || observation.HeadTime.After(now.Add(e.config.MaxFutureClockSkew)) || observation.AnchorTime.After(now.Add(e.config.MaxFutureClockSkew)) {
			return false, nil, "observer timestamps are invalid"
		}
		if now.Sub(observation.ObservedAt) > e.config.ObservationMaxAge {
			return false, nil, "observer result is stale even though the RPC may be reachable"
		}
		if now.Sub(observation.HeadTime) > e.config.StallThreshold {
			return false, nil, "Base head is not progressing within the configured threshold"
		}
		if observation.AnchorNumber != first.AnchorNumber || observation.AnchorHash != first.AnchorHash {
			return false, nil, "independent Base observers disagree on the canonical anchor"
		}
		if observation.HeadNumber < minHead {
			minHead = observation.HeadNumber
		}
		if observation.HeadNumber > maxHead {
			maxHead = observation.HeadNumber
		}
	}
	if maxHead-minHead > e.config.MaxHeadSkew {
		return false, nil, "independent Base observer heads exceed the configured skew"
	}
	if e.status.LastTrusted != nil {
		if first.AnchorNumber < e.status.LastTrusted.BlockNumber || first.AnchorNumber == e.status.LastTrusted.BlockNumber && first.AnchorHash != e.status.LastTrusted.BlockHash {
			return false, nil, "canonical anchor regressed or conflicts with the last trusted checkpoint"
		}
	}
	checkpoint := &Checkpoint{BlockNumber: first.AnchorNumber, BlockHash: first.AnchorHash, BlockTime: first.AnchorTime.UTC(), ObservedAt: now, Cursor: first.AnchorNumber}
	return true, checkpoint, "independent Base observers agree"
}

func (e *Engine) validateReceiptQuorum(expected ExpectedExecution, evidence []ReceiptEvidence) (ReceiptEvidence, error) {
	if len(evidence) < e.config.ObserverQuorum || len(evidence) > 5 {
		return ReceiptEvidence{}, ErrUnsafeFinality
	}
	seen := make(map[string]struct{}, len(evidence))
	canonical := evidence[0]
	for _, receipt := range evidence {
		if !identifierPattern.MatchString(receipt.Provider) {
			return ReceiptEvidence{}, ErrUnsafeFinality
		}
		if _, exists := seen[receipt.Provider]; exists {
			return ReceiptEvidence{}, ErrUnsafeFinality
		}
		seen[receipt.Provider] = struct{}{}
		if receipt.ChainID != expected.ChainID || receipt.TransactionHash != expected.TransactionHash || receipt.Sender != expected.Sender || receipt.Asset != expected.Asset || receipt.Recipient != expected.Recipient || receipt.AmountAtomic != expected.AmountAtomic {
			return ReceiptEvidence{}, ErrUnsafeFinality
		}
		if !hashPattern.MatchString(receipt.BlockHash) || receipt.BlockNumber == 0 || receipt.ConfirmedHead < receipt.BlockNumber || receipt.ConfirmedHead-receipt.BlockNumber+1 < e.config.MinConfirmations {
			return ReceiptEvidence{}, ErrUnsafeFinality
		}
		if receipt.BlockNumber != canonical.BlockNumber || receipt.BlockHash != canonical.BlockHash || receipt.Success != canonical.Success {
			return ReceiptEvidence{}, ErrUnsafeFinality
		}
	}
	if e.status.LastTrusted == nil || canonical.BlockNumber > e.status.LastTrusted.BlockNumber || canonical.BlockNumber == e.status.LastTrusted.BlockNumber && canonical.BlockHash != e.status.LastTrusted.BlockHash {
		return ReceiptEvidence{}, ErrUnsafeFinality
	}
	return canonical, nil
}

func (e *Engine) validateReorgQuorum(execution Execution, evidence []ReorgEvidence) error {
	_, err := e.validateCanonicalBlockQuorum(execution, evidence, true)
	return err
}

func (e *Engine) validateCanonicalBlockQuorum(execution Execution, evidence []ReorgEvidence, requireChanged bool) (ReorgEvidence, error) {
	if len(evidence) < e.config.ObserverQuorum {
		return ReorgEvidence{}, ErrUnsafeFinality
	}
	seen := make(map[string]struct{}, len(evidence))
	canonical := evidence[0].CanonicalBlockHash
	minimumHead := evidence[0].ObservedHead
	for _, observation := range evidence {
		if !identifierPattern.MatchString(observation.Provider) {
			return ReorgEvidence{}, ErrUnsafeFinality
		}
		if _, exists := seen[observation.Provider]; exists {
			return ReorgEvidence{}, ErrUnsafeFinality
		}
		seen[observation.Provider] = struct{}{}
		if observation.ChainID != execution.Expected.ChainID || observation.TransactionHash != execution.Expected.TransactionHash || observation.OriginalBlockNumber != execution.BlockNumber || observation.OriginalBlockHash != execution.BlockHash {
			return ReorgEvidence{}, ErrUnsafeFinality
		}
		if !hashPattern.MatchString(observation.CanonicalBlockHash) || observation.CanonicalBlockHash != canonical {
			return ReorgEvidence{}, ErrUnsafeFinality
		}
		if (requireChanged && observation.CanonicalBlockHash == execution.BlockHash) || (!requireChanged && observation.CanonicalBlockHash != execution.BlockHash) {
			return ReorgEvidence{}, ErrUnsafeFinality
		}
		if observation.ObservedHead < execution.BlockNumber || observation.ObservedHead-execution.BlockNumber < e.config.ReorgLookback {
			return ReorgEvidence{}, ErrUnsafeFinality
		}
		if observation.ObservedHead < minimumHead {
			minimumHead = observation.ObservedHead
		}
	}
	canonicalEvidence := evidence[0]
	canonicalEvidence.ObservedHead = minimumHead
	if e.status.LastTrusted == nil || e.status.LastTrusted.BlockNumber < execution.BlockNumber || e.status.LastTrusted.BlockNumber-execution.BlockNumber < e.config.ReorgLookback {
		return ReorgEvidence{}, ErrUnsafeFinality
	}
	return canonicalEvidence, nil
}

func (e *Engine) resolvedRequestMatches(execution Execution, evidence []ReceiptEvidence, settlement *LedgerTransaction) bool {
	canonical, err := e.validateReceiptQuorum(execution.Expected, evidence)
	if err != nil || canonical.BlockNumber != execution.BlockNumber || canonical.BlockHash != execution.BlockHash {
		return false
	}
	if execution.State == ExecutionReverted {
		return !canonical.Success && settlement == nil
	}
	if !canonical.Success || settlement == nil || settlement.TransactionID != execution.LedgerTransactionID {
		return false
	}
	stored, ok := e.ledger[settlement.TransactionID]
	return ok && equalJSON(stored, *settlement)
}

func (e *Engine) validateCorrection(transaction LedgerTransaction) error {
	if transaction.ReversesTransactionID == "" {
		return errors.New("correction must identify the transaction it reverses")
	}
	original, ok := e.ledger[transaction.ReversesTransactionID]
	if !ok || original.OrganizationID != transaction.OrganizationID {
		return errors.New("correction target is unknown or belongs to another organization")
	}
	want := make(map[string]*big.Int)
	for _, posting := range original.Postings {
		amount, _ := new(big.Int).SetString(posting.AmountAtomic, 10)
		if want[posting.Account] == nil {
			want[posting.Account] = new(big.Int)
		}
		want[posting.Account].Sub(want[posting.Account], amount)
	}
	actual := make(map[string]*big.Int)
	for _, posting := range transaction.Postings {
		amount, _ := new(big.Int).SetString(posting.AmountAtomic, 10)
		if actual[posting.Account] == nil {
			actual[posting.Account] = new(big.Int)
		}
		actual[posting.Account].Add(actual[posting.Account], amount)
	}
	if !equalBigIntMaps(want, actual) {
		return errors.New("correction postings must exactly reverse the original transaction")
	}
	return nil
}

func (e *Engine) setPauseFlags(status *ChainStatus) {
	paused := status.State != StateHealthy
	status.AuthorizationsPaused = paused
	status.BroadcastsPaused = paused
	status.FinalizationPaused = status.State == StateHalted || status.State == StateSuspectedStall
	status.RefundRecognitionPaused = paused
}

func (e *Engine) markBroadcastsPending(status *ChainStatus) {
	for id, execution := range e.executions {
		if execution.State == ExecutionBroadcast {
			execution.State = ExecutionPendingChainRecovery
			execution.Resolution = "chain halt requires canonical outcome reconciliation"
			e.executions[id] = execution
		}
	}
	for callID, call := range e.escrowCalls {
		if call.Pending != nil && call.Pending.State == EscrowTransitionBroadcast {
			call.Pending.State = EscrowTransitionPendingRecovery
			call.Pending.Resolution = "chain halt requires canonical escrow transition reconciliation"
			e.escrowCalls[callID] = call
		}
	}
	status.AffectedExecutions = e.unresolvedCount()
}

func (e *Engine) unresolvedCount() int {
	count := 0
	for _, execution := range e.executions {
		if execution.State == ExecutionBroadcast || execution.State == ExecutionPendingChainRecovery {
			count++
		}
	}
	for _, call := range e.escrowCalls {
		if call.Pending != nil && (call.Pending.State == EscrowTransitionBroadcast || call.Pending.State == EscrowTransitionPendingRecovery) {
			count++
		}
	}
	return count
}

func (e *Engine) refreshAffected() {
	e.status.AffectedExecutions = e.unresolvedCount()
	e.status.ReadyForManualResume = e.recoveryReady(e.config.Clock().UTC())
}

func (e *Engine) chainUsable(now time.Time) bool {
	return e.status.State == StateHealthy && e.trustedFresh(now)
}

func (e *Engine) statusAt(now time.Time) ChainStatus {
	status := cloneStatus(e.status)
	if status.State == StateHealthy && !e.trustedFresh(now) {
		status.State = StateSuspectedStall
		status.Reason = "observer heartbeat or trusted Base head expired"
		if status.LastTrusted != nil {
			status.StateChangedAt = status.LastTrusted.ObservedAt.Add(e.config.ObservationMaxAge)
		}
		e.setPauseFlags(&status)
	}
	return status
}

func (e *Engine) recoveryReady(now time.Time) bool {
	return e.status.State == StateRecovering && e.status.ConsecutiveRecovery >= e.config.RecoveryObservations && e.unresolvedCount() == 0 && e.trustedFresh(now)
}

func (e *Engine) trustedFresh(now time.Time) bool {
	if e.status.LastTrusted == nil || now.Before(e.status.LastTrusted.ObservedAt) || now.Before(e.status.LastTrusted.BlockTime) {
		return false
	}
	return now.Sub(e.status.LastTrusted.ObservedAt) <= e.config.ObservationMaxAge && now.Sub(e.status.LastTrusted.BlockTime) <= e.config.StallThreshold
}

func chainDependent(kind LedgerKind) bool {
	return kind == LedgerSettlement || kind == LedgerRefund || kind == LedgerFunding
}

func cloneStatus(status ChainStatus) ChainStatus {
	if status.LastTrusted != nil {
		checkpoint := *status.LastTrusted
		status.LastTrusted = &checkpoint
	}
	return status
}

func cloneExecution(execution Execution) Execution {
	if execution.BroadcastAttestation != nil {
		attestation := *execution.BroadcastAttestation
		execution.BroadcastAttestation = &attestation
	}
	if execution.ResolvedAt != nil {
		resolvedAt := *execution.ResolvedAt
		execution.ResolvedAt = &resolvedAt
	}
	if execution.FinalityCheckedAt != nil {
		checkedAt := *execution.FinalityCheckedAt
		execution.FinalityCheckedAt = &checkedAt
	}
	return execution
}

func cloneReorgEvidence(evidence []ReorgEvidence) []ReorgEvidence {
	return append([]ReorgEvidence(nil), evidence...)
}

func cloneLedger(transaction LedgerTransaction) LedgerTransaction {
	transaction.Postings = append([]Posting(nil), transaction.Postings...)
	return transaction
}

func cloneObservations(observations []Observation) []Observation {
	return append([]Observation(nil), observations...)
}
func cloneEvidence(evidence []ReceiptEvidence) []ReceiptEvidence {
	return append([]ReceiptEvidence(nil), evidence...)
}

func equalJSON(left, right any) bool {
	leftJSON, _ := json.Marshal(left)
	rightJSON, _ := json.Marshal(right)
	return string(leftJSON) == string(rightJSON)
}

func equalBigIntMaps(left, right map[string]*big.Int) bool {
	if len(left) != len(right) {
		return false
	}
	keys := make([]string, 0, len(left))
	for key := range left {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		value, ok := right[key]
		if !ok || left[key].Cmp(value) != 0 {
			return false
		}
	}
	return true
}

func digestReorgEvidence(evidence []ReorgEvidence) (string, []ReorgEvidence, error) {
	canonical := append([]ReorgEvidence(nil), evidence...)
	sort.Slice(canonical, func(i, j int) bool { return canonical[i].Provider < canonical[j].Provider })
	encoded, err := json.Marshal(canonical)
	if err != nil {
		return "", nil, err
	}
	hash := sha256.Sum256(encoded)
	return "0x" + hex.EncodeToString(hash[:]), canonical, nil
}
