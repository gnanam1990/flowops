package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/gnanam1990/flowops/internal/releaseadmission"
	"github.com/gnanam1990/flowops/internal/rpcadmission"
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
	if cfg.engine.ChainID != 84532 || cfg.engine.EscrowContract != "0x86e145397f58e71c134c0e054320db929483227a" || cfg.engine.EscrowAsset != "0x036cbd53842c5426634e7929541ec2318f3dcf7e" || cfg.engine.EscrowReleaseWindow != 3600 || cfg.engine.ObserverQuorum != 2 || cfg.engine.ObservationMaxAge != 45*time.Second || cfg.interval != 15*time.Second || cfg.timeout != 10*time.Second || cfg.reconciliationInterval != 20*time.Second || cfg.reconciliationTimeout != 10*time.Second {
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
	t.Run("Base mainnet accepts only a signed exact release", func(t *testing.T) {
		setObserverRuntime(t)
		setMainnetReleaseRuntime(t)
		cfg, err := loadObserverRuntimeConfig()
		if err != nil || cfg.engine.ChainID != 8453 || cfg.releaseManifest == nil {
			t.Fatalf("mainnet observer config=%+v error=%v", cfg, err)
		}
	})
	t.Run("Base mainnet rejects observer profile substitution", func(t *testing.T) {
		setObserverRuntime(t)
		setMainnetReleaseRuntime(t)
		t.Setenv("FLOWOPS_BASE_HALT_CONFIRMATIONS", "4")
		if _, err := loadObserverRuntimeConfig(); err == nil || !strings.Contains(err.Error(), "signed Base mainnet release") {
			t.Fatalf("observer substitution error=%v", err)
		}
	})
	t.Run("Base mainnet requires production RPC admission before the promotion gate", func(t *testing.T) {
		setObserverRuntime(t)
		t.Setenv("FLOWOPS_BASE_CHAIN_ID", "8453")
		t.Setenv("FLOWOPS_BASE_RPC_PROVIDERS_JSON", `[{"name":"rpc_alpha","url":"https://alpha.vendor.example/v1/secret"},{"name":"rpc_beta","url":"https://beta.vendor.example/v1/secret"}]`)
		if _, err := loadObserverRuntimeConfig(); err == nil || !strings.Contains(err.Error(), "ADMISSION_JSON is required") {
			t.Fatalf("missing admission error = %v", err)
		}
	})
	t.Run("Base Sepolia refuses production admission metadata", func(t *testing.T) {
		setObserverRuntime(t)
		t.Setenv("FLOWOPS_BASE_RPC_ADMISSION_JSON", `{"schemaVersion":1,"providers":[]}`)
		if _, err := loadObserverRuntimeConfig(); err == nil || !strings.Contains(err.Error(), "must be unset") {
			t.Fatalf("Sepolia admission error = %v", err)
		}
	})
	t.Run("partial escrow deployment is rejected", func(t *testing.T) {
		setObserverRuntime(t)
		t.Setenv("FLOWOPS_ESCROW_ASSET", "")
		if _, err := loadObserverRuntimeConfig(); err == nil || !strings.Contains(err.Error(), "configured together") {
			t.Fatalf("partial escrow deployment error = %v", err)
		}
	})
	t.Run("escrow deployment requires canonical distinct addresses", func(t *testing.T) {
		setObserverRuntime(t)
		t.Setenv("FLOWOPS_ESCROW_CONTRACT", "0x036CbD53842c5426634e7929541eC2318f3dCF7e")
		if _, err := loadObserverRuntimeConfig(); err == nil {
			t.Fatal("mixed-case escrow deployment was accepted")
		}
	})
}

