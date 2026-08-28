package main

import (
	"strings"
	"testing"
	"time"

	"github.com/gnanam1990/flowops/internal/controlplane"
	"github.com/gnanam1990/flowops/internal/reconciliation"
)

type outcomeReaderStub struct {
	execution reconciliation.Execution
	ok        bool
}

func (s outcomeReaderStub) Execution(string) (reconciliation.Execution, bool) {
	return s.execution, s.ok
}

func TestReconciliationOutcomeProjectionRequiresCanonicalFinality(t *testing.T) {
	checkedAt := time.Date(2026, 8, 28, 17, 0, 0, 0, time.UTC)
	base := reconciliation.Execution{
		Expected: reconciliation.ExpectedExecution{ExecutionID: "exec_1"}, State: reconciliation.ExecutionSettled,
		BlockNumber: 100, LedgerTransactionID: "settlement_1", FinalityCheckedAt: &checkedAt, FinalityCheckedHead: 120,
		BroadcastAttestation: &reconciliation.BroadcastAttestation{},
	}
	source := reconciliationOutcomeSource{reader: outcomeReaderStub{execution: base, ok: true}}
	if got, ok := source.FinalizedExecution("exec_1"); !ok || got.State != controlplane.CanonicalExecutionSettled || got.FinalizedAt != checkedAt.Unix() {
		t.Fatalf("settled projection = %+v, %v", got, ok)
	}
	reverted := base
	reverted.State, reverted.LedgerTransactionID = reconciliation.ExecutionReverted, ""
	source.reader = outcomeReaderStub{execution: reverted, ok: true}
	if got, ok := source.FinalizedExecution("exec_1"); !ok || got.State != controlplane.CanonicalExecutionReverted {
		t.Fatalf("reverted projection = %+v, %v", got, ok)
	}
	dropped := base
	dropped.State, dropped.LedgerTransactionID, dropped.BlockHash = reconciliation.ExecutionDropped, "", "0x"+strings.Repeat("a", 64)
	dropped.RecoveryResolutionActor = "operator_alice"
	dropped.TransactionRecovery = &reconciliation.TransactionRecovery{
		Nonce: 7, ScanFromBlock: 90, Outcome: reconciliation.RecoveryDropped, ThroughBlock: 100,
		ThroughBlockHash: dropped.BlockHash, AccountNonce: 8, EvidenceDigest: "0x" + strings.Repeat("b", 64), ObservedAt: checkedAt,
	}
	source.reader = outcomeReaderStub{execution: dropped, ok: true}
	if got, ok := source.FinalizedExecution("exec_1"); !ok || got.State != controlplane.CanonicalExecutionDropped {
		t.Fatalf("dropped projection = %+v, %v", got, ok)
	}
	for index, mutate := range []func(*reconciliation.Execution){
		func(e *reconciliation.Execution) { e.TransactionRecovery = nil },
		func(e *reconciliation.Execution) {
			e.TransactionRecovery.Outcome = reconciliation.RecoveryUnknownTransfer
		},
		func(e *reconciliation.Execution) {
			e.TransactionRecovery.ThroughBlockHash = "0x" + strings.Repeat("c", 64)
		},
		func(e *reconciliation.Execution) { e.RecoveryResolutionActor = "" },
		func(e *reconciliation.Execution) { e.ResolvedTransactionHash = "0x" + strings.Repeat("d", 64) },
	} {
		candidate := dropped
		recovery := *dropped.TransactionRecovery
		candidate.TransactionRecovery = &recovery
		mutate(&candidate)
		source.reader = outcomeReaderStub{execution: candidate, ok: true}
		if _, ok := source.FinalizedExecution("exec_1"); ok {
			t.Fatalf("unsafe dropped projection %d was accepted: %+v", index, candidate)
		}
	}
	unsafe := []reconciliation.Execution{base, base, base, base, base}
	unsafe[0].FinalityCheckedAt = nil
	unsafe[1].FinalityCheckedHead = 99
	unsafe[2].LedgerTransactionID = ""
	unsafe[3].Expected.ExecutionID = "exec_other"
	unsafe[4].BroadcastAttestation = nil
	for index, execution := range unsafe {
		source.reader = outcomeReaderStub{execution: execution, ok: true}
		if _, ok := source.FinalizedExecution("exec_1"); ok {
			t.Fatalf("unsafe projection %d was accepted: %+v", index, execution)
		}
	}
}
