package reconciliation

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gnanam1990/flowops/pkg/broadcastreceipt"
	"github.com/gnanam1990/flowops/pkg/envelope"
)

const (
	testSender    = "0x1111111111111111111111111111111111111111"
	testAsset     = "0x036cbd53842c5426634e7929541ec2318f3dcf7e"
	testRecipient = "0x2222222222222222222222222222222222222222"
)

type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) Add(duration time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(duration)
	c.mu.Unlock()
}

func testConfig(clock *testClock) Config {
	return Config{
		ChainID: 84532, EscrowContract: testEscrow, EscrowAsset: testEscrowAsset, EscrowReleaseWindow: 3600,
		ObserverQuorum: 2, HaltConfirmations: 2, RecoveryObservations: 2,
		MinConfirmations: 2, MaxHeadSkew: 2, StallThreshold: time.Minute,
		ObservationMaxAge: 20 * time.Second, MaxFutureClockSkew: 5 * time.Second, Clock: clock.Now,
	}
}

func TestConfigRejectsPartialOrInvalidEscrowDeploymentTuple(t *testing.T) {
	t.Parallel()
	clock := &testClock{now: time.Date(2026, 8, 14, 8, 0, 0, 0, time.UTC)}
	partial := testConfig(clock)
	partial.EscrowAsset = ""
	if _, err := Open(filepath.Join(t.TempDir(), "partial.log"), partial); err == nil {
		t.Fatal("partial escrow deployment configuration was accepted")
	}
	identical := testConfig(clock)
	identical.EscrowAsset = identical.EscrowContract
	if _, err := Open(filepath.Join(t.TempDir(), "identical.log"), identical); err == nil {
		t.Fatal("identical escrow contract and asset were accepted")
	}
}

func testHash(number uint64) string { return fmt.Sprintf("0x%064x", number) }

func healthyObservations(now time.Time, anchor, head uint64) []Observation {
	return []Observation{
		{Provider: "rpc_alpha", ChainID: 84532, HeadNumber: head, HeadHash: testHash(head), HeadTime: now.Add(-time.Second), AnchorNumber: anchor, AnchorHash: testHash(anchor), AnchorTime: now.Add(-2 * time.Second), ObservedAt: now},
		{Provider: "rpc_beta", ChainID: 84532, HeadNumber: head + 1, HeadHash: testHash(head + 1), HeadTime: now.Add(-time.Second), AnchorNumber: anchor, AnchorHash: testHash(anchor), AnchorTime: now.Add(-2 * time.Second), ObservedAt: now},
	}
}

func bootstrapHealthy(t *testing.T, engine *Engine, clock *testClock, anchor uint64) {
	t.Helper()
	status, err := engine.Observe(context.Background(), healthyObservations(clock.Now(), anchor, anchor+1))
	if err != nil || status.State != StateRecovering {
		t.Fatalf("first recovery snapshot = %+v, %v", status, err)
	}
	clock.Add(time.Second)
	status, err = engine.Observe(context.Background(), healthyObservations(clock.Now(), anchor+1, anchor+2))
	if err != nil || !status.ReadyForManualResume {
		t.Fatalf("stable recovery snapshot = %+v, %v", status, err)
	}
	status, err = engine.Resume(context.Background(), "operator_alice")
	if err != nil || status.State != StateHealthy {
		t.Fatalf("resume = %+v, %v", status, err)
	}
}

func TestManualResumeRetrySurvivesHealthyObserverTick(t *testing.T) {
	t.Parallel()
	clock := &testClock{now: time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)}
	engine, err := Open(filepath.Join(t.TempDir(), "reconciliation.log"), testConfig(clock))
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	bootstrapHealthy(t, engine, clock, 100)
	clock.Add(time.Second)
	if _, err := engine.Observe(context.Background(), healthyObservations(clock.Now(), 102, 103)); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Resume(context.Background(), "operator_alice"); err != nil {
		t.Fatalf("resume retry after observer tick = %v", err)
	}
}

func TestLegacyChainStatusReplayNormalizesObserverMetadata(t *testing.T) {
	t.Parallel()
	clock := &testClock{now: time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)}
	path := filepath.Join(t.TempDir(), "reconciliation.log")
	journal, err := openJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	observedAt := clock.Now().Add(-time.Second)
	legacy := ChainStatus{
		State: StateHalted, Reason: "legacy chain halt", StateChangedAt: observedAt,
		LastTrusted: &Checkpoint{BlockNumber: 99, BlockHash: testHash(99), BlockTime: observedAt, ObservedAt: observedAt},
	}
	if _, err := journal.append(context.Background(), observedAt, eventChainStatus, "legacy", chainPayload{Status: legacy}); err != nil {
		t.Fatal(err)
	}
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}
	engine, err := Open(path, testConfig(clock))
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	status := engine.Status()
	if status.RequiredObserverQuorum != 2 || !status.LastObservationAt.Equal(observedAt) {
		t.Fatalf("normalized legacy status = %+v", status)
	}
}

