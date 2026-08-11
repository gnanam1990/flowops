package x402adapter

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	x402types "github.com/x402-foundation/x402/go/v2/types"
)

const testRecipient = "0x1111111111111111111111111111111111111111"

func testAdapter(t *testing.T) *Adapter {
	t.Helper()
	adapter, err := New(Config{
		Network: BaseSepoliaNetwork, ChainID: BaseSepoliaChainID,
		USDCAddress: BaseSepoliaUSDC, MaxAmountAtomic: "1000000",
		MaxTimeoutSeconds: 300, ServiceCodes: []string{"flowops_client"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return adapter
}

func validRequirement() x402types.PaymentRequirements {
	return x402types.PaymentRequirements{
		Scheme: SchemeExact, Network: BaseSepoliaNetwork, Asset: BaseSepoliaUSDC,
		Amount: "250000", PayTo: testRecipient, MaxTimeoutSeconds: 120,
		Extra: map[string]interface{}{"assetTransferMethod": "eip3009", "name": "USDC", "version": "2"},
	}
}

func validPaymentRequired() x402types.PaymentRequired {
	return x402types.PaymentRequired{
		X402Version: VersionV2,
		Resource:    &x402types.ResourceInfo{URL: "https://evidence.example/fetch?url=example.com", Description: "Evidence Fetch"},
		Accepts:     []x402types.PaymentRequirements{validRequirement()},
		Extensions: map[string]interface{}{
			builderCodeKey: map[string]interface{}{
				"info": map[string]interface{}{"a": "evidence_fetch", "s": []interface{}{"provider_sdk"}},
			},
		},
	}
}

func TestDecodePaymentRequiredHeader(t *testing.T) {
	required := validPaymentRequired()
	raw, err := json.Marshal(required)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodePaymentRequiredHeader(base64.StdEncoding.EncodeToString(raw))
	if err != nil {
		t.Fatal(err)
	}
	if decoded.X402Version != VersionV2 || len(decoded.Accepts) != 1 {
		t.Fatalf("decoded unexpected requirement: %+v", decoded)
	}

	for name, header := range map[string]string{
		"empty": "", "not base64": "%%%", "not json": base64.StdEncoding.EncodeToString([]byte("no")),
		"v1": base64.StdEncoding.EncodeToString([]byte(`{"x402Version":1,"accepts":[]}`)),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodePaymentRequiredHeader(header); err == nil {
				t.Fatal("expected rejection")
			}
		})
	}
	duplicate := base64.StdEncoding.EncodeToString([]byte(`{"x402Version":2,"x402Version":1,"accepts":[]}`))
	if _, err := DecodePaymentRequiredHeader(duplicate); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate JSON key was not rejected: %v", err)
	}
}

func TestQuoteSelectsCheapestEligibleAndBindsRequest(t *testing.T) {
	adapter := testAdapter(t)
	required := validPaymentRequired()
	expensive := validRequirement()
	expensive.Amount = "300000"
	wrongNetwork := validRequirement()
	wrongNetwork.Network = "eip155:1"
	required.Accepts = []x402types.PaymentRequirements{expensive, wrongNetwork, validRequirement()}

	quote, err := adapter.Quote(required, "get", required.Resource.URL, nil, "500000")
	if err != nil {
		t.Fatal(err)
	}
	if quote.AmountAtomic != "250000" || quote.Network != BaseSepoliaNetwork || quote.Asset != BaseSepoliaUSDC {
		t.Fatalf("selected wrong quote: %+v", quote)
	}
	if quote.Request.Method != "GET" || !strings.HasPrefix(quote.Request.BodySHA256, "0x") || len(quote.Digest) != 66 {
		t.Fatalf("request was not canonically bound: %+v", quote.Request)
	}

	again, err := adapter.Quote(required, "GET", required.Resource.URL, nil, "500000")
	if err != nil || again.Digest != quote.Digest {
		t.Fatalf("same offer was not deterministic: %v %s %s", err, quote.Digest, again.Digest)
	}
	changedBody, err := adapter.Quote(required, "GET", required.Resource.URL, []byte("changed"), "500000")
	if err != nil {
		t.Fatal(err)
	}
	if changedBody.Digest == quote.Digest {
		t.Fatal("body substitution did not change quote digest")
	}

	quote.Extensions[builderCodeKey].(map[string]interface{})["info"] = "mutated"
	if required.Extensions[builderCodeKey].(map[string]interface{})["info"] == "mutated" {
		t.Fatal("quote retained caller-owned extension map")
	}
}

