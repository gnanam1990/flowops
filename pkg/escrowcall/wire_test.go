package escrowcall

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/gnanam1990/flowops/pkg/purchasespec"
	"github.com/gnanam1990/flowops/pkg/sellerquote"
	x402http "github.com/x402-foundation/x402/go/v2/http"
	x402types "github.com/x402-foundation/x402/go/v2/types"
)

const testNowUnix = int64(1_800_000_000)

func TestBuildOfferUsesCompleteX402V2Envelope(t *testing.T) {
	fixture := newFixture(t)
	offer := fixture.offer
	if offer.Required.X402Version != 2 || offer.Accepted.Scheme != "escrow-call/1" || offer.Accepted.Network != BaseSepoliaNetwork {
		t.Fatalf("unexpected envelope: %+v", offer.Required)
	}
	if offer.Accepted.Amount != fixture.quote.AmountBaseUnits || offer.Accepted.Asset != fixture.quote.Asset ||
		offer.Accepted.PayTo != fixture.quote.PayTo || offer.Accepted.MaxTimeoutSeconds != 900 {
		t.Fatalf("requirements drifted: %+v", offer.Accepted)
	}
	decoded, err := base64.StdEncoding.Strict().DecodeString(offer.PaymentRequiredHeader)
	if err != nil || !bytes.Equal(decoded, offer.CanonicalJSON) {
		t.Fatalf("PAYMENT-REQUIRED decode=%q err=%v", decoded, err)
	}
	if err := ValidateOffer(offer); err != nil {
		t.Fatalf("ValidateOffer: %v", err)
	}
	challenge, err := ChallengeHTTP(fixture.now, offer, fixture.specJSON, fixture.body)
	if err != nil || challenge.StatusCode != http.StatusPaymentRequired || challenge.Header.Get("Payment-Required") != offer.PaymentRequiredHeader || challenge.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("challenge HTTP=%+v err=%v", challenge, err)
	}
	if _, err := ChallengeHTTP(time.Unix(int64(fixture.quote.QuoteExpiresAt), 0), offer, fixture.specJSON, fixture.body); !errors.Is(err, ErrInvalidOffer) {
		t.Fatalf("expired challenge error = %v", err)
	}
	resourceSwap := deepCopyOffer(t, offer)
	resourceSwap.Required.Resource.URL = "https://seller.example/other"
	resourceSwap.CanonicalJSON, _ = canonicalJSON(resourceSwap.Required)
	resourceSwap.PaymentRequiredHeader = base64.StdEncoding.EncodeToString(resourceSwap.CanonicalJSON)
	if _, err := ChallengeHTTP(fixture.now, resourceSwap, fixture.specJSON, fixture.body); !errors.Is(err, ErrInvalidOffer) {
		t.Fatalf("coherent resource substitution error = %v", err)
	}
	extension := offer.Accepted.Extra["escrowCall"].(map[string]interface{})
	if extension["resourceRequestDigest"] != offer.ResourceRequestDigest || extension["sellerQuoteSignature"] != fixture.signature {
		t.Fatalf("seller quote extension drifted: %+v", extension)
	}
}

func TestBuildOfferRejectsUnprovenOrMismatchedInputs(t *testing.T) {
	fixture := newFixture(t)
	cases := []struct {
		name     string
		resource x402types.ResourceInfo
		spec     []byte
		body     []byte
		quote    sellerquote.Quote
		now      time.Time
	}{
		{name: "body drift", resource: fixture.resource, spec: fixture.specJSON, body: []byte(`{"query":"changed"}`), quote: fixture.quote, now: fixture.now},
		{name: "noncanonical spec", resource: fixture.resource, spec: append([]byte(" "), fixture.specJSON...), body: fixture.body, quote: fixture.quote, now: fixture.now},
		{name: "resource URL drift", resource: x402types.ResourceInfo{URL: "https://seller.example/other"}, spec: fixture.specJSON, body: fixture.body, quote: fixture.quote, now: fixture.now},
		{name: "purchase hash drift", resource: fixture.resource, spec: fixture.specJSON, body: fixture.body, quote: mutateQuote(fixture.quote, func(q *sellerquote.Quote) { q.PurchaseSpecHash = hashByte("99") }), now: fixture.now},
		{name: "expired", resource: fixture.resource, spec: fixture.specJSON, body: fixture.body, quote: mutateQuote(fixture.quote, func(q *sellerquote.Quote) { q.QuoteExpiresAt = uint64(testNowUnix) }), now: fixture.now},
		{name: "unsupported chain", resource: fixture.resource, spec: fixture.specJSON, body: fixture.body, quote: mutateQuote(fixture.quote, func(q *sellerquote.Quote) { q.ChainID = "1" }), now: fixture.now},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if _, err := BuildOffer(test.now, test.resource, test.spec, test.body, test.quote, fixture.signature, fixture.intake); !errors.Is(err, ErrInvalidOffer) {
				t.Fatalf("error = %v", err)
			}
		})
	}
	if _, err := BuildOffer(fixture.now, fixture.resource, fixture.specJSON, fixture.body, fixture.quote, "0x01", fixture.intake); !errors.Is(err, ErrInvalidOffer) {
		t.Fatalf("short quote signature error = %v", err)
	}
	wrongIntake := fixture.intake
	wrongIntake.QuoteSigner = "0x7777777777777777777777777777777777777777"
	if _, err := BuildOffer(fixture.now, fixture.resource, fixture.specJSON, fixture.body, fixture.quote, fixture.signature, wrongIntake); !errors.Is(err, ErrInvalidOffer) {
		t.Fatalf("wrong durable quote signer error = %v", err)
	}
}

