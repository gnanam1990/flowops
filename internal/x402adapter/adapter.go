// Package x402adapter is FlowOps' fail-closed boundary around x402 V2.
// It normalizes payment requirements into immutable quotes; it never signs or
// settles a payment and therefore never needs access to a customer wallet key.
package x402adapter

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/url"
	"regexp"
	"sort"
	"strings"

	x402types "github.com/x402-foundation/x402/go/v2/types"
)

const (
	VersionV2   = 2
	SchemeExact = "exact"

	BaseMainnetNetwork = "eip155:8453"
	BaseMainnetChainID = uint64(8453)
	BaseMainnetUSDC    = "0x833589fcd6edb6e08f4c7c32d4f71b54bda02913"

	BaseSepoliaNetwork = "eip155:84532"
	BaseSepoliaChainID = uint64(84532)
	BaseSepoliaUSDC    = "0x036cbd53842c5426634e7929541ec2318f3dcf7e"

	quoteDomain                   = "flowops:x402-quote:v1\n"
	maxPaymentRequiredHeaderBytes = 1 << 20
)

var addressPattern = mustCompileAddressPattern()
var methodPattern = regexp.MustCompile(`^[A-Z][A-Z0-9!#$%&'*+.^_` + "`" + `|~-]{0,31}$`)
var digestPattern = regexp.MustCompile(`^0x[0-9a-f]{64}$`)

type Config struct {
	Network           string
	ChainID           uint64
	USDCAddress       string
	MaxAmountAtomic   string
	MaxTimeoutSeconds int
	ServiceCodes      []string
}

type Adapter struct {
	config    Config
	maxAmount *big.Int
}

type RequestBinding struct {
	Method     string `json:"method"`
	URL        string `json:"url"`
	BodySHA256 string `json:"bodySha256"`
}

type Quote struct {
	X402Version       int                     `json:"x402Version"`
	Scheme            string                  `json:"scheme"`
	Network           string                  `json:"network"`
	ChainID           uint64                  `json:"chainId"`
	Asset             string                  `json:"asset"`
	AmountAtomic      string                  `json:"amountAtomic"`
	Recipient         string                  `json:"recipient"`
	MaxTimeoutSeconds int                     `json:"maxTimeoutSeconds"`
	Request           RequestBinding          `json:"request"`
	Resource          *x402types.ResourceInfo `json:"resource,omitempty"`
	Extra             map[string]interface{}  `json:"extra,omitempty"`
	Extensions        map[string]interface{}  `json:"extensions,omitempty"`
	Digest            string                  `json:"digest"`
}

type quoteDigestInput struct {
	X402Version       int                     `json:"x402Version"`
	Scheme            string                  `json:"scheme"`
	Network           string                  `json:"network"`
	ChainID           uint64                  `json:"chainId"`
	Asset             string                  `json:"asset"`
	AmountAtomic      string                  `json:"amountAtomic"`
	Recipient         string                  `json:"recipient"`
	MaxTimeoutSeconds int                     `json:"maxTimeoutSeconds"`
	Request           RequestBinding          `json:"request"`
	Resource          *x402types.ResourceInfo `json:"resource,omitempty"`
	Extra             map[string]interface{}  `json:"extra,omitempty"`
	Extensions        map[string]interface{}  `json:"extensions,omitempty"`
}

