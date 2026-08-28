package main

import (
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
