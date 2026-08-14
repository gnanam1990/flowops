package reconciliation

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestDurableEscrowReleaseLifecyclePostsCanonicalLedgerAndSurvivesRestart(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 14, 10, 0, 0, 0, time.UTC)
	clock := &testClock{now: now}
	path := filepath.Join(t.TempDir(), "reconciliation.log")
	engine, err := Open(path, testConfig(clock))
	if err != nil {
		t.Fatal(err)
	}
	bootstrapHealthy(t, engine, clock, 300)
	intent := durableEscrowIntent(t)
	call, err := engine.RegisterEscrowIntent(context.Background(), intent)
	if err != nil || call.State != EscrowPositionRegistered {
		t.Fatalf("RegisterEscrowIntent() = %+v, %v", call, err)
	}
	if replay, err := engine.RegisterEscrowIntent(context.Background(), intent); err != nil || replay.RegisteredAt != call.RegisteredAt {
		t.Fatalf("idempotent intent = %+v, %v", replay, err)
	}

	call = reconcileEscrowCandidate(t, engine, intent, EscrowTransitionCandidate{Action: EscrowFund, TransactionHash: testHash(201)}, 201)
	if call.State != EscrowPositionFunded || engine.Balance(intent.OrganizationID, "escrow_locked") != intent.AmountAtomic || engine.Balance(intent.OrganizationID, "pending_settlement") != "-100" {
		t.Fatalf("funded call = %+v balances=%s/%s", call, engine.Balance(intent.OrganizationID, "escrow_locked"), engine.Balance(intent.OrganizationID, "pending_settlement"))
	}
	if replay, err := engine.ReconcileEscrowTransition(context.Background(), intent.CallID, durableEscrowEvidence(call.Transitions[0].Expected, 201)); err != nil || len(replay.Transitions) != 1 || engine.Balance(intent.OrganizationID, "escrow_locked") != "100" {
		t.Fatalf("idempotent funding reconciliation = %+v balance=%s err=%v", replay, engine.Balance(intent.OrganizationID, "escrow_locked"), err)
	}
	call = reconcileEscrowCandidate(t, engine, intent, EscrowTransitionCandidate{Action: EscrowAcknowledge, TransactionHash: testHash(202)}, 202)
	if call.State != EscrowPositionAcknowledged {
		t.Fatalf("acknowledged call = %+v", call)
	}
	call = reconcileEscrowCandidate(t, engine, intent, EscrowTransitionCandidate{
		Action: EscrowDeliver, TransactionHash: testHash(203), ResponseDigest: testHash(701), EvidenceDigest: testHash(702),
	}, 203)
	if call.State != EscrowPositionDelivered {
		t.Fatalf("delivered call = %+v", call)
	}
	accepted := true
	call, err = engine.RegisterEscrowTransition(context.Background(), intent.OrganizationID, intent.CallID, EscrowTransitionCandidate{Action: EscrowRelease, TransactionHash: testHash(204), BuyerAccepted: &accepted})
	if err != nil || call.Pending == nil {
		t.Fatalf("release candidate = %+v, %v", call, err)
	}
	accepted = false
	stored, _ := engine.EscrowCall(intent.OrganizationID, intent.CallID)
	if stored.Pending == nil || stored.Pending.Expected.BuyerAccepted == nil || !*stored.Pending.Expected.BuyerAccepted {
		t.Fatal("caller pointer mutation changed the durable release authority")
	}
	call, err = engine.ReconcileEscrowTransition(context.Background(), intent.CallID, durableEscrowEvidence(stored.Pending.Expected, 204))
	if err != nil {
		t.Fatal(err)
	}
	if call.State != EscrowPositionReleased || engine.Balance(intent.OrganizationID, "escrow_locked") != "0" || engine.Balance(intent.OrganizationID, "agent_service_expense") != "100" {
		t.Fatalf("released call = %+v locked=%s expense=%s", call, engine.Balance(intent.OrganizationID, "escrow_locked"), engine.Balance(intent.OrganizationID, "agent_service_expense"))
	}
	if len(call.Transitions) != 4 || call.Pending != nil {
		t.Fatalf("release history = %+v", call)
	}
	if _, err := engine.RegisterEscrowTransition(context.Background(), intent.OrganizationID, intent.CallID, EscrowTransitionCandidate{Action: EscrowRefund, TransactionHash: testHash(205), RefundedFromState: 2}); err == nil {
		t.Fatal("terminal escrow call accepted another transition")
	}
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
	wrongConfig := testConfig(clock)
	wrongConfig.EscrowReleaseWindow = 7200
	if mismatched, err := Open(path, wrongConfig); err == nil {
		_ = mismatched.Close()
		t.Fatal("restart accepted a journal from a different escrow deployment tuple")
	}

	restarted, err := Open(path, testConfig(clock))
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	replayed, ok := restarted.EscrowCall(intent.OrganizationID, intent.CallID)
	if !ok || replayed.State != EscrowPositionReleased || len(replayed.Transitions) != 4 || restarted.Balance(intent.OrganizationID, "agent_service_expense") != "100" {
		t.Fatalf("restart replay = %+v ok=%v balance=%s", replayed, ok, restarted.Balance(intent.OrganizationID, "agent_service_expense"))
	}
}