func testExpected() ExpectedExecution {
	return ExpectedExecution{
		ExecutionID: "exec_1", OrganizationID: "org_acme", AgentID: "agent_research", TaskID: "task_9",
		IntentDigest: testHash(900), TransactionHash: testHash(901), ChainID: 84532,
		Sender: testSender, Asset: testAsset, Recipient: testRecipient, AmountAtomic: "100",
	}
}

func testBroadcastAttestation(t *testing.T, expected ExpectedExecution, broadcastAt time.Time) BroadcastAttestation {
	t.Helper()
	seed, err := hex.DecodeString(strings.Repeat("29", ed25519.SeedSize))
	if err != nil {
		t.Fatal(err)
	}
	privateKey := ed25519.NewKeyFromSeed(seed)
	authorization := envelope.Authorization{
		Version: envelope.Version, AuthorizationID: "auth_1", OrganizationID: expected.OrganizationID, CustomerID: "customer_acme",
		AgentID: expected.AgentID, TaskID: expected.TaskID, ActionID: "action_1", Rail: envelope.RailDirect,
		ChainID: expected.ChainID, Recipient: expected.Recipient, Asset: expected.Asset, AmountAtomic: expected.AmountAtomic,
		Resource: "direct USDC transfer", PolicyVersion: "policy_1", Nonce: testHash(898),
		IssuedAt: broadcastAt.Add(-time.Minute).Unix(), ExpiresAt: broadcastAt.Add(time.Minute).Unix(),
	}
	authorizationDigest, err := authorization.Digest()
	if err != nil {
		t.Fatal(err)
	}
	signed, err := broadcastreceipt.Sign(broadcastreceipt.Receipt{
		Version: broadcastreceipt.Version, OrganizationID: expected.OrganizationID, CustomerID: "customer_acme",
		AuthorizationID: authorization.AuthorizationID, AuthorizationDigest: "0x" + hex.EncodeToString(authorizationDigest[:]), TransactionHash: expected.TransactionHash,
		Sender: expected.Sender, Outcome: broadcastreceipt.OutcomeAmbiguous, BroadcastAt: broadcastAt.Unix(),
	}, "customer_signer_1", privateKey)
	if err != nil {
		t.Fatal(err)
	}
	return BroadcastAttestation{SignedReceipt: signed, Authorization: authorization, PublicKeyB64: base64.StdEncoding.EncodeToString(privateKey.Public().(ed25519.PublicKey))}
}

func receiptQuorum(expected ExpectedExecution, block uint64, success bool) []ReceiptEvidence {
	receipt := ReceiptEvidence{
		ChainID: expected.ChainID, TransactionHash: expected.TransactionHash, BlockNumber: block,
		BlockHash: testHash(block), ConfirmedHead: block + 1, Success: success,
		Sender: expected.Sender, Asset: expected.Asset, Recipient: expected.Recipient, AmountAtomic: expected.AmountAtomic,
	}
	alpha, beta := receipt, receipt
	alpha.Provider, beta.Provider = "rpc_alpha", "rpc_beta"
	return []ReceiptEvidence{alpha, beta}
}

func TestAttestedBroadcastRegistersDuringHaltAndSurvivesRestart(t *testing.T) {
	t.Parallel()
	clock := &testClock{now: time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)}
	path := filepath.Join(t.TempDir(), "reconciliation.log")
	engine, err := Open(path, testConfig(clock))
	if err != nil {
		t.Fatal(err)
	}
	broadcastAt := clock.Now().Add(-time.Second)
	expected := testExpected()
	attestation := testBroadcastAttestation(t, expected, broadcastAt)
	execution, err := engine.RegisterAttestedBroadcast(context.Background(), expected, attestation)
	if err != nil {
		t.Fatal(err)
	}
	if execution.State != ExecutionPendingChainRecovery || !execution.BroadcastAt.Equal(broadcastAt) {
		t.Fatalf("halt registration = %+v", execution)
	}
	if _, err := engine.RegisterBroadcast(context.Background(), ExpectedExecution{ExecutionID: "exec_other", OrganizationID: expected.OrganizationID, AgentID: expected.AgentID, TaskID: expected.TaskID, IntentDigest: expected.IntentDigest, TransactionHash: testHash(902), ChainID: expected.ChainID, Sender: expected.Sender, Asset: expected.Asset, Recipient: expected.Recipient, AmountAtomic: expected.AmountAtomic}); !errors.Is(err, ErrChainUnavailable) {
		t.Fatalf("ordinary broadcast during halt = %v", err)
	}
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := Open(path, testConfig(clock))
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	replayed, err := restarted.RegisterAttestedBroadcast(context.Background(), expected, attestation)
	if err != nil || replayed.State != ExecutionPendingChainRecovery || !replayed.BroadcastAt.Equal(broadcastAt) || replayed.BroadcastAttestation == nil || !equalJSON(*replayed.BroadcastAttestation, attestation) {
		t.Fatalf("replayed registration = %+v, %v", replayed, err)
	}
}

