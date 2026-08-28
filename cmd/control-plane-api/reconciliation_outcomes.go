package main

import (
	"github.com/gnanam1990/flowops/internal/controlplane"
	"github.com/gnanam1990/flowops/internal/reconciliation"
)

type reconciliationExecutionReader interface {
	Execution(executionID string) (reconciliation.Execution, bool)
}

// reconciliationOutcomeSource projects only canonically finalized outcomes
// into budget evaluation. Missing, unresolved, quarantined, or malformed state
// remains a conservative reservation.
type reconciliationOutcomeSource struct {
	reader reconciliationExecutionReader
}

func (s reconciliationOutcomeSource) FinalizedExecution(executionID string) (controlplane.FinalizedExecution, bool) {
	if s.reader == nil {
		return controlplane.FinalizedExecution{}, false
	}
	execution, ok := s.reader.Execution(executionID)
	if !ok || execution.Expected.ExecutionID != executionID || execution.FinalityCheckedAt == nil || execution.FinalityCheckedHead < execution.BlockNumber ||
		(execution.BroadcastAttestation == nil) == (execution.X402SettlementClaim == nil) {
		return controlplane.FinalizedExecution{}, false
	}
	state := controlplane.CanonicalExecutionSettled
	switch execution.State {
	case reconciliation.ExecutionSettled:
		if execution.LedgerTransactionID == "" {
			return controlplane.FinalizedExecution{}, false
		}
	case reconciliation.ExecutionReverted:
		if execution.LedgerTransactionID != "" {
			return controlplane.FinalizedExecution{}, false
		}
		state = controlplane.CanonicalExecutionReverted
	case reconciliation.ExecutionDropped:
		if execution.LedgerTransactionID != "" || (execution.ResolvedTransactionHash != "" && execution.ResolvedTransactionHash != execution.Expected.TransactionHash) || execution.TransactionRecovery == nil ||
			execution.TransactionRecovery.Outcome != reconciliation.RecoveryDropped || execution.TransactionRecovery.EvidenceDigest == "" || execution.RecoveryResolutionActor == "" ||
			execution.BlockNumber != execution.TransactionRecovery.ThroughBlock || execution.BlockHash != execution.TransactionRecovery.ThroughBlockHash {
			return controlplane.FinalizedExecution{}, false
		}
		state = controlplane.CanonicalExecutionDropped
	default:
		return controlplane.FinalizedExecution{}, false
	}
	return controlplane.FinalizedExecution{
		ExecutionID: execution.Expected.ExecutionID, OrganizationID: execution.Expected.OrganizationID,
		AgentID: execution.Expected.AgentID, TaskID: execution.Expected.TaskID, ChainID: execution.Expected.ChainID,
		Asset: execution.Expected.Asset, Recipient: execution.Expected.Recipient, AmountAtomic: execution.Expected.AmountAtomic,
		State: state, FinalizedAt: execution.FinalityCheckedAt.Unix(),
	}, true
}