func TestDurableEscrowRefundReversesLockedExposureWithoutInventingExpense(t *testing.T) {
	t.Parallel()
	clock := &testClock{now: time.Date(2026, 8, 14, 11, 0, 0, 0, time.UTC)}
	engine, err := Open(filepath.Join(t.TempDir(), "reconciliation.log"), testConfig(clock))
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	bootstrapHealthy(t, engine, clock, 300)
	intent := durableEscrowIntent(t)
	if _, err := engine.RegisterEscrowIntent(context.Background(), intent); err != nil {
		t.Fatal(err)
	}
	reconcileEscrowCandidate(t, engine, intent, EscrowTransitionCandidate{Action: EscrowFund, TransactionHash: testHash(211)}, 211)
	call := reconcileEscrowCandidate(t, engine, intent, EscrowTransitionCandidate{Action: EscrowRefund, TransactionHash: testHash(212), RefundedFromState: 1}, 212)
	if call.State != EscrowPositionRefunded || engine.Balance(intent.OrganizationID, "escrow_locked") != "0" || engine.Balance(intent.OrganizationID, "pending_settlement") != "0" || engine.Balance(intent.OrganizationID, "agent_service_expense") != "0" {
		t.Fatalf("refund call = %+v balances=%s/%s/%s", call, engine.Balance(intent.OrganizationID, "escrow_locked"), engine.Balance(intent.OrganizationID, "pending_settlement"), engine.Balance(intent.OrganizationID, "agent_service_expense"))
	}
}

func TestDurableEscrowRejectsSubstitutionReplayAndTransitionDuringUnhealthyRegistration(t *testing.T) {
	t.Parallel()
	clock := &testClock{now: time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)}
	engine, err := Open(filepath.Join(t.TempDir(), "reconciliation.log"), testConfig(clock))
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	intent := durableEscrowIntent(t)
	if _, err := engine.RegisterEscrowIntent(context.Background(), intent); !errors.Is(err, ErrChainUnavailable) {
		t.Fatalf("intent registration before healthy error = %v", err)
	}
	bootstrapHealthy(t, engine, clock, 300)
	wrongDeployment := intent
	wrongDeployment.ReleaseWindow = 7200
	if _, err := engine.RegisterEscrowIntent(context.Background(), wrongDeployment); !errors.Is(err, ErrEscrowDeployment) {
		t.Fatalf("unconfigured deployment error = %v", err)
	}
	expired := intent
	expired.AcknowledgeBy = uint64(clock.Now().Add(-time.Second).Unix())
	expired.DeliverBy = expired.AcknowledgeBy + 3600
	if _, err := engine.RegisterEscrowIntent(context.Background(), expired); err == nil {
		t.Fatal("escrow intent was durably registered after its acknowledgement deadline")
	}
	if _, err := engine.RegisterEscrowIntent(context.Background(), intent); err != nil {
		t.Fatal(err)
	}
	wrong := intent
	wrong.Provider = testHashAddress(99)
	if _, err := engine.RegisterEscrowIntent(context.Background(), wrong); !errors.Is(err, ErrConflict) {
		t.Fatalf("substituted intent error = %v", err)
	}
	if _, err := engine.RegisterEscrowTransition(context.Background(), intent.OrganizationID, intent.CallID, EscrowTransitionCandidate{Action: EscrowDeliver, TransactionHash: testHash(221), ResponseDigest: testHash(1), EvidenceDigest: testHash(2)}); err == nil {
		t.Fatal("delivery was accepted before funding")
	}
	call, err := engine.RegisterEscrowTransition(context.Background(), intent.OrganizationID, intent.CallID, EscrowTransitionCandidate{Action: EscrowFund, TransactionHash: testHash(222)})
	if err != nil || call.Pending == nil {
		t.Fatalf("fund candidate = %+v, %v", call, err)
	}
	badEvidence := durableEscrowEvidence(call.Pending.Expected, 222)
	badEvidence[1].CallID = testHash(999)
	if _, err := engine.ReconcileEscrowTransition(context.Background(), intent.CallID, badEvidence); !errors.Is(err, ErrUnsafeFinality) {
		t.Fatalf("substituted receipt error = %v", err)
	}
	stillPending, _ := engine.EscrowCall(intent.OrganizationID, intent.CallID)
	if stillPending.Pending == nil || stillPending.State != EscrowPositionRegistered || engine.Balance(intent.OrganizationID, "escrow_locked") != "0" {
		t.Fatalf("unsafe evidence mutated call = %+v", stillPending)
	}
}