func TestAttestedBroadcastRejectsAuthorizationAndHashConflicts(t *testing.T) {
	t.Parallel()
	clock := &testClock{now: time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)}
	engine, err := Open(filepath.Join(t.TempDir(), "reconciliation.log"), testConfig(clock))
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	expected := testExpected()
	if _, err := engine.RegisterAttestedBroadcast(context.Background(), expected, testBroadcastAttestation(t, expected, clock.Now())); err != nil {
		t.Fatal(err)
	}
	changedHash := expected
	changedHash.TransactionHash = testHash(902)
	if _, err := engine.RegisterAttestedBroadcast(context.Background(), changedHash, testBroadcastAttestation(t, changedHash, clock.Now())); !errors.Is(err, ErrConflict) {
		t.Fatalf("authorization rebound to another hash: %v", err)
	}
	changedExecution := expected
	changedExecution.ExecutionID = "exec_other"
	if _, err := engine.RegisterAttestedBroadcast(context.Background(), changedExecution, testBroadcastAttestation(t, changedExecution, clock.Now())); !errors.Is(err, ErrConflict) {
		t.Fatalf("hash rebound to another execution: %v", err)
	}
}

func TestAttestedBroadcastEngineRejectsUnverifiedProof(t *testing.T) {
	t.Parallel()
	clock := &testClock{now: time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)}
	engine, err := Open(filepath.Join(t.TempDir(), "reconciliation.log"), testConfig(clock))
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	expected := testExpected()
	attestation := testBroadcastAttestation(t, expected, clock.Now())
	attestation.SignedReceipt.Signature = "0x" + strings.Repeat("0", 128)
	if _, err := engine.RegisterAttestedBroadcast(context.Background(), expected, attestation); err == nil {
		t.Fatal("engine accepted an unverified customer attestation")
	}
	if len(engine.Executions()) != 0 {
		t.Fatal("invalid attestation changed reconciliation state")
	}
}

func TestAttestedBroadcastEngineRejectsAuthorizationFieldSubstitution(t *testing.T) {
	t.Parallel()
	clock := &testClock{now: time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)}
	engine, err := Open(filepath.Join(t.TempDir(), "reconciliation.log"), testConfig(clock))
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	expected := testExpected()
	for name, mutate := range map[string]func(*ExpectedExecution){
		"agent":     func(value *ExpectedExecution) { value.AgentID = "agent_other" },
		"task":      func(value *ExpectedExecution) { value.TaskID = "task_other" },
		"chain":     func(value *ExpectedExecution) { value.ChainID = 8453 },
		"asset":     func(value *ExpectedExecution) { value.Asset = "0x3333333333333333333333333333333333333333" },
		"recipient": func(value *ExpectedExecution) { value.Recipient = "0x3333333333333333333333333333333333333333" },
		"amount":    func(value *ExpectedExecution) { value.AmountAtomic = "101" },
	} {
		t.Run(name, func(t *testing.T) {
			changed := expected
			mutate(&changed)
			if _, err := engine.RegisterAttestedBroadcast(context.Background(), changed, testBroadcastAttestation(t, expected, clock.Now())); err == nil {
				t.Fatal("authorization field substitution reached reconciliation")
			}
		})
	}
	if len(engine.Executions()) != 0 {
		t.Fatal("substitution attempts changed reconciliation state")
	}
}

func TestAttestedBroadcastEngineRejectsProtocolRailSubstitution(t *testing.T) {
	t.Parallel()
	clock := &testClock{now: time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)}
	engine, err := Open(filepath.Join(t.TempDir(), "reconciliation.log"), testConfig(clock))
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	expected := testExpected()
	attestation := testBroadcastAttestation(t, expected, clock.Now())
	attestation.Authorization.Rail = envelope.RailX402
	digest, err := attestation.Authorization.Digest()
	if err != nil {
		t.Fatal(err)
	}
	receipt := attestation.SignedReceipt.Receipt
	receipt.AuthorizationDigest = "0x" + hex.EncodeToString(digest[:])
	seed, _ := hex.DecodeString(strings.Repeat("29", ed25519.SeedSize))
	attestation.SignedReceipt, err = broadcastreceipt.Sign(receipt, "customer_signer_1", ed25519.NewKeyFromSeed(seed))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.RegisterAttestedBroadcast(context.Background(), expected, attestation); err == nil {
		t.Fatal("x402 authorization entered the direct-USDC reconciler")
	}
}

