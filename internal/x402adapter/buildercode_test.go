package x402adapter

import (
	"reflect"
	"testing"
)

func TestPaymentExtensionsMergesClientAndServerCodes(t *testing.T) {
	adapter := testAdapter(t)
	required := validPaymentRequired()
	quote, err := adapter.Quote(required, "GET", required.Resource.URL, nil, "500000")
	if err != nil {
		t.Fatal(err)
	}
	extensions, evidence, err := adapter.PaymentExtensions(quote)
	if err != nil {
		t.Fatal(err)
	}
	info := extensions[builderCodeKey].(map[string]interface{})["info"].(map[string]interface{})
	if info["a"] != "evidence_fetch" || !reflect.DeepEqual(info["s"], []string{"flowops_client", "provider_sdk"}) {
		t.Fatalf("unexpected extension info: %#v", info)
	}
	if evidence.State != AttributionDeclaredExtensionOnly {
		t.Fatalf("declaration was incorrectly verified: %+v", evidence)
	}
}

func TestPaymentExtensionsClientOnly(t *testing.T) {
	adapter := testAdapter(t)
	required := validPaymentRequired()
	required.Extensions = nil
	quote, err := adapter.Quote(required, "GET", required.Resource.URL, nil, "500000")
	if err != nil {
		t.Fatal(err)
	}
	extensions, evidence, err := adapter.PaymentExtensions(quote)
	if err != nil {
		t.Fatal(err)
	}
	info := extensions[builderCodeKey].(map[string]interface{})["info"].(map[string]interface{})
	if _, hasApp := info["a"]; hasApp || !reflect.DeepEqual(info["s"], []string{"flowops_client"}) {
		t.Fatalf("unexpected client-only extension: %#v", info)
	}
	if evidence.State != AttributionDeclaredExtensionOnly {
		t.Fatalf("unexpected evidence: %+v", evidence)
	}
}

func TestPaymentExtensionsRejectsMalformedDeclaration(t *testing.T) {
	adapter := testAdapter(t)
	required := validPaymentRequired()
	required.Extensions[builderCodeKey] = map[string]interface{}{"info": map[string]interface{}{"a": "BAD"}}
	quote, err := adapter.Quote(required, "GET", required.Resource.URL, nil, "500000")
	if err != nil {
		t.Fatal(err)
	}
	if _, evidence, err := adapter.PaymentExtensions(quote); err == nil || evidence.State != AttributionUnknown {
		t.Fatalf("expected malformed declaration to be unknown: %+v %v", evidence, err)
	}
}

func TestPaymentExtensionsAcceptsScalarServerServiceCode(t *testing.T) {
	adapter := testAdapter(t)
	required := validPaymentRequired()
	required.Extensions[builderCodeKey] = map[string]interface{}{"info": map[string]interface{}{"a": "evidence_fetch", "s": "provider_sdk"}}
	quote, err := adapter.Quote(required, "GET", required.Resource.URL, nil, "500000")
	if err != nil {
		t.Fatal(err)
	}
	extensions, _, err := adapter.PaymentExtensions(quote)
	if err != nil {
		t.Fatal(err)
	}
	info := extensions[builderCodeKey].(map[string]interface{})["info"].(map[string]interface{})
	if !reflect.DeepEqual(info["s"], []string{"flowops_client", "provider_sdk"}) {
		t.Fatalf("scalar service code did not normalize: %#v", info)
	}
}

func TestParseBuilderCodeOfficialVectors(t *testing.T) {
	tests := []struct {
		name     string
		calldata string
		want     builderCodeData
	}{
		{
			name:     "app only",
			calldata: "0xdeadbeefa161616862635f6d79617070000c0280218021802180218021802180218021",
			want:     builderCodeData{A: "bc_myapp"},
		},
		{
			name:     "app and wallet",
			calldata: "0xa261616862635f6d7961707061777062635f6d79666163696c697461746f72001f0280218021802180218021802180218021",
			want:     builderCodeData{A: "bc_myapp", W: "bc_myfacilitator"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, ok := parseBuilderCodeSuffix(test.calldata)
			if !ok || !reflect.DeepEqual(*got, test.want) {
				t.Fatalf("parse mismatch: %+v %t", got, ok)
			}
		})
	}
}

func TestClassifyCalldataRequiresExpectedCodes(t *testing.T) {
	calldata := "0xdeadbeefa161616862635f6d79617070000c0280218021802180218021802180218021"
	verified := ClassifyCalldata(calldata, "bc_myapp", nil, true)
	if verified.State != AttributionVerifiedSuffix {
		t.Fatalf("expected verified suffix: %+v", verified)
	}
	mismatch := ClassifyCalldata(calldata, "other", nil, true)
	if mismatch.State != AttributionUnknown {
		t.Fatalf("app substitution was not rejected: %+v", mismatch)
	}
	missingService := ClassifyCalldata(calldata, "bc_myapp", []string{"flowops_client"}, true)
	if missingService.State != AttributionUnknown {
		t.Fatalf("missing service code was not rejected: %+v", missingService)
	}
}

func TestClassifyCalldataWithServiceAndWalletCodes(t *testing.T) {
	// Independent Schema 2 vector: {a:"bc_myapp",w:"bc_fac",s:["flowops_client"]}.
	calldata := "0xdeadbeefa361616862635f6d7961707061776662635f6661636173816e666c6f776f70735f636c69656e7400270280218021802180218021802180218021"
	evidence := ClassifyCalldata(calldata, "bc_myapp", []string{"flowops_client"}, true)
	if evidence.State != AttributionVerifiedSuffix || evidence.WalletCode != "bc_fac" || !reflect.DeepEqual(evidence.ServiceCodes, []string{"flowops_client"}) {
		t.Fatalf("expected fully verified attribution: %+v", evidence)
	}
}

func TestParseBuilderCodeRejectsMalformedSuffixes(t *testing.T) {
	valid := "0xdeadbeefa161616862635f6d79617070000c0280218021802180218021802180218021"
	tests := []string{
		"", "0xdeadbeef", valid + "00", "0xzz" + valid[2:],
		valid[:len(valid)-34] + "03" + valid[len(valid)-32:],
		valid[:len(valid)-38] + "ffff" + valid[len(valid)-34:],
	}
	for i, calldata := range tests {
		if _, ok := parseBuilderCodeSuffix(calldata); ok {
			t.Fatalf("case %d: expected malformed suffix rejection", i)
		}
	}
}

func FuzzParseBuilderCodeSuffix(f *testing.F) {
	f.Add("0xdeadbeefa161616862635f6d79617070000c0280218021802180218021802180218021")
	f.Add("0xdeadbeef")
	f.Add("")
	f.Fuzz(func(t *testing.T, calldata string) {
		parsed, ok := parseBuilderCodeSuffix(calldata)
		if !ok {
			return
		}
		if parsed.A != "" && !builderCodePattern.MatchString(parsed.A) {
			t.Fatalf("accepted invalid app code %q", parsed.A)
		}
		if parsed.W != "" && !builderCodePattern.MatchString(parsed.W) {
			t.Fatalf("accepted invalid wallet code %q", parsed.W)
		}
		for _, code := range parsed.S {
			if !builderCodePattern.MatchString(code) {
				t.Fatalf("accepted invalid service code %q", code)
			}
		}
	})
}
