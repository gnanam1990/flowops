package main

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestLoadConfigRequiresExplicitSecurityAndNetworkInputs(t *testing.T) {
	for _, name := range []string{
		"FLOWOPS_DATABASE_URL", "FLOWOPS_ENVELOPE_KEY_ID", "FLOWOPS_ENVELOPE_PRIVATE_KEY_B64",
		"FLOWOPS_SITE_SESSION_KEY_B64", "FLOWOPS_RECONCILIATION_JOURNAL", "FLOWOPS_OPERATOR_CONTROL_KEY_B64",
		"FLOWOPS_BASE_RPC_PROVIDERS_JSON",
		"FLOWOPS_PILOT_MAX_PER_ACTION_ATOMIC", "FLOWOPS_PILOT_MAX_OUTSTANDING_ATOMIC",
	} {
		t.Setenv(name, "")
	}
	if _, err := loadConfig(); err == nil {
		t.Fatal("empty configuration was accepted")
	}

	privateKey := ed25519.NewKeyFromSeed([]byte(strings.Repeat("s", ed25519.SeedSize)))
	setObserverRuntime(t)
	t.Setenv("FLOWOPS_DATABASE_URL", "postgres://flowops@localhost/flowops?sslmode=require")
	t.Setenv("FLOWOPS_ENVELOPE_KEY_ID", "flowops_control_1")
	t.Setenv("FLOWOPS_ENVELOPE_PRIVATE_KEY_B64", base64.StdEncoding.EncodeToString(privateKey))
	t.Setenv("FLOWOPS_SITE_SESSION_KEY_B64", base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32))))
	t.Setenv("FLOWOPS_RECONCILIATION_JOURNAL", "/var/lib/flowops/reconciliation.log")
	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.address != defaultAddress || cfg.trustProxy || !cfg.applyMigrations || len(cfg.envelopeKey) != ed25519.PrivateKeySize || len(cfg.siteSessionKey) != 32 {
		t.Fatalf("configuration was not normalized: %+v", cfg)
	}
}

func TestLoadConfigCanDisableRuntimeMigrations(t *testing.T) {
	privateKey := ed25519.NewKeyFromSeed([]byte(strings.Repeat("m", ed25519.SeedSize)))
	setObserverRuntime(t)
	t.Setenv("FLOWOPS_DATABASE_URL", "postgres://flowops@localhost/flowops?sslmode=require")
	t.Setenv("FLOWOPS_ENVELOPE_KEY_ID", "flowops_control_1")
	t.Setenv("FLOWOPS_ENVELOPE_PRIVATE_KEY_B64", base64.StdEncoding.EncodeToString(privateKey))
	t.Setenv("FLOWOPS_SITE_SESSION_KEY_B64", base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32))))
	t.Setenv("FLOWOPS_RECONCILIATION_JOURNAL", "/var/lib/flowops/reconciliation.log")
	t.Setenv("FLOWOPS_APPLY_MIGRATIONS", "false")
	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.applyMigrations {
		t.Fatal("runtime migrations remained enabled")
	}
	t.Setenv("FLOWOPS_APPLY_MIGRATIONS", "yes")
	if _, err := loadConfig(); err == nil {
		t.Fatal("invalid migration toggle was accepted")
	}
}

func TestDecodeSymmetricKeyRejectsWrongLengthAndEncoding(t *testing.T) {
	if _, err := decodeSymmetricKey("TEST_KEY", "not-base64"); err == nil {
		t.Fatal("malformed key was accepted")
	}
	if _, err := decodeSymmetricKey("TEST_KEY", base64.StdEncoding.EncodeToString(make([]byte, 31))); err == nil {
		t.Fatal("31-byte key was accepted")
	}
}

func TestControlPlaneListenAddressIsLoopbackOnly(t *testing.T) {
	for _, address := range []string{"127.0.0.1:8080", "127.1.2.3:8080", "[::1]:8080"} {
		if err := validateListenAddress(address, false); err != nil {
			t.Errorf("accepted address %q: %v", address, err)
		}
	}
	for _, address := range []string{"0.0.0.0:8080", "[::]:8080", "192.0.2.1:8080", "localhost:8080", "example.com:8080", ":8080", "not-an-address"} {
		if err := validateListenAddress(address, false); err == nil {
			t.Errorf("non-loopback address %q was accepted", address)
		}
	}
	for _, address := range []string{"0.0.0.0:8080", "[::]:8080"} {
		if err := validateListenAddress(address, true); err != nil {
			t.Errorf("trusted proxy address %q: %v", address, err)
		}
	}
}

func TestProxyTransportBoundaryRejectsUntrustedPlaintext(t *testing.T) {
	next := http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) { writer.WriteHeader(http.StatusNoContent) })
	handler := enforceTransportSecurity(next, true)

	tests := []struct {
		path, forwarded string
		want            int
	}{
		{path: "/health", want: http.StatusNoContent},
		{path: "/v1/dashboard/snapshot", forwarded: "https", want: http.StatusNoContent},
		{path: "/v1/dashboard/snapshot", forwarded: "HTTPS, http", want: http.StatusNoContent},
		{path: "/v1/dashboard/snapshot", forwarded: "http", want: http.StatusBadRequest},
		{path: "/v1/dashboard/snapshot", want: http.StatusBadRequest},
	}
	for _, item := range tests {
		request := httptest.NewRequest(http.MethodGet, item.path, nil)
		request.Header.Set("X-Forwarded-Proto", item.forwarded)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code != item.want {
			t.Errorf("%s forwarded=%q status=%d want=%d", item.path, item.forwarded, recorder.Code, item.want)
		}
	}
}