func TestDurableEscrowPendingTransitionBecomesChainRecoveryAndCannotFinalizeDuringHalt(t *testing.T) {
	t.Parallel()
	clock := &testClock{now: time.Date(2026, 8, 14, 13, 0, 0, 0, time.UTC)}
	path := filepath.Join(t.TempDir(), "reconciliation.log")
	engine, err := Open(path, testConfig(clock))
	if err != nil {
		t.Fatal(err)
	}
	bootstrapHealthy(t, engine, clock, 300)
	intent := durableEscrowIntent(t)
	if _, err := engine.RegisterEscrowIntent(context.Background(), intent); err != nil {
		t.Fatal(err)
	}
	call, err := engine.RegisterEscrowTransition(context.Background(), intent.OrganizationID, intent.CallID, EscrowTransitionCandidate{Action: EscrowFund, TransactionHash: testHash(231)})
	if err != nil {
		t.Fatal(err)
	}
	clock.Add(time.Second)
	stale := healthyObservations(clock.Now(), 301, 302)
	for index := range stale {
		stale[index].HeadTime = clock.Now().Add(-10 * time.Minute)
	}
	if _, err := engine.Observe(context.Background(), stale); err != nil {
		t.Fatal(err)
	}
	clock.Add(time.Second)
	status, err := engine.Observe(context.Background(), stale)
	if err != nil || status.State != StateHalted || status.AffectedExecutions != 1 {
		t.Fatalf("halt = %+v, %v", status, err)
	}
	call, _ = engine.EscrowCall(intent.OrganizationID, intent.CallID)
	if call.Pending == nil || call.Pending.State != EscrowTransitionPendingRecovery {
		t.Fatalf("halted escrow candidate = %+v", call)
	}
	if _, err := engine.ReconcileEscrowTransition(context.Background(), intent.CallID, durableEscrowEvidence(call.Pending.Expected, 231)); !errors.Is(err, ErrChainUnavailable) {
		t.Fatalf("halted reconciliation error = %v", err)
	}
	if engine.Balance(intent.OrganizationID, "escrow_locked") != "0" {
		t.Fatal("halted transition invented locked funds")
	}
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := Open(path, testConfig(clock))
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	call, _ = restarted.EscrowCall(intent.OrganizationID, intent.CallID)
	if call.Pending == nil || call.Pending.State != EscrowTransitionPendingRecovery || restarted.Status().AffectedExecutions != 1 {
		t.Fatalf("restart lost ambiguous escrow transition: call=%+v status=%+v", call, restarted.Status())
	}
}

