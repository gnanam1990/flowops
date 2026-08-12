package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"strings"
	"testing"
)

func TestLoadConfigRequiresExplicitSecurityAndNetworkInputs(t *testing.T) {
	for _, name := range []string{
		"FLOWOPS_DATABASE_URL", "FLOWOPS_ENVELOPE_KEY_ID", "FLOWOPS_ENVELOPE_PRIVATE_KEY_B64",
		"FLOWOPS_RECONCILIATION_JOURNAL",
	} {
		t.Setenv(name, "")
	}
	if _, err := loadConfig(); err == nil {
		t.Fatal("empty configuration was accepted")
	}

	privateKey := ed25519.NewKeyFromSeed([]byte(strings.Repeat("s", ed25519.SeedSize)))
	t.Setenv("FLOWOPS_DATABASE_URL", "postgres://flowops@localhost/flowops?sslmode=require")
	t.Setenv("FLOWOPS_ENVELOPE_KEY_ID", "flowops_control_1")
	t.Setenv("FLOWOPS_ENVELOPE_PRIVATE_KEY_B64", base64.StdEncoding.EncodeToString(privateKey))
	t.Setenv("FLOWOPS_RECONCILIATION_JOURNAL", "/var/lib/flowops/reconciliation.log")
	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.address != defaultAddress || len(cfg.envelopeKey) != ed25519.PrivateKeySize {
		t.Fatalf("configuration was not normalized: %+v", cfg)
	}
}

func TestDecodePrivateKeyRejectsSeedAndMalformedMaterial(t *testing.T) {
	seed := []byte(strings.Repeat("x", ed25519.SeedSize))
	if _, err := decodePrivateKey(base64.StdEncoding.EncodeToString(seed)); err == nil {
		t.Fatal("32-byte seed was accepted where a full private key is required")
	}
	if _, err := decodePrivateKey("not-base64"); err == nil {
		t.Fatal("malformed key was accepted")
	}
	if _, err := decodePrivateKey(base64.StdEncoding.EncodeToString([]byte(strings.Repeat("x", ed25519.PrivateKeySize)))); err == nil {
		t.Fatal("non-canonical 64-byte key was accepted")
	}
}

func TestRandomSourcesProduceCanonicalDistinctIdentifiers(t *testing.T) {
	first, err := randomID("req")
	if err != nil {
		t.Fatal(err)
	}
	second, err := randomID("req")
	if err != nil {
		t.Fatal(err)
	}
	if first == second || !strings.HasPrefix(first, "req_") {
		t.Fatalf("IDs are not distinct canonical values: %q %q", first, second)
	}
	nonce, err := randomNonce()
	if err != nil {
		t.Fatal(err)
	}
	if len(nonce) != 66 || !strings.HasPrefix(nonce, "0x") {
		t.Fatalf("nonce = %q", nonce)
	}
}