func TestCanonicalJSONUsesRFC8785ObjectOrderAndSafeIntegers(t *testing.T) {
	canonical, err := canonicalJSON(map[string]interface{}{"z": "<ok>", "a": uint64(2), "nested": map[string]interface{}{"b": true, "a": nil}})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(canonical), `{"a":2,"nested":{"a":null,"b":true},"z":"<ok>"}`; got != want {
		t.Fatalf("canonical JSON %s, want %s", got, want)
	}
	if _, err := canonicalJSON(map[string]interface{}{"unsafe": uint64(9_007_199_254_740_992)}); !errors.Is(err, errNonCanonicalValue) {
		t.Fatalf("unsafe integer error = %v", err)
	}
	unicode, err := canonicalJSON(map[string]interface{}{"line": "before\u2028after"})
	if err != nil || bytes.Contains(unicode, []byte(`\u2028`)) || !bytes.Contains(unicode, []byte("\u2028")) {
		t.Fatalf("RFC 8785 Unicode encoding=%q err=%v", unicode, err)
	}
	if _, err := canonicalJSON(map[string]interface{}{"invalid": string([]byte{0xff})}); !errors.Is(err, errNonCanonicalValue) {
		t.Fatalf("invalid UTF-8 error = %v", err)
	}
}

func TestResourceRequestDigestMutationsChangeDigest(t *testing.T) {
	baseline := newFixture(t)
	baselineDigest, err := ResourceRequestDigest(baseline.specJSON, baseline.body)
	if err != nil {
		t.Fatal(err)
	}
	mutations := []purchasespec.Input{
		purchaseInput("PUT", "https://seller.example/v1/jobs?a=1&b=2", baseline.body, "request-1"),
		purchaseInput("POST", "https://seller.example/v1/other?a=1&b=2", baseline.body, "request-1"),
		purchaseInput("POST", "https://seller.example/v1/jobs?a=1&b=2", baseline.body, "request-2"),
		purchaseInput("POST", "https://seller.example/v1/jobs?a=1&b=2", []byte(`{"query":"changed"}`), "request-1"),
	}
	for index, input := range mutations {
		result, err := purchasespec.Build(input)
		if err != nil {
			t.Fatalf("mutation %d: %v", index, err)
		}
		got, err := ResourceRequestDigest(result.CanonicalJSON, input.Body)
		if err != nil || got == baselineDigest {
			t.Fatalf("mutation %d digest=%s err=%v", index, got, err)
		}
	}
}

func TestPaymentPayloadAndPreEgressVerification(t *testing.T) {
	fixture := newFixture(t)
	payment, err := BuildPaymentSignature(fixture.offer, fixture.binding)
	if err != nil {
		t.Fatal(err)
	}
	if payment.Payload.Resource != nil || payment.Payload.X402Version != X402Version || payment.Payload.Accepted.Scheme != Scheme {
		t.Fatalf("unexpected payment payload: %+v", payment.Payload)
	}
	if len(payment.Payload.Payload) != 4 || payment.Payload.Payload["callId"] != fixture.binding.CallID ||
		payment.Payload.Payload["escrowContract"] != fixture.binding.EscrowContract ||
		payment.Payload.Payload["commitmentHash"] != fixture.binding.CommitmentHash ||
		payment.Payload.Payload["schemeVersion"] != floatCompatibleSchemeVersion() {
		t.Fatalf("operation binding drifted: %+v", payment.Payload.Payload)
	}
	digest, err := VerifyBeforeEgress(fixture.now, "POST", fixture.resource.URL, payment.PaymentSignatureHeader, fixture.offer, fixture.binding, fixture.specJSON, fixture.body, fixtureOutboundHeaders())
	if err != nil || digest != payment.PaymentProofDigest {
		t.Fatalf("pre-egress digest=%s err=%v", digest, err)
	}
}

