// Package escrowcall implements the ASCP escrow-call/1 x402 v2 wire profile.
// It creates and verifies protocol envelopes but never signs a wallet action,
// moves funds, or submits a transaction.
package escrowcall

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/gnanam1990/flowops/pkg/purchasespec"
	"github.com/gnanam1990/flowops/pkg/sellerquote"
	x402 "github.com/x402-foundation/x402/go/v2"
	x402types "github.com/x402-foundation/x402/go/v2/types"
)

const (
	X402Version   = 2
	Scheme        = "escrow-call/1"
	SchemeVersion = uint16(1)

	BaseMainnetNetwork = "eip155:8453"
	BaseSepoliaNetwork = "eip155:84532"

	resourceRequestDomain = "ASCP_RESOURCE_REQUEST_V1"
	paymentProofDomain    = "ASCP_PAYMENT_PROOF_V1"
	maxHeaderBytes        = 1 << 20
	MaxHTTPTimeout        = 60 * time.Second
)

const (
	ErrorEscrowNotFound     = "ESCROW_NOT_FOUND"
	ErrorEscrowStateInvalid = "ESCROW_STATE_INVALID"
	ErrorAmountMismatch     = "AMOUNT_MISMATCH"
	ErrorPayeeMismatch      = "PAYEE_MISMATCH"
	ErrorCommitmentMismatch = "COMMITMENT_MISMATCH"
	ErrorDeadlineUnworkable = "DEADLINE_UNWORKABLE"
	ErrorVersionUnsupported = "VERSION_UNSUPPORTED"
)

var (
	ErrInvalidOffer    = errors.New("invalid escrow-call payment offer")
	ErrInvalidPayload  = errors.New("invalid escrow-call payment payload")
	ErrInvalidResponse = errors.New("invalid escrow-call payment response")
	hashPattern        = regexp.MustCompile(`^0x[0-9a-f]{64}$`)
	signaturePattern   = regexp.MustCompile(`^0x[0-9a-f]{130}$`)
	decimalPattern     = regexp.MustCompile(`^[1-9][0-9]*$`)
)

type SellerQuoteExtension struct {
	SchemeVersion         uint16            `json:"schemeVersion"`
	ResourceRequestDigest string            `json:"resourceRequestDigest"`
	SellerQuote           sellerquote.Quote `json:"sellerQuote"`
	SellerQuoteSignature  string            `json:"sellerQuoteSignature"`
}

// QuoteIntakeBinding is loaded from the durable SellerQuote intake result. It
// lets this wire boundary reprove the stored quote digest and recovered signer
// without pretending to re-evaluate directory activation or nonce ownership.
type QuoteIntakeBinding struct {
	ServiceDirectory string `json:"serviceDirectory"`
	QuoteHash        string `json:"quoteHash"`
	QuoteSigner      string `json:"quoteSigner"`
}

type Offer struct {
	Required              x402types.PaymentRequired     `json:"required"`
	Accepted              x402types.PaymentRequirements `json:"accepted"`
	Intake                QuoteIntakeBinding            `json:"intake"`
	ResourceRequestDigest string                        `json:"resourceRequestDigest"`
	CanonicalJSON         []byte                        `json:"canonicalJSON"`
	PaymentRequiredHeader string                        `json:"paymentRequiredHeader"`
}

type OperationBinding struct {
	CallID          string `json:"callId"`
	EscrowContract  string `json:"escrowContract"`
	CommitmentHash  string `json:"commitmentHash"`
	SchemeVersion   uint16 `json:"schemeVersion"`
	ResourceRequest string `json:"resourceRequestDigest"`
}

type Payment struct {
	Payload                x402types.PaymentPayload `json:"payload"`
	CanonicalJSON          []byte                   `json:"canonicalJSON"`
	PaymentSignatureHeader string                   `json:"paymentSignatureHeader"`
	PaymentProofDigest     string                   `json:"paymentProofDigest"`
}

type ResponseBinding struct {
	CallID              string
	ContentDigest       string
	LockTransactionHash string
	Payer               string
}

type PaymentResponse struct {
	Response              x402.SettleResponse `json:"response"`
	CanonicalJSON         []byte              `json:"canonicalJSON"`
	PaymentResponseHeader string              `json:"paymentResponseHeader"`
}