func TestAttestationSurvivesLegacyResolutionAfterRollback(t *testing.T) {
	t.Parallel()
	clock := &testClock{now: time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)}
	path := filepath.Join(t.TempDir(), "reconciliation.log")
	engine, err := Open(path, testConfig(clock))
	if err != nil {
		t.Fatal(err)
	}
	expected := testExpected()
	attestation := testBroadcastAttestation(t, expected, clock.Now())
	execution, err := engine.RegisterAttestedBroadcast(context.Background(), expected, attestation)
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}

	// Simulate an older rollback binary: it ignored the additive attestation
	// field, then appended a terminal execution using its legacy schema.
	legacyJournal, err := openJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	legacyResolution := execution
	legacyResolution.BroadcastAttestation = nil
	legacyResolution.State = ExecutionReverted
	resolvedAt := clock.Now().Add(time.Second)
	legacyResolution.ResolvedAt = &resolvedAt
	legacyResolution.Resolution = "transaction reverted"
	if _, err := legacyJournal.append(context.Background(), resolvedAt, eventExecutionResolved, expected.ExecutionID, executionPayload{Execution: legacyResolution}); err != nil {
		t.Fatal(err)
	}
	if err := legacyJournal.Close(); err != nil {
		t.Fatal(err)
	}

	restarted, err := Open(path, testConfig(clock))
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	resolved, ok := restarted.Execution(expected.ExecutionID)
	if !ok || resolved.State != ExecutionReverted || resolved.BroadcastAttestation == nil || !equalJSON(*resolved.BroadcastAttestation, attestation) {
		t.Fatalf("legacy resolution lost proof: %+v exists=%v", resolved, ok)
	}
}

func settlement(now time.Time, executionID string) LedgerTransaction {
	return LedgerTransaction{
		TransactionID: "ledger_settlement_1", OrganizationID: "org_acme", Kind: LedgerSettlement,
		ReferenceID: executionID, RecordedAt: now,
		Postings: []Posting{{Account: "agent_service_expense", AmountAtomic: "100"}, {Account: "pending_settlement", AmountAtomic: "-100"}},
	}
}

func TestHaltDrillPreservesAmbiguousExecutionAndRecoversOnce(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 11, 16, 0, 0, 0, time.UTC)
	clock := &testClock{now: now}
	path := filepath.Join(t.TempDir(), "reconciliation.log")
	engine, err := Open(path, testConfig(clock))
	if err != nil {
		t.Fatal(err)
	}

	initial := engine.Status()
	if initial.State != StateSuspectedStall || !initial.AuthorizationsPaused || !initial.BroadcastsPaused || !initial.FinalizationPaused {
		t.Fatalf("startup status is not fail closed: %+v", initial)
	}
	if err := engine.CheckChain(context.Background(), 84532); !errors.Is(err, ErrChainUnavailable) {
		t.Fatalf("startup CheckChain() = %v", err)
	}
	bootstrapHealthy(t, engine, clock, 100)

	expected := testExpected()
	broadcast, err := engine.RegisterBroadcast(context.Background(), expected)
	if err != nil || broadcast.State != ExecutionBroadcast {
		t.Fatalf("RegisterBroadcast() = %+v, %v", broadcast, err)
	}
	if duplicate, err := engine.RegisterBroadcast(context.Background(), expected); err != nil || duplicate.State != ExecutionBroadcast {
		t.Fatalf("idempotent broadcast = %+v, %v", duplicate, err)
	}

	clock.Add(time.Second)
	stale := healthyObservations(clock.Now(), 102, 103)
	for index := range stale {
		stale[index].HeadTime = clock.Now().Add(-10 * time.Minute)
	}
	status, err := engine.Observe(context.Background(), stale)
	if err != nil || status.State != StateSuspectedStall || !status.AuthorizationsPaused {
		t.Fatalf("suspected stall = %+v, %v", status, err)
	}
	if _, err := engine.ReconcileReceipt(context.Background(), expected.ExecutionID, receiptQuorum(expected, 102, true), ptrLedger(settlement(clock.Now(), expected.ExecutionID))); !errors.Is(err, ErrChainUnavailable) {
		t.Fatalf("stale settlement error = %v", err)
	}
	clock.Add(time.Second)
	status, err = engine.Observe(context.Background(), stale)
	if err != nil || status.State != StateHalted || status.AffectedExecutions != 1 || !status.RefundRecognitionPaused {
		t.Fatalf("halt = %+v, %v", status, err)
	}
	pending, _ := engine.Execution(expected.ExecutionID)
	if pending.State != ExecutionPendingChainRecovery || pending.ResolvedAt != nil {
		t.Fatalf("halted execution = %+v", pending)
	}
	if replay, err := engine.RegisterBroadcast(context.Background(), expected); err != nil || replay.State != ExecutionPendingChainRecovery {
		t.Fatalf("ambiguous idempotent lookup = %+v, %v", replay, err)
	}
	newExecution := expected
	newExecution.ExecutionID = "exec_2"
	newExecution.TransactionHash = testHash(902)
	if _, err := engine.RegisterBroadcast(context.Background(), newExecution); !errors.Is(err, ErrChainUnavailable) {
		t.Fatalf("new broadcast during halt error = %v", err)
	}
	refund := settlement(clock.Now(), expected.ExecutionID)
	refund.TransactionID, refund.Kind = "ledger_refund_1", LedgerRefund
	if _, err := engine.Post(context.Background(), refund); err == nil {
		t.Fatal("refund was fabricated without canonical evidence")
	}
	clock.Add(24 * time.Hour)
	if current, _ := engine.Execution(expected.ExecutionID); current.State != ExecutionPendingChainRecovery {
		t.Fatalf("wall clock changed execution to %+v", current)
	}

	status, err = engine.Observe(context.Background(), healthyObservations(clock.Now(), 103, 104))
	if err != nil || status.State != StateRecovering || status.ReadyForManualResume {
		t.Fatalf("recovery start = %+v, %v", status, err)
	}
	ledger := settlement(clock.Now(), expected.ExecutionID)
	resolved, err := engine.ReconcileReceipt(context.Background(), expected.ExecutionID, receiptQuorum(expected, 103, true), &ledger)
	if err != nil || resolved.State != ExecutionSettled {
		t.Fatalf("reconcile = %+v, %v", resolved, err)
	}
	if engine.Balance("org_acme", "agent_service_expense") != "100" || engine.Balance("org_acme", "pending_settlement") != "-100" {
		t.Fatalf("unexpected ledger balances")
	}
	clock.Add(time.Second)
	status, err = engine.Observe(context.Background(), healthyObservations(clock.Now(), 104, 105))
	if err != nil || !status.ReadyForManualResume || status.AffectedExecutions != 0 {
		t.Fatalf("recovery ready = %+v, %v", status, err)
	}
	status, err = engine.Resume(context.Background(), "operator_alice")
	if err != nil || status.State != StateHealthy {
		t.Fatalf("manual resume = %+v, %v", status, err)
	}
	if again, err := engine.ReconcileReceipt(context.Background(), expected.ExecutionID, receiptQuorum(expected, 103, true), &ledger); err != nil || again.State != ExecutionSettled {
		t.Fatalf("idempotent resolution = %+v, %v", again, err)
	}
	alteredLedger := cloneLedger(ledger)
	alteredLedger.Postings[0].AmountAtomic, alteredLedger.Postings[1].AmountAtomic = "101", "-101"
	if _, err := engine.ReconcileReceipt(context.Background(), expected.ExecutionID, receiptQuorum(expected, 103, true), &alteredLedger); !errors.Is(err, ErrConflict) {
		t.Fatalf("altered idempotent resolution error = %v", err)
	}
	if engine.Balance("org_acme", "agent_service_expense") != "100" {
		t.Fatal("idempotent resolution double-posted the ledger")
	}
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}

	restarted, err := Open(path, testConfig(clock))
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	if restarted.Status().State != StateSuspectedStall || restarted.Balance("org_acme", "agent_service_expense") != "100" {
		t.Fatalf("restart did not pause and replay exactly once: status=%+v balance=%s", restarted.Status(), restarted.Balance("org_acme", "agent_service_expense"))
	}
	if replayed, _ := restarted.Execution(expected.ExecutionID); replayed.State != ExecutionSettled {
		t.Fatalf("replayed execution = %+v", replayed)
	}
}