func TestLoadConfigUsesPlatformPortOnlyWithExplicitProxyTrust(t *testing.T) {
	privateKey := ed25519.NewKeyFromSeed([]byte(strings.Repeat("p", ed25519.SeedSize)))
	setObserverRuntime(t)
	t.Setenv("FLOWOPS_DATABASE_URL", "postgres://flowops@localhost/flowops?sslmode=require")
	t.Setenv("FLOWOPS_ENVELOPE_KEY_ID", "flowops_control_1")
	t.Setenv("FLOWOPS_ENVELOPE_PRIVATE_KEY_B64", base64.StdEncoding.EncodeToString(privateKey))
	t.Setenv("FLOWOPS_SITE_SESSION_KEY_B64", base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32))))
	t.Setenv("FLOWOPS_RECONCILIATION_JOURNAL", "/var/lib/flowops/reconciliation.log")
	t.Setenv("FLOWOPS_CONTROL_ADDR", "")
	t.Setenv("PORT", "9090")
	if _, err := loadConfig(); err == nil {
		t.Fatal("platform port accepted without explicit trusted proxy mode")
	}
	t.Setenv("FLOWOPS_TRUST_PROXY_HEADERS", "true")
	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.address != "0.0.0.0:9090" || !cfg.trustProxy {
		t.Fatalf("platform config = %+v", cfg)
	}
}

func TestLoadConfigRejectsEveryZeroValuedPlatformPort(t *testing.T) {
	privateKey := ed25519.NewKeyFromSeed([]byte(strings.Repeat("z", ed25519.SeedSize)))
	setObserverRuntime(t)
	t.Setenv("FLOWOPS_DATABASE_URL", "postgres://flowops@localhost/flowops?sslmode=require")
	t.Setenv("FLOWOPS_ENVELOPE_KEY_ID", "flowops_control_1")
	t.Setenv("FLOWOPS_ENVELOPE_PRIVATE_KEY_B64", base64.StdEncoding.EncodeToString(privateKey))
	t.Setenv("FLOWOPS_SITE_SESSION_KEY_B64", base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32))))
	t.Setenv("FLOWOPS_RECONCILIATION_JOURNAL", "/var/lib/flowops/reconciliation.log")
	t.Setenv("FLOWOPS_TRUST_PROXY_HEADERS", "true")
	for _, port := range []string{"0", "00", "00000"} {
		t.Setenv("PORT", port)
		if _, err := loadConfig(); err == nil {
			t.Errorf("zero-valued PORT %q was accepted", port)
		}
	}
}

func setObserverRuntime(t *testing.T) {
	t.Helper()
	t.Setenv("FLOWOPS_OPERATOR_CONTROL_KEY_B64", base64.StdEncoding.EncodeToString([]byte(strings.Repeat("o", 32))))
	t.Setenv("FLOWOPS_BASE_RPC_PROVIDERS_JSON", `[{"name":"rpc_alpha","url":"https://alpha.rpc.example/v1"},{"name":"rpc_beta","url":"https://beta.rpc.example/v1"}]`)
	t.Setenv("FLOWOPS_PILOT_MAX_PER_ACTION_ATOMIC", "1000000")
	t.Setenv("FLOWOPS_PILOT_MAX_OUTSTANDING_ATOMIC", "10000000")
}

func TestLoadConfigRejectsUnsafePilotLimits(t *testing.T) {
	privateKey := ed25519.NewKeyFromSeed([]byte(strings.Repeat("l", ed25519.SeedSize)))
	setObserverRuntime(t)
	t.Setenv("FLOWOPS_DATABASE_URL", strings.Join([]string{"postgres", "://flowops@localhost/flowops?sslmode=require"}, ""))
	t.Setenv("FLOWOPS_ENVELOPE_KEY_ID", "flowops_control_1")
	t.Setenv("FLOWOPS_ENVELOPE_PRIVATE_KEY_B64", base64.StdEncoding.EncodeToString(privateKey))
	t.Setenv("FLOWOPS_SITE_SESSION_KEY_B64", base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32))))
	t.Setenv("FLOWOPS_RECONCILIATION_JOURNAL", "/var/lib/flowops/reconciliation.log")
	for _, value := range []string{"", "0", "010", "10000001"} {
		t.Setenv("FLOWOPS_PILOT_MAX_PER_ACTION_ATOMIC", value)
		t.Setenv("FLOWOPS_PILOT_MAX_OUTSTANDING_ATOMIC", "10000000")
		if _, err := loadConfig(); err == nil {
			t.Fatalf("unsafe per-action pilot limit %q accepted", value)
		}
	}
}

func TestExpirySweeperRejectsInvalidConfiguration(t *testing.T) {
	if err := runExpirySweeper(context.Background(), nil, time.Second); err == nil {
		t.Fatal("nil lifecycle was accepted")
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