type HTTPResult struct {
	StatusCode int
	Header     http.Header
}

// NewHTTPClient builds the only supported client shape for a prepared paid
// request: caller-supplied restricted transport, finite timeout, no cookie jar,
// and no redirects that could replay PAYMENT-SIGNATURE to another request.
func NewHTTPClient(transport http.RoundTripper, timeout time.Duration) (*http.Client, error) {
	if transport == nil || timeout <= 0 || timeout > MaxHTTPTimeout {
		return nil, ErrInvalidPayload
	}
	return &http.Client{
		Transport: transport,
		Timeout:   timeout,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}, nil
}

type responseExtension struct {
	CallID        string `json:"callId"`
	ContentDigest string `json:"contentDigest,omitempty"`
	SchemeVersion uint16 `json:"schemeVersion"`
}

type resourceRequest struct {
	Domain          string                       `json:"domain"`
	Method          string                       `json:"method"`
	CanonicalURL    string                       `json:"canonicalURL"`
	HeaderBindings  []purchasespec.HeaderBinding `json:"headerBindings"`
	RequestBodyHash string                       `json:"requestBodyHash"`
}

// ResourceRequestDigest binds only the original seller HTTP action after
// revalidating the canonical persisted PurchaseSpec and exact request body.
// Payment headers are already prohibited from PurchaseSpec header bindings.
func ResourceRequestDigest(canonicalSpecJSON, body []byte) (string, error) {
	spec, err := purchasespec.ValidatePersisted(canonicalSpecJSON, body)
	if err != nil {
		return "", ErrInvalidOffer
	}
	return resourceRequestDigest(spec)
}

func resourceRequestDigest(spec purchasespec.Spec) (string, error) {
	if spec.Method == "" || spec.CanonicalURL == "" || len(spec.HeaderBindings) > 256 {
		return "", ErrInvalidOffer
	}
	for index := 1; index < len(spec.HeaderBindings); index++ {
		if spec.HeaderBindings[index-1].LowercaseName >= spec.HeaderBindings[index].LowercaseName {
			return "", ErrInvalidOffer
		}
	}
	for _, binding := range spec.HeaderBindings {
		if binding.LowercaseName == "payment-signature" || !hashPattern.MatchString(binding.ValueHash) {
			return "", ErrInvalidOffer
		}
	}
	if spec.Method == "GET" {
		if spec.RequestBodyHash != "" {
			return "", ErrInvalidOffer
		}
	} else if !hashPattern.MatchString(spec.RequestBodyHash) {
		return "", ErrInvalidOffer
	}
	encoded, err := canonicalJSON(resourceRequest{
		Domain: resourceRequestDomain, Method: spec.Method, CanonicalURL: spec.CanonicalURL,
		HeaderBindings: spec.HeaderBindings, RequestBodyHash: spec.RequestBodyHash,
	})
	if err != nil {
		return "", err
	}
	return crypto.Keccak256Hash(encoded).Hex(), nil
}