func setMainnetReleaseRuntime(t *testing.T) releaseadmission.Manifest {
	t.Helper()
	const admissionRaw = `{"schemaVersion":1,"providers":[{"name":"rpc_alpha","operator":"vendor_alpha","failureDomain":"vendor_alpha_global","serviceTier":"paid","productionEligible":true},{"name":"rpc_beta","operator":"vendor_beta","failureDomain":"vendor_beta_global","serviceTier":"paid","productionEligible":true}]}`
	admission, err := rpcadmission.DecodeProductionAdmission(admissionRaw)
	if err != nil {
		t.Fatal(err)
	}
	admissionDigest, err := releaseadmission.RPCAdmissionSHA256(admission)
	if err != nil {
		t.Fatal(err)
	}
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	manifest := releaseadmission.Manifest{
		SchemaVersion: 1, ReleaseID: "release_test_mainnet", Network: releaseadmission.BaseMainnetNetwork,
		ChainID: releaseadmission.BaseMainnetChainID, SourceCommit: strings.Repeat("a", 40),
		TypedDataManifestSHA256: releaseadmission.TypedDataManifestSHA256, ExternalReviewSHA256: observerDigest(1),
		RPCAdmissionSHA256: admissionDigest, GovernanceFromBlock: 100, SettlementWindowSeconds: 3600,
		ReviewedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour), RuntimeEnabled: true,
		Asset: releaseadmission.AssetBinding{Address: releaseadmission.BaseMainnetUSDC, Symbol: "USDC", Decimals: releaseadmission.USDCDecimals, RuntimeCodeHash: observerDigest(2)},
		Contracts: []releaseadmission.ContractBinding{
			{Name: "service_directory", Address: observerAddress(10), RuntimeCodeHash: observerDigest(10), DeploymentTx: observerDigest(20), DeploymentBlock: 100, SourceVerified: true},
			{Name: "agent_registry", Address: observerAddress(11), RuntimeCodeHash: observerDigest(11), DeploymentTx: observerDigest(21), DeploymentBlock: 101, SourceVerified: true},
			{Name: "ascp_call_escrow", Address: observerAddress(12), RuntimeCodeHash: observerDigest(12), DeploymentTx: observerDigest(22), DeploymentBlock: 102, SourceVerified: true},
			{Name: "ascp_spend_module", Address: observerAddress(13), RuntimeCodeHash: observerDigest(13), DeploymentTx: observerDigest(23), DeploymentBlock: 103, SourceVerified: true},
		},
		Safe:        releaseadmission.SafeBinding{Address: observerAddress(1), Owners: []string{observerAddress(2), observerAddress(3), observerAddress(4)}, Threshold: 2},
		Authorities: releaseadmission.AuthorityBinding{Governor: observerAddress(1), DirectoryPublisher: observerAddress(5), DirectoryPauser: observerAddress(6), RegistryAdmin: observerAddress(7), SpendAuthorizer: observerAddress(8)},
		Pilot:       releaseadmission.PilotBinding{MaxPerActionAtomic: releaseadmission.InitialMaxPerActionAtomic, MaxOutstandingAtomic: releaseadmission.InitialMaxOutstandingAtomic},
		Observer:    releaseadmission.InitialObserverProfile(),
		SignerKeyID: "release_test_key",
	}
	manifest, err = releaseadmission.Sign(manifest, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("FLOWOPS_BASE_CHAIN_ID", "8453")
	t.Setenv("FLOWOPS_BASE_RPC_PROVIDERS_JSON", `[{"name":"rpc_alpha","url":"https://alpha.vendor.example/v1/secret"},{"name":"rpc_beta","url":"https://beta.vendor.example/v1/secret"}]`)
	t.Setenv("FLOWOPS_BASE_RPC_ADMISSION_JSON", admissionRaw)
	t.Setenv("FLOWOPS_BASE_MAINNET_RELEASE_MANIFEST_JSON", string(raw))
	t.Setenv("FLOWOPS_BASE_MAINNET_RELEASE_PUBLIC_KEY_B64", base64.StdEncoding.EncodeToString(publicKey))
	t.Setenv("FLOWOPS_ESCROW_CONTRACT", observerAddress(12))
	t.Setenv("FLOWOPS_ESCROW_ASSET", releaseadmission.BaseMainnetUSDC)
	t.Setenv("FLOWOPS_ESCROW_RELEASE_WINDOW_SECONDS", "3600")
	return manifest
}

func observerAddress(value int) string { return fmt.Sprintf("0x%040x", value) }
func observerDigest(value int) string  { return fmt.Sprintf("0x%064x", value) }