func TestOfficialX402V2HTTPClientConsumesExactHeaders(t *testing.T) {
	fixture := newFixture(t)
	payment, err := BuildPaymentSignature(fixture.offer, fixture.binding)
	if err != nil {
		t.Fatal(err)
	}
	official := x402http.NewClient(nil)
	required, err := official.GetPaymentRequiredResponse(map[string]string{"payment-required": fixture.offer.PaymentRequiredHeader}, nil)
	if err != nil {
		t.Fatalf("official PAYMENT-REQUIRED decode: %v", err)
	}
	reencodedRequired, err := canonicalJSON(required)
	if err != nil || !bytes.Equal(reencodedRequired, fixture.offer.CanonicalJSON) {
		t.Fatalf("official required bytes drifted: %s err=%v", reencodedRequired, err)
	}
	headers, err := official.EncodePaymentSignatureHeader(payment.CanonicalJSON)
	if err != nil || headers["PAYMENT-SIGNATURE"] != payment.PaymentSignatureHeader || len(headers) != 1 {
		t.Fatalf("official payment headers=%+v err=%v", headers, err)
	}
	response, err := BuildPaymentResponse(fixture.offer, ResponseBinding{
		CallID: fixture.binding.CallID, ContentDigest: hashByte("44"), LockTransactionHash: hashByte("55"),
		Payer: "0x5555555555555555555555555555555555555555",
	})
	if err != nil {
		t.Fatal(err)
	}
	settled, err := official.GetPaymentSettleResponse(map[string]string{"payment-response": response.PaymentResponseHeader})
	if err != nil || !settled.Success || settled.Transaction != hashByte("55") || settled.Amount != fixture.quote.AmountBaseUnits {
		t.Fatalf("official settle=%+v err=%v", settled, err)
	}
}

func TestPublishedEscrowCallVector(t *testing.T) {
	fixture := newFixture(t)
	root := repositoryRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, "vectors", "escrow-call-v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var vector struct {
		Profile                      string            `json:"profile"`
		X402Version                  int               `json:"x402Version"`
		SchemeVersion                uint16            `json:"schemeVersion"`
		PurchaseSpecCanonicalJSON    string            `json:"purchaseSpecCanonicalJSON"`
		SellerQuote                  sellerquote.Quote `json:"sellerQuote"`
		SellerQuoteHash              string            `json:"sellerQuoteHash"`
		SellerQuoteSignature         string            `json:"sellerQuoteSignature"`
		SellerQuoteSigner            string            `json:"sellerQuoteSigner"`
		ResourceRequestDigest        string            `json:"resourceRequestDigest"`
		PaymentRequiredCanonicalJSON string            `json:"paymentRequiredCanonicalJSON"`
		PaymentPayloadCanonicalJSON  string            `json:"paymentPayloadCanonicalJSON"`
		PaymentProofDigest           string            `json:"paymentProofDigest"`
		PaymentResponseCanonicalJSON string            `json:"paymentResponseCanonicalJSON"`
		VersionErrorCanonicalJSON    string            `json:"versionErrorCanonicalJSON"`
	}
	if err := json.Unmarshal(raw, &vector); err != nil {
		t.Fatal(err)
	}
	if vector.Profile != Scheme || vector.X402Version != X402Version || vector.SchemeVersion != SchemeVersion ||
		vector.PurchaseSpecCanonicalJSON != string(fixture.specJSON) || !reflect.DeepEqual(vector.SellerQuote, fixture.quote) ||
		vector.SellerQuoteHash != fixture.intake.QuoteHash || vector.SellerQuoteSignature != fixture.signature ||
		vector.SellerQuoteSigner != fixture.intake.QuoteSigner || vector.ResourceRequestDigest != fixture.offer.ResourceRequestDigest ||
		vector.PaymentRequiredCanonicalJSON != string(fixture.offer.CanonicalJSON) {
		t.Fatal("published challenge vector drifted")
	}
	payment, err := BuildPaymentSignature(fixture.offer, fixture.binding)
	if err != nil || vector.PaymentPayloadCanonicalJSON != string(payment.CanonicalJSON) || vector.PaymentProofDigest != payment.PaymentProofDigest ||
		base64.StdEncoding.EncodeToString([]byte(vector.PaymentPayloadCanonicalJSON)) != payment.PaymentSignatureHeader {
		t.Fatalf("published payment vector drifted: %v", err)
	}
	response, err := BuildPaymentResponse(fixture.offer, ResponseBinding{
		CallID: fixture.binding.CallID, ContentDigest: hashByte("44"), LockTransactionHash: hashByte("55"),
		Payer: "0x5555555555555555555555555555555555555555",
	})
	if err != nil || vector.PaymentResponseCanonicalJSON != string(response.CanonicalJSON) {
		t.Fatalf("published response vector drifted: %v", err)
	}
	failure, err := BuildPaymentError(fixture.offer, fixture.binding.CallID, ErrorVersionUnsupported, "seller rejected escrow proof")
	if err != nil || vector.VersionErrorCanonicalJSON != string(failure.CanonicalJSON) {
		t.Fatalf("published error vector drifted: %v", err)
	}
	manifest, err := os.ReadFile(filepath.Join(root, "artifacts", "escrow-call-v1.manifest.sha256"))
	if err != nil {
		t.Fatal(err)
	}
	fields := strings.Fields(string(manifest))
	if len(fields) != 2 || fields[1] != "vectors/escrow-call-v1.json" || fields[0] != sellerquote.ArtifactSHA256(raw) {
		t.Fatalf("vector integrity manifest is invalid: %q", manifest)
	}
}

