package reconciliation

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func transactionOutcomeQuorum(execution Execution, outcome TransactionRecoveryOutcome, nonce, accountNonce uint64, through uint64, replacementHash, recipient, amount string) []TransactionOutcomeEvidence {
	base := TransactionOutcomeEvidence{
		ChainID: execution.Expected.ChainID, OriginalTransactionHash: execution.Expected.TransactionHash,
		Outcome: outcome, Nonce: nonce, ThroughBlock: through, ThroughBlockHash: testHash(through), AccountNonce: accountNonce,
		ReplacementTransactionHash: replacementHash, ReplacementRecipient: recipient, ReplacementAmountAtomic: amount,
	}
	if replacementHash != "" {
		base.ReplacementBlockNumber, base.ReplacementBlockHash = through, testHash(through)
	}
	alpha, beta := base, base
	alpha.Provider, beta.Provider = "rpc_alpha", "rpc_beta"
	return []TransactionOutcomeEvidence{alpha, beta}
}

func TestTransactionRecoveryBindsNonceQuarantinesAndFinalizesProvenDrop(t *testing.T) {
	t.Parallel()
	clock := &testClock{now: time.Date(2026, 8, 28, 18, 30, 0, 0, time.UTC)}
	path := filepath.Join(t.TempDir(), "transaction-recovery.log")
	engine, err := Open(path, testConfig(clock))
	if err != nil {
		t.Fatal(err)
	}
	bootstrapHealthy(t, engine, clock, 100)
	expected := testExpected()
	if _, err := engine.RegisterAttestedBroadcast(context.Background(), expected, testBroadcastAttestation(t, expected, clock.Now())); err != nil {
		t.Fatal(err)
	}
	through := engine.Status().LastTrusted.BlockNumber - engine.FinalityDepth()
	tooRecent := transactionOutcomeQuorum(Execution{Expected: expected}, RecoveryOriginalPending, 7, 0, through+1, "", "", "")
	if _, err := engine.RecordTransactionOutcome(context.Background(), expected.ExecutionID, tooRecent); !errors.Is(err, ErrUnsafeFinality) {
		t.Fatalf("recent transaction evidence error = %v, want unsafe finality", err)
	}
	pendingEvidence := transactionOutcomeQuorum(Execution{Expected: expected}, RecoveryOriginalPending, 7, 0, through, "", "", "")
	bound, err := engine.RecordTransactionOutcome(context.Background(), expected.ExecutionID, pendingEvidence)
	if err != nil || bound.TransactionRecovery == nil || bound.TransactionRecovery.Nonce != 7 || bound.TransactionRecovery.ScanFromBlock != through || bound.State != ExecutionBroadcast {
		t.Fatalf("bound transaction identity = %+v, %v", bound, err)
	}
	if replay, err := engine.RecordTransactionOutcome(context.Background(), expected.ExecutionID, []TransactionOutcomeEvidence{pendingEvidence[1], pendingEvidence[0]}); err != nil || replay.TransactionRecovery.EvidenceDigest != bound.TransactionRecovery.EvidenceDigest {
		t.Fatalf("reordered pending replay = %+v, %v", replay, err)
	}

	droppedEvidence := transactionOutcomeQuorum(bound, RecoveryDropped, 7, 8, through, "", "", "")
	quarantined, err := engine.RecordTransactionOutcome(context.Background(), expected.ExecutionID, droppedEvidence)
	if err != nil || quarantined.State != ExecutionQuarantined || quarantined.TransactionRecovery.Outcome != RecoveryDropped || quarantined.FinalityCheckedAt != nil {
		t.Fatalf("proved drop quarantine = %+v, %v", quarantined, err)
	}
	forged := append([]TransactionOutcomeEvidence(nil), droppedEvidence...)
	forged[1].AccountNonce = 7
	if _, err := engine.RecordTransactionOutcome(context.Background(), expected.ExecutionID, forged); err == nil {
		t.Fatal("non-consuming account nonce changed quarantined state")
	}
	if _, err := engine.ResolveProvenDropForOrganization(context.Background(), "org_other", expected.ExecutionID, "operator_bob"); !errors.Is(err, ErrUnknownExecution) {
		t.Fatalf("cross-tenant dropped resolution error = %v", err)
	}
	resolved, err := engine.ResolveProvenDropForOrganization(context.Background(), expected.OrganizationID, expected.ExecutionID, "operator_bob")
	if err != nil || resolved.State != ExecutionDropped || resolved.FinalityCheckedAt == nil || resolved.FinalityCheckedHead != engine.Status().LastTrusted.BlockNumber || resolved.FinalityCheckedHead-through < engine.FinalityDepth() || resolved.BlockNumber != through || resolved.LedgerTransactionID != "" || resolved.RecoveryResolutionActor != "operator_bob" {
		t.Fatalf("dropped resolution = %+v, %v", resolved, err)
	}
	if engine.Balance(expected.OrganizationID, "agent_service_expense") != "0" {
		t.Fatal("proved drop invented settlement accounting")
	}
	if _, err := engine.RecordTransactionOutcome(context.Background(), expected.ExecutionID, droppedEvidence); err == nil {
		t.Fatal("finalized drop accepted stale recovery evidence as a valid state transition")
	}
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := Open(path, testConfig(clock))
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	replayed, ok := restarted.Execution(expected.ExecutionID)
	if !ok || replayed.State != ExecutionDropped || replayed.TransactionRecovery == nil || replayed.TransactionRecovery.EvidenceDigest == "" || replayed.RecoveryResolutionActor != "operator_bob" {
		t.Fatalf("replayed dropped recovery = %+v", replayed)
	}
}