func BuildOffer(now time.Time, resource x402types.ResourceInfo, canonicalSpecJSON, body []byte, quote sellerquote.Quote, quoteSignature string, intake QuoteIntakeBinding) (Offer, error) {
	if err := quote.Validate(); err != nil || quote.SchemeVersion != SchemeVersion || !signaturePattern.MatchString(quoteSignature) ||
		now.Unix() < 0 || uint64(now.UTC().Unix()) >= quote.QuoteExpiresAt {
		return Offer{}, ErrInvalidOffer
	}
	quoteDigest, err := quote.Digest(intake.ServiceDirectory)
	if err != nil || quoteDigest.Hex() != intake.QuoteHash || !canonicalAddress(intake.QuoteSigner) {
		return Offer{}, ErrInvalidOffer
	}
	recovered, err := quote.RecoverSigner(intake.ServiceDirectory, quoteSignature)
	if err != nil || strings.ToLower(recovered.Hex()) != intake.QuoteSigner {
		return Offer{}, ErrInvalidOffer
	}
	spec, err := purchasespec.ValidatePersisted(canonicalSpecJSON, body)
	if err != nil || !validResource(resource) || resource.URL != spec.CanonicalURL || crypto.Keccak256Hash(canonicalSpecJSON).Hex() != quote.PurchaseSpecHash {
		return Offer{}, ErrInvalidOffer
	}
	network := ""
	switch quote.ChainID {
	case "8453":
		network = BaseMainnetNetwork
	case "84532":
		network = BaseSepoliaNetwork
	default:
		return Offer{}, ErrInvalidOffer
	}
	requestDigest, err := resourceRequestDigest(spec)
	if err != nil {
		return Offer{}, err
	}
	timeout := quote.QuoteExpiresAt - uint64(now.UTC().Unix())
	if timeout == 0 || timeout > 3600 {
		return Offer{}, ErrInvalidOffer
	}
	extension := SellerQuoteExtension{
		SchemeVersion: SchemeVersion, ResourceRequestDigest: requestDigest,
		SellerQuote: quote, SellerQuoteSignature: quoteSignature,
	}
	extensionJSON, _ := json.Marshal(extension)
	var extensionMap map[string]interface{}
	decoder := json.NewDecoder(bytes.NewReader(extensionJSON))
	decoder.UseNumber()
	if err := decoder.Decode(&extensionMap); err != nil {
		return Offer{}, err
	}
	accepted := x402types.PaymentRequirements{
		Scheme: Scheme, Network: network, Asset: quote.Asset, Amount: quote.AmountBaseUnits, PayTo: quote.PayTo,
		MaxTimeoutSeconds: int(timeout), Extra: map[string]interface{}{"escrowCall": extensionMap},
	}
	required := x402types.PaymentRequired{X402Version: X402Version, Resource: &resource, Accepts: []x402types.PaymentRequirements{accepted}}
	canonical, err := canonicalJSON(required)
	if err != nil || len(canonical) > maxHeaderBytes {
		return Offer{}, ErrInvalidOffer
	}
	return Offer{
		Required: required, Accepted: accepted, Intake: intake, ResourceRequestDigest: requestDigest, CanonicalJSON: canonical,
		PaymentRequiredHeader: base64.StdEncoding.EncodeToString(canonical),
	}, nil
}

// BuildPaymentSignature accepts only stored operation bindings and the exact
// accepted requirement returned by BuildOffer. It emits the sole payment
// transport header generated for the seller request.
func BuildPaymentSignature(offer Offer, binding OperationBinding) (Payment, error) {
	if err := ValidateOffer(offer); err != nil || !nonZeroHash(binding.CallID) ||
		!nonZeroHash(binding.CommitmentHash) || !canonicalAddress(binding.EscrowContract) ||
		binding.SchemeVersion != SchemeVersion || binding.ResourceRequest != offer.ResourceRequestDigest {
		return Payment{}, ErrInvalidPayload
	}
	payload := x402types.PaymentPayload{
		X402Version: X402Version,
		Accepted:    offer.Accepted,
		Payload: map[string]interface{}{
			"callId": binding.CallID, "escrowContract": binding.EscrowContract,
			"commitmentHash": binding.CommitmentHash, "schemeVersion": SchemeVersion,
		},
	}
	canonical, err := canonicalJSON(payload)
	if err != nil || len(canonical) > maxHeaderBytes {
		return Payment{}, ErrInvalidPayload
	}
	digest := crypto.Keccak256Hash(append([]byte(paymentProofDomain), canonical...)).Hex()
	return Payment{Payload: payload, CanonicalJSON: canonical, PaymentSignatureHeader: base64.StdEncoding.EncodeToString(canonical), PaymentProofDigest: digest}, nil
}

