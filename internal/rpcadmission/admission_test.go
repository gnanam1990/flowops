package rpcadmission

import (
	"strings"
	"testing"
)

const validProviders = `[{"name":"rpc_alpha","url":"https://alpha.vendor.example/v1/credential"},{"name":"rpc_beta","url":"https://beta.vendor.example/v1/credential"}]`
const validAdmission = `{"schemaVersion":1,"providers":[{"name":"rpc_alpha","operator":"vendor_alpha","failureDomain":"vendor_alpha_global","serviceTier":"paid","productionEligible":true},{"name":"rpc_beta","operator":"vendor_beta","failureDomain":"vendor_beta_global","serviceTier":"paid","productionEligible":true}]}`

func TestProductionAdmissionBindsIndependentPaidProviders(t *testing.T) {
	providers, err := DecodeProviders(validProviders)
	if err != nil {
		t.Fatal(err)
	}
	admission, err := DecodeProductionAdmission(validAdmission)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateProduction(providers, admission); err != nil {
		t.Fatal(err)
	}
}

func TestProductionAdmissionRejectsUnsafeMutations(t *testing.T) {
	providers, err := DecodeProviders(validProviders)
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"wrong-schema":       strings.Replace(validAdmission, `"schemaVersion":1`, `"schemaVersion":2`, 1),
		"free-tier":          strings.Replace(validAdmission, `"serviceTier":"paid"`, `"serviceTier":"free"`, 1),
		"ineligible":         strings.Replace(validAdmission, `"productionEligible":true`, `"productionEligible":false`, 1),
		"shared-operator":    strings.Replace(validAdmission, `"operator":"vendor_beta"`, `"operator":"vendor_alpha"`, 1),
		"shared-domain":      strings.Replace(validAdmission, `"failureDomain":"vendor_beta_global"`, `"failureDomain":"vendor_alpha_global"`, 1),
		"unknown-provider":   strings.Replace(validAdmission, `"name":"rpc_beta"`, `"name":"rpc_gamma"`, 1),
		"duplicate-field":    strings.Replace(validAdmission, `"name":"rpc_alpha"`, `"name":"rpc_alpha","name":"rpc_shadow"`, 1),
		"case-variant-field": strings.Replace(validAdmission, `"operator":"vendor_alpha"`, `"Operator":"shadow","operator":"vendor_alpha"`, 1),
		"unknown-field":      strings.Replace(validAdmission, `"serviceTier":"paid"`, `"secret":"leak","serviceTier":"paid"`, 1),
		"trailing-value":     validAdmission + `{}`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			admission, decodeErr := DecodeProductionAdmission(raw)
			if decodeErr == nil {
				decodeErr = ValidateProduction(providers, admission)
			}
			if decodeErr == nil {
				t.Fatal("unsafe admission was accepted")
			}
		})
	}
}

func TestProductionAdmissionRejectsKnownPublicEndpoints(t *testing.T) {
	for _, host := range []string{"mainnet.base.org", "MAINNET.BASE.ORG.", "developer-access-mainnet.base.org", "base-rpc.publicnode.com"} {
		raw := strings.Replace(validProviders, "alpha.vendor.example", host, 1)
		providers, err := DecodeProviders(raw)
		if err != nil {
			t.Fatal(err)
		}
		admission, err := DecodeProductionAdmission(validAdmission)
		if err != nil {
			t.Fatal(err)
		}
		if err := ValidateProduction(providers, admission); err == nil || strings.Contains(err.Error(), "credential") {
			t.Fatalf("host %s error = %v", host, err)
		}
	}
}

func TestSecretProviderParserRejectsAmbiguousInputs(t *testing.T) {
	for name, raw := range map[string]string{
		"empty":        "",
		"null":         "null",
		"one":          `[{"name":"alpha","url":"https://alpha.example"}]`,
		"unknown":      `[{"name":"alpha","url":"https://alpha.example","token":"secret"},{"name":"beta","url":"https://beta.example"}]`,
		"same-host":    `[{"name":"alpha","url":"https://same.example/a"},{"name":"beta","url":"https://same.example/b"}]`,
		"duplicate":    `[{"name":"alpha","url":"https://alpha.example","url":"https://attacker.example"},{"name":"beta","url":"https://beta.example"}]`,
		"case-variant": `[{"name":"alpha","Name":"mallory","url":"https://alpha.example"},{"name":"beta","url":"https://beta.example"}]`,
		"trailing":     validProviders + `{}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeProviders(raw); err == nil {
				t.Fatal("unsafe provider set was accepted")
			}
		})
	}
	if _, err := DecodeProviders(strings.Repeat("x", MaxJSONBytes+1)); err == nil {
		t.Fatal("oversized provider set was accepted")
	}
}