func TestTransactionRecoveryReconcilesExactReplacementAndRejectsUnknownTransfer(t *testing.T) {
	t.Parallel()
	clock := &testClock{now: time.Date(2026, 8, 28, 19, 0, 0, 0, time.UTC)}
	engine, err := Open(filepath.Join(t.TempDir(), "replacement.log"), testConfig(clock))
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	bootstrapHealthy(t, engine, clock, 200)
	expected := testExpected()
	if _, err := engine.RegisterAttestedBroadcast(context.Background(), expected, testBroadcastAttestation(t, expected, clock.Now())); err != nil {
		t.Fatal(err)
	}
	through := engine.Status().LastTrusted.BlockNumber - engine.FinalityDepth()
	bound, err := engine.RecordTransactionOutcome(context.Background(), expected.ExecutionID, transactionOutcomeQuorum(Execution{Expected: expected}, RecoveryOriginalPending, 11, 0, through, "", "", ""))
	if err != nil {
		t.Fatal(err)
	}
	replacementHash := testHash(990)
	quarantined, err := engine.RecordTransactionOutcome(context.Background(), expected.ExecutionID, transactionOutcomeQuorum(bound, RecoveryExpectedReplacement, 11, 12, through, replacementHash, expected.Recipient, expected.AmountAtomic))
	if err != nil || quarantined.State != ExecutionQuarantined {
		t.Fatalf("replacement quarantine = %+v, %v", quarantined, err)
	}
	replacementExpected := expected
	replacementExpected.TransactionHash = replacementHash
	receipts := receiptQuorum(replacementExpected, through, true)
	settlement := settlementTransaction(quarantined, receipts[0], clock.Now())
	if _, err := engine.ReconcileProvenReplacementForOrganization(context.Background(), "org_other", expected.ExecutionID, "operator_bob", receipts, settlement); !errors.Is(err, ErrUnknownExecution) {
		t.Fatalf("cross-tenant replacement error = %v", err)
	}
	resolved, err := engine.ReconcileProvenReplacementForOrganization(context.Background(), expected.OrganizationID, expected.ExecutionID, "operator_bob", receipts, settlement)
	if err != nil || resolved.State != ExecutionSettled || resolved.ResolvedTransactionHash != replacementHash || resolved.LedgerTransactionID == "" || resolved.FinalityCheckedAt != nil {
		t.Fatalf("replacement resolution = %+v, %v", resolved, err)
	}
	if engine.Balance(expected.OrganizationID, "agent_service_expense") != expected.AmountAtomic {
		t.Fatal("exact replacement settlement was not accounted once")
	}
	canonicalChanged := canonicalBlockEvidence(resolved, testHash(9_999), engine.Status().LastTrusted.BlockNumber)
	originalLedger, _ := engine.LedgerTransaction(resolved.LedgerTransactionID)
	correction := correctionTransaction(resolved, originalLedger, canonicalChanged[0], clock.Now())
	reopened, err := engine.ReopenReorg(context.Background(), expected.ExecutionID, canonicalChanged, correction)
	if err != nil || reopened.State != ExecutionPendingChainRecovery {
		t.Fatalf("replacement reorg = %+v, %v", reopened, err)
	}
	originalReceipts := receiptQuorum(expected, through-1, true)
	originalSettlement := settlementTransaction(reopened, originalReceipts[0], clock.Now())
	reconciledOriginal, err := engine.ReconcileReceipt(context.Background(), expected.ExecutionID, originalReceipts, &originalSettlement)
	if err != nil || reconciledOriginal.State != ExecutionSettled || reconciledOriginal.ResolvedTransactionHash != expected.TransactionHash {
		t.Fatalf("post-reorg original reconciliation = %+v, %v", reconciledOriginal, err)
	}
	clock.Add(time.Second)
	if _, err := engine.Observe(context.Background(), healthyObservations(clock.Now(), 202, 203)); err != nil {
		t.Fatal(err)
	}
	clock.Add(time.Second)
	if _, err := engine.Observe(context.Background(), healthyObservations(clock.Now(), 203, 204)); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Resume(context.Background(), "operator_alice"); err != nil {
		t.Fatal(err)
	}

	other := expected
	other.ExecutionID = "exec_unknown"
	other.TransactionHash = testHash(991)
	if _, err := engine.RegisterBroadcast(context.Background(), other); err != nil {
		t.Fatal(err)
	}
	otherBound, err := engine.RecordTransactionOutcome(context.Background(), other.ExecutionID, transactionOutcomeQuorum(Execution{Expected: other}, RecoveryOriginalPending, 12, 0, through, "", "", ""))
	if err != nil {
		t.Fatal(err)
	}
	unknownHash := testHash(992)
	unknown, err := engine.RecordTransactionOutcome(context.Background(), other.ExecutionID, transactionOutcomeQuorum(otherBound, RecoveryUnknownTransfer, 12, 13, through, unknownHash, testSender, "55"))
	if err != nil || unknown.State != ExecutionQuarantined || unknown.TransactionRecovery.Outcome != RecoveryUnknownTransfer {
		t.Fatalf("unknown transfer quarantine = %+v, %v", unknown, err)
	}
	view := engine.OrganizationView(other.OrganizationID)
	if len(view.Exceptions) != 1 || view.Exceptions[0].RecoveryOutcome != string(RecoveryUnknownTransfer) || view.Exceptions[0].TransactionNonce != 12 || view.Exceptions[0].ReplacementHash != unknownHash || view.Exceptions[0].ReplacementAmount != "55" {
		t.Fatalf("unknown transfer operator projection = %+v", view.Exceptions)
	}
	if _, err := engine.ResolveProvenDropForOrganization(context.Background(), other.OrganizationID, other.ExecutionID, "operator_bob"); err == nil {
		t.Fatal("unknown transfer was released as a proved drop")
	}
}