func TestLedgerIsBalancedIdempotentAppendOnlyAndTenantScoped(t *testing.T) {
	t.Parallel()
	clock := &testClock{now: time.Date(2026, 8, 11, 17, 0, 0, 0, time.UTC)}
	path := filepath.Join(t.TempDir(), "ledger.log")
	engine, err := Open(path, testConfig(clock))
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()

	transaction := LedgerTransaction{
		TransactionID: "ledger_reservation_1", OrganizationID: "org_acme", Kind: LedgerReservation,
		ReferenceID: "intent_1", RecordedAt: clock.Now(),
		Postings: []Posting{{Account: "reserved_agent_usdc", AmountAtomic: "250"}, {Account: "agent_usdc", AmountAtomic: "-250"}},
	}
	if _, err := engine.Post(context.Background(), transaction); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Post(context.Background(), transaction); err != nil {
		t.Fatalf("idempotent Post() error = %v", err)
	}
	conflict := cloneLedger(transaction)
	conflict.Postings[0].AmountAtomic, conflict.Postings[1].AmountAtomic = "251", "-251"
	if _, err := engine.Post(context.Background(), conflict); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflicting Post() error = %v", err)
	}
	unbalanced := cloneLedger(transaction)
	unbalanced.TransactionID = "ledger_bad"
	unbalanced.Postings[1].AmountAtomic = "-249"
	if _, err := engine.Post(context.Background(), unbalanced); err == nil {
		t.Fatal("unbalanced transaction succeeded")
	}
	correction := LedgerTransaction{
		TransactionID: "ledger_correction_1", OrganizationID: "org_acme", Kind: LedgerCorrection,
		ReferenceID: "correction_1", ReversesTransactionID: transaction.TransactionID, RecordedAt: clock.Now(),
		Postings: []Posting{{Account: "reserved_agent_usdc", AmountAtomic: "-250"}, {Account: "agent_usdc", AmountAtomic: "250"}},
	}
	if _, err := engine.Post(context.Background(), correction); err != nil {
		t.Fatal(err)
	}
	if engine.Balance("org_acme", "agent_usdc") != "0" || engine.Balance("org_other", "agent_usdc") != "0" {
		t.Fatalf("balances were not corrected or tenant scoped")
	}
	if original, ok := engine.LedgerTransaction(transaction.TransactionID); !ok || original.Kind != LedgerReservation {
		t.Fatalf("original transaction was overwritten: %+v", original)
	}
	badCorrection := cloneLedger(correction)
	badCorrection.TransactionID = "ledger_correction_2"
	badCorrection.Postings[0].AmountAtomic, badCorrection.Postings[1].AmountAtomic = "-249", "249"
	if _, err := engine.Post(context.Background(), badCorrection); err == nil {
		t.Fatal("non-reversing correction succeeded")
	}
	directSettlement := settlement(clock.Now(), "exec_unknown")
	if _, err := engine.Post(context.Background(), directSettlement); err == nil {
		t.Fatal("settlement posted without canonical receipt evidence")
	}
}

