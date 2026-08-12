package referencesigner

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gnanam1990/flowops/pkg/broadcastreceipt"
	"github.com/gnanam1990/flowops/pkg/envelope"
	"golang.org/x/crypto/sha3"
)

type fakeAuthorizationVerifier struct {
	mu             sync.Mutex
	authorizeCalls int
	checkCalls     int
	checkErr       error
}

func (v *fakeAuthorizationVerifier) Authorize(_ context.Context, signed envelope.SignedAuthorization) (Authorized, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.authorizeCalls++
	digest, err := signed.Authorization.Digest()
	if err != nil {
		return Authorized{}, err
	}
	return Authorized{
		Authorization: signed.Authorization,
		Digest:        "0x" + hex.EncodeToString(digest[:]),
		KeyID:         signed.KeyID,
		ClaimedAt:     signed.Authorization.IssuedAt + 1,
	}, nil
}

func (v *fakeAuthorizationVerifier) CheckExecution(_ context.Context, _ envelope.SignedAuthorization) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.checkCalls++
	return v.checkErr
}

func (v *fakeAuthorizationVerifier) setCheckError(err error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.checkErr = err
}

type fakeWallet struct {
	mu             sync.Mutex
	prepared       PreparedTransaction
	prepareCalls   int
	broadcastCalls int
	prepareHook    func()
	broadcastHook  func(context.Context, PreparedTransaction) error
}

func (w *fakeWallet) Prepare(_ context.Context, _ Authorized) (PreparedTransaction, error) {
	w.mu.Lock()
	w.prepareCalls++
	hook := w.prepareHook
	prepared := clonePrepared(w.prepared)
	w.mu.Unlock()
	if hook != nil {
		hook()
	}
	return prepared, nil
}

func (w *fakeWallet) Broadcast(ctx context.Context, prepared PreparedTransaction) error {
	w.mu.Lock()
	w.broadcastCalls++
	hook := w.broadcastHook
	w.mu.Unlock()
	if hook != nil {
		return hook(ctx, prepared)
	}
	return nil
}

func (w *fakeWallet) calls() (int, int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.prepareCalls, w.broadcastCalls
}

type fakeRegistrationSink struct {
	mu       sync.Mutex
	calls    int
	err      error
	receipts []broadcastreceipt.SignedReceipt
	hook     func(context.Context)
}

func (s *fakeRegistrationSink) Register(ctx context.Context, receipt broadcastreceipt.SignedReceipt) error {
	s.mu.Lock()
	s.calls++
	s.receipts = append(s.receipts, receipt)
	hook := s.hook
	err := s.err
	s.mu.Unlock()
	if hook != nil {
		hook(ctx)
	}
	if err != nil {
		return err
	}
	return ctx.Err()
}

func (s *fakeRegistrationSink) setError(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.err = err
}

func (s *fakeRegistrationSink) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

type executorFixture struct {
	now         time.Time
	signed      envelope.SignedAuthorization
	verifier    *fakeAuthorizationVerifier
	wallet      *fakeWallet
	sink        *fakeRegistrationSink
	journal     *AttemptJournal
	journalPath string
	receiptKey  ed25519.PrivateKey
	executor    *Executor
}

func newExecutorFixture(t *testing.T) *executorFixture {
	t.Helper()
	now := time.Date(2026, 8, 12, 10, 0, 0, 0, time.UTC)
	_, flowOpsKey := testKeys(t)
	authorization := testAuthorization(now)
	authorization.Rail = envelope.RailDirect
	authorization.AuthorizationID = "auth_executor_1"
	signed := signTest(t, authorization, flowOpsKey)
	raw := []byte{0x02, 0xf8, 0x01, 0x01, 0x84, 0x3b, 0x9a, 0xca, 0x00}
	prepared := PreparedTransaction{
		RawTransaction:  raw,
		TransactionHash: transactionHash(raw),
		Sender:          "0x2222222222222222222222222222222222222222",
	}
	receiptSeed := []byte(strings.Repeat("r", ed25519.SeedSize))
	receiptKey := ed25519.NewKeyFromSeed(receiptSeed)
	journalPath := filepath.Join(t.TempDir(), "attempts.log")
	journal, err := OpenAttemptJournal(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = journal.Close() })
	fixture := &executorFixture{
		now: now, signed: signed, verifier: &fakeAuthorizationVerifier{},
		wallet: &fakeWallet{prepared: prepared}, sink: &fakeRegistrationSink{},
		journal: journal, journalPath: journalPath, receiptKey: receiptKey,
	}
	fixture.executor = fixture.newExecutor(t, journal, fixture.verifier, fixture.wallet, fixture.sink)
	return fixture
}