func TestDurableEscrowReorgReversesEveryDependentLedgerAndQuarantinesPosition(t *testing.T) {
	t.Parallel()
	clock := &testClock{now: time.Date(2026, 8, 14, 14, 0, 0, 0, time.UTC)}
	engine, err := Open(filepath.Join(t.TempDir(), "reconciliation.log"), testConfig(clock))
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	bootstrapHealthy(t, engine, clock, 300)
	intent := durableEscrowIntent(t)
	if _, err := engine.RegisterEscrowIntent(context.Background(), intent); err != nil {
		t.Fatal(err)
	}
	reconcileEscrowCandidate(t, engine, intent, EscrowTransitionCandidate{Action: EscrowFund, TransactionHash: testHash(241)}, 241)
	revertedAck, err := engine.RegisterEscrowTransition(context.Background(), intent.OrganizationID, intent.CallID, EscrowTransitionCandidate{Action: EscrowAcknowledge, TransactionHash: testHash(2402)})
	if err != nil {
		t.Fatal(err)
	}
	revertedEvidence := durableEscrowEvidence(revertedAck.Pending.Expected, 242)
	for index := range revertedEvidence {
		revertedEvidence[index].Success = false
	}
	revertedAck, err = engine.ReconcileEscrowTransition(context.Background(), intent.CallID, revertedEvidence)
	if err != nil || revertedAck.Transitions[1].State != EscrowTransitionReverted {
		t.Fatalf("reverted acknowledgement = %+v, %v", revertedAck, err)
	}
	revertedTransition := revertedAck.Transitions[1]
	if _, err := engine.ConfirmEscrowFinality(context.Background(), intent.CallID, revertedTransition.Expected.TransactionHash, durableEscrowReorgEvidence(revertedTransition, revertedTransition.BlockHash, 310)); err != nil {
		t.Fatal(err)
	}
	reconcileEscrowCandidate(t, engine, intent, EscrowTransitionCandidate{Action: EscrowAcknowledge, TransactionHash: testHash(242)}, 243)
	reconcileEscrowCandidate(t, engine, intent, EscrowTransitionCandidate{Action: EscrowDeliver, TransactionHash: testHash(243), ResponseDigest: testHash(801), EvidenceDigest: testHash(802)}, 244)
	accepted := true
	call := reconcileEscrowCandidate(t, engine, intent, EscrowTransitionCandidate{Action: EscrowRelease, TransactionHash: testHash(244), BuyerAccepted: &accepted}, 245)
	if engine.Balance(intent.OrganizationID, "agent_service_expense") != "100" || engine.Balance(intent.OrganizationID, "pending_settlement") != "-100" {
		t.Fatal("release setup ledger is wrong")
	}
	fund := call.Transitions[0]
	changed := testHash(999)
	engine.mu.Lock()
	engine.status.State = StateRecovering
	engine.status.ConsecutiveRecovery = engine.config.RecoveryObservations
	engine.status.ReadyForManualResume = true
	engine.setPauseFlags(&engine.status)
	engine.mu.Unlock()
	reorged, err := engine.ReopenEscrowReorg(context.Background(), intent.CallID, fund.Expected.TransactionHash, durableEscrowReorgEvidence(fund, changed, 310))
	if err != nil {
		t.Fatal(err)
	}
	status := engine.Status()
	if reorged.State != EscrowPositionQuarantined || len(reorged.Transitions) != 5 || status.State != StateRecovering || status.ConsecutiveRecovery != 0 || status.ReadyForManualResume {
		t.Fatalf("reorged call = %+v status=%+v", reorged, engine.Status())
	}
	for _, transition := range reorged.Transitions {
		if transition.State != EscrowTransitionReorged || transition.ReorgedFrom != EscrowTransitionConfirmed && transition.ReorgedFrom != EscrowTransitionReverted {
			t.Fatalf("dependent transition was not invalidated: %+v", transition)
		}
	}
	if engine.Balance(intent.OrganizationID, "escrow_locked") != "0" || engine.Balance(intent.OrganizationID, "pending_settlement") != "0" || engine.Balance(intent.OrganizationID, "agent_service_expense") != "0" {
		t.Fatalf("reorg corrections did not restore zero: %s/%s/%s", engine.Balance(intent.OrganizationID, "escrow_locked"), engine.Balance(intent.OrganizationID, "pending_settlement"), engine.Balance(intent.OrganizationID, "agent_service_expense"))
	}
	if _, err := engine.RegisterEscrowTransition(context.Background(), intent.OrganizationID, intent.CallID, EscrowTransitionCandidate{Action: EscrowFund, TransactionHash: testHash(245)}); err == nil {
		t.Fatal("quarantined position accepted a new transition")
	}
}

func TestDurableEscrowFinalityRequiresExactCanonicalQuorum(t *testing.T) {
	t.Parallel()
	clock := &testClock{now: time.Date(2026, 8, 14, 15, 0, 0, 0, time.UTC)}
	engine, err := Open(filepath.Join(t.TempDir(), "reconciliation.log"), testConfig(clock))
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	bootstrapHealthy(t, engine, clock, 300)
	intent := durableEscrowIntent(t)
	if _, err := engine.RegisterEscrowIntent(context.Background(), intent); err != nil {
		t.Fatal(err)
	}
	call := reconcileEscrowCandidate(t, engine, intent, EscrowTransitionCandidate{Action: EscrowFund, TransactionHash: testHash(251)}, 251)
	transition := call.Transitions[0]
	bad := durableEscrowReorgEvidence(transition, transition.BlockHash, 310)
	bad[1].CanonicalBlockHash = testHash(998)
	if _, err := engine.ConfirmEscrowFinality(context.Background(), intent.CallID, transition.Expected.TransactionHash, bad); !errors.Is(err, ErrUnsafeFinality) {
		t.Fatalf("disputed finality error = %v", err)
	}
	confirmed, err := engine.ConfirmEscrowFinality(context.Background(), intent.CallID, transition.Expected.TransactionHash, durableEscrowReorgEvidence(transition, transition.BlockHash, 310))
	if err != nil || confirmed.Transitions[0].FinalityCheckedAt == nil || confirmed.Transitions[0].FinalityCheckedHead != 310 {
		t.Fatalf("confirmed finality = %+v, %v", confirmed, err)
	}
	if replay, err := engine.ConfirmEscrowFinality(context.Background(), intent.CallID, transition.Expected.TransactionHash, durableEscrowReorgEvidence(transition, transition.BlockHash, 310)); err != nil || replay.Transitions[0].FinalityCheckedAt == nil {
		t.Fatalf("idempotent finality = %+v, %v", replay, err)
	}
}

