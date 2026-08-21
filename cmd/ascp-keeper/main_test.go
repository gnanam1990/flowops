package main

import (
	"net/url"
	"strings"
	"testing"
)

func databaseURL(query string) string {
	return (&url.URL{
		Scheme:   "postgres",
		User:     url.User("keeper"),
		Host:     "example.invalid",
		Path:     "/flowops",
		RawQuery: query,
	}).String()
}

func validEnvironment(t *testing.T) {
	t.Helper()
	t.Setenv("FLOWOPS_KEEPER_DATABASE_URL", databaseURL("sslmode=verify-full"))
	t.Setenv("FLOWOPS_KEEPER_ID", "keeper-primary")
	t.Setenv("FLOWOPS_KEEPER_GAS_PAYER", "0x1111111111111111111111111111111111111111")
	t.Setenv("FLOWOPS_KEEPER_CHAIN_ID", "84532")
	t.Setenv("FLOWOPS_KEEPER_MAX_FEE_PER_GAS_WEI", "100000000000")
	t.Setenv("FLOWOPS_KEEPER_MAX_PRIORITY_FEE_PER_GAS_WEI", "2000000000")
	for index, name := range []string{"ARTIFACT", "ASSEMBLER", "VERIFIER", "WALLET", "SEALER", "BROADCAST", "CHAIN"} {
		t.Setenv("FLOWOPS_KEEPER_"+name+"_SOCKET", "/run/flowops/keeper-"+strings.ToLower(name)+string(rune('a'+index))+".sock")
	}
}

func TestLoadConfigAcceptsSeparatedFailClosedBoundaries(t *testing.T) {
	validEnvironment(t)
	config, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if config.keeperID != "keeper-primary" || config.maxFeeBumps != 3 || len(config.sockets) != 7 {
		t.Fatalf("unexpected config: %+v", config)
	}
}

func TestLoadConfigRejectsSharedAssemblerVerifierBoundary(t *testing.T) {
	validEnvironment(t)
	t.Setenv("FLOWOPS_KEEPER_VERIFIER_SOCKET", "/run/flowops/keeper-assemblerb.sock")
	if _, err := loadConfig(); err == nil || !strings.Contains(err.Error(), "must not share") {
		t.Fatalf("expected shared boundary rejection, got %v", err)
	}
}

func TestLoadConfigRejectsUnsafeDatabaseOverridesAndTiming(t *testing.T) {
	validEnvironment(t)
	t.Setenv("FLOWOPS_KEEPER_DATABASE_URL", databaseURL("sslmode=verify-full&search_path=evil"))
	if _, err := loadConfig(); err == nil {
		t.Fatal("expected database override rejection")
	}
	validEnvironment(t)
	t.Setenv("FLOWOPS_KEEPER_CYCLE_TIMEOUT", "20s")
	if _, err := loadConfig(); err == nil {
		t.Fatal("expected insufficient boundary budget rejection")
	}
}

func TestLoadConfigRejectsNoncanonicalFeeAndAddress(t *testing.T) {
	validEnvironment(t)
	t.Setenv("FLOWOPS_KEEPER_MAX_FEE_PER_GAS_WEI", "0100")
	if _, err := loadConfig(); err == nil {
		t.Fatal("expected noncanonical fee rejection")
	}
	validEnvironment(t)
	t.Setenv("FLOWOPS_KEEPER_GAS_PAYER", "0x111111111111111111111111111111111111111A")
	if _, err := loadConfig(); err == nil {
		t.Fatal("expected noncanonical address rejection")
	}
	validEnvironment(t)
	t.Setenv("FLOWOPS_KEEPER_MAX_PRIORITY_FEE_PER_GAS_WEI", "100000000001")
	if _, err := loadConfig(); err == nil {
		t.Fatal("expected priority fee above max fee rejection")
	}
}
