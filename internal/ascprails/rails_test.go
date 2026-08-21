package ascprails

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/gnanam1990/flowops/pkg/escrowcall"
	"github.com/gnanam1990/flowops/pkg/purchasespec"
	"github.com/gnanam1990/flowops/pkg/sellerquote"
	x402types "github.com/x402-foundation/x402/go/v2/types"
)

func TestDispatchCapturesThenFinalizesWithoutSecondEgress(t *testing.T) {
	fixture := railsFixture(t)
	store := newMemoryStore(fixture.now)
	chain := &fakeChain{observations: []ChainObservation{fixture.observation, {Timestamp: uint64(fixture.now.Unix() + 5), EvidenceDigest: testHash("32"), ObservedAt: fixture.now}}}
	body := []byte(`{"result":"ok"}`)
	transport := &fakeRestrictedTransport{responses: []*http.Response{successResponse(t, fixture, body)}} //nolint:bodyclose // DispatchOne owns the synthetic response body.
	service := newTestService(t, store, chain, transport, fixture.now)
	if _, replay, err := service.Enqueue(context.Background(), fixture.input); err != nil || replay {
		t.Fatalf("enqueue replay=%t err=%v", replay, err)
	}
	job, err := service.DispatchOne(context.Background())
	if err != nil || job.State != StateResponseStored || transport.calls != 1 {
		t.Fatalf("dispatch state=%s calls=%d err=%v", job.State, transport.calls, err)
	}
	job, err = service.FinalizeOne(context.Background())
	if err != nil || job.State != StateCaptured || transport.calls != 1 {
		t.Fatalf("finalize state=%s calls=%d err=%v", job.State, transport.calls, err)
	}
	delivery, err := CapturedDelivery(job)
	if err != nil || !bytes.Equal(delivery.Content, body) || delivery.CapturedAt != fixture.observation.Timestamp+5 {
		t.Fatalf("delivery=%+v err=%v", delivery, err)
	}
	if _, err := service.FinalizeOne(context.Background()); !errors.Is(err, ErrNoWork) {
		t.Fatalf("second finalize=%v", err)
	}
}

func TestResponseStoredRecoveryNeverRecontactsSellerWhenChainClockFails(t *testing.T) {
	fixture := railsFixture(t)
	store := newMemoryStore(fixture.now)
	chain := &fakeChain{observations: []ChainObservation{fixture.observation}, errors: []error{nil, errors.New("observer quorum unavailable")}}
	transport := &fakeRestrictedTransport{responses: []*http.Response{successResponse(t, fixture, []byte("answer"))}} //nolint:bodyclose // DispatchOne owns the synthetic response body.
	service := newTestService(t, store, chain, transport, fixture.now)
	_, _, _ = service.Enqueue(context.Background(), fixture.input)
	if job, err := service.DispatchOne(context.Background()); err != nil || job.State != StateResponseStored {
		t.Fatalf("dispatch=%s err=%v", job.State, err)
	}
	if _, err := service.FinalizeOne(context.Background()); err == nil {
		t.Fatal("expected unavailable chain time")
	}
	if transport.calls != 1 {
		t.Fatalf("seller contacted %d times", transport.calls)
	}
	if job, _ := store.Get(context.Background(), fixture.input.JobID); job.State != StateResponseStored {
		t.Fatalf("state=%s", job.State)
	}
}