func TestDurableEscrowRevertedTransitionMustReachCanonicalFinalityBeforeRetry(t *testing.T) {
	t.Parallel()
	clock := &testClock{now: time.Date(2026, 8, 14, 16, 0, 0, 0, time.UTC)}
	engine, err := Open(filepath.Join(t.TempDir(), "reconciliation.log"), testConfig(clock))
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	bootstrapHealthy(t, engine, clock, 300)
	intent := durableEscrowIntent(t)
	if _, err := engine.RegisterEscrowIntent(context.Background(), intent); err != nil {
		t.Fatal(err)
	}
	call, err := engine.RegisterEscrowTransition(context.Background(), intent.OrganizationID, intent.CallID, EscrowTransitionCandidate{Action: EscrowFund, TransactionHash: testHash(271)})
	if err != nil {
		t.Fatal(err)
	}
	evidence := durableEscrowEvidence(call.Pending.Expected, 271)
	for index := range evidence {
		evidence[index].Success = false
	}
	call, err = engine.ReconcileEscrowTransition(context.Background(), intent.CallID, evidence)
	if err != nil || call.State != EscrowPositionRegistered || call.Transitions[0].State != EscrowTransitionReverted || engine.Balance(intent.OrganizationID, "escrow_locked") != "0" {
		t.Fatalf("reverted fund = %+v balance=%s err=%v", call, engine.Balance(intent.OrganizationID, "escrow_locked"), err)
	}
	if _, err := engine.RegisterEscrowTransition(context.Background(), intent.OrganizationID, intent.CallID, EscrowTransitionCandidate{Action: EscrowFund, TransactionHash: testHash(272)}); !errors.Is(err, ErrEscrowFinality) {
		t.Fatalf("retry before reverted finality error = %v", err)
	}
	reverted := call.Transitions[0]
	call, err = engine.ConfirmEscrowFinality(context.Background(), intent.CallID, reverted.Expected.TransactionHash, durableEscrowReorgEvidence(reverted, reverted.BlockHash, 310))
	if err != nil || call.Transitions[0].FinalityCheckedAt == nil {
		t.Fatalf("reverted finality = %+v, %v", call, err)
	}
	if _, err := engine.RegisterEscrowTransition(context.Background(), intent.OrganizationID, intent.CallID, EscrowTransitionCandidate{Action: EscrowFund, TransactionHash: testHash(272)}); err != nil {
		t.Fatalf("retry after canonical finality: %v", err)
	}
	call, err = engine.ReopenEscrowReorg(context.Background(), intent.CallID, reverted.Expected.TransactionHash, durableEscrowReorgEvidence(reverted, testHash(997), 311))
	if err != nil || call.State != EscrowPositionQuarantined || call.Pending != nil || call.Transitions[0].State != EscrowTransitionReorged {
		t.Fatalf("reorged reverted receipt = %+v, %v", call, err)
	}
}

