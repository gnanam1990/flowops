package reconciliation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"time"
)

type EscrowPositionState string

const (
	EscrowPositionRegistered   EscrowPositionState = "REGISTERED"
	EscrowPositionFunded       EscrowPositionState = "FUNDED"
	EscrowPositionAcknowledged EscrowPositionState = "ACKNOWLEDGED"
	EscrowPositionDelivered    EscrowPositionState = "DELIVERED"
	EscrowPositionReleased     EscrowPositionState = "RELEASED"
	EscrowPositionRefunded     EscrowPositionState = "REFUNDED"
	EscrowPositionQuarantined  EscrowPositionState = "QUARANTINED"
)

type EscrowTransitionState string

const (
	EscrowTransitionBroadcast       EscrowTransitionState = "BROADCAST"
	EscrowTransitionPendingRecovery EscrowTransitionState = "PENDING_CHAIN_RECOVERY"
	EscrowTransitionConfirmed       EscrowTransitionState = "CONFIRMED"
	EscrowTransitionReverted        EscrowTransitionState = "REVERTED"
	EscrowTransitionReorged         EscrowTransitionState = "REORGED"
)

// EscrowIntent is the immutable, approved call snapshot registered before any
// transaction is broadcast. Dynamic delivery and terminal fields are supplied
// only when their transaction hash is registered later.
type EscrowIntent struct {
	OrganizationID  string `json:"organizationId"`
	CustomerID      string `json:"customerId"`
	AgentID         string `json:"agentId"`
	TaskID          string `json:"taskId"`
	AuthorizationID string `json:"authorizationId"`
	IntentDigest    string `json:"intentDigest"`
	ChainID         uint64 `json:"chainId"`
	Contract        string `json:"contract"`
	Asset           string `json:"asset"`
	CallID          string `json:"callId"`
	Buyer           string `json:"buyer"`
	Provider        string `json:"provider"`
	AmountAtomic    string `json:"amountAtomic"`
	TaskDigest      string `json:"taskDigest"`
	RequestDigest   string `json:"requestDigest"`
	AcknowledgeBy   uint64 `json:"acknowledgeBy"`
	DeliverBy       uint64 `json:"deliverBy"`
	ReleaseWindow   uint64 `json:"releaseWindowSeconds"`
}

func (i EscrowIntent) Validate() error {
	for name, value := range map[string]string{
		"organizationId": i.OrganizationID, "customerId": i.CustomerID, "agentId": i.AgentID,
		"taskId": i.TaskID, "authorizationId": i.AuthorizationID,
	} {
		if !identifierPattern.MatchString(value) {
			return fmt.Errorf("%s is invalid", name)
		}
	}
	for name, value := range map[string]string{"intentDigest": i.IntentDigest, "callId": i.CallID, "taskDigest": i.TaskDigest, "requestDigest": i.RequestDigest} {
		if !hashPattern.MatchString(value) || value == zeroHash {
			return fmt.Errorf("%s must be a canonical non-zero hash", name)
		}
	}
	for name, value := range map[string]string{"contract": i.Contract, "asset": i.Asset, "buyer": i.Buyer, "provider": i.Provider} {
		if !addressPattern.MatchString(value) {
			return fmt.Errorf("%s must be a canonical lowercase EVM address", name)
		}
	}
	if i.Buyer == i.Provider {
		return errors.New("escrow buyer and provider must differ")
	}
	if i.ChainID != 8453 && i.ChainID != 84532 {
		return errors.New("escrow intent supports Base mainnet or Base Sepolia only")
	}
	if _, err := positiveInteger(i.AmountAtomic); err != nil {
		return fmt.Errorf("amountAtomic: %w", err)
	}
	if i.AcknowledgeBy == 0 || i.AcknowledgeBy > math.MaxInt64 || i.DeliverBy <= i.AcknowledgeBy || i.DeliverBy > math.MaxInt64 || i.ReleaseWindow == 0 || i.ReleaseWindow > 30*24*60*60 {
		return errors.New("escrow deadlines or release window are invalid")
	}
	want, err := DeriveEscrowCallID(i.ChainID, i.Contract, i.Buyer, i.TaskDigest, i.RequestDigest)
	if err != nil || want != i.CallID {
		return errors.New("callId does not bind the immutable escrow intent")
	}
	return nil
}

type EscrowTransitionCandidate struct {
	Action            EscrowAction `json:"action"`
	TransactionHash   string       `json:"transactionHash"`
	ResponseDigest    string       `json:"responseDigest,omitempty"`
	EvidenceDigest    string       `json:"evidenceDigest,omitempty"`
	BuyerAccepted     *bool        `json:"buyerAccepted,omitempty"`
	RefundedFromState uint8        `json:"refundedFromState,omitempty"`
}

type EscrowTransition struct {
	Expected                EscrowExpectedReceipt `json:"expected"`
	State                   EscrowTransitionState `json:"state"`
	ReorgedFrom             EscrowTransitionState `json:"reorgedFrom,omitempty"`
	RegisteredAt            time.Time             `json:"registeredAt"`
	ResolvedAt              *time.Time            `json:"resolvedAt,omitempty"`
	BlockNumber             uint64                `json:"blockNumber,omitempty"`
	BlockHash               string                `json:"blockHash,omitempty"`
	Resolution              string                `json:"resolution,omitempty"`
	EvidenceDigest          string                `json:"evidenceDigest,omitempty"`
	LedgerTransactionID     string                `json:"ledgerTransactionId,omitempty"`
	CorrectionTransactionID string                `json:"correctionTransactionId,omitempty"`
	FinalityCheckedAt       *time.Time            `json:"finalityCheckedAt,omitempty"`
	FinalityCheckedHead     uint64                `json:"finalityCheckedHead,omitempty"`
}