func TestAmbiguousTransportRetriesExactPaymentOnlyThreeTimes(t *testing.T) {
	fixture := railsFixture(t)
	store := newMemoryStore(fixture.now)
	chain := &fakeChain{observations: []ChainObservation{fixture.observation, fixture.observation, fixture.observation}}
	transport := &fakeRestrictedTransport{errors: []error{errors.New("lost response"), errors.New("lost response"), errors.New("lost response")}}
	service := newTestService(t, store, chain, transport, fixture.now)
	_, _, _ = service.Enqueue(context.Background(), fixture.input)
	for attempt := 1; attempt <= 3; attempt++ {
		job, err := service.DispatchOne(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		want := StateRetryWait
		if attempt == 3 {
			want = StateDeadLetter
		}
		if job.State != want || job.AttemptCount != attempt {
			t.Fatalf("attempt=%d state=%s count=%d", attempt, job.State, job.AttemptCount)
		}
		store.now = store.now.Add(2 * time.Second)
	}
	if transport.calls != 3 {
		t.Fatalf("calls=%d", transport.calls)
	}
	for index, request := range transport.requests {
		if request.payment != fixture.input.Payment.PaymentSignatureHeader || request.body != string(fixture.input.Body) || request.url != fixture.input.URL {
			t.Fatalf("request %d drifted: %+v", index, request)
		}
	}
	if _, err := service.DispatchOne(context.Background()); !errors.Is(err, ErrNoWork) {
		t.Fatalf("terminal dispatch=%v", err)
	}
}

func TestExhaustedRetryWaitCannotBecomeFourthAttempt(t *testing.T) {
	fixture := railsFixture(t)
	store := newMemoryStore(fixture.now)
	transport := &fakeRestrictedTransport{}
	service := newTestService(t, store, &fakeChain{}, transport, fixture.now)
	_, _, _ = service.Enqueue(context.Background(), fixture.input)
	store.mu.Lock()
	store.job.State = StateRetryWait
	store.job.AttemptCount = 3
	store.mu.Unlock()
	job, err := service.DispatchOne(context.Background())
	if err != nil || job.State != StateDeadLetter || job.LastError != "RESPONSE_UNKNOWN_RETRIES_EXHAUSTED" || transport.calls != 0 {
		t.Fatalf("state=%s code=%s calls=%d err=%v", job.State, job.LastError, transport.calls, err)
	}
}

func TestConfirmedDeadlineAndLeadershipFailureBlockNetwork(t *testing.T) {
	fixture := railsFixture(t)
	for _, test := range []struct {
		name        string
		epoch       uint64
		observation ChainObservation
		want        State
	}{
		{name: "deadline", epoch: 7, observation: ChainObservation{Timestamp: fixture.input.DeliverBy, EvidenceDigest: testHash("33"), ObservedAt: fixture.now}, want: StateMissing},
		{name: "leadership", epoch: 8, observation: fixture.observation, want: StateDeadLetter},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := newMemoryStore(fixture.now)
			transport := &fakeRestrictedTransport{}
			service := newTestServiceWithEpoch(t, store, &fakeChain{observations: []ChainObservation{test.observation}}, transport, fixture.now, test.epoch)
			_, _, _ = service.Enqueue(context.Background(), fixture.input)
			job, err := service.DispatchOne(context.Background())
			if err != nil || job.State != test.want || transport.calls != 0 {
				t.Fatalf("state=%s calls=%d err=%v", job.State, transport.calls, err)
			}
		})
	}
}

func TestCurrentOperationGateFailureBlocksNetwork(t *testing.T) {
	fixture := railsFixture(t)
	store := newMemoryStore(fixture.now)
	transport := &fakeRestrictedTransport{}
	service, err := NewService(store, staticLeadership(7), &fakeChain{observations: []ChainObservation{fixture.observation}},
		staticOperationGate{err: ErrOperationNotExecutable}, staticIntegrityGate{}, transport, testConfig(fixture.now))
	if err != nil {
		t.Fatal(err)
	}
	_, _, _ = service.Enqueue(context.Background(), fixture.input)
	job, err := service.DispatchOne(context.Background())
	if err != nil || job.State != StateDeadLetter || job.LastError != "OPERATION_NOT_EXECUTABLE" || transport.calls != 0 {
		t.Fatalf("state=%s code=%s calls=%d err=%v", job.State, job.LastError, transport.calls, err)
	}
}

func TestOperationChangeDuringPreparationBlocksNetworkBeforeSendingFence(t *testing.T) {
	fixture := railsFixture(t)
	store := newMemoryStore(fixture.now)
	transport := &fakeRestrictedTransport{}
	gate := &sequenceOperationGate{errors: []error{nil, ErrOperationNotExecutable}}
	service, err := NewService(store, staticLeadership(7), &fakeChain{observations: []ChainObservation{fixture.observation}},
		gate, staticIntegrityGate{}, transport, testConfig(fixture.now))
	if err != nil {
		t.Fatal(err)
	}
	_, _, _ = service.Enqueue(t.Context(), fixture.input)
	job, err := service.DispatchOne(t.Context())
	if err != nil || job.State != StateDeadLetter || job.AttemptCount != 0 || job.LastError != "OPERATION_NOT_EXECUTABLE" || transport.calls != 0 || gate.calls != 2 {
		t.Fatalf("state=%s attempts=%d code=%s calls=%d gate=%d err=%v", job.State, job.AttemptCount, job.LastError, transport.calls, gate.calls, err)
	}
}