func TestVerifyBeforeEgressRejectsMutationAndAmbiguousJSON(t *testing.T) {
	fixture := newFixture(t)
	payment, err := BuildPaymentSignature(fixture.offer, fixture.binding)
	if err != nil {
		t.Fatal(err)
	}
	changedBinding := fixture.binding
	changedBinding.CommitmentHash = hashByte("88")
	cases := []struct {
		name    string
		header  string
		binding OperationBinding
		body    []byte
	}{
		{name: "commitment substitution", header: payment.PaymentSignatureHeader, binding: changedBinding, body: fixture.body},
		{name: "body substitution", header: payment.PaymentSignatureHeader, binding: fixture.binding, body: []byte(`{"query":"changed"}`)},
		{name: "whitespace noncanonical", header: base64.StdEncoding.EncodeToString(append([]byte(" "), payment.CanonicalJSON...)), binding: fixture.binding, body: fixture.body},
		{name: "trailing JSON", header: base64.StdEncoding.EncodeToString(append(append([]byte{}, payment.CanonicalJSON...), []byte("{}")...)), binding: fixture.binding, body: fixture.body},
		{name: "invalid base64", header: "***", binding: fixture.binding, body: fixture.body},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if _, err := VerifyBeforeEgress(fixture.now, "POST", fixture.resource.URL, test.header, fixture.offer, test.binding, fixture.specJSON, test.body, fixtureOutboundHeaders()); err == nil {
				t.Fatal("mutation accepted")
			}
		})
	}
	duplicate := bytes.Replace(payment.CanonicalJSON, []byte(`"x402Version":2`), []byte(`"x402Version":2,"x402Version":2`), 1)
	if _, err := VerifyBeforeEgress(fixture.now, "POST", fixture.resource.URL, base64.StdEncoding.EncodeToString(duplicate), fixture.offer, fixture.binding, fixture.specJSON, fixture.body, fixtureOutboundHeaders()); err == nil {
		t.Fatal("duplicate JSON key accepted")
	}
	mutated := deepCopyOffer(t, fixture.offer)
	mutated.Accepted.Amount = "1"
	if _, err := BuildPaymentSignature(mutated, fixture.binding); !errors.Is(err, ErrInvalidPayload) {
		t.Fatalf("accepted substitution error = %v", err)
	}
	if _, err := VerifyBeforeEgress(time.Unix(int64(fixture.quote.QuoteExpiresAt), 0), "POST", fixture.resource.URL, payment.PaymentSignatureHeader, fixture.offer, fixture.binding, fixture.specJSON, fixture.body, fixtureOutboundHeaders()); !errors.Is(err, ErrInvalidPayload) {
		t.Fatalf("expired-at-egress error = %v", err)
	}
}