func TestObserverDisagreementAndCheckpointRegressionFailClosed(t *testing.T) {
	t.Parallel()
	clock := &testClock{now: time.Date(2026, 8, 11, 18, 0, 0, 0, time.UTC)}
	engine, err := Open(filepath.Join(t.TempDir(), "observer.log"), testConfig(clock))
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	bootstrapHealthy(t, engine, clock, 200)

	disagree := healthyObservations(clock.Now(), 202, 203)
	disagree[1].AnchorHash = testHash(999)
	status, err := engine.Observe(context.Background(), disagree)
	if err != nil || status.State != StateSuspectedStall || !strings.Contains(status.Reason, "disagree") {
		t.Fatalf("disagreement = %+v, %v", status, err)
	}
	if _, err := engine.Resume(context.Background(), "operator_alice"); !errors.Is(err, ErrResumeBlocked) {
		t.Fatalf("unsafe resume error = %v", err)
	}
	clock.Add(time.Second)
	status, err = engine.Observe(context.Background(), healthyObservations(clock.Now(), 199, 204))
	if err != nil || status.State != StateHalted || !strings.Contains(status.Reason, "regressed") {
		t.Fatalf("checkpoint regression = %+v, %v", status, err)
	}
}

func TestObserverProgressIsDurableAndManualResumeReplayIsIdempotent(t *testing.T) {
	t.Parallel()
	clock := &testClock{now: time.Date(2026, 8, 12, 10, 0, 0, 0, time.UTC)}
	path := filepath.Join(t.TempDir(), "observer-progress.log")
	engine, err := Open(path, testConfig(clock))
	if err != nil {
		t.Fatal(err)
	}
	status, err := engine.Observe(context.Background(), healthyObservations(clock.Now(), 800, 801))
	if err != nil {
		t.Fatal(err)
	}
	if status.RequiredObserverQuorum != 2 || status.RespondingObservers != 2 || !status.LastObservationAt.Equal(clock.Now()) {
		t.Fatalf("observer progress = %+v", status)
	}
	clock.Add(time.Second)
	status, err = engine.Observe(context.Background(), healthyObservations(clock.Now(), 801, 802))
	if err != nil || !status.ReadyForManualResume {
		t.Fatalf("recovery readiness = %+v, %v", status, err)
	}
	first, err := engine.Resume(context.Background(), "operator_alice")
	if err != nil {
		t.Fatal(err)
	}
	second, err := engine.Resume(context.Background(), "operator_alice")
	if err != nil || !equalJSON(first, second) {
		t.Fatalf("resume replay = %+v, %v", second, err)
	}
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := Open(path, testConfig(clock))
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	replayed := restarted.Status()
	if replayed.RequiredObserverQuorum != 2 || replayed.RespondingObservers != 2 || replayed.LastObservationAt.IsZero() {
		t.Fatalf("replayed observer progress = %+v", replayed)
	}
}

func TestExpiredObserverHeartbeatBlocksWithoutWaitingForAnotherPoll(t *testing.T) {
	t.Parallel()
	clock := &testClock{now: time.Date(2026, 8, 11, 18, 30, 0, 0, time.UTC)}
	engine, err := Open(filepath.Join(t.TempDir(), "heartbeat.log"), testConfig(clock))
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	bootstrapHealthy(t, engine, clock, 250)
	clock.Add(21 * time.Second)
	status := engine.Status()
	if status.State != StateSuspectedStall || !status.AuthorizationsPaused {
		t.Fatalf("expired heartbeat status = %+v", status)
	}
	if err := engine.CheckChain(context.Background(), 84532); !errors.Is(err, ErrChainUnavailable) {
		t.Fatalf("expired heartbeat CheckChain() = %v", err)
	}
	if _, err := engine.RegisterBroadcast(context.Background(), testExpected()); !errors.Is(err, ErrChainUnavailable) {
		t.Fatalf("expired heartbeat broadcast error = %v", err)
	}
	status, err = engine.Observe(context.Background(), healthyObservations(clock.Now(), 252, 253))
	if err != nil || status.State != StateRecovering {
		t.Fatalf("post-gap observation skipped recovery gate: %+v, %v", status, err)
	}
}