func TestLeadershipChangeAtSendFenceBlocksNetworkAndAttempt(t *testing.T) {
	fixture := railsFixture(t)
	store := newMemoryStore(fixture.now)
	transport := &fakeRestrictedTransport{}
	leadership := changingLeadership{current: 7, fenced: 8}
	service, err := NewService(store, leadership, &fakeChain{observations: []ChainObservation{fixture.observation}},
		staticOperationGate{}, staticIntegrityGate{}, transport, testConfig(fixture.now))
	if err != nil {
		t.Fatal(err)
	}
	_, _, _ = service.Enqueue(t.Context(), fixture.input)
	job, err := service.DispatchOne(t.Context())
	if err != nil || job.State != StateDeadLetter || job.AttemptCount != 0 || job.LastError != "LEADERSHIP_EPOCH_CHANGED" || transport.calls != 0 {
		t.Fatalf("state=%s attempts=%d code=%s calls=%d err=%v", job.State, job.AttemptCount, job.LastError, transport.calls, err)
	}
}

func TestLeadershipFenceCoversSellerEffect(t *testing.T) {
	fixture := railsFixture(t)
	store := newMemoryStore(fixture.now)
	leadership := &scopeLeadership{epoch: 7}
	body := []byte(`{"result":"fenced"}`)
	transport := &fakeRestrictedTransport{responses: []*http.Response{successResponse(t, fixture, body)}} //nolint:bodyclose // DispatchOne owns the synthetic response body.
	transport.onRoundTrip = func() {
		if !leadership.active {
			t.Fatal("seller network effect escaped the leadership fence")
		}
	}
	chain := &fakeChain{observations: []ChainObservation{fixture.observation}, onConfirmed: func() {
		if !leadership.active {
			t.Fatal("chain time was confirmed outside the leadership fence")
		}
	}}
	service, err := NewService(store, leadership, chain,
		staticOperationGate{}, staticIntegrityGate{}, transport, testConfig(fixture.now))
	if err != nil {
		t.Fatal(err)
	}
	_, _, _ = service.Enqueue(t.Context(), fixture.input)
	job, err := service.DispatchOne(t.Context())
	if err != nil || job.State != StateResponseStored || leadership.active || leadership.effects != 1 {
		t.Fatalf("state=%s active=%t effects=%d err=%v", job.State, leadership.active, leadership.effects, err)
	}
}

func TestDeadlineTransitionRecordsOperationalEvent(t *testing.T) {
	fixture := railsFixture(t)
	store := newMemoryStore(fixture.now)
	recorder := &eventRecorder{}
	config := testConfig(fixture.now)
	config.Recorder = recorder
	service, err := NewService(store, staticLeadership(7), &fakeChain{observations: []ChainObservation{{Timestamp: fixture.input.DeliverBy, EvidenceDigest: testHash("33"), ObservedAt: fixture.now}}},
		staticOperationGate{}, staticIntegrityGate{}, &fakeRestrictedTransport{}, config)
	if err != nil {
		t.Fatal(err)
	}
	_, _, _ = service.Enqueue(t.Context(), fixture.input)
	job, err := service.DispatchOne(t.Context())
	if err != nil || job.State != StateMissing || len(recorder.events) != 1 || recorder.events[0].Code != "DELIVER_BY_REACHED_BEFORE_EGRESS" {
		t.Fatalf("state=%s events=%+v err=%v", job.State, recorder.events, err)
	}
}