type EscrowCall struct {
	Intent       EscrowIntent        `json:"intent"`
	State        EscrowPositionState `json:"state"`
	RegisteredAt time.Time           `json:"registeredAt"`
	Pending      *EscrowTransition   `json:"pending,omitempty"`
	Transitions  []EscrowTransition  `json:"transitions"`
	Resolution   string              `json:"resolution,omitempty"`
}

type escrowPayload struct {
	Call     EscrowCall              `json:"call"`
	Evidence []EscrowReceiptEvidence `json:"evidence,omitempty"`
	Reorg    []ReorgEvidence         `json:"reorg,omitempty"`
	Ledgers  []LedgerTransaction     `json:"ledgers,omitempty"`
	Status   *ChainStatus            `json:"status,omitempty"`
}

func (e *Engine) RegisterEscrowIntent(ctx context.Context, intent EscrowIntent) (EscrowCall, error) {
	if err := intent.Validate(); err != nil {
		return EscrowCall{}, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if intent.ChainID != e.config.ChainID {
		return EscrowCall{}, errors.New("escrow intent chain does not match configured Base chain")
	}
	if e.config.EscrowContract == "" || intent.Contract != e.config.EscrowContract || intent.Asset != e.config.EscrowAsset || intent.ReleaseWindow != e.config.EscrowReleaseWindow {
		return EscrowCall{}, fmt.Errorf("%w: intent does not match the configured reviewed tuple", ErrEscrowDeployment)
	}
	if existing, ok := e.escrowCalls[intent.CallID]; ok {
		if equalJSON(existing.Intent, intent) {
			return cloneEscrowCall(existing), nil
		}
		return EscrowCall{}, ErrConflict
	}
	if other, ok := e.escrowByAuth[intent.AuthorizationID]; ok && other != intent.CallID {
		return EscrowCall{}, ErrConflict
	}
	now := e.config.Clock().UTC()
	if !e.chainUsable(now) {
		return EscrowCall{}, fmt.Errorf("%w: escrow intent must be registered before broadcast while Base is healthy", ErrChainUnavailable)
	}
	if now.Unix() < 0 || uint64(now.Unix()) >= intent.AcknowledgeBy {
		return EscrowCall{}, errors.New("escrow intent acknowledgement deadline elapsed before durable registration")
	}
	call := EscrowCall{Intent: intent, State: EscrowPositionRegistered, RegisteredAt: now, Transitions: []EscrowTransition{}}
	event, err := e.journal.append(ctx, now, eventEscrowIntent, intent.CallID, escrowPayload{Call: call})
	if err != nil {
		return EscrowCall{}, err
	}
	if err := e.apply(event); err != nil {
		return EscrowCall{}, err
	}
	return cloneEscrowCall(call), nil
}

func (e *Engine) RegisterEscrowTransition(ctx context.Context, organizationID, callID string, candidate EscrowTransitionCandidate) (EscrowCall, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	call, ok := e.escrowCalls[callID]
	if !ok || call.Intent.OrganizationID != organizationID {
		return EscrowCall{}, ErrUnknownExecution
	}
	for _, transition := range call.Transitions {
		if transition.Expected.TransactionHash == candidate.TransactionHash {
			if escrowCandidateMatchesExpected(candidate, transition.Expected) {
				return cloneEscrowCall(call), nil
			}
			return EscrowCall{}, ErrConflict
		}
	}
	if len(call.Transitions) > 0 {
		last := call.Transitions[len(call.Transitions)-1]
		if last.State == EscrowTransitionReverted && last.FinalityCheckedAt == nil {
			return EscrowCall{}, fmt.Errorf("%w: reverted receipt remains under canonical watch", ErrEscrowFinality)
		}
	}
	expected, err := expectedEscrowTransition(call, candidate)
	if err != nil {
		return EscrowCall{}, err
	}
	if call.Pending != nil {
		if equalJSON(call.Pending.Expected, expected) {
			return cloneEscrowCall(call), nil
		}
		return EscrowCall{}, ErrConflict
	}
	if other, exists := e.escrowByHash[expected.TransactionHash]; exists && other != callID {
		return EscrowCall{}, ErrConflict
	}
	if _, exists := e.executionByHash[expected.TransactionHash]; exists {
		return EscrowCall{}, ErrConflict
	}
	now := e.config.Clock().UTC()
	state := EscrowTransitionBroadcast
	resolution := "awaiting canonical escrow receipt"
	if !e.chainUsable(now) {
		state = EscrowTransitionPendingRecovery
		resolution = "registered during chain pause; canonical escrow outcome required"
	}
	pending := EscrowTransition{Expected: expected, State: state, RegisteredAt: now, Resolution: resolution}
	call.Pending = &pending
	event, err := e.journal.append(ctx, now, eventEscrowPending, callID, escrowPayload{Call: call})
	if err != nil {
		return EscrowCall{}, err
	}
	if err := e.apply(event); err != nil {
		return EscrowCall{}, err
	}
	e.refreshAffected()
	return cloneEscrowCall(call), nil
}

func escrowCandidateMatchesExpected(candidate EscrowTransitionCandidate, expected EscrowExpectedReceipt) bool {
	return candidate.Action == expected.Action && candidate.TransactionHash == expected.TransactionHash &&
		candidate.ResponseDigest == expected.ResponseDigest && candidate.EvidenceDigest == expected.EvidenceDigest &&
		equalJSON(candidate.BuyerAccepted, expected.BuyerAccepted) && candidate.RefundedFromState == expected.RefundedFromState
}

func expectedEscrowTransition(call EscrowCall, candidate EscrowTransitionCandidate) (EscrowExpectedReceipt, error) {
	if !hashPattern.MatchString(candidate.TransactionHash) || candidate.TransactionHash == zeroHash {
		return EscrowExpectedReceipt{}, fmt.Errorf("%w: transaction hash must be canonical and non-zero", ErrEscrowTransition)
	}
	valid := false
	switch call.State {
	case EscrowPositionRegistered:
		valid = candidate.Action == EscrowFund
	case EscrowPositionFunded:
		valid = candidate.Action == EscrowAcknowledge || candidate.Action == EscrowRefund && candidate.RefundedFromState == 1
	case EscrowPositionAcknowledged:
		valid = candidate.Action == EscrowDeliver || candidate.Action == EscrowRefund && candidate.RefundedFromState == 2
	case EscrowPositionDelivered:
		valid = candidate.Action == EscrowRelease
	}
	if !valid {
		return EscrowExpectedReceipt{}, fmt.Errorf("%w: action is not allowed for the durable position state", ErrEscrowTransition)
	}
	i := call.Intent
	expected := EscrowExpectedReceipt{
		Action: candidate.Action, TransactionHash: candidate.TransactionHash, ChainID: i.ChainID,
		Contract: i.Contract, Asset: i.Asset, CallID: i.CallID, Buyer: i.Buyer, Provider: i.Provider,
		AmountAtomic: i.AmountAtomic, TaskDigest: i.TaskDigest, RequestDigest: i.RequestDigest,
		ResponseDigest: candidate.ResponseDigest, EvidenceDigest: candidate.EvidenceDigest,
		AcknowledgeBy: i.AcknowledgeBy, DeliverBy: i.DeliverBy, ReleaseWindow: i.ReleaseWindow,
		BuyerAccepted: candidate.BuyerAccepted, RefundedFromState: candidate.RefundedFromState,
	}
	if candidate.BuyerAccepted != nil {
		accepted := *candidate.BuyerAccepted
		expected.BuyerAccepted = &accepted
	}
	if err := expected.Validate(); err != nil {
		return EscrowExpectedReceipt{}, fmt.Errorf("%w: %v", ErrEscrowTransition, err)
	}
	return expected, nil
}

func (e *Engine) ReconcileEscrowTransition(ctx context.Context, callID string, evidence []EscrowReceiptEvidence) (EscrowCall, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	call, ok := e.escrowCalls[callID]
	if !ok {
		return EscrowCall{}, ErrUnknownExecution
	}
	now := e.config.Clock().UTC()
	if e.status.FinalizationPaused || e.status.State == StateSuspectedStall || !e.trustedFresh(now) {
		return EscrowCall{}, fmt.Errorf("%w: state=%s", ErrChainUnavailable, e.statusAt(now).State)
	}
	if call.Pending == nil {
		if len(evidence) == 0 {
			return EscrowCall{}, ErrUnknownExecution
		}
		for _, transition := range call.Transitions {
			if transition.Expected.TransactionHash != evidence[0].TransactionHash || transition.State != EscrowTransitionConfirmed && transition.State != EscrowTransitionReverted {
				continue
			}
			canonical, err := e.validateEscrowReceiptQuorum(transition.Expected, evidence)
			if err != nil {
				return EscrowCall{}, err
			}
			if canonical.BlockNumber != transition.BlockNumber || canonical.BlockHash != transition.BlockHash || canonical.Success != (transition.State == EscrowTransitionConfirmed) {
				return EscrowCall{}, ErrConflict
			}
			return cloneEscrowCall(call), nil
		}
		return EscrowCall{}, ErrUnknownExecution
	}
	canonical, err := e.validateEscrowReceiptQuorum(call.Pending.Expected, evidence)
	if err != nil {
		return EscrowCall{}, err
	}
	if len(call.Transitions) > 0 && canonical.BlockNumber < call.Transitions[len(call.Transitions)-1].BlockNumber {
		return EscrowCall{}, ErrUnsafeFinality
	}
	transition := *call.Pending
	transition.ResolvedAt = &now
	transition.BlockNumber = canonical.BlockNumber
	transition.BlockHash = canonical.BlockHash
	transition.EvidenceDigest = digestEscrowEvidence(evidence)
	var ledgers []LedgerTransaction
	if !canonical.Success {
		transition.State = EscrowTransitionReverted
		transition.Resolution = "canonical escrow transaction reverted; position unchanged"
	} else {
		transition.State = EscrowTransitionConfirmed
		transition.Resolution = "canonical escrow transition confirmed"
		call.State = nextEscrowState(transition.Expected.Action)
		if ledger := escrowLedger(call.Intent, transition, now); ledger != nil {
			if existing, exists := e.ledger[ledger.TransactionID]; exists && !equalJSON(existing, *ledger) {
				return EscrowCall{}, ErrConflict
			}
			transition.LedgerTransactionID = ledger.TransactionID
			ledgers = append(ledgers, *ledger)
		}
	}
	call.Transitions = append(call.Transitions, transition)
	call.Pending = nil
	event, err := e.journal.append(ctx, now, eventEscrowResolved, callID, escrowPayload{Call: call, Evidence: cloneEscrowReceiptEvidence(evidence), Ledgers: ledgers})
	if err != nil {
		return EscrowCall{}, err
	}
	if err := e.apply(event); err != nil {
		return EscrowCall{}, err
	}
	e.refreshAffected()
	return cloneEscrowCall(call), nil
}

func (e *Engine) validateEscrowReceiptQuorum(expected EscrowExpectedReceipt, evidence []EscrowReceiptEvidence) (EscrowReceiptEvidence, error) {
	if len(evidence) < e.config.ObserverQuorum || len(evidence) > 5 {
		return EscrowReceiptEvidence{}, ErrUnsafeFinality
	}
	seen := make(map[string]struct{}, len(evidence))
	canonical := evidence[0]
	for _, receipt := range evidence {
		if !identifierPattern.MatchString(receipt.Provider) {
			return EscrowReceiptEvidence{}, ErrUnsafeFinality
		}
		if _, exists := seen[receipt.Provider]; exists {
			return EscrowReceiptEvidence{}, ErrUnsafeFinality
		}
		seen[receipt.Provider] = struct{}{}
		if receipt.Action != expected.Action || receipt.ChainID != expected.ChainID || receipt.TransactionHash != expected.TransactionHash || receipt.CallID != expected.CallID ||
			receipt.BlockNumber == 0 || !hashPattern.MatchString(receipt.BlockHash) || receipt.ConfirmedHead < receipt.BlockNumber || receipt.ConfirmedHead-receipt.BlockNumber+1 < e.config.MinConfirmations {
			return EscrowReceiptEvidence{}, ErrUnsafeFinality
		}
		if receipt.BlockNumber != canonical.BlockNumber || receipt.BlockHash != canonical.BlockHash || receipt.Success != canonical.Success || receipt.DeliveredAt != canonical.DeliveredAt || receipt.ReleasableAt != canonical.ReleasableAt {
			return EscrowReceiptEvidence{}, ErrUnsafeFinality
		}
	}
	if !canonical.Success {
		if canonical.DeliveredAt != 0 || canonical.ReleasableAt != 0 {
			return EscrowReceiptEvidence{}, ErrUnsafeFinality
		}
	} else if expected.Action == EscrowDeliver {
		if canonical.DeliveredAt == 0 || canonical.DeliveredAt > ^uint64(0)-expected.ReleaseWindow || canonical.ReleasableAt != canonical.DeliveredAt+expected.ReleaseWindow {
			return EscrowReceiptEvidence{}, ErrUnsafeFinality
		}
	} else if canonical.DeliveredAt != 0 || canonical.ReleasableAt != 0 {
		return EscrowReceiptEvidence{}, ErrUnsafeFinality
	}
	if e.status.LastTrusted == nil || canonical.BlockNumber > e.status.LastTrusted.BlockNumber || canonical.BlockNumber == e.status.LastTrusted.BlockNumber && canonical.BlockHash != e.status.LastTrusted.BlockHash {
		return EscrowReceiptEvidence{}, ErrUnsafeFinality
	}
	return canonical, nil
}

func nextEscrowState(action EscrowAction) EscrowPositionState {
	switch action {
	case EscrowFund:
		return EscrowPositionFunded
	case EscrowAcknowledge:
		return EscrowPositionAcknowledged
	case EscrowDeliver:
		return EscrowPositionDelivered
	case EscrowRelease:
		return EscrowPositionReleased
	case EscrowRefund:
		return EscrowPositionRefunded
	default:
		return EscrowPositionQuarantined
	}
}

func escrowLedger(intent EscrowIntent, transition EscrowTransition, at time.Time) *LedgerTransaction {
	amount := intent.AmountAtomic
	transaction := LedgerTransaction{
		TransactionID:  derivedLedgerID("escrow", intent.CallID, string(transition.Expected.Action), transition.BlockHash),
		OrganizationID: intent.OrganizationID, ReferenceID: intent.CallID, RecordedAt: at,
	}
	switch transition.Expected.Action {
	case EscrowFund:
		transaction.Kind = LedgerFunding
		transaction.Postings = []Posting{{Account: "escrow_locked", AmountAtomic: amount}, {Account: "pending_settlement", AmountAtomic: "-" + amount}}
	case EscrowRelease:
		transaction.Kind = LedgerSettlement
		transaction.Postings = []Posting{{Account: "agent_service_expense", AmountAtomic: amount}, {Account: "escrow_locked", AmountAtomic: "-" + amount}}
	case EscrowRefund:
		transaction.Kind = LedgerRefund
		transaction.Postings = []Posting{{Account: "pending_settlement", AmountAtomic: amount}, {Account: "escrow_locked", AmountAtomic: "-" + amount}}
	default:
		return nil
	}
	return &transaction
}

func (e *Engine) EscrowCall(organizationID, callID string) (EscrowCall, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	call, ok := e.escrowCalls[callID]
	if !ok || call.Intent.OrganizationID != organizationID {
		return EscrowCall{}, false
	}
	return cloneEscrowCall(call), true
}

func (e *Engine) EscrowCalls() []EscrowCall {
	e.mu.Lock()
	defer e.mu.Unlock()
	result := make([]EscrowCall, 0, len(e.escrowCalls))
	for _, call := range e.escrowCalls {
		result = append(result, cloneEscrowCall(call))
	}
	sort.Slice(result, func(a, b int) bool { return result[a].Intent.CallID < result[b].Intent.CallID })
	return result
}

func (e *Engine) ConfirmEscrowFinality(ctx context.Context, callID, transactionHash string, evidence []ReorgEvidence) (EscrowCall, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	call, transitionIndex, transition, err := e.escrowTransition(callID, transactionHash)
	if err != nil {
		return EscrowCall{}, err
	}
	if transition.State != EscrowTransitionConfirmed && transition.State != EscrowTransitionReverted {
		return EscrowCall{}, errors.New("only a resolved escrow transition can reach finality")
	}
	if transition.FinalityCheckedAt != nil {
		if _, err := e.validateEscrowCanonicalQuorum(transition, evidence, false); err != nil {
			return EscrowCall{}, ErrConflict
		}
		return cloneEscrowCall(call), nil
	}
	now := e.config.Clock().UTC()
	if e.status.FinalizationPaused || !e.trustedFresh(now) {
		return EscrowCall{}, fmt.Errorf("%w: state=%s", ErrChainUnavailable, e.statusAt(now).State)
	}
	canonical, err := e.validateEscrowCanonicalQuorum(transition, evidence, false)
	if err != nil {
		return EscrowCall{}, err
	}
	transition.FinalityCheckedAt = &now
	transition.FinalityCheckedHead = canonical.ObservedHead
	call.Transitions[transitionIndex] = transition
	event, err := e.journal.append(ctx, now, eventEscrowResolved, callID, escrowPayload{Call: call, Reorg: cloneReorgEvidence(evidence)})
	if err != nil {
		return EscrowCall{}, err
	}
	if err := e.apply(event); err != nil {
		return EscrowCall{}, err
	}
	return cloneEscrowCall(call), nil
}

func (e *Engine) ReopenEscrowReorg(ctx context.Context, callID, transactionHash string, evidence []ReorgEvidence) (EscrowCall, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	call, transitionIndex, transition, err := e.escrowTransition(callID, transactionHash)
	if err != nil {
		return EscrowCall{}, err
	}
	if transition.State != EscrowTransitionConfirmed && transition.State != EscrowTransitionReverted {
		return EscrowCall{}, errors.New("only a resolved escrow transition can be reopened")
	}
	now := e.config.Clock().UTC()
	if e.status.FinalizationPaused || !e.trustedFresh(now) {
		return EscrowCall{}, fmt.Errorf("%w: state=%s", ErrChainUnavailable, e.statusAt(now).State)
	}
	canonical, err := e.validateEscrowCanonicalQuorum(transition, evidence, true)
	if err != nil {
		return EscrowCall{}, err
	}
	// A confirmed transition is a state dependency for every later transition,
	// so removing it invalidates the complete suffix. A reverted transition did
	// not change contract state; removing its receipt must quarantine the call,
	// but must not reverse later independently confirmed economic effects.
	reopenEnd := len(call.Transitions)
	if transition.State == EscrowTransitionReverted {
		reopenEnd = transitionIndex + 1
	}
	ledgers := make([]LedgerTransaction, 0, reopenEnd-transitionIndex)
	for index := transitionIndex; index < reopenEnd; index++ {
		item := call.Transitions[index]
		if item.LedgerTransactionID != "" && item.CorrectionTransactionID == "" {
			original, ok := e.ledger[item.LedgerTransactionID]
			if !ok {
				return EscrowCall{}, errors.New("escrow reorg target ledger transaction is missing")
			}
			correction := reverseEscrowLedger(call.Intent, item, original, canonical.CanonicalBlockHash, now)
			if err := correction.validate(); err != nil {
				return EscrowCall{}, err
			}
			if err := e.validateCorrection(correction); err != nil {
				return EscrowCall{}, err
			}
			item.CorrectionTransactionID = correction.TransactionID
			ledgers = append(ledgers, correction)
		}
		item.ReorgedFrom = item.State
		item.State = EscrowTransitionReorged
		item.Resolution = "canonical reorg removed this transition or an earlier dependency"
		call.Transitions[index] = item
	}
	call.Pending = nil
	call.State = EscrowPositionQuarantined
	call.Resolution = "canonical reorg requires explicit operator resolution"
	status := cloneStatus(e.status)
	status.State = StateRecovering
	status.StateChangedAt = now
	status.Reason = "escrow transition reorg corrected; manual escrow resolution and chain release required"
	status.ConsecutiveRecovery = 0
	status.ReadyForManualResume = false
	e.setPauseFlags(&status)
	event, err := e.journal.append(ctx, now, eventEscrowReorged, callID, escrowPayload{Call: call, Reorg: cloneReorgEvidence(evidence), Ledgers: ledgers, Status: &status})
	if err != nil {
		return EscrowCall{}, err
	}
	if err := e.apply(event); err != nil {
		return EscrowCall{}, err
	}
	e.refreshAffected()
	return cloneEscrowCall(call), nil
}

func (e *Engine) escrowTransition(callID, transactionHash string) (EscrowCall, int, EscrowTransition, error) {
	call, ok := e.escrowCalls[callID]
	if !ok {
		return EscrowCall{}, 0, EscrowTransition{}, ErrUnknownExecution
	}
	for index, transition := range call.Transitions {
		if transition.Expected.TransactionHash == transactionHash {
			return cloneEscrowCall(call), index, transition, nil
		}
	}
	return EscrowCall{}, 0, EscrowTransition{}, ErrUnknownExecution
}

func (e *Engine) validateEscrowCanonicalQuorum(transition EscrowTransition, evidence []ReorgEvidence, requireChanged bool) (ReorgEvidence, error) {
	execution := Execution{
		Expected:    ExpectedExecution{ChainID: transition.Expected.ChainID, TransactionHash: transition.Expected.TransactionHash},
		BlockNumber: transition.BlockNumber, BlockHash: transition.BlockHash,
	}
	return e.validateCanonicalBlockQuorum(execution, evidence, requireChanged)
}

func reverseEscrowLedger(intent EscrowIntent, transition EscrowTransition, original LedgerTransaction, canonicalHash string, at time.Time) LedgerTransaction {
	postings := make([]Posting, len(original.Postings))
	for index, posting := range original.Postings {
		amount := posting.AmountAtomic
		if amount[0] == '-' {
			amount = amount[1:]
		} else {
			amount = "-" + amount
		}
		postings[index] = Posting{Account: posting.Account, AmountAtomic: amount}
	}
	return LedgerTransaction{
		TransactionID:  derivedLedgerID("escrow_correction", intent.CallID, transition.Expected.TransactionHash, original.TransactionID, canonicalHash),
		OrganizationID: intent.OrganizationID, Kind: LedgerCorrection, ReferenceID: intent.CallID,
		ReversesTransactionID: original.TransactionID, Postings: postings, RecordedAt: at,
	}
}

func (c EscrowCall) validateSnapshot(chainID uint64) error {
	if err := c.Intent.Validate(); err != nil || c.Intent.ChainID != chainID || c.RegisteredAt.IsZero() {
		return errors.New("durable escrow call identity is invalid")
	}
	switch c.State {
	case EscrowPositionRegistered, EscrowPositionFunded, EscrowPositionAcknowledged, EscrowPositionDelivered, EscrowPositionReleased, EscrowPositionRefunded, EscrowPositionQuarantined:
	default:
		return errors.New("durable escrow position state is invalid")
	}
	seenTransactions := make(map[string]struct{}, len(c.Transitions)+1)
	historicalState := EscrowPositionRegistered
	reorged := false
	confirmedDependencyRemoved := false
	var lastBlock uint64
	for _, transition := range c.Transitions {
		if err := transition.Expected.Validate(); err != nil || transition.RegisteredAt.IsZero() {
			return errors.New("durable escrow transition is invalid")
		}
		if !escrowExpectedMatchesIntent(transition.Expected, c.Intent) || !escrowActionAllowed(historicalState, transition.Expected.Action, transition.Expected.RefundedFromState) {
			return errors.New("durable escrow transition does not follow its immutable call")
		}
		if _, exists := seenTransactions[transition.Expected.TransactionHash]; exists {
			return errors.New("durable escrow transition hash is duplicated")
		}
		seenTransactions[transition.Expected.TransactionHash] = struct{}{}
		switch transition.State {
		case EscrowTransitionConfirmed:
			if confirmedDependencyRemoved || transition.ReorgedFrom != "" {
				return errors.New("confirmed escrow history follows a reorged dependency")
			}
			historicalState = nextEscrowState(transition.Expected.Action)
		case EscrowTransitionReverted:
			if confirmedDependencyRemoved || transition.ReorgedFrom != "" || transition.LedgerTransactionID != "" || transition.CorrectionTransactionID != "" {
				return errors.New("reverted escrow transition contains ledger or dependency state")
			}
		case EscrowTransitionReorged:
			reorged = true
			switch transition.ReorgedFrom {
			case EscrowTransitionConfirmed:
				confirmedDependencyRemoved = true
				// Prove the formerly confirmed suffix's historical ordering
				// without reviving live state.
				historicalState = nextEscrowState(transition.Expected.Action)
			case EscrowTransitionReverted:
				if transition.LedgerTransactionID != "" || transition.CorrectionTransactionID != "" {
					return errors.New("reorged reverted transition contains ledger state")
				}
			default:
				return errors.New("reorged escrow transition lost its prior outcome")
			}
		default:
			return errors.New("resolved escrow history contains a pending state")
		}
		ledgerRequired := transition.Expected.Action == EscrowFund || transition.Expected.Action == EscrowRelease || transition.Expected.Action == EscrowRefund
		if transition.State == EscrowTransitionConfirmed && ledgerRequired != (transition.LedgerTransactionID != "") {
			return errors.New("confirmed escrow transition ledger binding is invalid")
		}
		if transition.State == EscrowTransitionConfirmed && transition.CorrectionTransactionID != "" || transition.State == EscrowTransitionReorged && transition.LedgerTransactionID != "" && transition.CorrectionTransactionID == "" || transition.CorrectionTransactionID != "" && (transition.State != EscrowTransitionReorged || transition.LedgerTransactionID == "") {
			return errors.New("escrow correction binding is invalid")
		}
		if transition.ResolvedAt == nil || transition.BlockNumber == 0 || !hashPattern.MatchString(transition.BlockHash) || !hashPattern.MatchString(transition.EvidenceDigest) {
			return errors.New("resolved escrow transition lacks canonical evidence")
		}
		if transition.BlockNumber < lastBlock {
			return errors.New("escrow transition block chronology regressed")
		}
		lastBlock = transition.BlockNumber
		if transition.RegisteredAt.Before(c.RegisteredAt) || transition.ResolvedAt.Before(transition.RegisteredAt) || transition.FinalityCheckedAt == nil && transition.FinalityCheckedHead != 0 || transition.FinalityCheckedAt != nil && (transition.FinalityCheckedAt.Before(*transition.ResolvedAt) || transition.FinalityCheckedHead < transition.BlockNumber) {
			return errors.New("escrow transition chronology or finality binding is invalid")
		}
	}
	if reorged {
		if c.State != EscrowPositionQuarantined {
			return errors.New("reorged escrow history is not quarantined")
		}
	} else if c.State != historicalState {
		return errors.New("escrow position does not match its confirmed history")
	}
	if c.Pending != nil {
		if err := c.Pending.Expected.Validate(); err != nil || c.Pending.RegisteredAt.IsZero() || c.Pending.ReorgedFrom != "" || c.Pending.State != EscrowTransitionBroadcast && c.Pending.State != EscrowTransitionPendingRecovery {
			return errors.New("durable pending escrow transition is invalid")
		}
		if _, exists := seenTransactions[c.Pending.Expected.TransactionHash]; exists {
			return errors.New("durable pending escrow transition hash is duplicated")
		}
		if reorged || !escrowExpectedMatchesIntent(c.Pending.Expected, c.Intent) || !escrowActionAllowed(historicalState, c.Pending.Expected.Action, c.Pending.Expected.RefundedFromState) {
			return errors.New("pending escrow transition does not follow its immutable call")
		}
		if c.Pending.RegisteredAt.Before(c.RegisteredAt) || c.Pending.ResolvedAt != nil || c.Pending.BlockNumber != 0 || c.Pending.BlockHash != "" || c.Pending.EvidenceDigest != "" || c.Pending.LedgerTransactionID != "" || c.Pending.CorrectionTransactionID != "" || c.Pending.FinalityCheckedAt != nil || c.Pending.FinalityCheckedHead != 0 {
			return errors.New("pending escrow transition contains invented resolution evidence")
		}
	}
	return nil
}

func validateEscrowLedgerBindings(call EscrowCall, additions []LedgerTransaction, existing map[string]LedgerTransaction, eventAt time.Time) error {
	prospective := make(map[string]LedgerTransaction, len(existing)+len(additions))
	for id, transaction := range existing {
		prospective[id] = transaction
	}
	added := make(map[string]struct{}, len(additions))
	for _, transaction := range additions {
		if err := transaction.validate(); err != nil || transaction.OrganizationID != call.Intent.OrganizationID || transaction.ReferenceID != call.Intent.CallID || transaction.RecordedAt != eventAt {
			return errors.New("escrow event contains an invalid ledger transaction")
		}
		if prior, ok := prospective[transaction.TransactionID]; ok && !equalJSON(prior, transaction) {
			return errors.New("escrow event ledger transaction conflicts with durable state")
		}
		if _, duplicate := added[transaction.TransactionID]; duplicate {
			return errors.New("escrow event repeats a ledger transaction")
		}
		prospective[transaction.TransactionID] = transaction
		added[transaction.TransactionID] = struct{}{}
	}
	referenced := make(map[string]struct{}, len(additions))
	for _, transition := range call.Transitions {
		if transition.LedgerTransactionID != "" {
			transaction, ok := prospective[transition.LedgerTransactionID]
			if !ok || transition.ResolvedAt == nil || transaction.RecordedAt != *transition.ResolvedAt {
				return errors.New("escrow transition references a missing ledger transaction")
			}
			want := escrowLedger(call.Intent, transition, transaction.RecordedAt)
			if want == nil || !equalJSON(*want, transaction) {
				return errors.New("escrow transition ledger does not match canonical call economics")
			}
			referenced[transaction.TransactionID] = struct{}{}
		}
		if transition.CorrectionTransactionID != "" {
			correction, ok := prospective[transition.CorrectionTransactionID]
			original, originalOK := prospective[transition.LedgerTransactionID]
			if !ok || !originalOK || correction.Kind != LedgerCorrection || correction.OrganizationID != call.Intent.OrganizationID || correction.ReferenceID != call.Intent.CallID || correction.ReversesTransactionID != original.TransactionID || !inversePostings(original.Postings, correction.Postings) {
				return errors.New("escrow reorg correction does not exactly reverse its canonical ledger")
			}
			referenced[correction.TransactionID] = struct{}{}
		}
	}
	for transactionID := range added {
		if _, ok := referenced[transactionID]; !ok {
			return errors.New("escrow event contains an unreferenced ledger transaction")
		}
	}
	return nil
}

func (e *Engine) validateEscrowEventEvolution(kind string, eventAt time.Time, current EscrowCall, exists bool, payload escrowPayload) error {
	switch kind {
	case eventEscrowIntent:
		if exists || len(payload.Evidence) != 0 || len(payload.Reorg) != 0 || len(payload.Ledgers) != 0 || payload.Status != nil {
			return errors.New("escrow intent event contains transition evidence")
		}
		return nil
	case eventEscrowPending:
		if !exists || current.Pending != nil || payload.Call.Pending == nil || len(payload.Evidence) != 0 || len(payload.Reorg) != 0 || len(payload.Ledgers) != 0 || payload.Status != nil {
			return errors.New("escrow pending event shape is invalid")
		}
		prior := cloneEscrowCall(payload.Call)
		prior.Pending = nil
		if !equalJSON(prior, current) {
			return errors.New("escrow pending event changed state beyond one candidate")
		}
		return nil
	case eventEscrowResolved:
		if !exists || payload.Status != nil {
			return errors.New("escrow resolved event identity is invalid")
		}
		if current.Pending != nil {
			if payload.Call.Pending != nil || len(payload.Call.Transitions) != len(current.Transitions)+1 || len(payload.Evidence) == 0 || len(payload.Reorg) != 0 {
				return errors.New("escrow receipt resolution event shape is invalid")
			}
			prior := cloneEscrowCall(payload.Call)
			resolved := prior.Transitions[len(prior.Transitions)-1]
			prior.Transitions = append([]EscrowTransition(nil), current.Transitions...)
			prior.Pending = current.Pending
			prior.State = current.State
			prior.Resolution = current.Resolution
			if !equalJSON(prior, current) || !equalJSON(resolved.Expected, current.Pending.Expected) || resolved.RegisteredAt != current.Pending.RegisteredAt || resolved.ResolvedAt == nil || *resolved.ResolvedAt != eventAt {
				return errors.New("escrow receipt resolution changed immutable or unrelated state")
			}
			canonical, err := e.validateEscrowReceiptQuorum(resolved.Expected, payload.Evidence)
			if err != nil || resolved.BlockNumber != canonical.BlockNumber || resolved.BlockHash != canonical.BlockHash || resolved.EvidenceDigest != digestEscrowEvidence(payload.Evidence) || canonical.Success != (resolved.State == EscrowTransitionConfirmed) {
				return errors.New("escrow resolved snapshot does not match its quorum evidence")
			}
			return nil
		}
		if payload.Call.Pending != nil || len(payload.Call.Transitions) != len(current.Transitions) || len(payload.Evidence) != 0 || len(payload.Reorg) == 0 || len(payload.Ledgers) != 0 {
			return errors.New("escrow finality event shape is invalid")
		}
		changed := -1
		prior := cloneEscrowCall(payload.Call)
		for index := range prior.Transitions {
			before, after := current.Transitions[index], prior.Transitions[index]
			if equalJSON(before, after) {
				continue
			}
			candidate := after
			candidate.FinalityCheckedAt = before.FinalityCheckedAt
			candidate.FinalityCheckedHead = before.FinalityCheckedHead
			if changed != -1 || !equalJSON(candidate, before) || before.FinalityCheckedAt != nil || after.FinalityCheckedAt == nil || *after.FinalityCheckedAt != eventAt {
				return errors.New("escrow finality event changed more than finality evidence")
			}
			canonical, err := e.validateEscrowCanonicalQuorum(before, payload.Reorg, false)
			if err != nil || after.FinalityCheckedHead != canonical.ObservedHead {
				return errors.New("escrow finality snapshot does not match quorum evidence")
			}
			changed = index
			prior.Transitions[index] = before
		}
		if changed == -1 || !equalJSON(prior, current) {
			return errors.New("escrow finality event has no exact transition delta")
		}
		return nil
	case eventEscrowReorged:
		if !exists || payload.Call.Pending != nil || len(payload.Evidence) != 0 || len(payload.Reorg) == 0 || payload.Status == nil || len(payload.Call.Transitions) != len(current.Transitions) {
			return errors.New("escrow reorg event shape is invalid")
		}
		firstChanged := -1
		firstChangedFrom := EscrowTransitionState("")
		for index := range current.Transitions {
			before, after := current.Transitions[index], payload.Call.Transitions[index]
			if firstChanged == -1 && equalJSON(before, after) {
				continue
			}
			if firstChanged != -1 && equalJSON(before, after) {
				if firstChangedFrom == EscrowTransitionConfirmed {
					return errors.New("escrow reorg left a dependent transition live")
				}
				continue
			}
			if firstChanged == -1 {
				firstChanged = index
				firstChangedFrom = before.State
			} else if firstChangedFrom == EscrowTransitionReverted {
				return errors.New("reorged reverted transition altered an independent suffix")
			}
			if after.State != EscrowTransitionReorged || after.ReorgedFrom != before.State || !equalJSON(after.Expected, before.Expected) || after.RegisteredAt != before.RegisteredAt || !equalJSON(after.ResolvedAt, before.ResolvedAt) || after.BlockNumber != before.BlockNumber || after.BlockHash != before.BlockHash || after.EvidenceDigest != before.EvidenceDigest || !equalJSON(after.FinalityCheckedAt, before.FinalityCheckedAt) || after.FinalityCheckedHead != before.FinalityCheckedHead || after.LedgerTransactionID != before.LedgerTransactionID {
				return errors.New("escrow reorg event altered immutable transition history")
			}
		}
		if firstChanged == -1 {
			return errors.New("escrow reorg event has no transition delta")
		}
		if _, err := e.validateEscrowCanonicalQuorum(current.Transitions[firstChanged], payload.Reorg, true); err != nil {
			return errors.New("escrow reorg snapshot does not match quorum evidence")
		}
		if payload.Call.State != EscrowPositionQuarantined || payload.Status.State != StateRecovering || payload.Status.StateChangedAt != eventAt {
			return errors.New("escrow reorg did not quarantine call and chain state")
		}
		return nil
	default:
		return errors.New("unsupported escrow event evolution")
	}
}

func inversePostings(original, correction []Posting) bool {
	if len(original) != len(correction) {
		return false
	}
	for index := range original {
		if original[index].Account != correction[index].Account {
			return false
		}
		amount := original[index].AmountAtomic
		if amount[0] == '-' {
			amount = amount[1:]
		} else {
			amount = "-" + amount
		}
		if correction[index].AmountAtomic != amount {
			return false
		}
	}
	return true
}

func escrowExpectedMatchesIntent(expected EscrowExpectedReceipt, intent EscrowIntent) bool {
	return expected.ChainID == intent.ChainID && expected.Contract == intent.Contract && expected.Asset == intent.Asset && expected.CallID == intent.CallID &&
		expected.Buyer == intent.Buyer && expected.Provider == intent.Provider && expected.AmountAtomic == intent.AmountAtomic && expected.TaskDigest == intent.TaskDigest &&
		expected.RequestDigest == intent.RequestDigest && expected.AcknowledgeBy == intent.AcknowledgeBy && expected.DeliverBy == intent.DeliverBy && expected.ReleaseWindow == intent.ReleaseWindow
}

func escrowActionAllowed(state EscrowPositionState, action EscrowAction, refundedFrom uint8) bool {
	switch state {
	case EscrowPositionRegistered:
		return action == EscrowFund
	case EscrowPositionFunded:
		return action == EscrowAcknowledge || action == EscrowRefund && refundedFrom == 1
	case EscrowPositionAcknowledged:
		return action == EscrowDeliver || action == EscrowRefund && refundedFrom == 2
	case EscrowPositionDelivered:
		return action == EscrowRelease
	default:
		return false
	}
}

func cloneEscrowCall(call EscrowCall) EscrowCall {
	call.Transitions = append([]EscrowTransition(nil), call.Transitions...)
	for index := range call.Transitions {
		cloneEscrowTransitionTimes(&call.Transitions[index])
	}
	if call.Pending != nil {
		pending := *call.Pending
		cloneEscrowTransitionTimes(&pending)
		call.Pending = &pending
	}
	return call
}

func cloneEscrowTransitionTimes(transition *EscrowTransition) {
	if transition.Expected.BuyerAccepted != nil {
		accepted := *transition.Expected.BuyerAccepted
		transition.Expected.BuyerAccepted = &accepted
	}
	if transition.ResolvedAt != nil {
		value := *transition.ResolvedAt
		transition.ResolvedAt = &value
	}
	if transition.FinalityCheckedAt != nil {
		value := *transition.FinalityCheckedAt
		transition.FinalityCheckedAt = &value
	}
}

func cloneEscrowReceiptEvidence(evidence []EscrowReceiptEvidence) []EscrowReceiptEvidence {
	return append([]EscrowReceiptEvidence(nil), evidence...)
}

func digestEscrowEvidence(evidence []EscrowReceiptEvidence) string {
	copyEvidence := cloneEscrowReceiptEvidence(evidence)
	sort.Slice(copyEvidence, func(i, j int) bool { return copyEvidence[i].Provider < copyEvidence[j].Provider })
	encoded, _ := json.Marshal(copyEvidence)
	digest := sha256.Sum256(encoded)
	return "0x" + hex.EncodeToString(digest[:])
}