func TestRestartQuarantinesPreCrashBroadcastForReconciliation(t *testing.T) {
	t.Parallel()
	clock := &testClock{now: time.Date(2026, 8, 11, 18, 45, 0, 0, time.UTC)}
	path := filepath.Join(t.TempDir(), "restart-broadcast.log")
	engine, err := Open(path, testConfig(clock))
	if err != nil {
		t.Fatal(err)
	}
	bootstrapHealthy(t, engine, clock, 275)
	expected := testExpected()
	if _, err := engine.RegisterBroadcast(context.Background(), expected); err != nil {
		t.Fatal(err)
	}
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := Open(path, testConfig(clock))
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	execution, _ := restarted.Execution(expected.ExecutionID)
	if restarted.Status().State != StateSuspectedStall || execution.State != ExecutionPendingChainRecovery {
		t.Fatalf("restart state=%+v execution=%+v", restarted.Status(), execution)
	}
	if replay, err := restarted.RegisterBroadcast(context.Background(), expected); err != nil || replay.State != ExecutionPendingChainRecovery {
		t.Fatalf("restart idempotent lookup = %+v, %v", replay, err)
	}
}

func TestReceiptQuorumRejectsOneProviderMismatchAndLowConfirmations(t *testing.T) {
	t.Parallel()
	clock := &testClock{now: time.Date(2026, 8, 11, 19, 0, 0, 0, time.UTC)}
	engine, err := Open(filepath.Join(t.TempDir(), "receipt.log"), testConfig(clock))
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	bootstrapHealthy(t, engine, clock, 300)
	expected := testExpected()
	if _, err := engine.RegisterBroadcast(context.Background(), expected); err != nil {
		t.Fatal(err)
	}
	evidence := receiptQuorum(expected, 301, true)
	evidence[1].Recipient = testSender
	ledger := settlement(clock.Now(), expected.ExecutionID)
	if _, err := engine.ReconcileReceipt(context.Background(), expected.ExecutionID, evidence, &ledger); !errors.Is(err, ErrUnsafeFinality) {
		t.Fatalf("mismatched receipt error = %v", err)
	}
	evidence = receiptQuorum(expected, 301, true)
	for index := range evidence {
		evidence[index].ConfirmedHead = evidence[index].BlockNumber
	}
	if _, err := engine.ReconcileReceipt(context.Background(), expected.ExecutionID, evidence, &ledger); !errors.Is(err, ErrUnsafeFinality) {
		t.Fatalf("low-confirmation error = %v", err)
	}
	if _, ok := engine.LedgerTransaction(ledger.TransactionID); ok {
		t.Fatal("unsafe evidence posted ledger transaction")
	}
}