func TestStaleChainObservationNeverAuthorizesEgress(t *testing.T) {
	fixture := railsFixture(t)
	stale := fixture.observation
	stale.ObservedAt = fixture.now.Add(-31 * time.Second)
	store := newMemoryStore(fixture.now)
	transport := &fakeRestrictedTransport{}
	service := newTestService(t, store, &fakeChain{observations: []ChainObservation{stale}}, transport, fixture.now)
	_, _, _ = service.Enqueue(context.Background(), fixture.input)
	job, err := service.DispatchOne(context.Background())
	if err != nil || job.State != StateRetryWait || job.LastError != "CHAIN_TIME_UNAVAILABLE" || transport.calls != 0 {
		t.Fatalf("state=%s code=%s calls=%d err=%v", job.State, job.LastError, transport.calls, err)
	}
}

func TestEventIntegrityFailureFreezesEgress(t *testing.T) {
	fixture := railsFixture(t)
	store := newMemoryStore(fixture.now)
	transport := &fakeRestrictedTransport{}
	service, err := NewService(store, staticLeadership(7), &fakeChain{observations: []ChainObservation{fixture.observation}},
		staticOperationGate{}, staticIntegrityGate{err: errors.New("checkpoint mismatch")}, transport, testConfig(fixture.now))
	if err != nil {
		t.Fatal(err)
	}
	_, _, _ = service.Enqueue(context.Background(), fixture.input)
	job, err := service.DispatchOne(context.Background())
	if err != nil || job.State != StateRetryWait || job.LastError != "EVENT_INTEGRITY_UNAVAILABLE" || transport.calls != 0 {
		t.Fatalf("state=%s code=%s calls=%d err=%v", job.State, job.LastError, transport.calls, err)
	}
}

func TestInvalidPaymentResponseAndOversizedBodyDeadLetter(t *testing.T) {
	fixture := railsFixture(t)
	cases := []struct {
		name     string
		response func() *http.Response
		code     string
	}{
		{name: "digest mismatch", response: func() *http.Response {
			response := successResponse(t, fixture, []byte("right"))
			response.Body = io.NopCloser(strings.NewReader("wrong"))
			return response
		}, code: "RESPONSE_DIGEST_MISMATCH"},
		{name: "wrong lock transaction", response: func() *http.Response {
			body := []byte("answer")
			digest := sha256.Sum256(body)
			payment, err := escrowcall.BuildPaymentResponse(fixture.input.Offer, escrowcall.ResponseBinding{CallID: fixture.input.JobID,
				ContentDigest: "0x" + hex.EncodeToString(digest[:]), LockTransactionHash: testHash("98"), Payer: fixture.input.Payer})
			if err != nil {
				t.Fatal(err)
			}
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}, "Payment-Response": {payment.PaymentResponseHeader}}, Body: io.NopCloser(bytes.NewReader(body))}
		}, code: "INVALID_PAYMENT_RESPONSE"},
		{name: "oversized", response: func() *http.Response {
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(io.LimitReader(zeroReader{}, MaxResponseBytes+1))}
		}, code: "RESPONSE_TOO_LARGE"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			store := newMemoryStore(fixture.now)
			service := newTestService(t, store, &fakeChain{observations: []ChainObservation{fixture.observation}}, &fakeRestrictedTransport{responses: []*http.Response{test.response()}}, fixture.now) //nolint:bodyclose // DispatchOne owns the synthetic response body.
			_, _, _ = service.Enqueue(context.Background(), fixture.input)
			job, err := service.DispatchOne(context.Background())
			if err != nil || job.State != StateDeadLetter || job.LastError != test.code {
				t.Fatalf("state=%s code=%s err=%v", job.State, job.LastError, err)
			}
		})
	}
}

func TestNewServiceRejectsUnrestrictedTransport(t *testing.T) {
	fixture := railsFixture(t)
	_, err := NewService(newMemoryStore(fixture.now), staticLeadership(7), &fakeChain{}, staticOperationGate{}, staticIntegrityGate{}, http.DefaultTransport, testConfig(fixture.now))
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("error=%v", err)
	}
}

