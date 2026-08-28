package reconciliation

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/gnanam1990/flowops/pkg/envelope"
)

type workerSource struct {
	mu       sync.Mutex
	receipts map[string]ReceiptResult
	blocks   map[string]ReorgResult
	wait     bool
	queries  int
}

type escrowWorkerSource struct {
	*workerSource
	escrowReceipts map[string]EscrowReceiptResult
	escrowBlocks   map[string]ReorgResult
}

type recoveryWorkerSource struct {
	*workerSource
	outcomes map[string]TransactionOutcomeResult
}

func (s *recoveryWorkerSource) TransactionOutcomeQuorum(_ context.Context, execution Execution, _ uint64) TransactionOutcomeResult {
	return s.outcomes[execution.Expected.ExecutionID]
}

func (s *escrowWorkerSource) EscrowReceiptQuorum(_ context.Context, expected EscrowExpectedReceipt) EscrowReceiptResult {
	return s.escrowReceipts[expected.TransactionHash]
}

func (s *escrowWorkerSource) EscrowCanonicalBlockQuorum(_ context.Context, transition EscrowTransition) ReorgResult {
	return s.escrowBlocks[transition.Expected.TransactionHash]
}

func (s *workerSource) ReceiptQuorum(ctx context.Context, expected ExpectedExecution) ReceiptResult {
	s.mu.Lock()
	s.queries++
	wait := s.wait
	result := s.receipts[expected.ExecutionID]
	s.mu.Unlock()
	if wait {
		<-ctx.Done()
		return ReceiptResult{Failures: map[string]string{"rpc_alpha": "query timed out"}}
	}
	return result
}

func (s *workerSource) CanonicalBlockQuorum(ctx context.Context, execution Execution) ReorgResult {
	s.mu.Lock()
	s.queries++
	wait := s.wait
	result := s.blocks[execution.Expected.ExecutionID]
	s.mu.Unlock()
	if wait {
		<-ctx.Done()
		return ReorgResult{Failures: map[string]string{"rpc_alpha": "query timed out"}}
	}
	return result
}

