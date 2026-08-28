package reconciliation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"
)

var ErrRecoveryBlocked = errors.New("transaction recovery action is not supported by canonical evidence")

type RecoveryAction string

const (
	RecoveryActionProbe    RecoveryAction = "PROBE"
	RecoveryActionFinalize RecoveryAction = "FINALIZE_PROVEN"
)

type RecoveryCoordinator struct {
	source       TransactionOutcomeSource
	receipts     ReceiptAndBlockSource
	engine       *Engine
	queryTimeout time.Duration
	clock        func() time.Time
}

func NewRecoveryCoordinator(source TransactionOutcomeSource, receipts ReceiptAndBlockSource, engine *Engine, queryTimeout time.Duration, clock func() time.Time) (*RecoveryCoordinator, error) {
	if source == nil || receipts == nil || engine == nil || queryTimeout <= 0 {
		return nil, errors.New("recovery coordinator requires observers, engine, and a positive query timeout")
	}
	if clock == nil {
		clock = time.Now
	}
	return &RecoveryCoordinator{source: source, receipts: receipts, engine: engine, queryTimeout: queryTimeout, clock: clock}, nil
}

func (c *RecoveryCoordinator) RecoverForOrganization(ctx context.Context, organizationID, executionID, operator string, action RecoveryAction) (Execution, error) {
	if !identifierPattern.MatchString(organizationID) || !identifierPattern.MatchString(executionID) || !identifierPattern.MatchString(operator) {
		return Execution{}, ErrInvalidOperator
	}
	execution, ok := c.engine.Execution(executionID)
	if !ok || execution.Expected.OrganizationID != organizationID {
		return Execution{}, ErrUnknownExecution
	}
	if execution.BroadcastAttestation == nil || execution.X402SettlementClaim != nil {
		return Execution{}, ErrRecoveryBlocked
	}
	switch action {
	case RecoveryActionProbe:
		if execution.State == ExecutionQuarantined {
			return execution, nil
		}
		if execution.State != ExecutionBroadcast && execution.State != ExecutionPendingChainRecovery {
			return Execution{}, ErrRecoveryBlocked
		}
		status := c.engine.Status()
		if status.LastTrusted == nil || status.FinalizationPaused || status.LastTrusted.BlockNumber <= c.engine.FinalityDepth() {
			return Execution{}, ErrChainUnavailable
		}
		probeBlock := status.LastTrusted.BlockNumber - c.engine.FinalityDepth()
		queryCtx, cancel := context.WithTimeout(ctx, c.queryTimeout)
		result := c.source.TransactionOutcomeQuorum(queryCtx, execution, probeBlock)
		cancel()
		if err := ctx.Err(); err != nil {
			return Execution{}, err
		}
		if len(result.Evidence) == 0 {
			return Execution{}, ErrRecoveryBlocked
		}
		return c.engine.RecordTransactionOutcome(ctx, executionID, result.Evidence)
	case RecoveryActionFinalize:
		if (execution.State == ExecutionDropped || execution.State == ExecutionSettled) && execution.RecoveryResolutionActor == operator {
			return execution, nil
		}
		if execution.State != ExecutionQuarantined || execution.TransactionRecovery == nil {
			return Execution{}, ErrRecoveryBlocked
		}
		switch execution.TransactionRecovery.Outcome {
		case RecoveryDropped:
			return c.engine.ResolveProvenDropForOrganization(ctx, organizationID, executionID, operator)
		case RecoveryExpectedReplacement:
			replacementExpected := execution.Expected
			replacementExpected.TransactionHash = execution.TransactionRecovery.ReplacementTransaction
			queryCtx, cancel := context.WithTimeout(ctx, c.queryTimeout)
			result := c.receipts.ReceiptQuorum(queryCtx, replacementExpected)
			cancel()
			if err := ctx.Err(); err != nil {
				return Execution{}, err
			}
			if len(result.Evidence) == 0 {
				return Execution{}, ErrRecoveryBlocked
			}
			candidate := settlementTransaction(execution, result.Evidence[0], c.clock().UTC())
			return c.engine.ReconcileProvenReplacementForOrganization(ctx, organizationID, executionID, operator, result.Evidence, candidate)
		default:
			return Execution{}, ErrRecoveryBlocked
		}
	default:
		return Execution{}, ErrRecoveryBlocked
	}
}