func TestQuoteRejectsIneligibleRequirements(t *testing.T) {
	adapter := testAdapter(t)
	tests := map[string]func(*x402types.PaymentRequirements){
		"wrong scheme":           func(r *x402types.PaymentRequirements) { r.Scheme = "upto" },
		"wrong network":          func(r *x402types.PaymentRequirements) { r.Network = "eip155:8453" },
		"wrong asset":            func(r *x402types.PaymentRequirements) { r.Asset = testRecipient },
		"noncanonical asset":     func(r *x402types.PaymentRequirements) { r.Asset = strings.ToUpper(BaseSepoliaUSDC) },
		"noncanonical recipient": func(r *x402types.PaymentRequirements) { r.PayTo = strings.ToUpper(testRecipient) },
		"permit2":                func(r *x402types.PaymentRequirements) { r.Extra["assetTransferMethod"] = "permit2" },
		"unknown transfer":       func(r *x402types.PaymentRequirements) { r.Extra["assetTransferMethod"] = "future" },
		"too expensive":          func(r *x402types.PaymentRequirements) { r.Amount = "500001" },
		"zero amount":            func(r *x402types.PaymentRequirements) { r.Amount = "0" },
		"leading zero":           func(r *x402types.PaymentRequirements) { r.Amount = "0250000" },
		"timeout":                func(r *x402types.PaymentRequirements) { r.MaxTimeoutSeconds = 301 },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			required := validPaymentRequired()
			mutate(&required.Accepts[0])
			if _, err := adapter.Quote(required, "GET", required.Resource.URL, nil, "500000"); err == nil {
				t.Fatal("expected requirement rejection")
			}
		})
	}
}

func TestQuoteRejectsSubstitutionAndAmbiguity(t *testing.T) {
	adapter := testAdapter(t)
	required := validPaymentRequired()
	if _, err := adapter.Quote(required, "GET", "https://attacker.example/fetch", nil, "500000"); err == nil {
		t.Fatal("expected resource URL substitution rejection")
	}
	if _, err := adapter.Quote(required, "GET", required.Resource.URL, nil, "200000"); err == nil {
		t.Fatal("expected caller maximum rejection")
	}

	other := validRequirement()
	other.PayTo = "0x2222222222222222222222222222222222222222"
	required.Accepts = append(required.Accepts, other)
	if _, err := adapter.Quote(required, "GET", required.Resource.URL, nil, "500000"); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("expected ambiguity rejection, got %v", err)
	}
}

func TestValidateQuoteRejectsPostCreationMutation(t *testing.T) {
	adapter := testAdapter(t)
	required := validPaymentRequired()
	quote, err := adapter.Quote(required, "GET", required.Resource.URL, nil, "500000")
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.ValidateQuote(quote); err != nil {
		t.Fatalf("fresh quote did not validate: %v", err)
	}
	quote.Extensions[builderCodeKey].(map[string]interface{})["info"] = map[string]interface{}{"a": "attacker"}
	if err := adapter.ValidateQuote(quote); err == nil {
		t.Fatal("mutated quote retained authority")
	}
	if _, evidence, err := adapter.PaymentExtensions(quote); err == nil || evidence.State != AttributionUnknown {
		t.Fatalf("mutated quote reached payment extensions: %+v %v", evidence, err)
	}
}

func TestQuoteRejectsNonJSONExtension(t *testing.T) {
	adapter := testAdapter(t)
	required := validPaymentRequired()
	required.Extensions["bad"] = make(chan int)
	if _, err := adapter.Quote(required, "GET", required.Resource.URL, nil, "500000"); err == nil {
		t.Fatal("expected non-JSON extension to fail closed")
	}
}

func TestNewRejectsNetworkAssetAndBuilderCodeMismatch(t *testing.T) {
	base := Config{Network: BaseSepoliaNetwork, ChainID: BaseSepoliaChainID, USDCAddress: BaseSepoliaUSDC, MaxAmountAtomic: "1", MaxTimeoutSeconds: 1}
	tests := []Config{
		{Network: "eip155:1", ChainID: 1, USDCAddress: BaseSepoliaUSDC, MaxAmountAtomic: "1", MaxTimeoutSeconds: 1},
		{Network: BaseSepoliaNetwork, ChainID: BaseMainnetChainID, USDCAddress: BaseSepoliaUSDC, MaxAmountAtomic: "1", MaxTimeoutSeconds: 1},
		{Network: BaseSepoliaNetwork, ChainID: BaseSepoliaChainID, USDCAddress: BaseMainnetUSDC, MaxAmountAtomic: "1", MaxTimeoutSeconds: 1},
	}
	badCode := base
	badCode.ServiceCodes = []string{"Not_Canonical"}
	tests = append(tests, badCode)
	for i, config := range tests {
		if _, err := New(config); err == nil {
			t.Fatalf("case %d: expected invalid config", i)
		}
	}
}