func TestReorgedRevertedTransitionDoesNotReverseIndependentConfirmedSuffix(t *testing.T) {
	t.Parallel()
	clock := &testClock{now: time.Date(2026, 8, 14, 16, 15, 0, 0, time.UTC)}
	path := filepath.Join(t.TempDir(), "reconciliation.log")
	engine, err := Open(path, testConfig(clock))
	if err != nil {
		t.Fatal(err)
	}
	bootstrapHealthy(t, engine, clock, 300)
	intent := durableEscrowIntent(t)
	if _, err := engine.RegisterEscrowIntent(context.Background(), intent); err != nil {
		t.Fatal(err)
	}
	first, err := engine.RegisterEscrowTransition(context.Background(), intent.OrganizationID, intent.CallID, EscrowTransitionCandidate{Action: EscrowFund, TransactionHash: testHash(273)})
	if err != nil {
		t.Fatal(err)
	}
	revertedEvidence := durableEscrowEvidence(first.Pending.Expected, 273)
	for index := range revertedEvidence {
		revertedEvidence[index].Success = false
	}
	first, err = engine.ReconcileEscrowTransition(context.Background(), intent.CallID, revertedEvidence)
	if err != nil {
		t.Fatal(err)
	}
	reverted := first.Transitions[0]
	if _, err := engine.ConfirmEscrowFinality(context.Background(), intent.CallID, reverted.Expected.TransactionHash, durableEscrowReorgEvidence(reverted, reverted.BlockHash, 310)); err != nil {
		t.Fatal(err)
	}
	confirmed := reconcileEscrowCandidate(t, engine, intent, EscrowTransitionCandidate{Action: EscrowFund, TransactionHash: testHash(274)}, 274)
	if engine.Balance(intent.OrganizationID, "escrow_locked") != "100" || engine.Balance(intent.OrganizationID, "pending_settlement") != "-100" {
		t.Fatal("confirmed retry did not create the expected locked balance")
	}

	reorged, err := engine.ReopenEscrowReorg(context.Background(), intent.CallID, reverted.Expected.TransactionHash, durableEscrowReorgEvidence(reverted, testHash(996), 311))
	if err != nil {
		t.Fatal(err)
	}
	if reorged.State != EscrowPositionQuarantined || reorged.Transitions[0].State != EscrowTransitionReorged || reorged.Transitions[1].State != EscrowTransitionConfirmed {
		t.Fatalf("reorged reverted receipt changed independent suffix: %+v", reorged)
	}
	if reorged.Transitions[1].LedgerTransactionID != confirmed.Transitions[1].LedgerTransactionID || reorged.Transitions[1].CorrectionTransactionID != "" {
		t.Fatalf("independent funding ledger was corrected: %+v", reorged.Transitions[1])
	}
	if engine.Balance(intent.OrganizationID, "escrow_locked") != "100" || engine.Balance(intent.OrganizationID, "pending_settlement") != "-100" {
		t.Fatalf("reorged reverted receipt changed real locked exposure: %s/%s", engine.Balance(intent.OrganizationID, "escrow_locked"), engine.Balance(intent.OrganizationID, "pending_settlement"))
	}
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := Open(path, testConfig(clock))
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	replayed, ok := restarted.EscrowCall(intent.OrganizationID, intent.CallID)
	if !ok || replayed.Transitions[1].State != EscrowTransitionConfirmed || restarted.Balance(intent.OrganizationID, "escrow_locked") != "100" {
		t.Fatalf("restart lost independent confirmed suffix: call=%+v locked=%s", replayed, restarted.Balance(intent.OrganizationID, "escrow_locked"))
	}
}

func TestDurableEscrowRejectsForgedDeliveryTimingEvidence(t *testing.T) {
	t.Parallel()
	clock := &testClock{now: time.Date(2026, 8, 14, 16, 30, 0, 0, time.UTC)}
	engine, err := Open(filepath.Join(t.TempDir(), "reconciliation.log"), testConfig(clock))
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	bootstrapHealthy(t, engine, clock, 300)
	intent := durableEscrowIntent(t)
	if _, err := engine.RegisterEscrowIntent(context.Background(), intent); err != nil {
		t.Fatal(err)
	}
	reconcileEscrowCandidate(t, engine, intent, EscrowTransitionCandidate{Action: EscrowFund, TransactionHash: testHash(281)}, 281)
	reconcileEscrowCandidate(t, engine, intent, EscrowTransitionCandidate{Action: EscrowAcknowledge, TransactionHash: testHash(282)}, 282)
	call, err := engine.RegisterEscrowTransition(context.Background(), intent.OrganizationID, intent.CallID, EscrowTransitionCandidate{Action: EscrowDeliver, TransactionHash: testHash(283), ResponseDigest: testHash(901), EvidenceDigest: testHash(902)})
	if err != nil {
		t.Fatal(err)
	}
	forged := durableEscrowEvidence(call.Pending.Expected, 283)
	forged[0].ReleasableAt++
	forged[1].ReleasableAt++
	if _, err := engine.ReconcileEscrowTransition(context.Background(), intent.CallID, forged); !errors.Is(err, ErrUnsafeFinality) {
		t.Fatalf("forged delivery timing error = %v", err)
	}
	stored, _ := engine.EscrowCall(intent.OrganizationID, intent.CallID)
	if stored.State != EscrowPositionAcknowledged || stored.Pending == nil {
		t.Fatalf("forged timing mutated durable call: %+v", stored)
	}
	regressed := durableEscrowEvidence(call.Pending.Expected, 281)
	if _, err := engine.ReconcileEscrowTransition(context.Background(), intent.CallID, regressed); !errors.Is(err, ErrUnsafeFinality) {
		t.Fatalf("regressed transition block error = %v", err)
	}
}