// RecordTransactionOutcome persists independent transaction identity or a
// terminal nonce outcome. Terminal evidence always enters quarantine first;
// it cannot release a reservation or recognize spend without the separate
// operator recovery transition.
func (e *Engine) RecordTransactionOutcome(ctx context.Context, executionID string, evidence []TransactionOutcomeEvidence) (Execution, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	execution, ok := e.executions[executionID]
	if !ok {
		return Execution{}, ErrUnknownExecution
	}
	canonical, digest, err := e.validateTransactionOutcomeQuorum(execution, evidence)
	if err != nil {
		return Execution{}, err
	}
	if execution.TransactionRecovery != nil && execution.TransactionRecovery.EvidenceDigest == digest {
		if (canonical.Outcome == RecoveryOriginalPending && (execution.State == ExecutionBroadcast || execution.State == ExecutionPendingChainRecovery)) ||
			(canonical.Outcome != RecoveryOriginalPending && execution.State == ExecutionQuarantined) {
			return cloneExecution(execution), nil
		}
	}
	if execution.State != ExecutionBroadcast && execution.State != ExecutionPendingChainRecovery {
		return Execution{}, errors.New("transaction recovery evidence can update only an unresolved execution")
	}
	now := e.config.Clock().UTC()
	updated := cloneExecution(execution)
	if canonical.Outcome == RecoveryOriginalPending {
		if updated.TransactionRecovery != nil && updated.TransactionRecovery.Nonce != canonical.Nonce {
			return Execution{}, ErrConflict
		}
		scanFrom := canonical.ThroughBlock
		if updated.TransactionRecovery != nil {
			scanFrom = updated.TransactionRecovery.ScanFromBlock
		}
		updated.TransactionRecovery = &TransactionRecovery{
			Nonce: canonical.Nonce, ScanFromBlock: scanFrom, Outcome: RecoveryOriginalPending,
			ThroughBlock: canonical.ThroughBlock, ThroughBlockHash: canonical.ThroughBlockHash,
			AccountNonce: canonical.AccountNonce, EvidenceDigest: digest, ObservedAt: now,
		}
		event, err := e.journal.append(ctx, now, eventExecutionBroadcast, executionID, executionPayload{Execution: updated})
		if err != nil {
			return Execution{}, err
		}
		if err := e.apply(event); err != nil {
			return Execution{}, err
		}
		return cloneExecution(updated), nil
	}
	if updated.TransactionRecovery == nil || updated.TransactionRecovery.Nonce != canonical.Nonce || updated.TransactionRecovery.ScanFromBlock == 0 {
		return Execution{}, errors.New("terminal transaction outcome requires a previously bound pending nonce")
	}
	updated.TransactionRecovery = &TransactionRecovery{
		Nonce: canonical.Nonce, ScanFromBlock: updated.TransactionRecovery.ScanFromBlock, Outcome: canonical.Outcome,
		ThroughBlock: canonical.ThroughBlock, ThroughBlockHash: canonical.ThroughBlockHash, AccountNonce: canonical.AccountNonce,
		ReplacementTransaction: canonical.ReplacementTransactionHash, ReplacementRecipient: canonical.ReplacementRecipient,
		ReplacementAmountAtomic: canonical.ReplacementAmountAtomic, ReplacementBlockNumber: canonical.ReplacementBlockNumber,
		ReplacementBlockHash: canonical.ReplacementBlockHash, EvidenceDigest: digest, ObservedAt: now,
	}
	updated.State = ExecutionQuarantined
	updated.ResolvedAt = &now
	switch canonical.Outcome {
	case RecoveryDropped:
		updated.Resolution = "canonical nonce was consumed without an observed transfer of the governed asset; operator finalization required"
	case RecoveryExpectedReplacement:
		updated.Resolution = "canonical nonce was consumed by an exact-content replacement; operator receipt reconciliation required"
	case RecoveryUnknownTransfer:
		updated.Resolution = "canonical nonce was consumed by an unknown governed-asset transfer; independent accounting investigation required"
	default:
		return Execution{}, ErrUnsafeFinality
	}
	event, err := e.journal.append(ctx, now, eventExecutionQuarantine, executionID, executionPayload{Execution: updated})
	if err != nil {
		return Execution{}, err
	}
	if err := e.apply(event); err != nil {
		return Execution{}, err
	}
	e.refreshAffected()
	return cloneExecution(updated), nil
}