func TestPaymentHeadersStripsCallerPaymentAndLegacyEscrowHeaders(t *testing.T) {
	fixture := newFixture(t)
	payment, err := BuildPaymentSignature(fixture.offer, fixture.binding)
	if err != nil {
		t.Fatal(err)
	}
	source := http.Header{
		"Content-Type":      []string{"application/json"},
		"X-Request-Id":      []string{"request-1"},
		"Payment-Signature": []string{"caller-forged"},
		"Payment-Required":  []string{"caller-forged"},
		"Payment-Response":  []string{"caller-forged"},
		"X-Escrow-Intent":   []string{"legacy"},
		"X-Escrow-Call":     []string{"legacy"},
		"X-Payment":         []string{"legacy-v1"},
		"payment-response":  []string{"raw-lowercase-smuggle"},
		"Traceparent":       []string{"caller-generated"},
		"Accept-Encoding":   []string{"caller-generated"},
		"Connection":        []string{"caller-generated"},
	}
	result, err := PaymentHeaders(fixture.now, "POST", fixture.resource.URL, source, payment, fixture.offer, fixture.binding, fixture.specJSON, fixture.body)
	if err != nil {
		t.Fatal(err)
	}
	if result.Get("PAYMENT-SIGNATURE") != payment.PaymentSignatureHeader || result.Get("X-Request-Id") != "request-1" {
		t.Fatalf("generated headers = %+v", result)
	}
	for _, removed := range []string{"Payment-Required", "Payment-Response", "X-Payment", "X-Escrow-Intent", "X-Escrow-Call", "Traceparent", "Accept-Encoding", "Connection"} {
		if result.Get(removed) != "" {
			t.Fatalf("%s was not removed", removed)
		}
	}
	for name := range result {
		if strings.EqualFold(name, "payment-response") {
			t.Fatalf("raw-cased payment header survived: %q", name)
		}
	}
	if source.Get("Payment-Signature") != "caller-forged" {
		t.Fatal("source headers were mutated")
	}
	if _, err := PaymentHeaders(fixture.now, "POST", fixture.resource.URL, nil, payment, fixture.offer, fixture.binding, fixture.specJSON, fixture.body); !errors.Is(err, ErrInvalidPayload) {
		t.Fatalf("nil source error=%v", err)
	}
	changed := payment
	changed.PaymentProofDigest = hashByte("88")
	if _, err := PaymentHeaders(fixture.now, "POST", fixture.resource.URL, source, changed, fixture.offer, fixture.binding, fixture.specJSON, fixture.body); !errors.Is(err, ErrInvalidPayload) {
		t.Fatalf("forged payment object error = %v", err)
	}
	injected := source.Clone()
	injected.Set("Authorization", "secret")
	if _, err := PaymentHeaders(fixture.now, "POST", fixture.resource.URL, injected, payment, fixture.offer, fixture.binding, fixture.specJSON, fixture.body); !errors.Is(err, ErrInvalidPayload) {
		t.Fatalf("unbound authorization error = %v", err)
	}
	mutatedHeader := source.Clone()
	mutatedHeader.Set("X-Request-Id", "request-2")
	if _, err := PaymentHeaders(fixture.now, "POST", fixture.resource.URL, mutatedHeader, payment, fixture.offer, fixture.binding, fixture.specJSON, fixture.body); !errors.Is(err, ErrInvalidPayload) {
		t.Fatalf("bound header mutation error = %v", err)
	}
	duplicateHeader := source.Clone()
	duplicateHeader["X-Request-Id"] = []string{"request-1", "request-1"}
	if _, err := PaymentHeaders(fixture.now, "POST", fixture.resource.URL, duplicateHeader, payment, fixture.offer, fixture.binding, fixture.specJSON, fixture.body); !errors.Is(err, ErrInvalidPayload) {
		t.Fatalf("duplicate bound header error = %v", err)
	}
}