func TestCanonicalReorgReversesLedgerAndRequiresFreshOutcome(t *testing.T) {
	t.Parallel()
	clock := &testClock{now: time.Date(2026, 8, 11, 19, 30, 0, 0, time.UTC)}
	engine, err := Open(filepath.Join(t.TempDir(), "reorg.log"), testConfig(clock))
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	bootstrapHealthy(t, engine, clock, 600)
	expected := testExpected()
	if _, err := engine.RegisterBroadcast(context.Background(), expected); err != nil {
		t.Fatal(err)
	}
	firstLedger := settlement(clock.Now(), expected.ExecutionID)
	if _, err := engine.ReconcileReceipt(context.Background(), expected.ExecutionID, receiptQuorum(expected, 601, true), &firstLedger); err != nil {
		t.Fatal(err)
	}
	clock.Add(time.Second)
	if _, err := engine.Observe(context.Background(), healthyObservations(clock.Now(), 613, 614)); err != nil {
		t.Fatal(err)
	}
	reorg := []ReorgEvidence{
		{Provider: "rpc_alpha", ChainID: 84532, TransactionHash: expected.TransactionHash, OriginalBlockNumber: 601, OriginalBlockHash: testHash(601), CanonicalBlockHash: testHash(1601), ObservedHead: 613},
		{Provider: "rpc_beta", ChainID: 84532, TransactionHash: expected.TransactionHash, OriginalBlockNumber: 601, OriginalBlockHash: testHash(601), CanonicalBlockHash: testHash(1601), ObservedHead: 614},
	}
	correction := LedgerTransaction{
		TransactionID: "ledger_reorg_1", OrganizationID: "org_acme", Kind: LedgerCorrection,
		ReferenceID: expected.ExecutionID, ReversesTransactionID: firstLedger.TransactionID, RecordedAt: clock.Now(),
		Postings: []Posting{{Account: "agent_service_expense", AmountAtomic: "-100"}, {Account: "pending_settlement", AmountAtomic: "100"}},
	}
	reopened, err := engine.ReopenReorg(context.Background(), expected.ExecutionID, reorg, correction)
	if err != nil || reopened.State != ExecutionPendingChainRecovery || reopened.CorrectionTransactionID != correction.TransactionID {
		t.Fatalf("ReopenReorg() = %+v, %v", reopened, err)
	}
	if engine.Status().State != StateRecovering || engine.Balance("org_acme", "agent_service_expense") != "0" {
		t.Fatalf("reorg did not pause and reverse: status=%+v balance=%s", engine.Status(), engine.Balance("org_acme", "agent_service_expense"))
	}
	if duplicate, err := engine.ReopenReorg(context.Background(), expected.ExecutionID, reorg, correction); err != nil || duplicate.State != ExecutionPendingChainRecovery {
		t.Fatalf("idempotent ReopenReorg() = %+v, %v", duplicate, err)
	}
	reordered := []ReorgEvidence{reorg[1], reorg[0]}
	if _, err := engine.ReopenReorg(context.Background(), expected.ExecutionID, reordered, correction); err != nil {
		t.Fatalf("reordered quorum changed idempotency: %v", err)
	}
	alteredReorg := append([]ReorgEvidence(nil), reorg...)
	alteredReorg[1].ObservedHead++
	if _, err := engine.ReopenReorg(context.Background(), expected.ExecutionID, alteredReorg, correction); !errors.Is(err, ErrConflict) {
		t.Fatalf("altered reorg replay error = %v", err)
	}
	if _, err := engine.Resume(context.Background(), "operator_alice"); !errors.Is(err, ErrResumeBlocked) {
		t.Fatalf("resume with reorg unresolved = %v", err)
	}
	clock.Add(time.Second)
	if _, err := engine.Observe(context.Background(), healthyObservations(clock.Now(), 614, 615)); err != nil {
		t.Fatal(err)
	}
	secondLedger := settlement(clock.Now(), expected.ExecutionID)
	secondLedger.TransactionID = "ledger_settlement_2"
	if resolved, err := engine.ReconcileReceipt(context.Background(), expected.ExecutionID, receiptQuorum(expected, 602, true), &secondLedger); err != nil || resolved.State != ExecutionSettled || resolved.BlockNumber != 602 {
		t.Fatalf("post-reorg reconciliation = %+v, %v", resolved, err)
	}
	if engine.Balance("org_acme", "agent_service_expense") != "100" {
		t.Fatal("post-reorg settlement balance is not exactly one payment")
	}
	clock.Add(time.Second)
	status, err := engine.Observe(context.Background(), healthyObservations(clock.Now(), 615, 616))
	if err != nil || !status.ReadyForManualResume {
		t.Fatalf("post-reorg recovery = %+v, %v", status, err)
	}
	if _, err := engine.Resume(context.Background(), "operator_alice"); err != nil {
		t.Fatal(err)
	}
	if original, ok := engine.LedgerTransaction(firstLedger.TransactionID); !ok || original.Kind != LedgerSettlement {
		t.Fatal("reorg rewrote the original ledger transaction")
	}
}

func TestJournalRejectsTamperingAndConcurrentOpen(t *testing.T) {
	t.Parallel()
	clock := &testClock{now: time.Date(2026, 8, 11, 20, 0, 0, 0, time.UTC)}
	path := filepath.Join(t.TempDir(), "durable.log")
	engine, err := Open(path, testConfig(clock))
	if err != nil {
		t.Fatal(err)
	}
	transaction := LedgerTransaction{
		TransactionID: "ledger_1", OrganizationID: "org_acme", Kind: LedgerSuspense, ReferenceID: "unknown_1", RecordedAt: clock.Now(),
		Postings: []Posting{{Account: "unclassified_incoming", AmountAtomic: "5"}, {Account: "reconciliation_suspense", AmountAtomic: "-5"}},
	}
	if _, err := engine.Post(context.Background(), transaction); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path, testConfig(clock)); err == nil {
		t.Fatal("concurrent reconciliation journal open succeeded")
	}
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	tampered := strings.Replace(string(body), "unclassified_incoming", "unclassified_outgoing", 1)
	if err := os.WriteFile(path, []byte(tampered), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path, testConfig(clock)); err == nil || !strings.Contains(err.Error(), "hash") {
		t.Fatalf("tampered journal error = %v", err)
	}
}

func TestInvalidJournalTimestampNeverBecomesVisible(t *testing.T) {
	t.Parallel()
	clock := &testClock{}
	engine, err := Open(filepath.Join(t.TempDir(), "invalid-time.log"), testConfig(clock))
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	transaction := LedgerTransaction{
		TransactionID: "ledger_1", OrganizationID: "org_acme", Kind: LedgerSuspense, ReferenceID: "unknown_1",
		RecordedAt: time.Date(2026, 8, 11, 20, 0, 0, 0, time.UTC),
		Postings:   []Posting{{Account: "unclassified_incoming", AmountAtomic: "5"}, {Account: "reconciliation_suspense", AmountAtomic: "-5"}},
	}
	if _, err := engine.Post(context.Background(), transaction); err == nil {
		t.Fatal("zero-time journal event succeeded")
	}
	if _, exists := engine.LedgerTransaction(transaction.TransactionID); exists || engine.Balance("org_acme", "unclassified_incoming") != "0" {
		t.Fatal("failed durable append became visible")
	}
}

func ptrLedger(transaction LedgerTransaction) *LedgerTransaction { return &transaction }