func testWorker(t *testing.T, source *workerSource, engine *Engine, clock *testClock) *Worker {
	t.Helper()
	worker, err := NewWorker(source, engine, WorkerConfig{
		Interval: time.Second, QueryTimeout: 50 * time.Millisecond, Clock: clock.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	return worker
}

func testEscrowWorker(t *testing.T, source *escrowWorkerSource, engine *Engine, clock *testClock) *Worker {
	t.Helper()
	worker, err := NewWorker(source, engine, WorkerConfig{Interval: time.Second, QueryTimeout: 50 * time.Millisecond, Clock: clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	return worker
}

func canonicalBlockEvidence(execution Execution, canonicalHash string, head uint64) []ReorgEvidence {
	base := ReorgEvidence{
		ChainID: execution.Expected.ChainID, TransactionHash: resolvedTransactionHash(execution),
		OriginalBlockNumber: execution.BlockNumber, OriginalBlockHash: execution.BlockHash,
		CanonicalBlockHash: canonicalHash, ObservedHead: head,
	}
	alpha, beta := base, base
	alpha.Provider, beta.Provider = "rpc_alpha", "rpc_beta"
	return []ReorgEvidence{alpha, beta}
}

func TestWorkerFinalizesCanonicalReceiptExactlyOnce(t *testing.T) {
	t.Parallel()
	clock := &testClock{now: time.Date(2026, 8, 12, 13, 0, 0, 0, time.UTC)}
	engine, err := Open(filepath.Join(t.TempDir(), "worker.log"), testConfig(clock))
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	bootstrapHealthy(t, engine, clock, 100)
	expected := testExpected()
	if _, err := engine.RegisterAttestedBroadcast(context.Background(), expected, testBroadcastAttestation(t, expected, clock.Now())); err != nil {
		t.Fatal(err)
	}
	source := &workerSource{receipts: map[string]ReceiptResult{
		expected.ExecutionID: {Evidence: receiptQuorum(expected, 100, true)},
	}}
	worker := testWorker(t, source, engine, clock)
	cycle, err := worker.RunOnce(context.Background())
	if err != nil || cycle.Settled != 1 || cycle.ReceiptCandidates != 1 || cycle.Deferred != 0 {
		t.Fatalf("RunOnce() = %+v, %v", cycle, err)
	}
	resolved, _ := engine.Execution(expected.ExecutionID)
	ledger, ok := engine.LedgerTransaction(resolved.LedgerTransactionID)
	if !ok || ledger.TransactionID != derivedLedgerID("settlement", expected.ExecutionID, testHash(100)) {
		t.Fatalf("deterministic settlement = %+v", ledger)
	}
	if engine.Balance(expected.OrganizationID, "agent_service_expense") != expected.AmountAtomic {
		t.Fatal("canonical settlement was not posted")
	}
	cycle, err = worker.RunOnce(context.Background())
	if err != nil || cycle.Settled != 0 || engine.Balance(expected.OrganizationID, "agent_service_expense") != expected.AmountAtomic {
		t.Fatalf("second cycle = %+v, %v", cycle, err)
	}
}

func TestWorkerContinuouslyReconcilesAndFinalizesDurableEscrowTransition(t *testing.T) {
	t.Parallel()
	clock := &testClock{now: time.Date(2026, 8, 14, 16, 0, 0, 0, time.UTC)}
	engine, err := Open(filepath.Join(t.TempDir(), "escrow-worker.log"), testConfig(clock))
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	bootstrapHealthy(t, engine, clock, 300)
	intent := durableEscrowIntent(t)
	if _, err := engine.RegisterEscrowIntent(context.Background(), intent); err != nil {
		t.Fatal(err)
	}
	call, err := engine.RegisterEscrowTransition(context.Background(), intent.OrganizationID, intent.CallID, EscrowTransitionCandidate{Action: EscrowFund, TransactionHash: testHash(261)})
	if err != nil || call.Pending == nil {
		t.Fatalf("pending fund = %+v, %v", call, err)
	}
	source := &escrowWorkerSource{
		workerSource: &workerSource{receipts: map[string]ReceiptResult{}, blocks: map[string]ReorgResult{}},
		escrowReceipts: map[string]EscrowReceiptResult{
			call.Pending.Expected.TransactionHash: {Evidence: durableEscrowEvidence(call.Pending.Expected, 261)},
		},
		escrowBlocks: make(map[string]ReorgResult),
	}
	worker := testEscrowWorker(t, source, engine, clock)
	cycle, err := worker.RunOnce(context.Background())
	if err != nil || cycle.EscrowCandidates != 1 || cycle.EscrowConfirmed != 1 || cycle.EscrowFinalized != 0 {
		t.Fatalf("receipt cycle = %+v, %v", cycle, err)
	}
	confirmed, _ := engine.EscrowCall(intent.OrganizationID, intent.CallID)
	transition := confirmed.Transitions[0]
	source.escrowBlocks[transition.Expected.TransactionHash] = ReorgResult{Evidence: durableEscrowReorgEvidence(transition, transition.BlockHash, 310)}
	cycle, err = worker.RunOnce(context.Background())
	if err != nil || cycle.EscrowFinalized != 1 || cycle.EscrowCandidates != 0 {
		t.Fatalf("finality cycle = %+v, %v", cycle, err)
	}
	finalized, _ := engine.EscrowCall(intent.OrganizationID, intent.CallID)
	if finalized.Transitions[0].FinalityCheckedAt == nil || engine.Balance(intent.OrganizationID, "escrow_locked") != "100" {
		t.Fatalf("finalized transition = %+v balance=%s", finalized.Transitions[0], engine.Balance(intent.OrganizationID, "escrow_locked"))
	}
	cycle, err = worker.RunOnce(context.Background())
	if err != nil || cycle.EscrowFinalized != 0 || engine.Balance(intent.OrganizationID, "escrow_locked") != "100" {
		t.Fatalf("idempotent cycle = %+v, %v", cycle, err)
	}
}

func TestWorkerRecordsRevertWithoutInventingLedgerState(t *testing.T) {
	t.Parallel()
	clock := &testClock{now: time.Date(2026, 8, 12, 13, 5, 0, 0, time.UTC)}
	engine, err := Open(filepath.Join(t.TempDir(), "revert.log"), testConfig(clock))
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	bootstrapHealthy(t, engine, clock, 100)
	expected := testExpected()
	if _, err := engine.RegisterBroadcast(context.Background(), expected); err != nil {
		t.Fatal(err)
	}
	source := &workerSource{receipts: map[string]ReceiptResult{expected.ExecutionID: {Evidence: receiptQuorum(expected, 100, false)}}}
	cycle, err := testWorker(t, source, engine, clock).RunOnce(context.Background())
	if err != nil || cycle.Reverted != 1 {
		t.Fatalf("RunOnce() = %+v, %v", cycle, err)
	}
	resolved, _ := engine.Execution(expected.ExecutionID)
	if resolved.State != ExecutionReverted || resolved.LedgerTransactionID != "" || engine.Balance(expected.OrganizationID, "agent_service_expense") != "0" {
		t.Fatalf("reverted execution = %+v", resolved)
	}
}

func TestWorkerDefersDisputedOrMissingReceiptAndNeverRebroadcasts(t *testing.T) {
	t.Parallel()
	clock := &testClock{now: time.Date(2026, 8, 12, 13, 10, 0, 0, time.UTC)}
	engine, err := Open(filepath.Join(t.TempDir(), "defer.log"), testConfig(clock))
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	bootstrapHealthy(t, engine, clock, 100)
	expected := testExpected()
	if _, err := engine.RegisterBroadcast(context.Background(), expected); err != nil {
		t.Fatal(err)
	}
	evidence := receiptQuorum(expected, 100, true)
	evidence[1].Recipient = testSender
	source := &workerSource{receipts: map[string]ReceiptResult{expected.ExecutionID: {Evidence: evidence}}}
	cycle, err := testWorker(t, source, engine, clock).RunOnce(context.Background())
	if err != nil || cycle.Deferred != 1 {
		t.Fatalf("RunOnce() = %+v, %v", cycle, err)
	}
	unresolved, _ := engine.Execution(expected.ExecutionID)
	if unresolved.State != ExecutionBroadcast || unresolved.LedgerTransactionID != "" || engine.Balance(expected.OrganizationID, "agent_service_expense") != "0" {
		t.Fatalf("unsafe evidence changed state: %+v", unresolved)
	}
}

func TestWorkerBindsTransactionIdentityThenQuarantinesProvedDrop(t *testing.T) {
	t.Parallel()
	clock := &testClock{now: time.Date(2026, 8, 28, 20, 0, 0, 0, time.UTC)}
	engine, err := Open(filepath.Join(t.TempDir(), "worker-recovery.log"), testConfig(clock))
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	bootstrapHealthy(t, engine, clock, 300)
	expected := testExpected()
	if _, err := engine.RegisterAttestedBroadcast(context.Background(), expected, testBroadcastAttestation(t, expected, clock.Now())); err != nil {
		t.Fatal(err)
	}
	through := engine.Status().LastTrusted.BlockNumber - engine.FinalityDepth()
	source := &recoveryWorkerSource{
		workerSource: &workerSource{receipts: map[string]ReceiptResult{}},
		outcomes: map[string]TransactionOutcomeResult{expected.ExecutionID: {
			Evidence: transactionOutcomeQuorum(Execution{Expected: expected}, RecoveryOriginalPending, 21, 0, through, "", "", ""),
		}},
	}
	worker, err := NewWorker(source, engine, WorkerConfig{Interval: time.Second, QueryTimeout: 50 * time.Millisecond, Clock: clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	cycle, err := worker.RunOnce(context.Background())
	if err != nil || cycle.TransactionProbes != 1 || cycle.IdentitiesBound != 1 || cycle.AutoQuarantined != 0 {
		t.Fatalf("identity cycle = %+v, %v", cycle, err)
	}
	bound, _ := engine.Execution(expected.ExecutionID)
	if bound.TransactionRecovery == nil || bound.TransactionRecovery.Nonce != 21 || bound.State != ExecutionBroadcast {
		t.Fatalf("bound execution = %+v", bound)
	}
	source.outcomes[expected.ExecutionID] = TransactionOutcomeResult{Evidence: transactionOutcomeQuorum(bound, RecoveryDropped, 21, 22, through, "", "", "")}
	cycle, err = worker.RunOnce(context.Background())
	if err != nil || cycle.TransactionProbes != 1 || cycle.AutoQuarantined != 1 {
		t.Fatalf("drop cycle = %+v, %v", cycle, err)
	}
	quarantined, _ := engine.Execution(expected.ExecutionID)
	if quarantined.State != ExecutionQuarantined || quarantined.TransactionRecovery.Outcome != RecoveryDropped || engine.Balance(expected.OrganizationID, "agent_service_expense") != "0" {
		t.Fatalf("quarantined execution = %+v", quarantined)
	}
	cycle, err = worker.RunOnce(context.Background())
	if err != nil || cycle.ReceiptCandidates != 0 || cycle.TransactionProbes != 0 {
		t.Fatalf("quarantined execution was probed again = %+v, %v", cycle, err)
	}
}

func TestWorkerNeverAppliesDirectNonceRecoveryToX402Settlement(t *testing.T) {
	t.Parallel()
	clock := &testClock{now: time.Date(2026, 8, 28, 20, 15, 0, 0, time.UTC)}
	engine, err := Open(filepath.Join(t.TempDir(), "worker-x402-recovery.log"), testConfig(clock))
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	bootstrapHealthy(t, engine, clock, 300)
	expected := testExpected()
	expected.ExecutionID = envelope.ExecutionID("auth_x402")
	claim := testX402Claim(t, expected)
	if _, err := engine.RegisterX402Settlement(context.Background(), expected, claim); err != nil {
		t.Fatal(err)
	}
	through := engine.Status().LastTrusted.BlockNumber - engine.FinalityDepth()
	source := &recoveryWorkerSource{
		workerSource: &workerSource{receipts: map[string]ReceiptResult{}},
		outcomes:     map[string]TransactionOutcomeResult{expected.ExecutionID: {Evidence: transactionOutcomeQuorum(Execution{Expected: expected}, RecoveryDropped, 1, 2, through, "", "", "")}},
	}
	worker, err := NewWorker(source, engine, WorkerConfig{Interval: time.Second, QueryTimeout: 50 * time.Millisecond, Clock: clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	cycle, err := worker.RunOnce(context.Background())
	if err != nil || cycle.TransactionProbes != 0 || cycle.Deferred != 1 {
		t.Fatalf("x402 direct recovery cycle = %+v, %v", cycle, err)
	}
	unresolved, _ := engine.Execution(expected.ExecutionID)
	if unresolved.State != ExecutionBroadcast || unresolved.TransactionRecovery != nil {
		t.Fatalf("x402 execution entered direct nonce recovery = %+v", unresolved)
	}
}

func TestWorkerPersistsPositiveFinalityAndDoesNotPollItAgain(t *testing.T) {
	t.Parallel()
	clock := &testClock{now: time.Date(2026, 8, 12, 13, 15, 0, 0, time.UTC)}
	path := filepath.Join(t.TempDir(), "finality.log")
	engine, err := Open(path, testConfig(clock))
	if err != nil {
		t.Fatal(err)
	}
	bootstrapHealthy(t, engine, clock, 120)
	expected := testExpected()
	if _, err := engine.RegisterBroadcast(context.Background(), expected); err != nil {
		t.Fatal(err)
	}
	ledger := settlement(clock.Now(), expected.ExecutionID)
	if _, err := engine.ReconcileReceipt(context.Background(), expected.ExecutionID, receiptQuorum(expected, 100, true), &ledger); err != nil {
		t.Fatal(err)
	}
	settled, _ := engine.Execution(expected.ExecutionID)
	source := &workerSource{blocks: map[string]ReorgResult{
		expected.ExecutionID: {Evidence: canonicalBlockEvidence(settled, settled.BlockHash, 120)},
	}}
	worker := testWorker(t, source, engine, clock)
	cycle, err := worker.RunOnce(context.Background())
	if err != nil || cycle.FinalityConfirmed != 1 {
		t.Fatalf("RunOnce() = %+v, %v", cycle, err)
	}
	confirmed, _ := engine.Execution(expected.ExecutionID)
	if confirmed.FinalityCheckedAt == nil || confirmed.FinalityCheckedHead != 120 {
		t.Fatalf("finality state = %+v", confirmed)
	}
	source.mu.Lock()
	queries := source.queries
	source.mu.Unlock()
	if _, err := worker.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	source.mu.Lock()
	if source.queries != queries {
		t.Fatal("completed finality was polled again")
	}
	source.mu.Unlock()
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := Open(path, testConfig(clock))
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	replayed, _ := restarted.Execution(expected.ExecutionID)
	if replayed.FinalityCheckedAt == nil || replayed.FinalityCheckedHead != 120 {
		t.Fatalf("replayed finality = %+v", replayed)
	}
}

func TestWorkerFinalizesRevertedReceiptForBudgetProjection(t *testing.T) {
	t.Parallel()
	clock := &testClock{now: time.Date(2026, 8, 28, 16, 30, 0, 0, time.UTC)}
	engine, err := Open(filepath.Join(t.TempDir(), "reverted-finality.log"), testConfig(clock))
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	bootstrapHealthy(t, engine, clock, 120)
	expected := testExpected()
	if _, err := engine.RegisterBroadcast(context.Background(), expected); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.ReconcileReceipt(context.Background(), expected.ExecutionID, receiptQuorum(expected, 100, false), nil); err != nil {
		t.Fatal(err)
	}
	reverted, _ := engine.Execution(expected.ExecutionID)
	if reverted.State != ExecutionReverted || reverted.FinalityCheckedAt != nil {
		t.Fatalf("pre-finality revert = %+v", reverted)
	}
	source := &workerSource{blocks: map[string]ReorgResult{
		expected.ExecutionID: {Evidence: canonicalBlockEvidence(reverted, reverted.BlockHash, 120)},
	}}
	worker := testWorker(t, source, engine, clock)
	cycle, err := worker.RunOnce(context.Background())
	if err != nil || cycle.FinalityConfirmed != 1 {
		t.Fatalf("reverted finality cycle=%+v err=%v", cycle, err)
	}
	finalized, _ := engine.Execution(expected.ExecutionID)
	if finalized.FinalityCheckedAt == nil || finalized.FinalityCheckedHead != 120 {
		t.Fatalf("finalized revert = %+v", finalized)
	}
}

func TestWorkerReopensRevertedReceiptWhenCanonicalBlockChanges(t *testing.T) {
	t.Parallel()
	clock := &testClock{now: time.Date(2026, 8, 28, 16, 45, 0, 0, time.UTC)}
	engine, err := Open(filepath.Join(t.TempDir(), "reverted-reorg.log"), testConfig(clock))
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	bootstrapHealthy(t, engine, clock, 120)
	expected := testExpected()
	if _, err := engine.RegisterBroadcast(context.Background(), expected); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.ReconcileReceipt(context.Background(), expected.ExecutionID, receiptQuorum(expected, 100, false), nil); err != nil {
		t.Fatal(err)
	}
	reverted, _ := engine.Execution(expected.ExecutionID)
	source := &workerSource{blocks: map[string]ReorgResult{
		expected.ExecutionID: {Evidence: canonicalBlockEvidence(reverted, testHash(999), 120)},
	}}
	cycle, err := testWorker(t, source, engine, clock).RunOnce(context.Background())
	if err != nil || cycle.ReorgsReopened != 1 {
		t.Fatalf("reverted reorg cycle=%+v err=%v", cycle, err)
	}
	reopened, _ := engine.Execution(expected.ExecutionID)
	if reopened.State != ExecutionPendingChainRecovery || reopened.ReorgEvidenceDigest == "" || reopened.LedgerTransactionID != "" {
		t.Fatalf("reopened reverted execution = %+v", reopened)
	}
}

func TestWorkerAcceptsCanonicalHeadAheadOfLastHealthSnapshot(t *testing.T) {
	t.Parallel()
	clock := &testClock{now: time.Date(2026, 8, 12, 13, 17, 0, 0, time.UTC)}
	engine, err := Open(filepath.Join(t.TempDir(), "head-lag.log"), testConfig(clock))
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	bootstrapHealthy(t, engine, clock, 120)
	expected := testExpected()
	if _, err := engine.RegisterBroadcast(context.Background(), expected); err != nil {
		t.Fatal(err)
	}
	ledger := settlement(clock.Now(), expected.ExecutionID)
	if _, err := engine.ReconcileReceipt(context.Background(), expected.ExecutionID, receiptQuorum(expected, 100, true), &ledger); err != nil {
		t.Fatal(err)
	}
	settled, _ := engine.Execution(expected.ExecutionID)
	source := &workerSource{blocks: map[string]ReorgResult{
		expected.ExecutionID: {Evidence: canonicalBlockEvidence(settled, settled.BlockHash, 125)},
	}}
	cycle, err := testWorker(t, source, engine, clock).RunOnce(context.Background())
	if err != nil || cycle.FinalityConfirmed != 1 || cycle.Deferred != 0 {
		t.Fatalf("normal head advance was not reconciled: cycle=%+v err=%v", cycle, err)
	}
}

func TestFinalityCheckpointUsesRollbackCompatibleJournalEvent(t *testing.T) {
	t.Parallel()
	clock := &testClock{now: time.Date(2026, 8, 12, 13, 18, 0, 0, time.UTC)}
	engine, err := Open(filepath.Join(t.TempDir(), "rollback.log"), testConfig(clock))
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	bootstrapHealthy(t, engine, clock, 120)
	expected := testExpected()
	if _, err := engine.RegisterBroadcast(context.Background(), expected); err != nil {
		t.Fatal(err)
	}
	ledger := settlement(clock.Now(), expected.ExecutionID)
	if _, err := engine.ReconcileReceipt(context.Background(), expected.ExecutionID, receiptQuorum(expected, 100, true), &ledger); err != nil {
		t.Fatal(err)
	}
	settled, _ := engine.Execution(expected.ExecutionID)
	if _, err := engine.ConfirmFinality(context.Background(), expected.ExecutionID, canonicalBlockEvidence(settled, settled.BlockHash, 120)); err != nil {
		t.Fatal(err)
	}
	events := engine.journal.Events()
	if got := events[len(events)-1].Kind; got != eventExecutionResolved {
		t.Fatalf("finality journal kind = %q, want rollback-compatible %q", got, eventExecutionResolved)
	}
}

func TestWorkerReorgAtomicallyReversesSettlement(t *testing.T) {
	t.Parallel()
	clock := &testClock{now: time.Date(2026, 8, 12, 13, 20, 0, 0, time.UTC)}
	engine, err := Open(filepath.Join(t.TempDir(), "worker-reorg.log"), testConfig(clock))
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	bootstrapHealthy(t, engine, clock, 120)
	expected := testExpected()
	if _, err := engine.RegisterBroadcast(context.Background(), expected); err != nil {
		t.Fatal(err)
	}
	ledger := settlement(clock.Now(), expected.ExecutionID)
	if _, err := engine.ReconcileReceipt(context.Background(), expected.ExecutionID, receiptQuorum(expected, 100, true), &ledger); err != nil {
		t.Fatal(err)
	}
	settled, _ := engine.Execution(expected.ExecutionID)
	changedHash := testHash(1600)
	source := &workerSource{blocks: map[string]ReorgResult{
		expected.ExecutionID: {Evidence: canonicalBlockEvidence(settled, changedHash, 120)},
	}}
	cycle, err := testWorker(t, source, engine, clock).RunOnce(context.Background())
	if err != nil || cycle.ReorgsReopened != 1 {
		t.Fatalf("RunOnce() = %+v, %v", cycle, err)
	}
	reopened, _ := engine.Execution(expected.ExecutionID)
	if reopened.State != ExecutionPendingChainRecovery || reopened.CorrectionTransactionID == "" || engine.Balance(expected.OrganizationID, "agent_service_expense") != "0" {
		t.Fatalf("reopened execution = %+v balance=%s", reopened, engine.Balance(expected.OrganizationID, "agent_service_expense"))
	}
	correction, ok := engine.LedgerTransaction(reopened.CorrectionTransactionID)
	if !ok || correction.ReversesTransactionID != ledger.TransactionID || correction.TransactionID != derivedLedgerID("correction", expected.ExecutionID, ledger.TransactionID, changedHash) {
		t.Fatalf("correction = %+v", correction)
	}

	source.receipts = map[string]ReceiptResult{
		expected.ExecutionID: {Evidence: receiptQuorum(expected, settled.BlockNumber, true)},
	}
	cycle, err = testWorker(t, source, engine, clock).RunOnce(context.Background())
	if err != nil || cycle.Deferred != 1 || cycle.Settled != 0 {
		t.Fatalf("stale removed-block cycle = %+v, %v", cycle, err)
	}
	stillPending, _ := engine.Execution(expected.ExecutionID)
	if stillPending.State != ExecutionPendingChainRecovery || engine.Balance(expected.OrganizationID, "agent_service_expense") != "0" {
		t.Fatalf("stale removed receipt changed state: %+v balance=%s", stillPending, engine.Balance(expected.OrganizationID, "agent_service_expense"))
	}

	source.receipts[expected.ExecutionID] = ReceiptResult{Evidence: receiptQuorum(expected, settled.BlockNumber+1, true)}
	cycle, err = testWorker(t, source, engine, clock).RunOnce(context.Background())
	if err != nil || cycle.Settled != 1 || cycle.Deferred != 0 {
		t.Fatalf("fresh post-reorg cycle = %+v, %v", cycle, err)
	}
	fresh, _ := engine.Execution(expected.ExecutionID)
	if fresh.State != ExecutionSettled || fresh.BlockNumber != settled.BlockNumber+1 || fresh.LedgerTransactionID == ledger.TransactionID || engine.Balance(expected.OrganizationID, "agent_service_expense") != expected.AmountAtomic {
		t.Fatalf("fresh post-reorg settlement = %+v balance=%s", fresh, engine.Balance(expected.OrganizationID, "agent_service_expense"))
	}
}

func TestWorkerSkipsUnsafeChainAndBoundsProviderWait(t *testing.T) {
	t.Parallel()
	clock := &testClock{now: time.Date(2026, 8, 12, 13, 25, 0, 0, time.UTC)}
	engine, err := Open(filepath.Join(t.TempDir(), "timeout.log"), testConfig(clock))
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	source := &workerSource{wait: true}
	worker := testWorker(t, source, engine, clock)
	cycle, err := worker.RunOnce(context.Background())
	if err != nil || !cycle.SkippedForChain {
		t.Fatalf("startup cycle = %+v, %v", cycle, err)
	}
	bootstrapHealthy(t, engine, clock, 100)
	expected := testExpected()
	if _, err := engine.RegisterBroadcast(context.Background(), expected); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	cycle, err = worker.RunOnce(context.Background())
	if err != nil || cycle.Deferred != 1 || time.Since(started) > 500*time.Millisecond {
		t.Fatalf("bounded cycle = %+v, %v elapsed=%s", cycle, err, time.Since(started))
	}
}

func TestWorkerConfigurationAndCancellation(t *testing.T) {
	t.Parallel()
	clock := &testClock{now: time.Date(2026, 8, 12, 13, 30, 0, 0, time.UTC)}
	engine, err := Open(filepath.Join(t.TempDir(), "cancel.log"), testConfig(clock))
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	source := &workerSource{}
	if _, err := NewWorker(nil, engine, WorkerConfig{}); err == nil {
		t.Fatal("nil source was accepted")
	}
	if _, err := NewWorker(source, engine, WorkerConfig{Interval: time.Second, QueryTimeout: time.Second}); err == nil {
		t.Fatal("overlapping query timeout was accepted")
	}
	worker := testWorker(t, source, engine, clock)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := worker.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() cancellation = %v", err)
	}
}