func TestPrepareRequestBindsActualRoutingAndRebuildsExactBody(t *testing.T) {
	fixture := newFixture(t)
	payment, err := BuildPaymentSignature(fixture.offer, fixture.binding)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest("POST", fixture.resource.URL, bytes.NewReader([]byte("untrusted original body")))
	if err != nil {
		t.Fatal(err)
	}
	request.Header = fixtureOutboundHeaders()
	prepared, err := PrepareRequest(fixture.now, request, fixture.body, payment, fixture.offer, fixture.binding, fixture.specJSON)
	if err != nil {
		t.Fatalf("prepare error=%v method=%q url=%q host=%q requestURI=%q trailers=%v transfer=%v headers=%v", err, request.Method, request.URL.String(), request.Host, request.RequestURI, request.Trailer, request.TransferEncoding, request.Header)
	}
	preparedBody, err := io.ReadAll(prepared.Body)
	if err != nil || !bytes.Equal(preparedBody, fixture.body) || prepared.ContentLength != int64(len(fixture.body)) ||
		prepared.Header.Get("PAYMENT-SIGNATURE") != payment.PaymentSignatureHeader || request.Header.Get("PAYMENT-SIGNATURE") != "" {
		t.Fatalf("prepared body=%q length=%d headers=%+v source=%+v err=%v", preparedBody, prepared.ContentLength, prepared.Header, request.Header, err)
	}
	replayedBody, err := prepared.GetBody()
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := io.ReadAll(replayedBody)
	if err != nil || !bytes.Equal(replayed, fixture.body) {
		t.Fatalf("GetBody=%q err=%v", replayed, err)
	}

	cases := []struct {
		name   string
		mutate func(*http.Request)
		body   []byte
	}{
		{name: "method", mutate: func(request *http.Request) { request.Method = "PUT" }, body: fixture.body},
		{name: "canonical URL", mutate: func(request *http.Request) { request.URL.RawQuery = "b=2&a=1" }, body: fixture.body},
		{name: "host override", mutate: func(request *http.Request) { request.Host = "attacker.example" }, body: fixture.body},
		{name: "request URI", mutate: func(request *http.Request) { request.RequestURI = "/proxy-form" }, body: fixture.body},
		{name: "trailer", mutate: func(request *http.Request) { request.Trailer = http.Header{"X-Late": []string{"value"}} }, body: fixture.body},
		{name: "transfer encoding", mutate: func(request *http.Request) { request.TransferEncoding = []string{"chunked"} }, body: fixture.body},
		{name: "body", mutate: func(_ *http.Request) {}, body: []byte(`{"query":"changed"}`)},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			candidate, err := http.NewRequest("POST", fixture.resource.URL, bytes.NewReader(fixture.body))
			if err != nil {
				t.Fatal(err)
			}
			candidate.Header = fixtureOutboundHeaders()
			test.mutate(candidate)
			if _, err := PrepareRequest(fixture.now, candidate, test.body, payment, fixture.offer, fixture.binding, fixture.specJSON); !errors.Is(err, ErrInvalidPayload) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestNewHTTPClientRequiresRestrictedShapeAndRefusesRedirects(t *testing.T) {
	transport := roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New("not called") })
	client, err := NewHTTPClient(transport, 30*time.Second)
	if err != nil || client.Transport == nil || client.Timeout != 30*time.Second || client.Jar != nil {
		t.Fatalf("client=%+v err=%v", client, err)
	}
	if err := client.CheckRedirect(&http.Request{}, nil); !errors.Is(err, http.ErrUseLastResponse) {
		t.Fatalf("redirect error = %v", err)
	}
	for _, input := range []struct {
		transport http.RoundTripper
		timeout   time.Duration
	}{{nil, time.Second}, {transport, 0}, {transport, MaxHTTPTimeout + time.Nanosecond}} {
		if _, err := NewHTTPClient(input.transport, input.timeout); !errors.Is(err, ErrInvalidPayload) {
			t.Fatalf("unsafe client config error = %v", err)
		}
	}
}