func TestDurableEscrowLedgerBindingsRejectMissingAlteredAndUnreferencedEntries(t *testing.T) {
	t.Parallel()
	clock := &testClock{now: time.Date(2026, 8, 14, 17, 0, 0, 0, time.UTC)}
	engine, err := Open(filepath.Join(t.TempDir(), "reconciliation.log"), testConfig(clock))
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	bootstrapHealthy(t, engine, clock, 300)
	intent := durableEscrowIntent(t)
	if _, err := engine.RegisterEscrowIntent(context.Background(), intent); err != nil {
		t.Fatal(err)
	}
	call := reconcileEscrowCandidate(t, engine, intent, EscrowTransitionCandidate{Action: EscrowFund, TransactionHash: testHash(291)}, 291)
	transaction, ok := engine.LedgerTransaction(call.Transitions[0].LedgerTransactionID)
	if !ok {
		t.Fatal("canonical escrow funding ledger is missing")
	}
	if err := validateEscrowLedgerBindings(call, nil, map[string]LedgerTransaction{transaction.TransactionID: transaction}, clock.Now()); err != nil {
		t.Fatalf("valid replay ledger bindings: %v", err)
	}
	if err := validateEscrowLedgerBindings(call, nil, map[string]LedgerTransaction{}, clock.Now()); err == nil {
		t.Fatal("missing replay ledger was accepted")
	}
	altered := cloneLedger(transaction)
	altered.Postings[0].AmountAtomic, altered.Postings[1].AmountAtomic = "101", "-101"
	if err := validateEscrowLedgerBindings(call, nil, map[string]LedgerTransaction{altered.TransactionID: altered}, clock.Now()); err == nil {
		t.Fatal("economically altered replay ledger was accepted")
	}
	extra := LedgerTransaction{
		TransactionID: "escrow_unreferenced_1", OrganizationID: intent.OrganizationID, Kind: LedgerSuspense, ReferenceID: intent.CallID,
		Postings: []Posting{{Account: "unclassified_incoming", AmountAtomic: "1"}, {Account: "reconciliation_suspense", AmountAtomic: "-1"}}, RecordedAt: clock.Now(),
	}
	if err := validateEscrowLedgerBindings(call, []LedgerTransaction{extra}, map[string]LedgerTransaction{transaction.TransactionID: transaction}, clock.Now()); err == nil {
		t.Fatal("unreferenced escrow event ledger was accepted")
	}
}

func TestTransactionHashCannotBeRegisteredAcrossDirectAndEscrowRails(t *testing.T) {
	t.Parallel()
	newEngine := func(name string) (*Engine, *testClock) {
		t.Helper()
		clock := &testClock{now: time.Date(2026, 8, 14, 17, 30, 0, 0, time.UTC)}
		engine, err := Open(filepath.Join(t.TempDir(), name+".log"), testConfig(clock))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = engine.Close() })
		bootstrapHealthy(t, engine, clock, 300)
		return engine, clock
	}

	hash := testHash(299)
	escrowFirst, _ := newEngine("escrow-first")
	intent := durableEscrowIntent(t)
	if _, err := escrowFirst.RegisterEscrowIntent(context.Background(), intent); err != nil {
		t.Fatal(err)
	}
	if _, err := escrowFirst.RegisterEscrowTransition(context.Background(), intent.OrganizationID, intent.CallID, EscrowTransitionCandidate{Action: EscrowFund, TransactionHash: hash}); err != nil {
		t.Fatal(err)
	}
	direct := testExpected()
	direct.TransactionHash = hash
	if _, err := escrowFirst.RegisterBroadcast(context.Background(), direct); !errors.Is(err, ErrConflict) {
		t.Fatalf("escrow then direct error = %v", err)
	}

	directFirst, _ := newEngine("direct-first")
	if _, err := directFirst.RegisterBroadcast(context.Background(), direct); err != nil {
		t.Fatal(err)
	}
	if _, err := directFirst.RegisterEscrowIntent(context.Background(), intent); err != nil {
		t.Fatal(err)
	}
	if _, err := directFirst.RegisterEscrowTransition(context.Background(), intent.OrganizationID, intent.CallID, EscrowTransitionCandidate{Action: EscrowFund, TransactionHash: hash}); !errors.Is(err, ErrConflict) {
		t.Fatalf("direct then escrow error = %v", err)
	}
}

