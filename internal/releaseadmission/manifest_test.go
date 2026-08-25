package releaseadmission

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
)

func TestSignedBaseMainnetReleaseManifestBindsCompleteRuntime(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	manifest := signedManifest(t, privateKey, now)
	if err := Verify(manifest, publicKey, now); err != nil {
		t.Fatal(err)
	}
	if err := BindRuntime(manifest, RuntimeBindings{
		EscrowAsset: BaseMainnetUSDC, DirectoryContract: address(10), AgentRegistry: address(11),
		CallEscrow: address(12), SpendModule: address(13), PilotPerAction: InitialMaxPerActionAtomic,
		PilotOutstanding: InitialMaxOutstandingAtomic, GovernanceFromBlock: 100, SettlementWindowSeconds: 3600,
	}); err != nil {
		t.Fatal(err)
	}
	if err := BindBuildProvenance(manifest, strings.Repeat("a", 40), manifest.ControlPlaneArtifactSHA256); err != nil {
		t.Fatal(err)
	}
	if key, err := DecodePublicKey(base64.StdEncoding.EncodeToString(publicKey)); err != nil || string(key) != string(publicKey) {
		t.Fatalf("decoded key=%x err=%v", key, err)
	}
}

func TestReleaseManifestRejectsSourceAndArtifactSubstitution(t *testing.T) {
	manifest := validManifest(time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC))
	for _, source := range []string{"", "unversioned", strings.Repeat("b", 40), strings.Repeat("A", 40)} {
		if err := BindBuildProvenance(manifest, source, manifest.ControlPlaneArtifactSHA256); err == nil {
			t.Fatalf("build source %q was accepted", source)
		}
	}
	artifact := filepath.Join(t.TempDir(), "control-plane-api")
	if err := os.WriteFile(artifact, []byte("reviewed control-plane artifact"), 0o600); err != nil {
		t.Fatal(err)
	}
	reviewedDigest, err := ArtifactSHA256(artifact)
	if err != nil {
		t.Fatal(err)
	}
	manifest.ControlPlaneArtifactSHA256 = reviewedDigest
	if err := BindBuildProvenance(manifest, manifest.SourceCommit, reviewedDigest); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(artifact, []byte("modified source with the same commit claim"), 0o600); err != nil {
		t.Fatal(err)
	}
	modifiedDigest, err := ArtifactSHA256(artifact)
	if err != nil {
		t.Fatal(err)
	}
	if err := BindBuildProvenance(manifest, manifest.SourceCommit, modifiedDigest); err == nil {
		t.Fatal("modified artifact with the reviewed source claim was accepted")
	}
	currentDigest, err := CurrentExecutableSHA256()
	if err != nil || !nonZeroDigest(currentDigest) {
		t.Fatalf("current executable digest=%q err=%v", currentDigest, err)
	}
}

func TestReleaseManifestRejectsResignedUnsafeMutations(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	mutations := map[string]func(*Manifest){
		"old schema":            func(m *Manifest) { m.SchemaVersion = 1 },
		"wrong chain":           func(m *Manifest) { m.ChainID = 84532 },
		"missing artifact":      func(m *Manifest) { m.ControlPlaneArtifactSHA256 = "" },
		"zero review digest":    func(m *Manifest) { m.ExternalReviewSHA256 = "0x" + strings.Repeat("0", 64) },
		"runtime disabled":      func(m *Manifest) { m.RuntimeEnabled = false },
		"expired":               func(m *Manifest) { m.ExpiresAt = now },
		"wrong asset":           func(m *Manifest) { m.Asset.Address = address(99) },
		"unverified source":     func(m *Manifest) { m.Contracts[0].SourceVerified = false },
		"duplicate contract":    func(m *Manifest) { m.Contracts[1].Address = m.Contracts[0].Address },
		"contract is asset":     func(m *Manifest) { m.Contracts[0].Address = m.Asset.Address },
		"missing contract":      func(m *Manifest) { m.Contracts = m.Contracts[:3] },
		"weak Safe":             func(m *Manifest) { m.Safe.Threshold = 1 },
		"diluted Safe":          func(m *Manifest) { m.Safe.Owners = append(m.Safe.Owners, address(9)) },
		"deployer is asset":     func(m *Manifest) { m.Deployer = m.Asset.Address },
		"Safe is asset":         func(m *Manifest) { m.Safe.Address = m.Asset.Address; m.Authorities.Governor = m.Asset.Address },
		"deployer is owner":     func(m *Manifest) { m.Deployer = m.Safe.Owners[0] },
		"duplicate owner":       func(m *Manifest) { m.Safe.Owners[1] = m.Safe.Owners[0] },
		"shared authority":      func(m *Manifest) { m.Authorities.RegistryAdmin = m.Authorities.DirectoryPublisher },
		"wrong pilot":           func(m *Manifest) { m.Pilot.MaxPerActionAtomic = "1000001" },
		"unsafe observer":       func(m *Manifest) { m.Observer.Quorum = 1 },
		"funding without proof": func(m *Manifest) { m.Pilot.FundingEnabled = true },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			manifest := validManifest(now)
			mutate(&manifest)
			manifest, err = Sign(manifest, privateKey)
			if err != nil {
				t.Fatal(err)
			}
			if err := Verify(manifest, publicKey, now); err == nil {
				t.Fatal("unsafe resigned manifest was accepted")
			}
		})
	}

	manifest := signedManifest(t, privateKey, now)
	manifest.SourceCommit = strings.Repeat("f", 40)
	if err := Verify(manifest, publicKey, now); err == nil {
		t.Fatal("tampered manifest signature was accepted")
	}
}

