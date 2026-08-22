package main

import (
	"net/url"
	"strings"
	"testing"
)

func bearerDatabaseURL(query string) string {
	return (&url.URL{Scheme: "postgres", User: url.User("bearer"), Host: "example.invalid", Path: "/flowops", RawQuery: query}).String()
}

func validBearerEnvironment(t *testing.T) {
	t.Helper()
	t.Setenv("FLOWOPS_BEARER_DATABASE_URL", bearerDatabaseURL("sslmode=verify-full"))
	t.Setenv("FLOWOPS_BEARER_WORKER_ID", "bearer-worker-1")
	t.Setenv("FLOWOPS_BEARER_SIGNER_KEY_ID", "signer-key-1")
	t.Setenv("FLOWOPS_BEARER_KEY_EPOCH", "1")
	t.Setenv("FLOWOPS_BEARER_KEEPER_ID", "keeper-primary")
	t.Setenv("FLOWOPS_BEARER_ORGANIZATION_ID", "org-pilot")
	t.Setenv("FLOWOPS_BEARER_LEADERSHIP_EPOCH", "7")
	t.Setenv("FLOWOPS_BEARER_SIGNER_SOCKET", "/run/flowops/bearer-signer.sock")
	t.Setenv("FLOWOPS_BEARER_MIRROR_SOCKET", "/run/flowops/bearer-mirror.sock")
}

func TestLoadConfigAcceptsSeparatedFailClosedBoundaries(t *testing.T) {
	validBearerEnvironment(t)
	config, err := loadConfig()
	if err != nil || config.keyEpoch != 1 || config.expiryBatchSize != 10 || config.advanceBatchSize != 40 {
		t.Fatalf("config=%+v err=%v", config, err)
	}
}

func TestLoadConfigRejectsSocketAndDatabaseSubstitution(t *testing.T) {
	validBearerEnvironment(t)
	t.Setenv("FLOWOPS_BEARER_MIRROR_SOCKET", "/run/flowops/bearer-signer.sock")
	if _, err := loadConfig(); err == nil || !strings.Contains(err.Error(), "must not share") {
		t.Fatalf("shared socket err=%v", err)
	}
	validBearerEnvironment(t)
	t.Setenv("FLOWOPS_BEARER_DATABASE_URL", bearerDatabaseURL("sslmode=verify-full&search_path=attacker"))
	if _, err := loadConfig(); err == nil {
		t.Fatal("database search path override was accepted")
	}
}

func TestLoadConfigRejectsLeaseShorterThanBoundaryRecoveryBudget(t *testing.T) {
	validBearerEnvironment(t)
	t.Setenv("FLOWOPS_BEARER_BOUNDARY_TIMEOUT", "5s")
	t.Setenv("FLOWOPS_BEARER_LEASE_DURATION", "6s")
	if _, err := loadConfig(); err == nil {
		t.Fatal("unsafe lease duration was accepted")
	}
}