func TestPaymentResponseSuccessAndNormativeFailures(t *testing.T) {
	fixture := newFixture(t)
	binding := ResponseBinding{CallID: fixture.binding.CallID, ContentDigest: hashByte("44"), LockTransactionHash: hashByte("55"), Payer: "0x5555555555555555555555555555555555555555"}
	response, err := BuildPaymentResponse(fixture.offer, binding)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodePaymentResponse(response.PaymentResponseHeader, fixture.offer, fixture.binding.CallID)
	if err != nil || !decoded.Response.Success || decoded.Response.Amount != fixture.quote.AmountBaseUnits || decoded.Response.Transaction != binding.LockTransactionHash {
		t.Fatalf("decoded=%+v err=%v", decoded, err)
	}
	if _, err := DecodePaymentResponse(response.PaymentResponseHeader, fixture.offer, hashByte("77")); !errors.Is(err, ErrInvalidResponse) {
		t.Fatalf("cross-operation response error = %v", err)
	}
	successHTTP, err := PaymentResponseHTTP(response, fixture.offer, fixture.binding.CallID)
	if err != nil || successHTTP.StatusCode != http.StatusOK || successHTTP.Header.Get("Cache-Control") != "private" {
		t.Fatalf("success HTTP=%+v err=%v", successHTTP, err)
	}
	for _, reason := range []string{ErrorEscrowNotFound, ErrorEscrowStateInvalid, ErrorAmountMismatch, ErrorPayeeMismatch, ErrorCommitmentMismatch, ErrorDeadlineUnworkable, ErrorVersionUnsupported} {
		t.Run(reason, func(t *testing.T) {
			failed, err := BuildPaymentError(fixture.offer, fixture.binding.CallID, reason, "seller rejected escrow proof")
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := DecodePaymentResponse(failed.PaymentResponseHeader, fixture.offer, fixture.binding.CallID)
			if err != nil || decoded.Response.Success || decoded.Response.ErrorReason != reason {
				t.Fatalf("decoded=%+v err=%v", decoded, err)
			}
			if reason == ErrorVersionUnsupported && decoded.Response.Extra["supportedSchemeVersions"] == nil {
				t.Fatal("VERSION_UNSUPPORTED omitted supported list")
			}
			failureHTTP, err := PaymentResponseHTTP(failed, fixture.offer, fixture.binding.CallID)
			if err != nil || failureHTTP.StatusCode != http.StatusPaymentRequired || failureHTTP.Header.Get("Cache-Control") != "no-store" {
				t.Fatalf("failure HTTP=%+v err=%v", failureHTTP, err)
			}
		})
	}
	if _, err := BuildPaymentError(fixture.offer, fixture.binding.CallID, "UNKNOWN", ""); !errors.Is(err, ErrInvalidResponse) {
		t.Fatalf("unknown failure error = %v", err)
	}
	if _, err := BuildPaymentError(fixture.offer, "", ErrorEscrowNotFound, ""); !errors.Is(err, ErrInvalidResponse) {
		t.Fatalf("unbound failure error = %v", err)
	}
	if _, err := DecodePaymentResponse(base64.StdEncoding.EncodeToString(append([]byte(" "), response.CanonicalJSON...)), fixture.offer, fixture.binding.CallID); !errors.Is(err, ErrInvalidResponse) {
		t.Fatalf("noncanonical response error = %v", err)
	}
}

func TestValidateOfferRejectsCoherentForgedRequirement(t *testing.T) {
	fixture := newFixture(t)
	forged := deepCopyOffer(t, fixture.offer)
	forged.Accepted.Amount = "1"
	forged.Required.Accepts[0].Amount = "1"
	forged.CanonicalJSON, _ = canonicalJSON(forged.Required)
	forged.PaymentRequiredHeader = base64.StdEncoding.EncodeToString(forged.CanonicalJSON)
	if err := ValidateOffer(forged); !errors.Is(err, ErrInvalidOffer) {
		t.Fatalf("forged quote/requirement relationship error = %v", err)
	}
	forged = deepCopyOffer(t, fixture.offer)
	extension := forged.Accepted.Extra["escrowCall"].(map[string]interface{})
	extension["unknown"] = "smuggled"
	forged.Required.Accepts[0] = forged.Accepted
	forged.CanonicalJSON, _ = canonicalJSON(forged.Required)
	forged.PaymentRequiredHeader = base64.StdEncoding.EncodeToString(forged.CanonicalJSON)
	if err := ValidateOffer(forged); !errors.Is(err, ErrInvalidOffer) {
		t.Fatalf("unknown extension error = %v", err)
	}
	forged = deepCopyOffer(t, fixture.offer)
	forged.Intake.QuoteSigner = "0x7777777777777777777777777777777777777777"
	if err := ValidateOffer(forged); !errors.Is(err, ErrInvalidOffer) {
		t.Fatalf("forged durable intake binding error = %v", err)
	}
}

func TestParallelBuildAndVerifyDoesNotMutateSharedOffer(t *testing.T) {
	fixture := newFixture(t)
	const workers = 64
	var wait sync.WaitGroup
	errorsSeen := make(chan error, workers)
	for index := 0; index < workers; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			payment, err := BuildPaymentSignature(fixture.offer, fixture.binding)
			if err == nil {
				_, err = VerifyBeforeEgress(fixture.now, "POST", fixture.resource.URL, payment.PaymentSignatureHeader, fixture.offer, fixture.binding, fixture.specJSON, fixture.body, fixtureOutboundHeaders())
			}
			errorsSeen <- err
		}()
	}
	wait.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		if err != nil {
			t.Fatal(err)
		}
	}
}