func New(cfg Config) (*Adapter, error) {
	if cfg.Network != BaseMainnetNetwork && cfg.Network != BaseSepoliaNetwork {
		return nil, fmt.Errorf("network %q is not an allowed Base network", cfg.Network)
	}
	wantChain := BaseMainnetChainID
	wantAsset := BaseMainnetUSDC
	if cfg.Network == BaseSepoliaNetwork {
		wantChain = BaseSepoliaChainID
		wantAsset = BaseSepoliaUSDC
	}
	if cfg.ChainID != wantChain {
		return nil, fmt.Errorf("chain ID %d does not match network %s", cfg.ChainID, cfg.Network)
	}
	asset, err := canonicalAddress(cfg.USDCAddress)
	if err != nil || asset != wantAsset {
		return nil, fmt.Errorf("USDC address does not match native USDC for %s", cfg.Network)
	}
	maxAmount, err := positiveInteger(cfg.MaxAmountAtomic)
	if err != nil {
		return nil, fmt.Errorf("maximum amount: %w", err)
	}
	if cfg.MaxTimeoutSeconds <= 0 || cfg.MaxTimeoutSeconds > 3600 {
		return nil, errors.New("maximum timeout must be between 1 and 3600 seconds")
	}
	if err := validateServiceCodes(cfg.ServiceCodes); err != nil {
		return nil, err
	}
	cfg.USDCAddress = asset
	cfg.ServiceCodes = append([]string(nil), cfg.ServiceCodes...)
	return &Adapter{config: cfg, maxAmount: maxAmount}, nil
}

// DecodePaymentRequiredHeader decodes the x402 V2 PAYMENT-REQUIRED header.
func DecodePaymentRequiredHeader(header string) (x402types.PaymentRequired, error) {
	header = strings.TrimSpace(header)
	if header == "" {
		return x402types.PaymentRequired{}, errors.New("PAYMENT-REQUIRED header is empty")
	}
	if len(header) > base64.StdEncoding.EncodedLen(maxPaymentRequiredHeaderBytes) {
		return x402types.PaymentRequired{}, errors.New("PAYMENT-REQUIRED header exceeds 1 MiB decoded limit")
	}
	raw, err := base64.StdEncoding.DecodeString(header)
	if err != nil {
		return x402types.PaymentRequired{}, fmt.Errorf("decode PAYMENT-REQUIRED: %w", err)
	}
	if len(raw) > maxPaymentRequiredHeaderBytes {
		return x402types.PaymentRequired{}, errors.New("PAYMENT-REQUIRED payload exceeds 1 MiB")
	}
	if err := rejectDuplicateJSONKeys(raw); err != nil {
		return x402types.PaymentRequired{}, fmt.Errorf("parse PAYMENT-REQUIRED: %w", err)
	}
	var required x402types.PaymentRequired
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&required); err != nil {
		return x402types.PaymentRequired{}, fmt.Errorf("parse PAYMENT-REQUIRED: %w", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return x402types.PaymentRequired{}, fmt.Errorf("parse PAYMENT-REQUIRED: %w", err)
	}
	if required.X402Version != VersionV2 {
		return x402types.PaymentRequired{}, fmt.Errorf("x402 version %d is unsupported", required.X402Version)
	}
	return required, nil
}