func TestEscrowFinalityAndReorgRemainInvisibleWhenJournalAppendFails(t *testing.T) {
	t.Parallel()
	clock := &testClock{now: time.Date(2026, 8, 14, 18, 0, 0, 0, time.UTC)}
	engine, err := Open(filepath.Join(t.TempDir(), "faulted.log"), testConfig(clock))
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	bootstrapHealthy(t, engine, clock, 300)
	intent := durableEscrowIntent(t)
	if _, err := engine.RegisterEscrowIntent(context.Background(), intent); err != nil {
		t.Fatal(err)
	}
	before := reconcileEscrowCandidate(t, engine, intent, EscrowTransitionCandidate{Action: EscrowFund, TransactionHash: testHash(298)}, 298)
	transition := before.Transitions[0]
	engine.journal.mu.Lock()
	engine.journal.fault = errors.New("injected durable write failure")
	engine.journal.mu.Unlock()
	if _, err := engine.ConfirmEscrowFinality(context.Background(), intent.CallID, transition.Expected.TransactionHash, durableEscrowReorgEvidence(transition, transition.BlockHash, 310)); err == nil {
		t.Fatal("finality succeeded after durable journal failure")
	}
	afterFinality, _ := engine.EscrowCall(intent.OrganizationID, intent.CallID)
	if !equalJSON(afterFinality, before) {
		t.Fatalf("failed finality mutated live call: before=%+v after=%+v", before, afterFinality)
	}
	if _, err := engine.ReopenEscrowReorg(context.Background(), intent.CallID, transition.Expected.TransactionHash, durableEscrowReorgEvidence(transition, testHash(996), 310)); err == nil {
		t.Fatal("reorg succeeded after durable journal failure")
	}
	afterReorg, _ := engine.EscrowCall(intent.OrganizationID, intent.CallID)
	if !equalJSON(afterReorg, before) || engine.Balance(intent.OrganizationID, "escrow_locked") != "100" {
		t.Fatalf("failed reorg mutated live state: before=%+v after=%+v balance=%s", before, afterReorg, engine.Balance(intent.OrganizationID, "escrow_locked"))
	}
}

func durableEscrowIntent(t *testing.T) EscrowIntent {
	t.Helper()
	task, request := testHash(501), testHash(502)
	callID, err := DeriveEscrowCallID(84532, testEscrow, testBuyer, task, request)
	if err != nil {
		t.Fatal(err)
	}
	return EscrowIntent{
		OrganizationID: "org_acme", CustomerID: "customer_acme", AgentID: "agent_fetch", TaskID: "task_fetch",
		AuthorizationID: "auth_escrow_1", IntentDigest: testHash(503), ChainID: 84532,
		Contract: testEscrow, Asset: testEscrowAsset, CallID: callID, Buyer: testBuyer, Provider: testProvider,
		AmountAtomic: "100", TaskDigest: task, RequestDigest: request,
		AcknowledgeBy: 1_800_000_000, DeliverBy: 1_800_003_600, ReleaseWindow: 3600,
	}
}

func reconcileEscrowCandidate(t *testing.T, engine *Engine, intent EscrowIntent, candidate EscrowTransitionCandidate, block uint64) EscrowCall {
	t.Helper()
	call, err := engine.RegisterEscrowTransition(context.Background(), intent.OrganizationID, intent.CallID, candidate)
	if err != nil || call.Pending == nil {
		t.Fatalf("RegisterEscrowTransition(%s) = %+v, %v", candidate.Action, call, err)
	}
	call, err = engine.ReconcileEscrowTransition(context.Background(), intent.CallID, durableEscrowEvidence(call.Pending.Expected, block))
	if err != nil {
		t.Fatalf("ReconcileEscrowTransition(%s) = %+v, %v", candidate.Action, call, err)
	}
	return call
}

func durableEscrowEvidence(expected EscrowExpectedReceipt, block uint64) []EscrowReceiptEvidence {
	evidence := func(provider string) EscrowReceiptEvidence {
		receipt := EscrowReceiptEvidence{
			Provider: provider, Action: expected.Action, ChainID: expected.ChainID, TransactionHash: expected.TransactionHash,
			BlockNumber: block, BlockHash: testHash(block), ConfirmedHead: block + 2, Success: true, CallID: expected.CallID,
		}
		if expected.Action == EscrowDeliver {
			receipt.DeliveredAt = 1_800_000_100
			receipt.ReleasableAt = receipt.DeliveredAt + expected.ReleaseWindow
		}
		return receipt
	}
	return []EscrowReceiptEvidence{evidence("alpha_rpc"), evidence("beta_rpc")}
}

func durableEscrowReorgEvidence(transition EscrowTransition, canonicalHash string, head uint64) []ReorgEvidence {
	evidence := func(provider string) ReorgEvidence {
		return ReorgEvidence{
			Provider: provider, ChainID: transition.Expected.ChainID, TransactionHash: transition.Expected.TransactionHash,
			OriginalBlockNumber: transition.BlockNumber, OriginalBlockHash: transition.BlockHash,
			CanonicalBlockHash: canonicalHash, ObservedHead: head,
		}
	}
	return []ReorgEvidence{evidence("alpha_rpc"), evidence("beta_rpc")}
}

func testHashAddress(value byte) string {
	const digits = "0123456789abcdef"
	raw := make([]byte, 40)
	for index := range raw {
		raw[index] = digits[int(value+byte(index))%len(digits)]
	}
	return "0x" + string(raw)
}
