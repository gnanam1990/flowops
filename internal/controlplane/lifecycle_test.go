package controlplane

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gnanam1990/flowops/internal/policy"
	"github.com/gnanam1990/flowops/pkg/envelope"
)

const (
	controlTestUSDC      = "0x036cbd53842c5426634e7929541ec2318f3dcf7e"
	controlTestRecipient = "0x1111111111111111111111111111111111111111"
)

type fakeClock struct {
	mu  sync.RWMutex
	now time.Time
}

func (c *fakeClock) Now() time.Time      { c.mu.RLock(); defer c.mu.RUnlock(); return c.now }
func (c *fakeClock) Add(d time.Duration) { c.mu.Lock(); c.now = c.now.Add(d); c.mu.Unlock() }

type fakeFreeze struct {
	mu  sync.RWMutex
	err error
}

func (f *fakeFreeze) set(err error) { f.mu.Lock(); f.err = err; f.mu.Unlock() }
func (f *fakeFreeze) Check(context.Context, string, string, string) error {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.err
}

type healthyControlChain struct{}

func (healthyControlChain) CheckChain(context.Context, uint64) error { return nil }

type unavailableControlChain struct{ err error }

func (g unavailableControlChain) CheckChain(context.Context, uint64) error { return g.err }

type policyVersion struct {
	mu      sync.RWMutex
	version string
}

func (v *policyVersion) get() string      { v.mu.RLock(); defer v.mu.RUnlock(); return v.version }
func (v *policyVersion) set(value string) { v.mu.Lock(); v.version = value; v.mu.Unlock() }

type sources struct {
	request atomic.Uint64
	auth    atomic.Uint64
	nonce   atomic.Uint64
}

func (s *sources) requestID() (string, error) {
	return fmt.Sprintf("req_%d", s.request.Add(1)), nil
}
func (s *sources) authorizationID() (string, error) {
	return fmt.Sprintf("auth_%d", s.auth.Add(1)), nil
}
func (s *sources) nextNonce() (string, error) {
	return fmt.Sprintf("0x%064x", s.nonce.Add(1)), nil
}

func controlPolicy(t *testing.T) *policy.Engine {
	t.Helper()
	engine, err := policy.Compile(policy.Config{
		Version: "policy_7", Enabled: true, AllowedChainIDs: []uint64{84532},
		AllowedRails: []envelope.Rail{envelope.RailX402}, AllowedAssets: []string{controlTestUSDC},
		AllowedRecipients: []string{controlTestRecipient}, BlockedCategories: []string{"gambling"},
		PerActionLimitAtomic: "200", AutoApproveThresholdAtomic: "100",
		TaskBudgetAtomic: "200", DailyBudgetAtomic: "1000",
	})
	if err != nil {
		t.Fatal(err)
	}
	return engine
}

func controlKeys(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	seed, err := hex.DecodeString(strings.Repeat("03", ed25519.SeedSize))
	if err != nil {
		t.Fatal(err)
	}
	privateKey := ed25519.NewKeyFromSeed(seed)
	return privateKey.Public().(ed25519.PublicKey), privateKey
}

func newLifecycleForTest(t *testing.T, path string, clock *fakeClock, freeze *fakeFreeze, version *policyVersion, ids *sources) (*Lifecycle, *Journal, ed25519.PublicKey) {
	t.Helper()
	journal, err := OpenJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	publicKey, privateKey := controlKeys(t)
	lifecycle, err := New(Config{
		Policy: controlPolicy(t), ActivePolicyVersion: version.get, Journal: journal, FreezeGate: freeze, ChainGate: healthyControlChain{},
		Clock: clock.Now, ApprovalTTL: 10 * time.Minute, AuthorizationTTL: 5 * time.Minute,
		RequestIDSource: ids.requestID, AuthorizationIDSource: ids.authorizationID, NonceSource: ids.nextNonce,
		EnvelopeKeyID: "flowops_control_1", EnvelopePrivateKey: privateKey,
	})
	if err != nil {
		_ = journal.Close()
		t.Fatal(err)
	}
	return lifecycle, journal, publicKey
}

func controlIntent(id string, amount string) PaymentIntent {
	return PaymentIntent{
		IntentID: id, OrganizationID: "org_demo", CustomerID: "cust_acme", AgentID: "agent_research",
		TaskID: "task_104", ActionID: "action_fetch", Rail: envelope.RailX402, ChainID: 84532,
		Recipient: controlTestRecipient, Asset: controlTestUSDC, AmountAtomic: amount,
		Resource: "https://evidence.flowops.example/v1/fetch", Category: "research_data", Purpose: "fetch cited source",
	}
}