type fixture struct {
	now       time.Time
	body      []byte
	specJSON  []byte
	quote     sellerquote.Quote
	signature string
	intake    QuoteIntakeBinding
	resource  x402types.ResourceInfo
	offer     Offer
	binding   OperationBinding
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	now := time.Unix(testNowUnix, 0).UTC()
	body := []byte(`{"query":"status"}`)
	input := purchaseInput("POST", "https://seller.example/v1/jobs?b=2&a=1", body, "request-1")
	spec, err := purchasespec.Build(input)
	if err != nil {
		t.Fatal(err)
	}
	quote := sellerquote.Quote{
		PurchaseSpecHash: spec.PurchaseSpecHash, SellerID: hashByte("11"), ResourceID: hashByte("12"), DirectoryVersion: 7,
		SchemeVersion: SchemeVersion, ChainID: "84532", Asset: "0x1111111111111111111111111111111111111111",
		AmountBaseUnits: "2500000", PayTo: "0x2222222222222222222222222222222222222222",
		AckAuthority: "0x3333333333333333333333333333333333333333", VerificationSpecHash: hashByte("13"),
		DeclaredWorkTime: 120, VerificationBudgetSeconds: 60, QuoteExpiresAt: uint64(testNowUnix + 900), QuoteNonce: hashByte("14"),
	}
	privateKey, err := crypto.HexToECDSA(strings.Repeat("42", 32))
	if err != nil {
		t.Fatal(err)
	}
	serviceDirectory := "0x6666666666666666666666666666666666666666"
	quoteDigest, err := quote.Digest(serviceDirectory)
	if err != nil {
		t.Fatal(err)
	}
	signatureBytes, err := crypto.Sign(quoteDigest[:], privateKey)
	if err != nil {
		t.Fatal(err)
	}
	signature := "0x" + strings.ToLower(commonHex(signatureBytes))
	intake := QuoteIntakeBinding{
		ServiceDirectory: serviceDirectory, QuoteHash: quoteDigest.Hex(),
		QuoteSigner: strings.ToLower(crypto.PubkeyToAddress(privateKey.PublicKey).Hex()),
	}
	resource := x402types.ResourceInfo{URL: spec.Spec.CanonicalURL, Description: "Run seller job", MimeType: "application/json"}
	offer, err := BuildOffer(now, resource, spec.CanonicalJSON, body, quote, signature, intake)
	if err != nil {
		t.Fatal(err)
	}
	binding := OperationBinding{
		CallID: hashByte("21"), EscrowContract: "0x4444444444444444444444444444444444444444",
		CommitmentHash: hashByte("22"), SchemeVersion: SchemeVersion, ResourceRequest: offer.ResourceRequestDigest,
	}
	return fixture{now: now, body: body, specJSON: spec.CanonicalJSON, quote: quote, signature: signature, intake: intake, resource: resource, offer: offer, binding: binding}
}

func purchaseInput(method, url string, body []byte, requestID string) purchasespec.Input {
	return purchasespec.Input{
		OrgID: "org-1", AgentID: "agent-1", TaskID: "task-1", Method: method, URL: url, Body: body,
		Headers:  []purchasespec.Header{{Name: "Content-Type", Value: "application/json"}, {Name: "X-Request-ID", Value: requestID}},
		Response: purchasespec.ResponseContract{ContentType: "application/json", SchemaRef: "schema:job-v1"}, Category: "research",
	}
}

func mutateQuote(quote sellerquote.Quote, mutation func(*sellerquote.Quote)) sellerquote.Quote {
	mutation(&quote)
	return quote
}

func hashByte(value string) string { return "0x" + strings.Repeat(value, 32) }

func fixtureOutboundHeaders() http.Header {
	return http.Header{"Content-Type": []string{"application/json"}, "X-Request-Id": []string{"request-1"}}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func commonHex(value []byte) string {
	const alphabet = "0123456789abcdef"
	result := make([]byte, len(value)*2)
	for index, item := range value {
		result[index*2] = alphabet[item>>4]
		result[index*2+1] = alphabet[item&0x0f]
	}
	return string(result)
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("find source")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(source), "..", ".."))
}

func deepCopyOffer(t *testing.T, source Offer) Offer {
	t.Helper()
	var required x402types.PaymentRequired
	if err := json.Unmarshal(source.CanonicalJSON, &required); err != nil {
		t.Fatal(err)
	}
	return Offer{
		Required: required, Accepted: required.Accepts[0], Intake: source.Intake, ResourceRequestDigest: source.ResourceRequestDigest,
		CanonicalJSON: append([]byte(nil), source.CanonicalJSON...), PaymentRequiredHeader: source.PaymentRequiredHeader,
	}
}

func floatCompatibleSchemeVersion() interface{} { return uint16(SchemeVersion) }