func TestReleaseManifestStrictDecodeAndRuntimeSubstitution(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	manifest := signedManifest(t, privateKey, time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC))
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Decode(string(raw)); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{
		strings.Replace(string(raw), `"schemaVersion":2`, `"schemaVersion":2,"schemaVersion":2`, 1),
		strings.Replace(string(raw), `"schemaVersion":2`, `"schemaVersion":2,"unknown":true`, 1),
		string(raw) + `{}`,
		strings.Repeat("x", MaxJSONBytes+1),
	} {
		if _, err := Decode(invalid); err == nil {
			t.Fatal("ambiguous release manifest accepted")
		}
	}
	bindings := RuntimeBindings{
		EscrowAsset: BaseMainnetUSDC, DirectoryContract: address(10), AgentRegistry: address(11),
		CallEscrow: address(12), SpendModule: address(13), PilotPerAction: InitialMaxPerActionAtomic,
		PilotOutstanding: InitialMaxOutstandingAtomic, GovernanceFromBlock: 100, SettlementWindowSeconds: 3600,
	}
	bindings.CallEscrow = address(88)
	if err := BindRuntime(manifest, bindings); err == nil {
		t.Fatal("runtime contract substitution accepted")
	}
}

func signedManifest(t *testing.T, key ed25519.PrivateKey, now time.Time) Manifest {
	t.Helper()
	manifest, err := Sign(validManifest(now), key)
	if err != nil {
		t.Fatal(err)
	}
	return manifest
}

func validManifest(now time.Time) Manifest {
	contracts := []ContractBinding{
		{Name: "service_directory", Address: address(10), RuntimeCodeHash: digest(10), DeploymentTx: digest(20), DeploymentBlock: 100, SourceVerified: true},
		{Name: "agent_registry", Address: address(11), RuntimeCodeHash: digest(11), DeploymentTx: digest(21), DeploymentBlock: 101, SourceVerified: true},
		{Name: "ascp_call_escrow", Address: address(12), RuntimeCodeHash: digest(12), DeploymentTx: digest(22), DeploymentBlock: 102, SourceVerified: true},
		{Name: "ascp_spend_module", Address: address(13), RuntimeCodeHash: digest(13), DeploymentTx: digest(23), DeploymentBlock: 103, SourceVerified: true},
	}
	return Manifest{
		SchemaVersion: ReleaseManifestSchemaVersion, ReleaseID: "release_2026_08", Network: BaseMainnetNetwork, ChainID: BaseMainnetChainID,
		SourceCommit: strings.Repeat("a", 40), ControlPlaneArtifactSHA256: digest(5), TypedDataManifestSHA256: TypedDataManifestSHA256, ExternalReviewSHA256: digest(2),
		RPCAdmissionSHA256: digest(3), GovernanceFromBlock: 100, SettlementWindowSeconds: 3600,
		ReviewedAt: now.Add(-time.Hour), ExpiresAt: now.Add(7 * 24 * time.Hour), RuntimeEnabled: true,
		Asset:       AssetBinding{Address: BaseMainnetUSDC, Symbol: "USDC", Decimals: USDCDecimals, RuntimeCodeHash: digest(4)},
		Contracts:   contracts,
		Deployer:    address(14),
		Safe:        SafeBinding{Address: address(1), Owners: []string{address(2), address(3), address(4)}, Threshold: 2},
		Authorities: AuthorityBinding{Governor: address(1), DirectoryPublisher: address(5), DirectoryPauser: address(6), RegistryAdmin: address(7), SpendAuthorizer: address(8)},
		Pilot:       PilotBinding{MaxPerActionAtomic: InitialMaxPerActionAtomic, MaxOutstandingAtomic: InitialMaxOutstandingAtomic},
		Observer:    InitialObserverProfile(),
		SignerKeyID: "release_operator_2026_08",
	}
}

func address(value int) string {
	return "0x" + strings.Repeat("0", 38) + string(rune('0'+value/10)) + string(rune('0'+value%10))
}

func digest(value int) string {
	return "0x" + strings.Repeat("0", 62) + string(rune('0'+value/10)) + string(rune('0'+value%10))
}