func TestNewServiceRejectsLeaseShorterThanNetworkAndPersistenceWindow(t *testing.T) {
	fixture := railsFixture(t)
	config := testConfig(fixture.now)
	config.LeaseDuration = config.HTTPTimeout + leaseWriteMargin - time.Nanosecond
	_, err := NewService(newMemoryStore(fixture.now), staticLeadership(7), &fakeChain{}, staticOperationGate{}, staticIntegrityGate{}, &fakeRestrictedTransport{}, config)
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("error=%v", err)
	}
}

func TestEnqueueRejectsZeroLockIdentity(t *testing.T) {
	fixture := railsFixture(t)
	service := newTestService(t, newMemoryStore(fixture.now), &fakeChain{}, &fakeRestrictedTransport{}, fixture.now)
	fixture.input.LockedTransactionHash = "0x" + strings.Repeat("0", 64)
	if _, _, err := service.Enqueue(context.Background(), fixture.input); !errors.Is(err, ErrInvalidJob) {
		t.Fatalf("zero lock error=%v", err)
	}
}

func TestEnqueueRejectsDestinationOutsideTransportContract(t *testing.T) {
	fixture := railsFixture(t)
	service := newTestService(t, newMemoryStore(fixture.now), &fakeChain{}, &fakeRestrictedTransport{}, fixture.now)
	for _, raw := range []string{"http://seller.example/v1/jobs", "https://seller.example/" + strings.Repeat("a", maxDestinationURLBytes)} {
		candidate := fixture.input
		candidate.URL = raw
		if _, _, err := service.Enqueue(t.Context(), candidate); !errors.Is(err, ErrInvalidJob) {
			t.Fatalf("destination %q error=%v", raw, err)
		}
	}
}

func TestEnqueueRejectsOfferNetworkThatDiffersFromJobChain(t *testing.T) {
	fixture := railsFixture(t)
	fixture.input.Offer.Accepted.Network = escrowcall.BaseMainnetNetwork
	service := newTestService(t, newMemoryStore(fixture.now), &fakeChain{}, &fakeRestrictedTransport{}, fixture.now)
	if _, _, err := service.Enqueue(t.Context(), fixture.input); !errors.Is(err, ErrInvalidJob) {
		t.Fatalf("error=%v", err)
	}
}

func TestEncodeInputNormalizesNilHeadersToJSONObject(t *testing.T) {
	fixture := railsFixture(t)
	fixture.input.Headers = nil
	_, headers, _, _, _, err := encodeInput(fixture.input)
	if err != nil || string(headers) != "{}" {
		t.Fatalf("headers=%s err=%v", headers, err)
	}
}

type railsTestFixture struct {
	now              time.Time
	input            EnqueueInput
	observation      ChainObservation
	purchaseSpecHash string
}