func TestApprovalIssueIsExactIdempotentAndRestartSafe(t *testing.T) {
	now := time.Date(2026, 8, 11, 14, 0, 0, 0, time.UTC)
	clock, freeze, version, ids := &fakeClock{now: now}, &fakeFreeze{}, &policyVersion{version: "policy_7"}, &sources{}
	path := filepath.Join(t.TempDir(), "control.log")
	lifecycle, journal, publicKey := newLifecycleForTest(t, path, clock, freeze, version, ids)

	record, err := lifecycle.Submit(context.Background(), controlIntent("intent_1", "150"))
	if err != nil {
		t.Fatal(err)
	}
	if record.State != StatePendingApproval || record.Decision.Outcome != policy.RequireApproval {
		t.Fatalf("unexpected submit: %+v", record)
	}
	if _, err := lifecycle.Decide(context.Background(), record.RequestID, "0xwrong", Approve, "owner_alice", "needed"); !errors.Is(err, ErrApprovalDigest) {
		t.Fatalf("digest substitution error = %v", err)
	}
	record, err = lifecycle.Decide(context.Background(), record.RequestID, record.RequestDigest, Approve, "owner_alice", "needed")
	if err != nil || record.State != StateApproved {
		t.Fatalf("approve = %+v, %v", record, err)
	}
	signed, err := lifecycle.Issue(context.Background(), record.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	if err := envelope.Verify(signed, publicKey); err != nil {
		t.Fatalf("issued signature: %v", err)
	}
	if signed.Authorization.AmountAtomic != "150" || signed.Authorization.Recipient != controlTestRecipient || signed.Authorization.TaskID != "task_104" {
		t.Fatalf("issued terms changed: %+v", signed.Authorization)
	}
	again, err := lifecycle.Issue(context.Background(), record.RequestID)
	if err != nil || !reflect.DeepEqual(again, signed) {
		t.Fatalf("idempotent issue changed: %+v / %v", again, err)
	}
	if got := len(journal.Events()); got != 3 {
		t.Fatalf("events = %d, want submit + approve + issue", got)
	}

	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, restartedJournal, _ := newLifecycleForTest(t, path, clock, freeze, version, ids)
	defer restartedJournal.Close()
	replayed, ok := restarted.Get(record.RequestID)
	if !ok || replayed.State != StateIssued {
		t.Fatalf("replayed record = %+v, %v", replayed, ok)
	}
	afterRestart, err := restarted.Issue(context.Background(), record.RequestID)
	if err != nil || !reflect.DeepEqual(afterRestart, signed) {
		t.Fatalf("restart issue changed: %+v / %v", afterRestart, err)
	}
}

func TestPolicyFreezeAndChainRecheckedBeforeIssuanceWithoutBurning(t *testing.T) {
	now := time.Date(2026, 8, 11, 14, 0, 0, 0, time.UTC)
	clock, freeze, version, ids := &fakeClock{now: now}, &fakeFreeze{}, &policyVersion{version: "policy_7"}, &sources{}
	lifecycle, journal, _ := newLifecycleForTest(t, filepath.Join(t.TempDir(), "control.log"), clock, freeze, version, ids)
	defer journal.Close()
	record, err := lifecycle.Submit(context.Background(), controlIntent("intent_auto", "100"))
	if err != nil || record.State != StateApproved {
		t.Fatalf("auto approval = %+v, %v", record, err)
	}
	version.set("policy_8")
	if _, err := lifecycle.Issue(context.Background(), record.RequestID); !errors.Is(err, ErrPolicyChanged) {
		t.Fatalf("policy change error = %v", err)
	}
	version.set("policy_7")
	freeze.set(errors.New("task frozen"))
	if _, err := lifecycle.Issue(context.Background(), record.RequestID); err == nil || !strings.Contains(err.Error(), "frozen") {
		t.Fatalf("freeze error = %v", err)
	}
	if got := len(journal.Events()); got != 1 {
		t.Fatalf("failed gates wrote %d events, want only submit", got)
	}
	freeze.set(nil)
	lifecycle.chainGate = unavailableControlChain{err: errors.New("Base head stale")}
	if _, err := lifecycle.Issue(context.Background(), record.RequestID); err == nil || !strings.Contains(err.Error(), "chain unavailable") {
		t.Fatalf("chain gate error = %v", err)
	}
	if got := len(journal.Events()); got != 1 {
		t.Fatalf("chain gate wrote %d events, want only submit", got)
	}
	if ids.auth.Load() != 0 || ids.nonce.Load() != 0 {
		t.Fatal("failed chain gate burned an authorization identity or nonce")
	}
	lifecycle.chainGate = healthyControlChain{}
	if _, err := lifecycle.Issue(context.Background(), record.RequestID); err != nil {
		t.Fatalf("failed gate burned issuance: %v", err)
	}
}

func TestReservationsAreAtomicAndRejectionReleasesThem(t *testing.T) {
	now := time.Date(2026, 8, 11, 14, 0, 0, 0, time.UTC)
	lifecycle, journal, _ := newLifecycleForTest(t, filepath.Join(t.TempDir(), "control.log"), &fakeClock{now: now}, &fakeFreeze{}, &policyVersion{version: "policy_7"}, &sources{})
	defer journal.Close()

	type result struct {
		record Record
		err    error
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	for _, id := range []string{"intent_a", "intent_b"} {
		id := id
		go func() {
			<-start
			record, err := lifecycle.Submit(context.Background(), controlIntent(id, "150"))
			results <- result{record, err}
		}()
	}
	close(start)
	first, second := <-results, <-results
	if first.err != nil || second.err != nil {
		t.Fatalf("submit errors: %v, %v", first.err, second.err)
	}
	states := map[State]int{first.record.State: 1}
	states[second.record.State]++
	if states[StatePendingApproval] != 1 || states[StateDenied] != 1 {
		t.Fatalf("reservation race states = %+v, want one pending and one denied", states)
	}
	var pending Record
	if first.record.State == StatePendingApproval {
		pending = first.record
	} else {
		pending = second.record
	}
	if _, err := lifecycle.Decide(context.Background(), pending.RequestID, pending.RequestDigest, Reject, "owner_alice", "not needed"); err != nil {
		t.Fatal(err)
	}
	afterRelease, err := lifecycle.Submit(context.Background(), controlIntent("intent_c", "100"))
	if err != nil || afterRelease.State != StateApproved {
		t.Fatalf("released reservation not reusable: %+v, %v", afterRelease, err)
	}
}

func TestIntentIdempotencyAndConflict(t *testing.T) {
	now := time.Date(2026, 8, 11, 14, 0, 0, 0, time.UTC)
	lifecycle, journal, _ := newLifecycleForTest(t, filepath.Join(t.TempDir(), "control.log"), &fakeClock{now: now}, &fakeFreeze{}, &policyVersion{version: "policy_7"}, &sources{})
	defer journal.Close()
	intent := controlIntent("intent_same", "100")
	first, err := lifecycle.Submit(context.Background(), intent)
	if err != nil {
		t.Fatal(err)
	}
	second, err := lifecycle.Submit(context.Background(), intent)
	if err != nil || !reflect.DeepEqual(first, second) || len(journal.Events()) != 1 {
		t.Fatalf("idempotent submit = %+v / %v", second, err)
	}
	intent.AmountAtomic = "101"
	if _, err := lifecycle.Submit(context.Background(), intent); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("conflict error = %v", err)
	}
}

func TestExpiryIsDurableAndReleasesReservation(t *testing.T) {
	now := time.Date(2026, 8, 11, 14, 0, 0, 0, time.UTC)
	clock := &fakeClock{now: now}
	lifecycle, journal, _ := newLifecycleForTest(t, filepath.Join(t.TempDir(), "control.log"), clock, &fakeFreeze{}, &policyVersion{version: "policy_7"}, &sources{})
	defer journal.Close()
	record, err := lifecycle.Submit(context.Background(), controlIntent("intent_expire", "150"))
	if err != nil {
		t.Fatal(err)
	}
	clock.Add(10 * time.Minute)
	if _, err := lifecycle.Decide(context.Background(), record.RequestID, record.RequestDigest, Approve, "owner_alice", "late"); !errors.Is(err, ErrApprovalExpired) {
		t.Fatalf("late approval error = %v", err)
	}
	expired, _ := lifecycle.Get(record.RequestID)
	if expired.State != StateExpired {
		t.Fatalf("state = %s, want expired", expired.State)
	}
	if got, err := lifecycle.SweepExpired(context.Background()); err != nil || got != 0 {
		t.Fatalf("idempotent sweep = %d, %v", got, err)
	}
	available, err := lifecycle.Submit(context.Background(), controlIntent("intent_after_expiry", "200"))
	if err != nil || available.State != StatePendingApproval {
		t.Fatalf("expired reservation still counted: %+v, %v", available, err)
	}
}

func TestDuplicateGeneratedAuthorizationDoesNotWriteEvent(t *testing.T) {
	now := time.Date(2026, 8, 11, 14, 0, 0, 0, time.UTC)
	clock, freeze, version := &fakeClock{now: now}, &fakeFreeze{}, &policyVersion{version: "policy_7"}
	ids := &sources{}
	lifecycle, journal, _ := newLifecycleForTest(t, filepath.Join(t.TempDir(), "control.log"), clock, freeze, version, ids)
	defer journal.Close()
	first, _ := lifecycle.Submit(context.Background(), controlIntent("intent_first", "100"))
	if _, err := lifecycle.Issue(context.Background(), first.RequestID); err != nil {
		t.Fatal(err)
	}
	secondIntent := controlIntent("intent_second", "100")
	secondIntent.TaskID = "task_other"
	second, err := lifecycle.Submit(context.Background(), secondIntent)
	if err != nil {
		t.Fatal(err)
	}
	ids.auth.Store(0)
	before := len(journal.Events())
	if _, err := lifecycle.Issue(context.Background(), second.RequestID); err == nil || !strings.Contains(err.Error(), "authorization ID already exists") {
		t.Fatalf("duplicate auth ID error = %v", err)
	}
	if got := len(journal.Events()); got != before {
		t.Fatalf("duplicate generated identity wrote event: %d -> %d", before, got)
	}
}

func TestClosedJournalCannotCreateVisibleState(t *testing.T) {
	now := time.Date(2026, 8, 11, 14, 0, 0, 0, time.UTC)
	lifecycle, journal, _ := newLifecycleForTest(t, filepath.Join(t.TempDir(), "control.log"), &fakeClock{now: now}, &fakeFreeze{}, &policyVersion{version: "policy_7"}, &sources{})
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := lifecycle.Submit(context.Background(), controlIntent("intent_no_write", "100")); err == nil {
		t.Fatal("submit succeeded with closed journal")
	}
	if _, ok := lifecycle.Get("req_1"); ok {
		t.Fatal("failed durable write became visible")
	}
}

func TestJournalTamperAndConcurrentOwnerFailClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.log")
	journal, err := OpenJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := OpenJournal(path); err == nil {
		t.Fatal("second process acquired the journal")
	}
	if _, err := journal.Append(context.Background(), time.Date(2026, 8, 11, 14, 0, 0, 0, time.UTC), "test.event", "req_1", map[string]string{"task": "task_104"}); err != nil {
		t.Fatal(err)
	}
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	raw = []byte(strings.Replace(string(raw), "task_104", "task_999", 1))
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenJournal(path); err == nil || !strings.Contains(err.Error(), "hash") {
		t.Fatalf("tampered journal error = %v", err)
	}
}

