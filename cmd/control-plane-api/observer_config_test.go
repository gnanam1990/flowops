package main

import (
	"strings"
	"testing"
	"time"
)

func TestParseObserverProvidersRejectsUnsafeOrAmbiguousSets(t *testing.T) {
	t.Parallel()
	valid := `[{"name":"alpha","url":"https://alpha.example/v1"},{"name":"beta","url":"https://beta.example/v1"}]`
	providers, err := parseObserverProviders(valid)
	if err != nil || len(providers) != 2 {
		t.Fatalf("valid providers = %+v, %v", providers, err)
	}
	for name, raw := range map[string]string{
		"empty":        "",
		"null":         "null",
		"one":          `[{"name":"alpha","url":"https://alpha.example"}]`,
		"unknown":      `[{"name":"alpha","url":"https://alpha.example","token":"secret"},{"name":"beta","url":"https://beta.example"}]`,
		"same-host":    `[{"name":"alpha","url":"https://same.example/a"},{"name":"beta","url":"https://same.example/b"}]`,
		"credentials":  `[{"name":"alpha","url":"https://user:pass@alpha.example"},{"name":"beta","url":"https://beta.example"}]`,
		"duplicate":    `[{"name":"alpha","url":"https://alpha.example","url":"https://attacker.example"},{"name":"beta","url":"https://beta.example"}]`,
		"case-variant": `[{"name":"alpha","Name":"mallory","url":"https://alpha.example"},{"name":"beta","url":"https://beta.example"}]`,
		"trailing":     valid + `{}`,
	} {
		if _, err := parseObserverProviders(raw); err == nil {
			t.Errorf("%s provider set was accepted", name)
		}
	}
	if _, err := parseObserverProviders(strings.Repeat("x", maxObserverProvidersJSONBytes+1)); err == nil {
		t.Fatal("oversized provider set was accepted")
	}
}

func TestLoadObserverRuntimeConfigValidatesTimingThresholdsAndChain(t *testing.T) {
	setObserverRuntime(t)
	cfg, err := loadObserverRuntimeConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.engine.ChainID != 84532 || cfg.engine.ObserverQuorum != 2 || cfg.engine.ObservationMaxAge != 45*time.Second || cfg.interval != 15*time.Second || cfg.timeout != 10*time.Second || cfg.reconciliationInterval != 20*time.Second || cfg.reconciliationTimeout != 10*time.Second {
		t.Fatalf("observer config = %+v", cfg)
	}

	for name, value := range map[string]string{
		"FLOWOPS_BASE_CHAIN_ID":               "1",
		"FLOWOPS_BASE_OBSERVER_QUORUM":        "3",
		"FLOWOPS_BASE_OBSERVER_TIMEOUT":       "15s",
		"FLOWOPS_BASE_RECONCILIATION_TIMEOUT": "20s",
		"FLOWOPS_BASE_OBSERVATION_MAX_AGE":    "15s",
		"FLOWOPS_BASE_STALL_THRESHOLD":        "15s",
		"FLOWOPS_BASE_HALT_CONFIRMATIONS":     "0",
		"FLOWOPS_BASE_RECOVERY_OBSERVATIONS":  "-1",
	} {
		t.Run(name, func(t *testing.T) {
			setObserverRuntime(t)
			t.Setenv(name, value)
			if _, err := loadObserverRuntimeConfig(); err == nil {
				t.Fatalf("accepted %s=%q", name, value)
			}
		})
	}
	t.Run("Base mainnet remains gated", func(t *testing.T) {
		setObserverRuntime(t)
		t.Setenv("FLOWOPS_BASE_CHAIN_ID", "8453")
		if _, err := loadObserverRuntimeConfig(); err == nil || !strings.Contains(err.Error(), "mainnet gate") {
			t.Fatalf("mainnet observer config error = %v", err)
		}
	})
}
