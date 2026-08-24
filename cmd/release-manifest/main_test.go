package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gnanam1990/flowops/internal/releaseadmission"
)

func TestReleaseManifestCommandSignsVerifiesAndRefusesOverwrite(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	manifest := commandManifest(now)
	path := filepath.Join(t.TempDir(), "release.json")
	writeManifest(t, path, manifest)
	t.Setenv("FLOWOPS_BASE_MAINNET_RELEASE_PRIVATE_KEY_B64", base64.StdEncoding.EncodeToString(privateKey))
	if err := run([]string{"sign", path}, now); err != nil {
		t.Fatal(err)
	}

	manifest, err = releaseadmission.Sign(manifest, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	writeManifest(t, path, manifest)
	t.Setenv("FLOWOPS_BASE_MAINNET_RELEASE_PUBLIC_KEY_B64", base64.StdEncoding.EncodeToString(publicKey))
	if err := run([]string{"verify", path}, now); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"digest", path}, now); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"sign", path}, now); err == nil || !strings.Contains(err.Error(), "replace") {
		t.Fatalf("overwrite error=%v", err)
	}
}

func writeManifest(t *testing.T, path string, manifest releaseadmission.Manifest) {
	t.Helper()
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func commandManifest(now time.Time) releaseadmission.Manifest {
	a := func(value int) string {
		return "0x" + strings.Repeat("0", 38) + string(rune('0'+value/10)) + string(rune('0'+value%10))
	}
	d := func(value int) string {
		return "0x" + strings.Repeat("0", 62) + string(rune('0'+value/10)) + string(rune('0'+value%10))
	}
	return releaseadmission.Manifest{
		SchemaVersion: 1, ReleaseID: "release_command_test", Network: releaseadmission.BaseMainnetNetwork,
		ChainID: releaseadmission.BaseMainnetChainID, SourceCommit: strings.Repeat("a", 40),
		TypedDataManifestSHA256: releaseadmission.TypedDataManifestSHA256, ExternalReviewSHA256: d(1), RPCAdmissionSHA256: d(2),
		GovernanceFromBlock: 100, SettlementWindowSeconds: 3600, ReviewedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour), RuntimeEnabled: true,
		Asset: releaseadmission.AssetBinding{Address: releaseadmission.BaseMainnetUSDC, Symbol: "USDC", Decimals: 6, RuntimeCodeHash: d(3)},
		Contracts: []releaseadmission.ContractBinding{
			{Name: "service_directory", Address: a(10), RuntimeCodeHash: d(10), DeploymentTx: d(20), DeploymentBlock: 100, SourceVerified: true},
			{Name: "agent_registry", Address: a(11), RuntimeCodeHash: d(11), DeploymentTx: d(21), DeploymentBlock: 101, SourceVerified: true},
			{Name: "ascp_call_escrow", Address: a(12), RuntimeCodeHash: d(12), DeploymentTx: d(22), DeploymentBlock: 102, SourceVerified: true},
			{Name: "ascp_spend_module", Address: a(13), RuntimeCodeHash: d(13), DeploymentTx: d(23), DeploymentBlock: 103, SourceVerified: true},
		},
		Safe:        releaseadmission.SafeBinding{Address: a(1), Owners: []string{a(2), a(3), a(4)}, Threshold: 2},
		Authorities: releaseadmission.AuthorityBinding{Governor: a(1), DirectoryPublisher: a(5), DirectoryPauser: a(6), RegistryAdmin: a(7), SpendAuthorizer: a(8)},
		Pilot:       releaseadmission.PilotBinding{MaxPerActionAtomic: "1000000", MaxOutstandingAtomic: "10000000"},
		Observer:    releaseadmission.InitialObserverProfile(),
		SignerKeyID: "release_command_key",
	}
}