func (f *executorFixture) newExecutor(t *testing.T, journal *AttemptJournal, verifier AuthorizationVerifier, wallet WalletAdapter, sink RegistrationSink) *Executor {
	t.Helper()
	executor, err := NewExecutor(ExecutorConfig{
		Verifier: verifier, Wallet: wallet, Registration: sink, Journal: journal,
		ReceiptKeyID: "customer_receipt_1", ReceiptPrivateKey: f.receiptKey,
		Clock: func() time.Time { return f.now },
	})
	if err != nil {
		t.Fatal(err)
	}
	return executor
}

func transactionHash(raw []byte) string {
	hash := sha3.NewLegacyKeccak256()
	_, _ = hash.Write(raw)
	return "0x" + hex.EncodeToString(hash.Sum(nil))
}

func TestExecutorBroadcastsAndRegistersExactlyOnce(t *testing.T) {
	f := newExecutorFixture(t)
	attempt, err := f.executor.Execute(context.Background(), f.signed)
	if err != nil {
		t.Fatal(err)
	}
	if attempt.State != AttemptRegistered || attempt.Receipt == nil || attempt.Receipt.Receipt.Outcome != broadcastreceipt.OutcomeSubmitted {
		t.Fatalf("unexpected terminal attempt: %+v", attempt)
	}
	for range 3 {
		if replay, err := f.executor.Execute(context.Background(), f.signed); err != nil || replay.State != AttemptRegistered {
			t.Fatalf("idempotent replay = (%s, %v)", replay.State, err)
		}
	}
	prepareCalls, broadcastCalls := f.wallet.calls()
	if prepareCalls != 1 || broadcastCalls != 1 || f.sink.count() != 1 {
		t.Fatalf("calls prepare=%d broadcast=%d register=%d, want 1/1/1", prepareCalls, broadcastCalls, f.sink.count())
	}
	if err := f.journal.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenAttemptJournal(f.journalPath)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	restarted := f.newExecutor(t, reopened, f.verifier, f.wallet, f.sink)
	if replay, err := restarted.Execute(context.Background(), f.signed); err != nil || replay.State != AttemptRegistered {
		t.Fatalf("restart replay = (%s, %v)", replay.State, err)
	}
	_, broadcastCalls = f.wallet.calls()
	if broadcastCalls != 1 {
		t.Fatalf("restart rebroadcasted %d times", broadcastCalls)
	}
}

func TestExecutorBroadcastErrorBecomesDurableAmbiguousWithoutRetry(t *testing.T) {
	f := newExecutorFixture(t)
	f.wallet.broadcastHook = func(context.Context, PreparedTransaction) error { return errors.New("timeout after send") }
	attempt, err := f.executor.Execute(context.Background(), f.signed)
	if !errors.Is(err, ErrBroadcastAmbiguous) || attempt.State != AttemptRegistered || attempt.Receipt.Receipt.Outcome != broadcastreceipt.OutcomeAmbiguous {
		t.Fatalf("ambiguous result = (%+v, %v)", attempt, err)
	}
	if _, err := f.executor.Execute(context.Background(), f.signed); !errors.Is(err, ErrBroadcastAmbiguous) {
		t.Fatalf("ambiguous replay error = %v", err)
	}
	_, broadcasts := f.wallet.calls()
	if broadcasts != 1 {
		t.Fatalf("ambiguous transaction broadcast %d times", broadcasts)
	}
}

func TestRegistrationRetryNeverRebroadcasts(t *testing.T) {
	f := newExecutorFixture(t)
	f.sink.setError(errors.New("control plane unavailable"))
	attempt, err := f.executor.Execute(context.Background(), f.signed)
	if !errors.Is(err, ErrRegistrationPending) || attempt.State != AttemptSubmitted {
		t.Fatalf("first result = (%s, %v)", attempt.State, err)
	}
	f.sink.setError(nil)
	attempt, err = f.executor.Execute(context.Background(), f.signed)
	if err != nil || attempt.State != AttemptRegistered {
		t.Fatalf("retry result = (%s, %v)", attempt.State, err)
	}
	_, broadcasts := f.wallet.calls()
	if broadcasts != 1 || f.sink.count() != 2 {
		t.Fatalf("broadcasts=%d registrations=%d, want 1/2", broadcasts, f.sink.count())
	}
}