// Quote selects the unique cheapest eligible Base/USDC/exact offer and binds it
// to the exact HTTP request. Duplicate identical offers are harmless; equal-price
// offers that differ in recipient or terms are rejected as ambiguous.
func (a *Adapter) Quote(required x402types.PaymentRequired, method, rawURL string, body []byte, callerMaxAtomic string) (Quote, error) {
	if required.X402Version != VersionV2 {
		return Quote{}, fmt.Errorf("x402 version %d is unsupported", required.X402Version)
	}
	request, err := bindRequest(method, rawURL, body)
	if err != nil {
		return Quote{}, err
	}
	if required.Resource != nil && required.Resource.URL != "" {
		resourceURL, err := canonicalURL(required.Resource.URL)
		if err != nil {
			return Quote{}, fmt.Errorf("resource URL: %w", err)
		}
		if resourceURL != request.URL {
			return Quote{}, errors.New("resource URL does not match the paid request URL")
		}
	}
	callerMax, err := positiveInteger(callerMaxAtomic)
	if err != nil {
		return Quote{}, fmt.Errorf("caller maximum: %w", err)
	}
	effectiveMax := new(big.Int).Set(a.maxAmount)
	if callerMax.Cmp(effectiveMax) < 0 {
		effectiveMax.Set(callerMax)
	}

	type candidate struct {
		requirement x402types.PaymentRequirements
		amount      *big.Int
		canonical   []byte
	}
	var eligible []candidate
	for _, requirement := range required.Accepts {
		if requirement.Scheme != SchemeExact || requirement.Network != a.config.Network {
			continue
		}
		asset, err := canonicalAddress(requirement.Asset)
		if err != nil || asset != a.config.USDCAddress {
			continue
		}
		amount, err := positiveInteger(requirement.Amount)
		if err != nil || amount.Cmp(effectiveMax) > 0 {
			continue
		}
		recipient, err := canonicalAddress(requirement.PayTo)
		if err != nil || recipient != requirement.PayTo {
			continue
		}
		if requirement.MaxTimeoutSeconds <= 0 || requirement.MaxTimeoutSeconds > a.config.MaxTimeoutSeconds {
			continue
		}
		if method, present := requirement.Extra["assetTransferMethod"]; present {
			text, ok := method.(string)
			if !ok || text != "eip3009" {
				continue
			}
		}
		requirement.Asset = asset
		requirement.PayTo = recipient
		canonical, err := json.Marshal(requirement)
		if err != nil {
			return Quote{}, err
		}
		eligible = append(eligible, candidate{requirement: requirement, amount: amount, canonical: canonical})
	}
	if len(eligible) == 0 {
		return Quote{}, errors.New("no eligible Base native-USDC exact-payment requirement")
	}
	sort.Slice(eligible, func(i, j int) bool {
		if cmp := eligible[i].amount.Cmp(eligible[j].amount); cmp != 0 {
			return cmp < 0
		}
		return string(eligible[i].canonical) < string(eligible[j].canonical)
	})
	selected := eligible[0]
	for _, other := range eligible[1:] {
		if other.amount.Cmp(selected.amount) != 0 {
			break
		}
		if string(other.canonical) != string(selected.canonical) {
			return Quote{}, errors.New("ambiguous equal-price payment requirements")
		}
	}

	extra, err := cloneMap(selected.requirement.Extra)
	if err != nil {
		return Quote{}, fmt.Errorf("canonicalize payment requirement extra: %w", err)
	}
	extensions, err := cloneMap(required.Extensions)
	if err != nil {
		return Quote{}, fmt.Errorf("canonicalize payment extensions: %w", err)
	}
	input := quoteDigestInput{
		X402Version: VersionV2, Scheme: selected.requirement.Scheme,
		Network: selected.requirement.Network, ChainID: a.config.ChainID,
		Asset: selected.requirement.Asset, AmountAtomic: selected.requirement.Amount,
		Recipient: selected.requirement.PayTo, MaxTimeoutSeconds: selected.requirement.MaxTimeoutSeconds,
		Request: request, Resource: cloneResource(required.Resource),
		Extra: extra, Extensions: extensions,
	}
	digest, err := digestQuote(input)
	if err != nil {
		return Quote{}, err
	}
	return Quote{
		X402Version: input.X402Version, Scheme: input.Scheme, Network: input.Network,
		ChainID: input.ChainID, Asset: input.Asset, AmountAtomic: input.AmountAtomic,
		Recipient: input.Recipient, MaxTimeoutSeconds: input.MaxTimeoutSeconds,
		Request: input.Request, Resource: input.Resource, Extra: input.Extra,
		Extensions: input.Extensions, Digest: digest,
	}, nil
}