func railsFixture(t *testing.T) railsTestFixture {
	t.Helper()
	now := time.Unix(1_800_000_000, 0).UTC()
	body := []byte(`{"query":"status"}`)
	spec, err := purchasespec.Build(purchasespec.Input{OrgID: "org-test", AgentID: "agent-test", TaskID: "task-test", Method: "POST",
		URL: "https://seller.example/v1/jobs", Body: body, Headers: []purchasespec.Header{{Name: "Content-Type", Value: "application/json"}, {Name: "X-Request-ID", Value: "request-1"}},
		Response: purchasespec.ResponseContract{ContentType: "application/json", SchemaRef: "schema:job-v1"}, Category: "research"})
	if err != nil {
		t.Fatal(err)
	}
	quote := sellerquote.Quote{PurchaseSpecHash: spec.PurchaseSpecHash, SellerID: testHash("11"), ResourceID: testHash("12"), DirectoryVersion: 7,
		SchemeVersion: escrowcall.SchemeVersion, ChainID: "84532", Asset: "0x1111111111111111111111111111111111111111", AmountBaseUnits: "2500000",
		PayTo: "0x2222222222222222222222222222222222222222", AckAuthority: "0x3333333333333333333333333333333333333333",
		VerificationSpecHash: testHash("13"), DeclaredWorkTime: 120, VerificationBudgetSeconds: 60, QuoteExpiresAt: uint64(now.Unix() + 900), QuoteNonce: testHash("14")}
	key, _ := crypto.HexToECDSA(strings.Repeat("42", 32))
	directory := "0x6666666666666666666666666666666666666666"
	digest, err := quote.Digest(directory)
	if err != nil {
		t.Fatal(err)
	}
	signatureBytes, err := crypto.Sign(digest[:], key)
	if err != nil {
		t.Fatal(err)
	}
	intake := escrowcall.QuoteIntakeBinding{ServiceDirectory: directory, QuoteHash: digest.Hex(), QuoteSigner: strings.ToLower(crypto.PubkeyToAddress(key.PublicKey).Hex())}
	offer, err := escrowcall.BuildOffer(now, x402types.ResourceInfo{URL: spec.Spec.CanonicalURL, Description: "Run", MimeType: "application/json"}, spec.CanonicalJSON, body, quote, "0x"+hex.EncodeToString(signatureBytes), intake)
	if err != nil {
		t.Fatal(err)
	}
	binding := escrowcall.OperationBinding{CallID: testHash("21"), EscrowContract: "0x4444444444444444444444444444444444444444", CommitmentHash: testHash("22"), SchemeVersion: 1, ResourceRequest: offer.ResourceRequestDigest}
	payment, err := escrowcall.BuildPaymentSignature(offer, binding)
	if err != nil {
		t.Fatal(err)
	}
	input := EnqueueInput{JobID: binding.CallID, OperationID: testHash("23"), OrganizationID: "org-test", ChainID: 84532, LeadershipEpoch: 7,
		DeliverBy: uint64(now.Unix() + 600), Method: "POST", URL: spec.Spec.CanonicalURL, Headers: http.Header{"Content-Type": {"application/json"}, "X-Request-Id": {"request-1"}},
		Body: body, CanonicalSpecJSON: spec.CanonicalJSON, Offer: offer, Payment: payment, Binding: binding,
		LockedTransactionHash: testHash("41"), Payer: "0x5555555555555555555555555555555555555555", ValidatedChainTime: uint64(now.Unix())}
	return railsTestFixture{now: now, input: input, observation: ChainObservation{Timestamp: uint64(now.Unix()), EvidenceDigest: testHash("31"), ObservedAt: now}, purchaseSpecHash: spec.PurchaseSpecHash}
}

func successResponse(t *testing.T, fixture railsTestFixture, body []byte) *http.Response {
	t.Helper()
	digest := sha256.Sum256(body)
	payment, err := escrowcall.BuildPaymentResponse(fixture.input.Offer, escrowcall.ResponseBinding{CallID: fixture.input.Binding.CallID,
		ContentDigest: "0x" + hex.EncodeToString(digest[:]), LockTransactionHash: fixture.input.LockedTransactionHash, Payer: fixture.input.Payer})
	if err != nil {
		t.Fatal(err)
	}
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}, "Payment-Response": {payment.PaymentResponseHeader}}, Body: io.NopCloser(bytes.NewReader(body))}
}

type staticLeadership uint64

func (s staticLeadership) Current(context.Context, string) (uint64, error) { return uint64(s), nil }

func (s staticLeadership) Fence(ctx context.Context, _ string, expected uint64, effect func(context.Context) error) error {
	if uint64(s) != expected {
		return ErrLeadershipChanged
	}
	return effect(ctx)
}

type changingLeadership struct{ current, fenced uint64 }

func (g changingLeadership) Current(context.Context, string) (uint64, error) { return g.current, nil }

func (g changingLeadership) Fence(ctx context.Context, _ string, expected uint64, effect func(context.Context) error) error {
	if g.fenced != expected {
		return ErrLeadershipChanged
	}
	return effect(ctx)
}

type scopeLeadership struct {
	epoch   uint64
	active  bool
	effects int
}

func (g *scopeLeadership) Current(context.Context, string) (uint64, error) { return g.epoch, nil }

func (g *scopeLeadership) Fence(ctx context.Context, _ string, expected uint64, effect func(context.Context) error) error {
	if g.epoch != expected {
		return ErrLeadershipChanged
	}
	g.active = true
	g.effects++
	defer func() { g.active = false }()
	return effect(ctx)
}

type fakeChain struct {
	mu           sync.Mutex
	observations []ChainObservation
	errors       []error
	onConfirmed  func()
	calls        int
}

type staticOperationGate struct{ err error }