func TestJournalRejectsInvalidEventBeforeItBecomesDurable(t *testing.T) {
	journal, err := OpenJournal(filepath.Join(t.TempDir(), "control.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	if _, err := journal.Append(context.Background(), time.Unix(0, 0), "test.event", "req_1", map[string]string{"ok": "true"}); err == nil {
		t.Fatal("invalid timestamp was appended")
	}
	if got := len(journal.Events()); got != 0 {
		t.Fatalf("invalid event became visible: %d", got)
	}
}

func TestRequestDigestBindsPolicyDecision(t *testing.T) {
	intentDigest, err := controlIntent("intent_digest", "100").Digest()
	if err != nil {
		t.Fatal(err)
	}
	allowed := policy.Decision{Outcome: policy.AutoApprove, Reason: policy.ReasonAllowed, PolicyVersion: "policy_7"}
	approval := policy.Decision{Outcome: policy.RequireApproval, Reason: policy.ReasonHumanApprovalThreshold, PolicyVersion: "policy_7"}
	first, err := requestDigest("req_1", intentDigest, allowed, 1786457400)
	if err != nil {
		t.Fatal(err)
	}
	second, err := requestDigest("req_1", intentDigest, approval, 1786457400)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("approval request digest did not bind policy outcome and reason")
	}
}