func ValidateOffer(offer Offer) error {
	if offer.Required.X402Version != X402Version || len(offer.Required.Accepts) != 1 || offer.Accepted.Scheme != Scheme ||
		!nonZeroHash(offer.ResourceRequestDigest) || offer.Required.Resource == nil ||
		!validResource(*offer.Required.Resource) || offer.Required.Error != "" || len(offer.Required.Extensions) != 0 {
		return ErrInvalidOffer
	}
	acceptedJSON, err := canonicalJSON(offer.Accepted)
	if err != nil {
		return ErrInvalidOffer
	}
	requiredAcceptedJSON, err := canonicalJSON(offer.Required.Accepts[0])
	if err != nil || !bytes.Equal(acceptedJSON, requiredAcceptedJSON) {
		return ErrInvalidOffer
	}
	canonical, err := canonicalJSON(offer.Required)
	if err != nil || len(canonical) > maxHeaderBytes || !bytes.Equal(canonical, offer.CanonicalJSON) || base64.StdEncoding.EncodeToString(canonical) != offer.PaymentRequiredHeader {
		return ErrInvalidOffer
	}
	extension, err := acceptedExtension(offer.Accepted, offer.ResourceRequestDigest)
	if err != nil {
		return err
	}
	quoteDigest, err := extension.SellerQuote.Digest(offer.Intake.ServiceDirectory)
	if err != nil || quoteDigest.Hex() != offer.Intake.QuoteHash || !canonicalAddress(offer.Intake.QuoteSigner) {
		return ErrInvalidOffer
	}
	recovered, err := extension.SellerQuote.RecoverSigner(offer.Intake.ServiceDirectory, extension.SellerQuoteSignature)
	if err != nil || strings.ToLower(recovered.Hex()) != offer.Intake.QuoteSigner {
		return ErrInvalidOffer
	}
	return nil
}

// ChallengeHTTP returns the standard HTTP 402 signaling surface. Challenges
// are never cacheable because they carry operation-specific quote material.
func ChallengeHTTP(now time.Time, offer Offer, canonicalSpecJSON, body []byte) (HTTPResult, error) {
	if err := ValidateOffer(offer); err != nil {
		return HTTPResult{}, err
	}
	spec, err := purchasespec.ValidatePersisted(canonicalSpecJSON, body)
	extension, extensionErr := acceptedExtension(offer.Accepted, offer.ResourceRequestDigest)
	requestDigest, digestErr := resourceRequestDigest(spec)
	if err != nil || extensionErr != nil || digestErr != nil || now.Unix() < 0 ||
		uint64(now.UTC().Unix()) >= extension.SellerQuote.QuoteExpiresAt || offer.Required.Resource.URL != spec.CanonicalURL ||
		crypto.Keccak256Hash(canonicalSpecJSON).Hex() != extension.SellerQuote.PurchaseSpecHash || requestDigest != offer.ResourceRequestDigest {
		return HTTPResult{}, ErrInvalidOffer
	}
	return HTTPResult{StatusCode: http.StatusPaymentRequired, Header: http.Header{
		"Payment-Required": []string{offer.PaymentRequiredHeader}, "Cache-Control": []string{"no-store"},
	}}, nil
}

// PaymentHeaders creates the retry headers without mutating the source. It
// removes legacy escrow headers and every caller-provided payment-* header,
// then installs the sole rails-generated PAYMENT-SIGNATURE value.
func PaymentHeaders(now time.Time, method, requestURL string, source http.Header, payment Payment, offer Offer, binding OperationBinding, canonicalSpecJSON, body []byte) (http.Header, error) {
	result := source.Clone()
	if result == nil {
		result = make(http.Header)
	}
	for name := range result {
		lower := strings.ToLower(name)
		if strings.HasPrefix(lower, "payment-") || lower == "x-payment" || strings.HasPrefix(lower, "x-payment-") ||
			lower == "x-escrow-intent" || lower == "x-escrow-call" || lower == "traceparent" ||
			lower == "accept-encoding" || lower == "connection" {
			delete(result, name)
		}
	}
	proofDigest, err := VerifyBeforeEgress(now, method, requestURL, payment.PaymentSignatureHeader, offer, binding, canonicalSpecJSON, body, result)
	if err != nil || proofDigest != payment.PaymentProofDigest || !bytes.Equal(payment.CanonicalJSON, mustDecodeHeader(payment.PaymentSignatureHeader)) {
		return nil, ErrInvalidPayload
	}
	result.Set("PAYMENT-SIGNATURE", payment.PaymentSignatureHeader)
	return result, nil
}

