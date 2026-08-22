package main

import (
	"strings"
	"testing"
)

func TestLoadConfigAcceptsPinnedLeastPrivilegeRuntime(t *testing.T) {
	setValidEnvironment(t)
	config, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if config.chainID != 84532 || config.quorum != 2 || len(config.providers) != 2 {
		t.Fatalf("config=%+v", config)
	}
}

func TestLoadConfigRejectsTLSDowngradeAndNonIndependentRPCs(t *testing.T) {
	setValidEnvironment(t)
	t.Setenv("FLOWOPS_ASSET_HEALTH_DATABASE_URL", strings.Join([]string{"postgresql", "://health@db.example/flowops?sslmode=require"}, ""))
	if _, err := loadConfig(); err == nil {
		t.Fatal("TLS downgrade was accepted")
	}
	setValidEnvironment(t)
	t.Setenv("FLOWOPS_ASSET_HEALTH_RPC_PROVIDERS_JSON", `[{"name":"one","url":"https://same.example/rpc"},{"name":"two","url":"https://same.example/other"}]`)
	if _, err := loadConfig(); err == nil {
		t.Fatal("same-host RPC providers were accepted")
	}
}

func TestLoadConfigRejectsObservationWindowShorterThanCycle(t *testing.T) {
	setValidEnvironment(t)
	t.Setenv("FLOWOPS_ASSET_HEALTH_INTERVAL", "30s")
	t.Setenv("FLOWOPS_ASSET_HEALTH_MAX_OBSERVATION_AGE", "20s")
	if _, err := loadConfig(); err == nil {
		t.Fatal("unsafe observation window was accepted")
	}
}

func setValidEnvironment(t *testing.T) {
	t.Helper()
	t.Setenv("FLOWOPS_ASSET_HEALTH_DATABASE_URL", strings.Join([]string{"postgresql", "://health@db.example/flowops?sslmode=verify-full"}, ""))
	t.Setenv("FLOWOPS_ASSET_HEALTH_CHAIN_ID", "84532")
	t.Setenv("FLOWOPS_ASSET_HEALTH_ASSET", "0x036cbd53842c5426634e7929541ec2318f3dcf7e")
	t.Setenv("FLOWOPS_ASSET_HEALTH_PROXY_IMPLEMENTATION", "0x1111111111111111111111111111111111111111")
	t.Setenv("FLOWOPS_ASSET_HEALTH_RUNTIME_CODE_HASH", "0x1111111111111111111111111111111111111111111111111111111111111111")
	t.Setenv("FLOWOPS_ASSET_HEALTH_BUYER", "0x2222222222222222222222222222222222222222")
	t.Setenv("FLOWOPS_ASSET_HEALTH_ESCROW", "0x3333333333333333333333333333333333333333")
	t.Setenv("FLOWOPS_ASSET_HEALTH_RPC_PROVIDERS_JSON", `[{"name":"one","url":"https://one.example/rpc"},{"name":"two","url":"https://two.example/rpc"}]`)
	t.Setenv("FLOWOPS_ASSET_HEALTH_RPC_QUORUM", "2")
	for _, name := range []string{"FLOWOPS_ASSET_HEALTH_INTERVAL", "FLOWOPS_ASSET_HEALTH_QUERY_TIMEOUT", "FLOWOPS_ASSET_HEALTH_MAX_OBSERVATION_AGE"} {
		t.Setenv(name, "")
	}
}
