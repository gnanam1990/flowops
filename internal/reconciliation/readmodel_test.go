package reconciliation

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestOrganizationViewSeparatesProvedAssetAggregatesAndExceptions(t *testing.T) {
	clock := &testClock{now: time.Date(2026, 8, 15, 9, 0, 0, 0, time.UTC)}
	engine, err := Open(filepath.Join(t.TempDir(), "reconciliation.log"), testConfig(clock))
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	bootstrapHealthy(t, engine, clock, 100)

	unresolved := testExpected()
	if _, err := engine.RegisterBroadcast(context.Background(), unresolved); err != nil {
		t.Fatal(err)
	}
	view := engine.OrganizationView(unresolved.OrganizationID)
	if !view.Available || view.Recovery.TotalCandidates != 1 || view.Recovery.UnresolvedOutcomes != 1 || len(view.Exceptions) != 1 || len(view.Assets) != 1 || view.Assets[0].UnresolvedAtomic != "100" {
		t.Fatalf("unresolved view = %+v", view)
	}
	if view.Exceptions[0].OperatorActionNeeded {
		t.Fatal("healthy broadcast was presented as requiring manual quarantine")
	}

	if _, err := engine.ForceHalt(context.Background(), "operator_alice", "test containment"); err != nil {
		t.Fatal(err)
	}
	view = engine.OrganizationView(unresolved.OrganizationID)
	if !view.Exceptions[0].OperatorActionNeeded || view.Exceptions[0].State != string(ExecutionPendingChainRecovery) {
		t.Fatalf("halted exception = %+v", view.Exceptions[0])
	}
	if _, err := engine.QuarantineForOrganization(context.Background(), "org_other", unresolved.ExecutionID, "operator_alice", "DROPPED_UNPROVEN: nonce outcome not independently proved"); err != ErrUnknownExecution {
		t.Fatalf("cross-tenant quarantine = %v", err)
	}
	if _, err := engine.QuarantineForOrganization(context.Background(), unresolved.OrganizationID, unresolved.ExecutionID, "operator_alice", "DROPPED_UNPROVEN: nonce outcome not independently proved"); err != nil {
		t.Fatal(err)
	}
	view = engine.OrganizationView(unresolved.OrganizationID)
	if view.Recovery.UnresolvedOutcomes != 0 || view.Recovery.QuarantinedOutcomes != 1 || view.Exceptions[0].State != string(ExecutionQuarantined) || view.Assets[0].UnresolvedAtomic != "0" {
		t.Fatalf("quarantined view = %+v", view)
	}

	clock.Add(time.Second)
	if _, err := engine.Observe(context.Background(), healthyObservations(clock.Now(), 102, 103)); err != nil {
		t.Fatal(err)
	}
	clock.Add(time.Second)
	status, err := engine.Observe(context.Background(), healthyObservations(clock.Now(), 103, 104))
	if err != nil || !status.ReadyForManualResume {
		t.Fatalf("recovery status = %+v err=%v", status, err)
	}
	if _, err := engine.Resume(context.Background(), "operator_alice"); err != nil {
		t.Fatal(err)
	}
	settledExpected := testExpected()
	settledExpected.ExecutionID = "exec_settled"
	settledExpected.TransactionHash = testHash(990)
	if _, err := engine.RegisterBroadcast(context.Background(), settledExpected); err != nil {
		t.Fatal(err)
	}
	ledger := settlement(clock.Now(), settledExpected.ExecutionID)
	if _, err := engine.ReconcileReceipt(context.Background(), settledExpected.ExecutionID, receiptQuorum(settledExpected, 103, true), &ledger); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Post(context.Background(), LedgerTransaction{
		TransactionID: "reservation_unbound", OrganizationID: unresolved.OrganizationID,
		Kind: LedgerReservation, ReferenceID: "external_budget", RecordedAt: clock.Now(),
		Postings: []Posting{{Account: "reserved_agent_usdc", AmountAtomic: "25"}, {Account: "agent_usdc", AmountAtomic: "-25"}},
	}); err != nil {
		t.Fatal(err)
	}
	view = engine.OrganizationView(unresolved.OrganizationID)
	if len(view.Assets) != 1 || view.Assets[0].RecognizedExpenseAtomic != "100" || view.Assets[0].SpentTodayAtomic != "100" || view.Assets[0].SpentMonthAtomic != "100" || view.Recovery.PendingFinality != 1 || view.Unclassified != 1 {
		t.Fatalf("settled aggregate = %+v", view)
	}
	if other := engine.OrganizationView("org_other"); len(other.Assets) != 0 || len(other.Exceptions) != 0 || other.Unclassified != 0 {
		t.Fatalf("tenant boundary leaked = %+v", other)
	} else if other.Assets == nil || other.Exceptions == nil {
		t.Fatalf("empty tenant projection must encode arrays, got %+v", other)
	}
}