// PrepareRequest is the send-ready rails boundary. It clones the request,
// rejects alternate HTTP routing surfaces, rebuilds its body from the exact
// PurchaseSpec-bound bytes, and installs only the verified payment header.
func PrepareRequest(now time.Time, request *http.Request, body []byte, payment Payment, offer Offer, binding OperationBinding, canonicalSpecJSON []byte) (*http.Request, error) {
	if request == nil || request.URL == nil || request.Host != request.URL.Host || request.RequestURI != "" || len(request.Trailer) != 0 || len(request.TransferEncoding) != 0 {
		return nil, ErrInvalidPayload
	}
	headers, err := PaymentHeaders(now, request.Method, request.URL.String(), request.Header, payment, offer, binding, canonicalSpecJSON, body)
	if err != nil {
		return nil, err
	}
	prepared := request.Clone(request.Context())
	prepared.Header = headers
	prepared.Host = prepared.URL.Host
	prepared.RequestURI = ""
	prepared.TransferEncoding = nil
	prepared.Trailer = nil
	prepared.Close = false
	prepared.ContentLength = int64(len(body))
	prepared.Body = io.NopCloser(bytes.NewReader(body))
	prepared.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
	return prepared, nil
}

func mustDecodeHeader(header string) []byte {
	raw, _ := base64.StdEncoding.Strict().DecodeString(header)
	return raw
}

// VerifyBeforeEgress strictly decodes PAYMENT-SIGNATURE and proves the exact
// accepted object plus stored call/escrow/commitment/request bindings.
func VerifyBeforeEgress(now time.Time, method, requestURL, header string, offer Offer, binding OperationBinding, canonicalSpecJSON, body []byte, outboundHeaders http.Header) (string, error) {
	if err := ValidateOffer(offer); err != nil {
		return "", err
	}
	spec, err := purchasespec.ValidatePersisted(canonicalSpecJSON, body)
	canonicalOutboundURL, urlErr := purchasespec.CanonicalURL(requestURL)
	if err != nil || urlErr != nil || method != spec.Method || requestURL != canonicalOutboundURL ||
		canonicalOutboundURL != spec.CanonicalURL || offer.Required.Resource.URL != spec.CanonicalURL {
		return "", ErrInvalidPayload
	}
	requestDigest, err := resourceRequestDigest(spec)
	extension, extensionErr := acceptedExtension(offer.Accepted, offer.ResourceRequestDigest)
	if err != nil || extensionErr != nil || now.Unix() < 0 || uint64(now.UTC().Unix()) >= extension.SellerQuote.QuoteExpiresAt ||
		requestDigest != offer.ResourceRequestDigest || requestDigest != binding.ResourceRequest {
		return "", ErrInvalidPayload
	}
	if err := validateOutboundHeaders(outboundHeaders, spec, header, len(body)); err != nil {
		return "", err
	}
	raw, err := decodeHeader(header)
	if err != nil {
		return "", err
	}
	if err := rejectDuplicateKeys(raw); err != nil {
		return "", err
	}
	var payload x402types.PaymentPayload
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&payload); err != nil {
		return "", ErrInvalidPayload
	}
	if err := requireEOF(decoder); err != nil {
		return "", err
	}
	expected, err := BuildPaymentSignature(offer, binding)
	if err != nil || !bytes.Equal(raw, expected.CanonicalJSON) {
		return "", ErrInvalidPayload
	}
	return expected.PaymentProofDigest, nil
}

func validateOutboundHeaders(headers http.Header, spec purchasespec.Spec, paymentHeader string, bodyLength int) error {
	bindings := make([]purchasespec.HeaderBinding, 0, len(headers))
	for name, values := range headers {
		lower := strings.ToLower(name)
		switch {
		case lower == "payment-signature":
			if len(values) != 1 || values[0] != paymentHeader {
				return ErrInvalidPayload
			}
			continue
		case lower == "traceparent" || lower == "accept-encoding" || lower == "connection":
			continue
		case strings.HasPrefix(lower, "payment-") || lower == "x-payment" || strings.HasPrefix(lower, "x-payment-") ||
			lower == "x-escrow-intent" || lower == "x-escrow-call":
			return ErrInvalidPayload
		}
		if len(values) != 1 || !utf8.ValidString(values[0]) || strings.ContainsAny(values[0], "\r\n\x00") {
			return ErrInvalidPayload
		}
		value := strings.Trim(values[0], " \t")
		if lower == "content-length" {
			parsed, err := strconv.ParseUint(value, 10, 63)
			if err != nil || parsed != uint64(bodyLength) || value != strconv.FormatUint(parsed, 10) {
				return ErrInvalidPayload
			}
		}
		bindings = append(bindings, purchasespec.HeaderBinding{
			LowercaseName: lower, ValueHash: crypto.Keccak256Hash([]byte(value)).Hex(),
		})
	}
	sort.Slice(bindings, func(left, right int) bool { return bindings[left].LowercaseName < bindings[right].LowercaseName })
	if len(bindings) != len(spec.HeaderBindings) {
		return ErrInvalidPayload
	}
	for index := range bindings {
		if bindings[index] != spec.HeaderBindings[index] {
			return ErrInvalidPayload
		}
	}
	return nil
}