func TestLostLocalRegistrationAckRetriesReceiptOnlyAfterRestart(t *testing.T) {
	f := newExecutorFixture(t)
	f.sink.hook = func(context.Context) { _ = f.journal.Close() }
	attempt, err := f.executor.Execute(context.Background(), f.signed)
	if !errors.Is(err, ErrRegistrationPending) || attempt.State != AttemptSubmitted {
		t.Fatalf("lost ack result = (%s, %v)", attempt.State, err)
	}
	reopened, err := OpenAttemptJournal(f.journalPath)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	f.sink.hook = nil
	restarted := f.newExecutor(t, reopened, f.verifier, f.wallet, f.sink)
	results, err := restarted.ResumePending(context.Background())
	if err != nil || len(results) != 1 || results[0].State != AttemptRegistered {
		t.Fatalf("resume result = (%+v, %v)", results, err)
	}
	_, broadcasts := f.wallet.calls()
	if broadcasts != 1 || f.sink.count() != 2 {
		t.Fatalf("broadcasts=%d registrations=%d, want 1/2", broadcasts, f.sink.count())
	}
}

func TestPreparedGateFailureStopsNetworkAndCanResume(t *testing.T) {
	f := newExecutorFixture(t)
	f.wallet.prepareHook = func() { f.verifier.setCheckError(errors.New("task frozen")) }
	attempt, err := f.executor.Execute(context.Background(), f.signed)
	if err == nil || attempt.State != AttemptPrepared {
		t.Fatalf("gate result = (%s, %v)", attempt.State, err)
	}
	_, broadcasts := f.wallet.calls()
	if broadcasts != 0 {
		t.Fatalf("broadcast called behind a failed second gate")
	}
	f.verifier.setCheckError(nil)
	if attempt, err = f.executor.Execute(context.Background(), f.signed); err != nil || attempt.State != AttemptRegistered {
		t.Fatalf("resume result = (%s, %v)", attempt.State, err)
	}
	_, broadcasts = f.wallet.calls()
	if broadcasts != 1 {
		t.Fatalf("resume broadcasts=%d, want 1", broadcasts)
	}
}

func TestRestartFromBroadcastingMarksAmbiguousWithoutWallet(t *testing.T) {
	f := newExecutorFixture(t)
	authorized, err := f.verifier.Authorize(context.Background(), f.signed)
	if err != nil {
		t.Fatal(err)
	}
	prepared, _ := f.wallet.Prepare(context.Background(), authorized)
	attempt := Attempt{Authorization: f.signed, Authorized: authorized, Prepared: prepared, State: AttemptPrepared, PreparedAt: f.now.Unix()}
	attempt, err = f.journal.Append(context.Background(), f.now, attempt)
	if err != nil {
		t.Fatal(err)
	}
	attempt.State, attempt.BroadcastAt = AttemptBroadcasting, f.now.Unix()
	if _, err := f.journal.Append(context.Background(), f.now, attempt); err != nil {
		t.Fatal(err)
	}
	if err := f.journal.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenAttemptJournal(f.journalPath)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	restarted := f.newExecutor(t, reopened, f.verifier, f.wallet, f.sink)
	results, err := restarted.ResumePending(context.Background())
	if !errors.Is(err, ErrBroadcastAmbiguous) || len(results) != 1 || results[0].State != AttemptRegistered {
		t.Fatalf("resume result = (%+v, %v)", results, err)
	}
	_, broadcasts := f.wallet.calls()
	if broadcasts != 0 {
		t.Fatalf("BROADCASTING recovery touched wallet %d times", broadcasts)
	}
}

func TestRestartFromPreparedBroadcastsOnce(t *testing.T) {
	f := newExecutorFixture(t)
	authorized, err := f.verifier.Authorize(context.Background(), f.signed)
	if err != nil {
		t.Fatal(err)
	}
	prepared, _ := f.wallet.Prepare(context.Background(), authorized)
	attempt := Attempt{Authorization: f.signed, Authorized: authorized, Prepared: prepared, State: AttemptPrepared, PreparedAt: f.now.Unix()}
	if _, err := f.journal.Append(context.Background(), f.now, attempt); err != nil {
		t.Fatal(err)
	}
	if err := f.journal.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenAttemptJournal(f.journalPath)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	restarted := f.newExecutor(t, reopened, f.verifier, f.wallet, f.sink)
	results, err := restarted.ResumePending(context.Background())
	if err != nil || len(results) != 1 || results[0].State != AttemptRegistered {
		t.Fatalf("resume result = (%+v, %v)", results, err)
	}
	_, broadcasts := f.wallet.calls()
	if broadcasts != 1 {
		t.Fatalf("PREPARED recovery broadcast %d times, want 1", broadcasts)
	}
}