// ValidateQuote rechecks a quote before it crosses into policy, signing, or
// execution. This makes post-creation map or field mutation detectable.
func (a *Adapter) ValidateQuote(quote Quote) error {
	if quote.X402Version != VersionV2 || quote.Scheme != SchemeExact || quote.Network != a.config.Network || quote.ChainID != a.config.ChainID {
		return errors.New("quote protocol or network does not match adapter configuration")
	}
	if quote.Asset != a.config.USDCAddress {
		return errors.New("quote asset does not match configured native USDC")
	}
	amount, err := positiveInteger(quote.AmountAtomic)
	if err != nil || amount.Cmp(a.maxAmount) > 0 {
		return errors.New("quote amount is invalid or exceeds the environment maximum")
	}
	if _, err := canonicalAddress(quote.Recipient); err != nil {
		return fmt.Errorf("quote recipient: %w", err)
	}
	if quote.MaxTimeoutSeconds <= 0 || quote.MaxTimeoutSeconds > a.config.MaxTimeoutSeconds {
		return errors.New("quote timeout exceeds the environment maximum")
	}
	if !methodPattern.MatchString(quote.Request.Method) || !digestPattern.MatchString(quote.Request.BodySHA256) {
		return errors.New("quote request binding is invalid")
	}
	canonical, err := canonicalURL(quote.Request.URL)
	if err != nil || canonical != quote.Request.URL {
		return errors.New("quote request URL is not canonical")
	}
	if !digestPattern.MatchString(quote.Digest) {
		return errors.New("quote digest encoding is invalid")
	}
	want, err := digestQuote(quoteDigestInput{
		X402Version: quote.X402Version, Scheme: quote.Scheme, Network: quote.Network,
		ChainID: quote.ChainID, Asset: quote.Asset, AmountAtomic: quote.AmountAtomic,
		Recipient: quote.Recipient, MaxTimeoutSeconds: quote.MaxTimeoutSeconds,
		Request: quote.Request, Resource: quote.Resource, Extra: quote.Extra, Extensions: quote.Extensions,
	})
	if err != nil {
		return fmt.Errorf("recompute quote digest: %w", err)
	}
	if want != quote.Digest {
		return errors.New("quote digest does not match its contents")
	}
	return nil
}

func bindRequest(method, rawURL string, body []byte) (RequestBinding, error) {
	method = strings.ToUpper(strings.TrimSpace(method))
	if !methodPattern.MatchString(method) {
		return RequestBinding{}, errors.New("HTTP method is invalid")
	}
	canonical, err := canonicalURL(rawURL)
	if err != nil {
		return RequestBinding{}, err
	}
	sum := sha256.Sum256(body)
	return RequestBinding{Method: method, URL: canonical, BodySHA256: "0x" + hex.EncodeToString(sum[:])}, nil
}

func canonicalURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", errors.New("request URL must be absolute")
	}
	if u.Scheme != "https" {
		return "", errors.New("paid request URL must use HTTPS")
	}
	if u.User != nil || u.Fragment != "" {
		return "", errors.New("request URL cannot contain user info or a fragment")
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	return u.String(), nil
}

func digestQuote(input quoteDigestInput) (string, error) {
	encoded, err := json.Marshal(input)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(append([]byte(quoteDomain), encoded...))
	return "0x" + hex.EncodeToString(sum[:]), nil
}

func positiveInteger(value string) (*big.Int, error) {
	if value == "" || value[0] == '0' {
		return nil, errors.New("must be a canonical positive integer")
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return nil, errors.New("must contain decimal digits only")
		}
	}
	n, ok := new(big.Int).SetString(value, 10)
	if !ok || n.Sign() <= 0 || n.BitLen() > 256 {
		return nil, errors.New("must fit uint256 and be positive")
	}
	return n, nil
}

func canonicalAddress(value string) (string, error) {
	if !addressPattern.MatchString(value) {
		return "", errors.New("must be a lowercase 20-byte EVM address")
	}
	return value, nil
}

func cloneMap(input map[string]interface{}) (map[string]interface{}, error) {
	if input == nil {
		return nil, nil
	}
	raw, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}
	var output map[string]interface{}
	if err := json.Unmarshal(raw, &output); err != nil {
		return nil, err
	}
	return output, nil
}

func cloneResource(input *x402types.ResourceInfo) *x402types.ResourceInfo {
	if input == nil {
		return nil
	}
	output := *input
	output.Tags = append([]string(nil), input.Tags...)
	return &output
}