// BuildPaymentResponse encodes the standard x402 v2 PAYMENT-RESPONSE success
// envelope and binds the delivered content to the escrow call.
func BuildPaymentResponse(offer Offer, binding ResponseBinding) (PaymentResponse, error) {
	if err := ValidateOffer(offer); err != nil || !nonZeroHash(binding.CallID) ||
		!nonZeroHash(binding.ContentDigest) || !nonZeroHash(binding.LockTransactionHash) ||
		!canonicalAddress(binding.Payer) {
		return PaymentResponse{}, ErrInvalidResponse
	}
	extension := responseExtension{CallID: binding.CallID, ContentDigest: binding.ContentDigest, SchemeVersion: SchemeVersion}
	response := x402.SettleResponse{
		Success: true, Payer: binding.Payer, Transaction: binding.LockTransactionHash,
		Network: x402.Network(offer.Accepted.Network), Amount: offer.Accepted.Amount,
		Extensions: map[string]interface{}{"escrowCall": structMap(extension)},
	}
	return encodePaymentResponse(response)
}

// BuildPaymentError encodes the normative escrow-call/1 failure reasons. A
// VERSION_UNSUPPORTED response also publishes the supported scheme versions.
func BuildPaymentError(offer Offer, callID, reason, message string) (PaymentResponse, error) {
	if err := ValidateOffer(offer); err != nil || !nonZeroHash(callID) || !validErrorReason(reason) || len(message) > 1024 || !utf8.ValidString(message) {
		return PaymentResponse{}, ErrInvalidResponse
	}
	extension := responseExtension{CallID: callID, SchemeVersion: SchemeVersion}
	response := x402.SettleResponse{
		Success: false, ErrorReason: reason, ErrorMessage: message, Transaction: "",
		Network: x402.Network(offer.Accepted.Network), Extensions: map[string]interface{}{"escrowCall": structMap(extension)},
	}
	if reason == ErrorVersionUnsupported {
		response.Extra = map[string]interface{}{"supportedSchemeVersions": []uint16{SchemeVersion}}
	}
	return encodePaymentResponse(response)
}

// DecodePaymentResponse rejects non-canonical, duplicated, oversized, or
// semantically inconsistent PAYMENT-RESPONSE values before returning them.
func DecodePaymentResponse(header string, offer Offer, expectedCallID string) (PaymentResponse, error) {
	if err := ValidateOffer(offer); err != nil || !nonZeroHash(expectedCallID) {
		return PaymentResponse{}, ErrInvalidResponse
	}
	raw, err := decodeHeader(header)
	if err != nil || rejectDuplicateKeys(raw) != nil {
		return PaymentResponse{}, ErrInvalidResponse
	}
	var response x402.SettleResponse
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&response); err != nil || requireEOF(decoder) != nil {
		return PaymentResponse{}, ErrInvalidResponse
	}
	reencoded, err := canonicalJSON(response)
	if err != nil || !bytes.Equal(raw, reencoded) || string(response.Network) != offer.Accepted.Network {
		return PaymentResponse{}, ErrInvalidResponse
	}
	if err := validateResponse(response, offer, expectedCallID); err != nil {
		return PaymentResponse{}, err
	}
	return PaymentResponse{Response: response, CanonicalJSON: raw, PaymentResponseHeader: header}, nil
}