func TestRemovingFlowOpsTrustStopsPreparedAttempt(t *testing.T) {
	f := newExecutorFixture(t)
	flowOpsPublic, _ := testKeys(t)
	firstNonces, err := OpenFileNonceStore(filepath.Join(t.TempDir(), "first-nonces.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer firstNonces.Close()
	firstVerifier := newDirectVerifier(t, f.now, map[string]ed25519.PublicKey{"flowops_control_1": flowOpsPublic}, firstNonces)
	authorized, err := firstVerifier.Authorize(context.Background(), f.signed)
	if err != nil {
		t.Fatal(err)
	}
	prepared, _ := f.wallet.Prepare(context.Background(), authorized)
	attempt := Attempt{Authorization: f.signed, Authorized: authorized, Prepared: prepared, State: AttemptPrepared, PreparedAt: f.now.Unix()}
	if _, err := f.journal.Append(context.Background(), f.now, attempt); err != nil {
		t.Fatal(err)
	}
	if err := f.journal.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenAttemptJournal(f.journalPath)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	otherKey := ed25519.NewKeyFromSeed([]byte(strings.Repeat("o", ed25519.SeedSize))).Public().(ed25519.PublicKey)
	rotatedNonces, err := OpenFileNonceStore(filepath.Join(t.TempDir(), "rotated-nonces.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer rotatedNonces.Close()
	rotatedVerifier := newDirectVerifier(t, f.now, map[string]ed25519.PublicKey{"other_control_1": otherKey}, rotatedNonces)
	restarted := f.newExecutor(t, reopened, rotatedVerifier, f.wallet, f.sink)
	results, err := restarted.ResumePending(context.Background())
	if !RefusalIs(err, RefusalUnknownTrustKey) || len(results) != 1 || results[0].State != AttemptPrepared {
		t.Fatalf("trust removal result = (%+v, %v)", results, err)
	}
	_, broadcasts := f.wallet.calls()
	if broadcasts != 0 {
		t.Fatalf("removed trust root still broadcast %d times", broadcasts)
	}
}

func newDirectVerifier(t *testing.T, now time.Time, trustKeys map[string]ed25519.PublicKey, nonces NonceStore) *Verifier {
	t.Helper()
	verifier, err := New(Config{
		OrganizationID: "org_demo", CustomerID: "cust_acme", TrustKeys: trustKeys,
		AllowedChainIDs: []uint64{84532}, AllowedRails: []envelope.Rail{envelope.RailDirect},
		AllowedAssets: []string{testUSDC}, AllowedRecipients: []string{testRecipient},
		MaxAmountAtomic: "100000", MaxTTL: 10 * time.Minute, MaxFutureSkew: 30 * time.Second,
		Clock: func() time.Time { return now }, ChainGate: &mutableGate{}, FreezeGate: &mutableGate{}, Nonces: nonces,
	})
	if err != nil {
		t.Fatal(err)
	}
	return verifier
}

func TestCallerCancellationAfterWalletStillPersistsAmbiguity(t *testing.T) {
	f := newExecutorFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	f.wallet.broadcastHook = func(ctx context.Context, _ PreparedTransaction) error {
		cancel()
		return ctx.Err()
	}
	attempt, err := f.executor.Execute(ctx, f.signed)
	if !errors.Is(err, ErrRegistrationPending) || attempt.State != AttemptAmbiguous {
		t.Fatalf("cancel result = (%s, %v)", attempt.State, err)
	}
	durable, ok := f.journal.Get(f.signed.Authorization.AuthorizationID)
	if !ok || durable.State != AttemptAmbiguous || durable.Receipt.Receipt.Outcome != broadcastreceipt.OutcomeAmbiguous {
		t.Fatalf("ambiguous state was not durable: %+v", durable)
	}
	if attempt, err = f.executor.Execute(context.Background(), f.signed); !errors.Is(err, ErrBroadcastAmbiguous) || attempt.State != AttemptRegistered {
		t.Fatalf("registration retry = (%s, %v)", attempt.State, err)
	}
	_, broadcasts := f.wallet.calls()
	if broadcasts != 1 {
		t.Fatalf("cancelled send rebroadcast %d times", broadcasts)
	}
}

func TestConcurrentExecuteHasOneWalletCrossing(t *testing.T) {
	f := newExecutorFixture(t)
	var wait sync.WaitGroup
	errorsSeen := make(chan error, 50)
	for range 50 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			attempt, err := f.executor.Execute(context.Background(), f.signed)
			if err != nil || attempt.State != AttemptRegistered {
				errorsSeen <- errors.Join(err, errors.New(string(attempt.State)))
			}
		}()
	}
	wait.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		t.Fatalf("concurrent execute: %v", err)
	}
	prepareCalls, broadcastCalls := f.wallet.calls()
	if prepareCalls != 1 || broadcastCalls != 1 || f.sink.count() != 1 {
		t.Fatalf("calls prepare=%d broadcast=%d register=%d", prepareCalls, broadcastCalls, f.sink.count())
	}
}

func TestAttemptConflictAndUnsupportedRailNeverReachWallet(t *testing.T) {
	f := newExecutorFixture(t)
	if _, err := f.executor.Execute(context.Background(), f.signed); err != nil {
		t.Fatal(err)
	}
	tampered := f.signed
	tampered.Authorization.AmountAtomic = "999"
	if _, err := f.executor.Execute(context.Background(), tampered); !errors.Is(err, ErrAttemptConflict) {
		t.Fatalf("attempt conflict = %v", err)
	}
	other := f.signed
	other.Authorization.AuthorizationID = "auth_x402_1"
	other.Authorization.Rail = envelope.RailX402
	if _, err := f.executor.Execute(context.Background(), other); !errors.Is(err, ErrUnsupportedRail) {
		t.Fatalf("unsupported rail = %v", err)
	}
	prepareCalls, broadcastCalls := f.wallet.calls()
	if prepareCalls != 1 || broadcastCalls != 1 || f.verifier.authorizeCalls != 1 {
		t.Fatalf("unsupported/conflict reached boundary: prepare=%d broadcast=%d authorize=%d", prepareCalls, broadcastCalls, f.verifier.authorizeCalls)
	}
}

func TestInvalidPreparedTransactionNeverEntersJournal(t *testing.T) {
	f := newExecutorFixture(t)
	f.wallet.prepared.TransactionHash = "0x" + strings.Repeat("0", 64)
	if _, err := f.executor.Execute(context.Background(), f.signed); !errors.Is(err, ErrPreparedTransaction) {
		t.Fatalf("prepared transaction error = %v", err)
	}
	if _, ok := f.journal.Get(f.signed.Authorization.AuthorizationID); ok {
		t.Fatal("invalid prepared transaction entered journal")
	}
	_, broadcasts := f.wallet.calls()
	if broadcasts != 0 {
		t.Fatal("invalid prepared transaction reached broadcast")
	}
}

func TestExecutorRejectsNonCanonicalReceiptPrivateKey(t *testing.T) {
	f := newExecutorFixture(t)
	bad := append(ed25519.PrivateKey(nil), f.receiptKey...)
	bad[len(bad)-1] ^= 1
	if _, err := NewExecutor(ExecutorConfig{
		Verifier: f.verifier, Wallet: f.wallet, Registration: f.sink, Journal: f.journal,
		ReceiptKeyID: "customer_receipt_1", ReceiptPrivateKey: bad,
	}); err == nil {
		t.Fatal("non-canonical receipt key accepted")
	}
}

func TestAttemptJournalRejectsConcurrentOwnerAndTampering(t *testing.T) {
	f := newExecutorFixture(t)
	if _, err := OpenAttemptJournal(f.journalPath); err == nil {
		t.Fatal("second process acquired attempt journal")
	}
	if _, err := f.executor.Execute(context.Background(), f.signed); err != nil {
		t.Fatal(err)
	}
	if err := f.journal.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(f.journalPath)
	if err != nil {
		t.Fatal(err)
	}
	raw[len(raw)/2] ^= 1
	if err := os.WriteFile(f.journalPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenAttemptJournal(f.journalPath); err == nil {
		t.Fatal("tampered attempt journal opened")
	}
}

func TestAttemptJournalRejectsInsecureFileAndSymlink(t *testing.T) {
	directory := t.TempDir()
	insecure := filepath.Join(directory, "insecure.log")
	if err := os.WriteFile(insecure, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenAttemptJournal(insecure); err == nil {
		t.Fatal("group/world-readable attempt journal accepted")
	}
	target := filepath.Join(directory, "target.log")
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(directory, "attempts.log")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenAttemptJournal(link); err == nil {
		t.Fatal("symlink attempt journal accepted")
	}
}