func (e *Engine) ResolveProvenDropForOrganization(ctx context.Context, organizationID, executionID, operator string) (Execution, error) {
	if !identifierPattern.MatchString(organizationID) || !identifierPattern.MatchString(operator) {
		return Execution{}, ErrInvalidOperator
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	execution, ok := e.executions[executionID]
	if !ok || execution.Expected.OrganizationID != organizationID {
		return Execution{}, ErrUnknownExecution
	}
	if execution.State == ExecutionDropped {
		if execution.RecoveryResolutionActor == operator {
			return cloneExecution(execution), nil
		}
		return Execution{}, ErrConflict
	}
	if execution.State != ExecutionQuarantined || execution.TransactionRecovery == nil || execution.TransactionRecovery.Outcome != RecoveryDropped {
		return Execution{}, errors.New("execution does not carry a proved dropped outcome")
	}
	now := e.config.Clock().UTC()
	if !e.chainUsable(now) || e.status.LastTrusted == nil || e.status.LastTrusted.BlockNumber < execution.TransactionRecovery.ThroughBlock ||
		e.status.LastTrusted.BlockNumber-execution.TransactionRecovery.ThroughBlock < e.config.ReorgLookback {
		return Execution{}, fmt.Errorf("%w: dropped proof is not under the current trusted checkpoint", ErrChainUnavailable)
	}
	resolved := cloneExecution(execution)
	resolved.State = ExecutionDropped
	resolved.Resolution = "operator finalized canonically displaced transaction with no governed-asset transfer"
	resolved.RecoveryResolutionActor = operator
	resolved.ResolvedAt = &now
	resolved.BlockNumber = resolved.TransactionRecovery.ThroughBlock
	resolved.BlockHash = resolved.TransactionRecovery.ThroughBlockHash
	resolved.ResolvedTransactionHash = execution.Expected.TransactionHash
	resolved.FinalityCheckedAt = &now
	resolved.FinalityCheckedHead = e.status.LastTrusted.BlockNumber
	event, err := e.journal.append(ctx, now, eventExecutionResolved, executionID, executionPayload{Execution: resolved})
	if err != nil {
		return Execution{}, err
	}
	if err := e.apply(event); err != nil {
		return Execution{}, err
	}
	e.refreshAffected()
	return cloneExecution(resolved), nil
}

func (e *Engine) ReconcileProvenReplacementForOrganization(ctx context.Context, organizationID, executionID, operator string, evidence []ReceiptEvidence, settlement LedgerTransaction) (Execution, error) {
	if !identifierPattern.MatchString(organizationID) || !identifierPattern.MatchString(operator) {
		return Execution{}, ErrInvalidOperator
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	execution, ok := e.executions[executionID]
	if !ok || execution.Expected.OrganizationID != organizationID {
		return Execution{}, ErrUnknownExecution
	}
	if execution.State != ExecutionQuarantined || execution.TransactionRecovery == nil || execution.TransactionRecovery.Outcome != RecoveryExpectedReplacement {
		return Execution{}, errors.New("execution does not carry a proved exact-content replacement")
	}
	now := e.config.Clock().UTC()
	if !e.chainUsable(now) {
		return Execution{}, fmt.Errorf("%w: replacement cannot reconcile while chain state is unavailable", ErrChainUnavailable)
	}
	replacementExpected := execution.Expected
	replacementExpected.TransactionHash = execution.TransactionRecovery.ReplacementTransaction
	canonical, err := e.validateReceiptQuorum(replacementExpected, evidence)
	if err != nil || !canonical.Success {
		return Execution{}, ErrUnsafeFinality
	}
	if settlement.Kind != LedgerSettlement || settlement.ReferenceID != executionID || settlement.OrganizationID != organizationID {
		return Execution{}, errors.New("replacement settlement ledger transaction is not bound to the execution")
	}
	if err := settlement.validate(); err != nil {
		return Execution{}, err
	}
	if existing, exists := e.ledger[settlement.TransactionID]; exists && !equalJSON(existing, settlement) {
		return Execution{}, ErrConflict
	}
	resolved := cloneExecution(execution)
	resolved.State = ExecutionSettled
	resolved.Resolution = "operator reconciled canonically mined exact-content replacement"
	resolved.RecoveryResolutionActor = operator
	resolved.ResolvedTransactionHash = replacementExpected.TransactionHash
	resolved.ResolvedAt = &now
	resolved.BlockNumber = canonical.BlockNumber
	resolved.BlockHash = canonical.BlockHash
	resolved.LedgerTransactionID = settlement.TransactionID
	resolved.FinalityCheckedAt = nil
	resolved.FinalityCheckedHead = 0
	ledgerCopy := cloneLedger(settlement)
	event, err := e.journal.append(ctx, now, eventExecutionResolved, executionID, executionPayload{Execution: resolved, Evidence: cloneEvidence(evidence), Ledger: &ledgerCopy})
	if err != nil {
		return Execution{}, err
	}
	if err := e.apply(event); err != nil {
		return Execution{}, err
	}
	e.refreshAffected()
	return cloneExecution(resolved), nil
}

func (e *Engine) validateTransactionOutcomeQuorum(execution Execution, evidence []TransactionOutcomeEvidence) (TransactionOutcomeEvidence, string, error) {
	if len(evidence) < e.config.ObserverQuorum || len(evidence) > 5 || e.status.LastTrusted == nil {
		return TransactionOutcomeEvidence{}, "", ErrUnsafeFinality
	}
	copyEvidence := append([]TransactionOutcomeEvidence(nil), evidence...)
	sort.Slice(copyEvidence, func(i, j int) bool { return copyEvidence[i].Provider < copyEvidence[j].Provider })
	seen := make(map[string]struct{}, len(copyEvidence))
	canonical := copyEvidence[0]
	for _, observation := range copyEvidence {
		if !identifierPattern.MatchString(observation.Provider) {
			return TransactionOutcomeEvidence{}, "", ErrUnsafeFinality
		}
		if _, exists := seen[observation.Provider]; exists {
			return TransactionOutcomeEvidence{}, "", ErrUnsafeFinality
		}
		seen[observation.Provider] = struct{}{}
		if observation.ChainID != execution.Expected.ChainID || observation.OriginalTransactionHash != execution.Expected.TransactionHash ||
			observation.Outcome != canonical.Outcome || observation.Nonce != canonical.Nonce || observation.ThroughBlock != canonical.ThroughBlock ||
			observation.ThroughBlockHash != canonical.ThroughBlockHash || observation.AccountNonce != canonical.AccountNonce ||
			observation.ReplacementTransactionHash != canonical.ReplacementTransactionHash || observation.ReplacementRecipient != canonical.ReplacementRecipient ||
			observation.ReplacementAmountAtomic != canonical.ReplacementAmountAtomic || observation.ReplacementBlockNumber != canonical.ReplacementBlockNumber ||
			observation.ReplacementBlockHash != canonical.ReplacementBlockHash || !hashPattern.MatchString(observation.ThroughBlockHash) {
			return TransactionOutcomeEvidence{}, "", ErrUnsafeFinality
		}
	}
	if canonical.ThroughBlock > e.status.LastTrusted.BlockNumber || e.status.LastTrusted.BlockNumber-canonical.ThroughBlock < e.config.ReorgLookback {
		return TransactionOutcomeEvidence{}, "", ErrUnsafeFinality
	}
	switch canonical.Outcome {
	case RecoveryOriginalPending:
		if canonical.AccountNonce > canonical.Nonce || canonical.ReplacementTransactionHash != "" || canonical.ReplacementRecipient != "" || canonical.ReplacementAmountAtomic != "" || canonical.ReplacementBlockNumber != 0 || canonical.ReplacementBlockHash != "" {
			return TransactionOutcomeEvidence{}, "", ErrUnsafeFinality
		}
	case RecoveryDropped:
		if execution.TransactionRecovery == nil || canonical.AccountNonce <= canonical.Nonce || canonical.ReplacementTransactionHash != "" || canonical.ReplacementRecipient != "" || canonical.ReplacementAmountAtomic != "" || canonical.ReplacementBlockNumber != 0 || canonical.ReplacementBlockHash != "" {
			return TransactionOutcomeEvidence{}, "", ErrUnsafeFinality
		}
	case RecoveryExpectedReplacement:
		if execution.TransactionRecovery == nil || canonical.AccountNonce <= canonical.Nonce || !hashPattern.MatchString(canonical.ReplacementTransactionHash) || canonical.ReplacementTransactionHash == execution.Expected.TransactionHash ||
			canonical.ReplacementRecipient != execution.Expected.Recipient || canonical.ReplacementAmountAtomic != execution.Expected.AmountAtomic || canonical.ReplacementBlockNumber == 0 || canonical.ReplacementBlockNumber > canonical.ThroughBlock || !hashPattern.MatchString(canonical.ReplacementBlockHash) {
			return TransactionOutcomeEvidence{}, "", ErrUnsafeFinality
		}
	case RecoveryUnknownTransfer:
		if execution.TransactionRecovery == nil || canonical.AccountNonce <= canonical.Nonce || !hashPattern.MatchString(canonical.ReplacementTransactionHash) || canonical.ReplacementTransactionHash == execution.Expected.TransactionHash ||
			!addressPattern.MatchString(canonical.ReplacementRecipient) || canonical.ReplacementBlockNumber == 0 || canonical.ReplacementBlockNumber > canonical.ThroughBlock || !hashPattern.MatchString(canonical.ReplacementBlockHash) {
			return TransactionOutcomeEvidence{}, "", ErrUnsafeFinality
		}
		if _, err := positiveInteger(canonical.ReplacementAmountAtomic); err != nil {
			return TransactionOutcomeEvidence{}, "", ErrUnsafeFinality
		}
		if canonical.ReplacementRecipient == execution.Expected.Recipient && canonical.ReplacementAmountAtomic == execution.Expected.AmountAtomic {
			return TransactionOutcomeEvidence{}, "", ErrUnsafeFinality
		}
	default:
		return TransactionOutcomeEvidence{}, "", ErrUnsafeFinality
	}
	raw, err := json.Marshal(copyEvidence)
	if err != nil {
		return TransactionOutcomeEvidence{}, "", err
	}
	digest := sha256.Sum256(raw)
	return canonical, "0x" + hex.EncodeToString(digest[:]), nil
}