// PaymentResponseHTTP returns 200/private for success and 402/no-store for a
// normative escrow-call failure, matching x402 v2 response signaling.
func PaymentResponseHTTP(response PaymentResponse, offer Offer, expectedCallID string) (HTTPResult, error) {
	decoded, err := DecodePaymentResponse(response.PaymentResponseHeader, offer, expectedCallID)
	if err != nil || !bytes.Equal(decoded.CanonicalJSON, response.CanonicalJSON) {
		return HTTPResult{}, ErrInvalidResponse
	}
	status := http.StatusOK
	cacheControl := "private"
	if !decoded.Response.Success {
		status = http.StatusPaymentRequired
		cacheControl = "no-store"
	}
	return HTTPResult{StatusCode: status, Header: http.Header{
		"Payment-Response": []string{response.PaymentResponseHeader}, "Cache-Control": []string{cacheControl},
	}}, nil
}

func validateResponse(response x402.SettleResponse, offer Offer, expectedCallID string) error {
	if len(response.Extensions) != 1 || len(response.ErrorMessage) > 1024 {
		return ErrInvalidResponse
	}
	extensionJSON, ok := response.Extensions["escrowCall"]
	if !ok {
		return ErrInvalidResponse
	}
	rawExtension, err := json.Marshal(extensionJSON)
	if err != nil {
		return ErrInvalidResponse
	}
	var extension responseExtension
	if err := decodeExact(rawExtension, &extension); err != nil || extension.SchemeVersion != SchemeVersion || extension.CallID != expectedCallID {
		return ErrInvalidResponse
	}
	if response.Success {
		if response.ErrorReason != "" || response.ErrorMessage != "" || response.Amount != offer.Accepted.Amount ||
			!nonZeroHash(response.Transaction) || !canonicalAddress(response.Payer) ||
			!nonZeroHash(extension.ContentDigest) || len(response.Extra) != 0 {
			return ErrInvalidResponse
		}
	} else if response.Transaction != "" || response.Amount != "" || response.Payer != "" || extension.ContentDigest != "" ||
		!validErrorReason(response.ErrorReason) {
		return ErrInvalidResponse
	}
	if !response.Success {
		if response.ErrorReason == ErrorVersionUnsupported {
			if !supportedVersionsOnly(response.Extra) {
				return ErrInvalidResponse
			}
		} else if len(response.Extra) != 0 {
			return ErrInvalidResponse
		}
	}
	return nil
}

func supportedVersionsOnly(extra map[string]interface{}) bool {
	if len(extra) != 1 {
		return false
	}
	versions, ok := extra["supportedSchemeVersions"].([]interface{})
	if !ok || len(versions) != 1 {
		return false
	}
	switch value := versions[0].(type) {
	case json.Number:
		return value.String() == "1"
	case float64:
		return value == 1
	case uint16:
		return value == SchemeVersion
	default:
		return false
	}
}

func acceptedExtension(accepted x402types.PaymentRequirements, requestDigest string) (SellerQuoteExtension, error) {
	if accepted.Scheme != Scheme || (accepted.Network != BaseMainnetNetwork && accepted.Network != BaseSepoliaNetwork) ||
		!canonicalAddress(accepted.Asset) || !canonicalAddress(accepted.PayTo) || !decimalPattern.MatchString(accepted.Amount) ||
		accepted.MaxTimeoutSeconds < 1 || accepted.MaxTimeoutSeconds > 3600 || len(accepted.Extra) != 1 {
		return SellerQuoteExtension{}, ErrInvalidOffer
	}
	rawExtension, exists := accepted.Extra["escrowCall"]
	if !exists {
		return SellerQuoteExtension{}, ErrInvalidOffer
	}
	raw, err := json.Marshal(rawExtension)
	if err != nil {
		return SellerQuoteExtension{}, ErrInvalidOffer
	}
	var extension SellerQuoteExtension
	if err := decodeExact(raw, &extension); err != nil || extension.SchemeVersion != SchemeVersion ||
		extension.ResourceRequestDigest != requestDigest || !signaturePattern.MatchString(extension.SellerQuoteSignature) ||
		extension.SellerQuote.Validate() != nil || extension.SellerQuote.SchemeVersion != SchemeVersion ||
		extension.SellerQuote.Asset != accepted.Asset || extension.SellerQuote.PayTo != accepted.PayTo ||
		extension.SellerQuote.AmountBaseUnits != accepted.Amount || networkForChain(extension.SellerQuote.ChainID) != accepted.Network {
		return SellerQuoteExtension{}, ErrInvalidOffer
	}
	return extension, nil
}