func (g staticOperationGate) Check(context.Context, Job) error { return g.err }

type sequenceOperationGate struct {
	errors []error
	calls  int
}

func (g *sequenceOperationGate) Check(context.Context, Job) error {
	index := g.calls
	g.calls++
	if index >= len(g.errors) {
		return nil
	}
	return g.errors[index]
}

type staticIntegrityGate struct{ err error }

func (g staticIntegrityGate) Check(context.Context) error { return g.err }

type eventRecorder struct{ events []Event }

func (r *eventRecorder) Record(_ context.Context, event Event) { r.events = append(r.events, event) }

func (f *fakeChain) Confirmed(context.Context, uint64) (ChainObservation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.onConfirmed != nil {
		f.onConfirmed()
	}
	index := f.calls
	f.calls++
	var err error
	if index < len(f.errors) {
		err = f.errors[index]
	}
	if err != nil {
		return ChainObservation{}, err
	}
	if index >= len(f.observations) {
		return ChainObservation{}, errors.New("no observation")
	}
	return f.observations[index], nil
}

type requestRecord struct{ url, payment, body string }
type fakeRestrictedTransport struct {
	mu          sync.Mutex
	responses   []*http.Response
	errors      []error
	requests    []requestRecord
	onRoundTrip func()
	calls       int
}

func (*fakeRestrictedTransport) ascpRestrictedTransport() {}
func (f *fakeRestrictedTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.onRoundTrip != nil {
		f.onRoundTrip()
	}
	body, _ := io.ReadAll(request.Body)
	index := f.calls
	f.calls++
	f.requests = append(f.requests, requestRecord{url: request.URL.String(), payment: request.Header.Get("Payment-Signature"), body: string(body)})
	if index < len(f.errors) && f.errors[index] != nil {
		return nil, f.errors[index]
	}
	if index >= len(f.responses) {
		return nil, errors.New("unexpected request")
	}
	return f.responses[index], nil
}

type zeroReader struct{}

func (zeroReader) Read(value []byte) (int, error) {
	for index := range value {
		value[index] = 0
	}
	return len(value), nil
}

func testHash(pair string) string { return "0x" + strings.Repeat(pair, 32) }

func testConfig(now time.Time) Config {
	return Config{WorkerID: "rails-test", LeaseDuration: 20 * time.Second, HTTPTimeout: 10 * time.Second, RetryDelay: time.Second, MaxAttempts: 3, MaxObservationAge: 30 * time.Second, Clock: func() time.Time { return now }}
}
func newTestService(t *testing.T, store *memoryStore, chain *fakeChain, transport *fakeRestrictedTransport, now time.Time) *Service {
	return newTestServiceWithEpoch(t, store, chain, transport, now, 7)
}
func newTestServiceWithEpoch(t *testing.T, store *memoryStore, chain *fakeChain, transport *fakeRestrictedTransport, now time.Time, epoch uint64) *Service {
	t.Helper()
	service, err := NewService(store, staticLeadership(epoch), chain, staticOperationGate{}, staticIntegrityGate{}, transport, testConfig(now))
	if err != nil {
		t.Fatal(err)
	}
	return service
}

type memoryStore struct {
	mu  sync.Mutex
	now time.Time
	job *Job
}