func TestPaymentIntentDigestGoldenAndValidation(t *testing.T) {
	intent := controlIntent("intent_golden", "100")
	digest, err := intent.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if want := "0x2929d80279fdb594fcb76728dac31390b6e942de0589261a2a15592dcd03dbaa"; digest != want {
		t.Fatalf("digest = %s, want %s", digest, want)
	}
	invalid := intent
	invalid.Recipient = strings.ToUpper(invalid.Recipient)
	if err := invalid.Validate(); err == nil {
		t.Fatal("noncanonical recipient accepted")
	}
}

func TestIntentIdempotencyIsScopedToOrganization(t *testing.T) {
	now := time.Date(2026, 8, 11, 14, 0, 0, 0, time.UTC)
	lifecycle, journal, _ := newLifecycleForTest(t, filepath.Join(t.TempDir(), "control.log"), &fakeClock{now: now}, &fakeFreeze{}, &policyVersion{version: "policy_7"}, &sources{})
	defer journal.Close()

	first := controlIntent("shared_key", "50")
	if _, err := lifecycle.Submit(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	second := first
	second.OrganizationID = "org_other"
	second.CustomerID = "cust_other"
	if _, err := lifecycle.Submit(context.Background(), second); err != nil {
		t.Fatalf("second organization could not reuse its own idempotency key: %v", err)
	}
	if got := len(journal.Events()); got != 2 {
		t.Fatalf("events = %d, want one independently scoped request per organization", got)
	}
}
