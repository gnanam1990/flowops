package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"strings"
	"testing"
)

func TestParseSignerKeysIsStrictPublicKeyOnlyConfiguration(t *testing.T) {
	publicKey := []byte(strings.Repeat("p", ed25519.PublicKeySize))
	raw := `[{"organizationId":"org_a","customerId":"customer_a","keyId":"signer_1","publicKeyB64":"` + base64.StdEncoding.EncodeToString(publicKey) + `"}]`
	keys, err := parseSignerKeys(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1 || keys[0].OrganizationID != "org_a" || keys[0].CustomerID != "customer_a" || keys[0].KeyID != "signer_1" || string(keys[0].PublicKey) != string(publicKey) {
		t.Fatalf("parsed keys = %+v", keys)
	}
	if keys, err := parseSignerKeys(""); err != nil || keys != nil {
		t.Fatalf("empty keys = %+v, %v", keys, err)
	}
}

func TestParseSignerKeysRejectsAmbiguousPrivateAndDuplicateMaterial(t *testing.T) {
	publicKey := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("p", ed25519.PublicKeySize)))
	privateKey := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("s", ed25519.PrivateKeySize)))
	tests := map[string]string{
		"private key length": `[{"organizationId":"org_a","customerId":"customer_a","keyId":"signer_1","publicKeyB64":"` + privateKey + `"}]`,
		"unknown field":      `[{"organizationId":"org_a","customerId":"customer_a","keyId":"signer_1","publicKeyB64":"` + publicKey + `","privateKeyB64":"never"}]`,
		"missing field":      `[{"organizationId":"org_a","customerId":"customer_a","keyId":"signer_1"}]`,
		"duplicate field":    `[{"organizationId":"org_a","organizationId":"org_b","customerId":"customer_a","keyId":"signer_1","publicKeyB64":"` + publicKey + `"}]`,
		"duplicate identity": `[{"organizationId":"org_a","customerId":"customer_a","keyId":"signer_1","publicKeyB64":"` + publicKey + `"},{"organizationId":"org_a","customerId":"customer_a","keyId":"signer_1","publicKeyB64":"` + publicKey + `"}]`,
		"trailing JSON":      `[] {}`,
	}
	for name, raw := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := parseSignerKeys(raw); err == nil {
				t.Fatal("unsafe key configuration accepted")
			}
		})
	}
}