func decodeExact(raw []byte, destination interface{}) error {
	if err := rejectDuplicateKeys(raw); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	return requireEOF(decoder)
}

func networkForChain(chainID string) string {
	switch chainID {
	case "8453":
		return BaseMainnetNetwork
	case "84532":
		return BaseSepoliaNetwork
	default:
		return ""
	}
}

func encodePaymentResponse(response x402.SettleResponse) (PaymentResponse, error) {
	canonical, err := canonicalJSON(response)
	if err != nil || len(canonical) > maxHeaderBytes {
		return PaymentResponse{}, ErrInvalidResponse
	}
	return PaymentResponse{Response: response, CanonicalJSON: canonical, PaymentResponseHeader: base64.StdEncoding.EncodeToString(canonical)}, nil
}

func validErrorReason(reason string) bool {
	switch reason {
	case ErrorEscrowNotFound, ErrorEscrowStateInvalid, ErrorAmountMismatch, ErrorPayeeMismatch,
		ErrorCommitmentMismatch, ErrorDeadlineUnworkable, ErrorVersionUnsupported:
		return true
	default:
		return false
	}
}

func structMap(value interface{}) map[string]interface{} {
	raw, _ := json.Marshal(value)
	var result map[string]interface{}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	_ = decoder.Decode(&result)
	return result
}

func decodeHeader(header string) ([]byte, error) {
	if header == "" || len(header) > base64.StdEncoding.EncodedLen(maxHeaderBytes) {
		return nil, ErrInvalidPayload
	}
	raw, err := base64.StdEncoding.Strict().DecodeString(header)
	if err != nil || len(raw) > maxHeaderBytes {
		return nil, ErrInvalidPayload
	}
	return raw, nil
}

func rejectDuplicateKeys(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var walk func() error
	walk = func() error {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delimiter, composite := token.(json.Delim)
		if !composite {
			return nil
		}
		if delimiter == '{' {
			seen := map[string]struct{}{}
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok {
					return ErrInvalidPayload
				}
				if _, duplicate := seen[key]; duplicate {
					return fmt.Errorf("%w: duplicate key %q", ErrInvalidPayload, key)
				}
				seen[key] = struct{}{}
				if err := walk(); err != nil {
					return err
				}
			}
		} else if delimiter == '[' {
			for decoder.More() {
				if err := walk(); err != nil {
					return err
				}
			}
		} else {
			return ErrInvalidPayload
		}
		_, err = decoder.Token()
		return err
	}
	if err := walk(); err != nil {
		return err
	}
	return requireEOF(decoder)
}

func requireEOF(decoder *json.Decoder) error {
	var extra interface{}
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return ErrInvalidPayload
	}
	return nil
}

func canonicalAddress(value string) bool {
	if len(value) != 42 || !strings.HasPrefix(value, "0x") || value != strings.ToLower(value) || !common.IsHexAddress(value) {
		return false
	}
	decoded, err := hex.DecodeString(value[2:])
	return err == nil && common.BytesToAddress(decoded) != (common.Address{})
}

func nonZeroHash(value string) bool {
	if !hashPattern.MatchString(value) {
		return false
	}
	return strings.TrimPrefix(value, "0x") != strings.Repeat("0", 64)
}

func validResource(resource x402types.ResourceInfo) bool {
	fields := []string{resource.URL, resource.Description, resource.MimeType, resource.ServiceName, resource.IconUrl}
	for _, value := range fields {
		if len(value) > 8192 || !utf8.ValidString(value) {
			return false
		}
	}
	if len(resource.Tags) > 64 {
		return false
	}
	for _, tag := range resource.Tags {
		if len(tag) > 256 || !utf8.ValidString(tag) {
			return false
		}
	}
	return resource.URL != ""
}