func newMemoryStore(now time.Time) *memoryStore { return &memoryStore{now: now} }
func (s *memoryStore) Enqueue(_ context.Context, input EnqueueInput) (Job, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.job != nil {
		return cloneJob(*s.job), true, nil
	}
	job := Job{EnqueueInput: cloneInput(input), State: StateQueued, EligibleAfter: s.now, CreatedAt: s.now, UpdatedAt: s.now}
	s.job = &job
	return cloneJob(job), false, nil
}
func (s *memoryStore) claim(finalize bool, worker string, duration time.Duration) (Lease, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.job == nil {
		return Lease{}, ErrNoWork
	}
	eligible := s.job.State == StateResponseStored
	if !finalize {
		eligible = (s.job.State == StateQueued || s.job.State == StateRetryWait || s.job.State == StateSending) && !s.job.EligibleAfter.After(s.now)
	}
	if !eligible || (!s.job.LeaseExpiresAt.IsZero() && s.job.LeaseExpiresAt.After(s.now)) {
		return Lease{}, ErrNoWork
	}
	s.job.LeaseOwner = worker
	s.job.LeaseToken = "lease-token"
	s.job.LeaseExpiresAt = s.now.Add(duration)
	return Lease{Job: cloneJob(*s.job), Token: s.job.LeaseToken}, nil
}
func (s *memoryStore) ClaimDispatch(_ context.Context, w string, d time.Duration) (Lease, error) {
	return s.claim(false, w, d)
}
func (s *memoryStore) ClaimFinalization(_ context.Context, w string, d time.Duration) (Lease, error) {
	return s.claim(true, w, d)
}
func (s *memoryStore) leased(lease Lease) error {
	if s.job == nil || s.job.LeaseOwner != lease.Job.LeaseOwner || s.job.LeaseToken != lease.Token || !s.job.LeaseExpiresAt.After(s.now) {
		return ErrLeaseLost
	}
	return nil
}
func (s *memoryStore) MarkSending(_ context.Context, l Lease, _ ChainObservation) (Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.leased(l); err != nil {
		return Job{}, err
	}
	if s.job.AttemptCount >= maxAttempts {
		return Job{}, ErrStateConflict
	}
	s.job.State = StateSending
	s.job.AttemptCount++
	s.job.UpdatedAt = s.now
	return cloneJob(*s.job), nil
}
func (s *memoryStore) RecordResponse(_ context.Context, l Lease, r StoredResponse, state State, code string, next time.Time) (Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.leased(l); err != nil {
		return Job{}, err
	}
	if s.job.State != StateSending || s.job.AttemptCount != r.Attempt {
		return Job{}, ErrStateConflict
	}
	s.job.State = state
	s.job.LastError = code
	s.job.EligibleAfter = next
	s.job.ResponseStatus = r.Status
	s.job.ResponseType = r.ContentType
	s.job.ResponseDigest = r.Digest
	s.job.PaymentResponse = r.PaymentResponse
	s.job.ResponseBody = append([]byte(nil), r.Body...)
	s.job.UpdatedAt = s.now
	return cloneJob(*s.job), nil
}
func (s *memoryStore) RecordTransportFailure(_ context.Context, l Lease, code string, state State, next time.Time) (Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.leased(l); err != nil {
		return Job{}, err
	}
	s.job.State = state
	s.job.LastError = code
	s.job.EligibleAfter = next
	s.job.UpdatedAt = s.now
	return cloneJob(*s.job), nil
}
func (s *memoryStore) MarkDeadlineMissing(_ context.Context, l Lease, o ChainObservation, code string) (Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.leased(l); err != nil {
		return Job{}, err
	}
	s.job.State = StateMissing
	s.job.CaptureEvidence = o.EvidenceDigest
	s.job.LastError = code
	return cloneJob(*s.job), nil
}
func (s *memoryStore) FinalizeCapture(_ context.Context, l Lease, o ChainObservation) (Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.leased(l); err != nil {
		return Job{}, err
	}
	if s.job.State != StateResponseStored {
		return Job{}, ErrStateConflict
	}
	s.job.State = StateCaptured
	s.job.CapturedAt = o.Timestamp
	s.job.CaptureEvidence = o.EvidenceDigest
	s.job.LastError = ""
	return cloneJob(*s.job), nil
}
func (s *memoryStore) ReleaseLease(_ context.Context, l Lease) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.job == nil || s.job.LeaseToken != l.Token {
		return ErrLeaseLost
	}
	s.job.LeaseOwner = ""
	s.job.LeaseToken = ""
	s.job.LeaseExpiresAt = time.Time{}
	return nil
}
func (s *memoryStore) Get(_ context.Context, _ string) (Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.job == nil {
		return Job{}, errNoRows
	}
	return cloneJob(*s.job), nil
}
func cloneJob(job Job) Job {
	job.EnqueueInput = cloneInput(job.EnqueueInput)
	job.ResponseBody = append([]byte(nil), job.ResponseBody...)
	return job
}

var errNoRows = errors.New("not found")

var _ Store = (*memoryStore)(nil)
