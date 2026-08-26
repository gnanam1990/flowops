package directoryrelease

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/gnanam1990/flowops/pkg/directoryproof"
)

func TestCompileVerifyAndEveryOddTreeProof(t *testing.T) {
	manifest, deployment := validFixture()
	artifact, err := Compile(manifest, deployment)
	if err != nil {
		t.Fatal(err)
	}
	if err := Verify(artifact, deployment); err != nil {
		t.Fatal(err)
	}
	golden := map[string]string{
		"root":      "0x1ecc06c3c00f64a7d77054f6d8131e03c74bf8d021b777f9c88de8fd3d710257",
		"blob":      "0x39d45c036716126c31d3747c96811eba0c29826acecf8d9d40ce7a3a7c1ab3be",
		"locations": "0xc0091241e31568f69766d46edaea56132172c8869a8098803c31c58bda8880f6",
		"workflow":  "0x55cf5f2f1243c758c246fa52357c21d5489ea21e854b5a81afa86c2e7de71bcf",
		"proposal":  "0x864f171371d53c8b9c48d46c789586119cba3f2a181fba2cdff01939388271e0",
		"propose":   "0xd3840894110cb6203f36cd34e5d5042933f88b9d9f5b6552552b68fc014b65d9",
	}
	if artifact.MerkleRoot != golden["root"] || artifact.BlobContentHash != golden["blob"] ||
		artifact.LocationsHash != golden["locations"] || artifact.Proposal.WorkflowPayloadHash != golden["workflow"] ||
		artifact.Proposal.ProposalHash != golden["proposal"] || artifact.Proposal.ProposePayloadHash != golden["propose"] {
		t.Fatalf("golden mismatch artifact=%+v", artifact)
	}
	if len(artifact.Leaves) != 3 || artifact.FundingEnabled || artifact.Proposal.ChangeClass != PayoutAuthorityChange {
		t.Fatalf("artifact boundary=%+v", artifact)
	}
	if artifact.Proposal.WorkflowPayloadHash != artifact.Approval.PayloadHash ||
		artifact.Proposal.ProposalHash == zeroHash() || artifact.Proposal.ProposePayloadHash == zeroHash() ||
		artifact.Approval.FunctionSelector != "0x0bf45ed9" || artifact.PublisherAuthorization.FunctionSelector != "0xfd0d35e6" {
		t.Fatalf("governance binding=%+v approval=%+v", artifact.Proposal, artifact.Approval)
	}
	for _, leaf := range artifact.Leaves {
		if err := directoryproof.Verify(artifact.MerkleRoot, common.HexToHash(leaf.Hash), leaf.Proof); err != nil {
			t.Fatalf("%s proof: %v", leaf.Kind, err)
		}
	}
	if got := string(artifact.CanonicalBlob); strings.Contains(got, "workflowId") || strings.Contains(got, "locations") || !strings.Contains(got, manifest.ReleaseID) {
		t.Fatalf("canonical blob scope=%s", got)
	}
}

func TestCompileRejectsManifestSubstitutionAndUnsafeBoundaries(t *testing.T) {
	base, deployment := validFixture()
	mutations := map[string]func(*Manifest){
		"schema":               func(m *Manifest) { m.SchemaVersion++ },
		"release":              func(m *Manifest) { m.ReleaseID = "X" },
		"chain":                func(m *Manifest) { m.ChainID = 8453 },
		"source release":       func(m *Manifest) { m.SourceDeployment.ReleaseID = "substituted-release" },
		"source commit":        func(m *Manifest) { m.SourceDeployment.SourceCommit = strings.Repeat("b", 40) },
		"directory":            func(m *Manifest) { m.DirectoryContract = address(99) },
		"organization":         func(m *Manifest) { m.OrganizationDomain = hash(99) },
		"publisher":            func(m *Manifest) { m.DirectoryPublisher = address(99) },
		"publisher epoch":      func(m *Manifest) { m.DirectoryPublisherEpoch = 2 },
		"asset":                func(m *Manifest) { m.Asset.Address = address(99) },
		"asset code":           func(m *Manifest) { m.Asset.RuntimeCodeHash = hash(99) },
		"version replay":       func(m *Manifest) { m.VersionID = 2 },
		"predecessor":          func(m *Manifest) { m.PreviousVersion = 1 },
		"previous root":        func(m *Manifest) { m.PreviousRoot = hash(98) },
		"ordinary downgrade":   func(m *Manifest) { m.ChangeClass = 1 },
		"stale requested time": func(m *Manifest) { m.RequestedActivatesAt = 1 },
		"zero workflow":        func(m *Manifest) { m.WorkflowID = zeroHash() },
		"zero nonce":           func(m *Manifest) { m.ProposerNonce = "0" },
		"noncanonical nonce":   func(m *Manifest) { m.ProposerNonce = "07" },
		"funding":              func(m *Manifest) { m.FundingEnabled = true },
		"one location":         func(m *Manifest) { m.Locations = m.Locations[:1] },
		"mutable location":     func(m *Manifest) { m.Locations[0] = "https://example.com/directory.json" },
		"wrong content cid":    func(m *Manifest) { m.Locations[1] = rawIPFSCID([]byte("substituted blob")) },
		"same location system": func(m *Manifest) { m.Locations[0] = m.Locations[1] },
		"unsorted locations":   func(m *Manifest) { m.Locations[0], m.Locations[1] = m.Locations[1], m.Locations[0] },
		"inactive seller":      func(m *Manifest) { m.Sellers[0].Status = 0 },
		"noncanonical origin":  func(m *Manifest) { m.Sellers[0].BaseURLOrigin = "https://Seller.Example/path" },
		"ip origin":            func(m *Manifest) { m.Sellers[0].BaseURLOrigin = "https://127.0.0.1" },
		"local origin":         func(m *Manifest) { m.Sellers[0].BaseURLOrigin = "https://seller.local" },
		"origin port":          func(m *Manifest) { m.Sellers[0].BaseURLOrigin = "https://seller.flowopsagent.xyz:8443" },
		"duplicate seller":     func(m *Manifest) { m.Sellers = append(m.Sellers, m.Sellers[0]) },
		"duplicate quote key": func(m *Manifest) {
			duplicate := m.Sellers[0]
			duplicate.SellerID = hash(55)
			m.Sellers = append(m.Sellers, duplicate)
		},
		"unknown resource seller":  func(m *Manifest) { m.Resources[0].SellerID = hash(55) },
		"direct-only resource":     func(m *Manifest) { m.Resources[0].EscrowSupported = false },
		"zero price":               func(m *Manifest) { m.Resources[0].Price = "0" },
		"zero work time":           func(m *Manifest) { m.Resources[0].DeclaredWorkTime = 0 },
		"zero verification budget": func(m *Manifest) { m.Resources[0].VerificationBudgetSeconds = 0 },
		"duplicate resource":       func(m *Manifest) { m.Resources = append(m.Resources, m.Resources[0]) },
		"seller without resource": func(m *Manifest) {
			orphan := m.Sellers[0]
			orphan.SellerID, orphan.QuoteSigningKey = hash(55), address(55)
			m.Sellers = append(m.Sellers, orphan)
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			changed := cloneManifest(t, base)
			mutate(&changed)
			if _, err := Compile(changed, deployment); err == nil {
				t.Fatal("unsafe manifest accepted")
			}
		})
	}

	changedDeployment := append([]byte(nil), deployment...)
	changedDeployment = []byte(strings.Replace(string(changedDeployment), base.DirectoryContract, address(88), 1))
	if _, err := Compile(base, changedDeployment); err == nil {
		t.Fatal("substituted deployment binding accepted")
	}
}

func TestVerifyRejectsEveryDerivedArtifactMutation(t *testing.T) {
	manifest, deployment := validFixture()
	base, err := Compile(manifest, deployment)
	if err != nil {
		t.Fatal(err)
	}
	mutations := map[string]func(*Artifact){
		"blob":               func(a *Artifact) { a.BlobContentHash = hash(88) },
		"canonical blob":     func(a *Artifact) { a.CanonicalBlob = json.RawMessage(`{"schemaVersion":1}`) },
		"locations":          func(a *Artifact) { a.LocationsHash = hash(88) },
		"root":               func(a *Artifact) { a.MerkleRoot = hash(88) },
		"leaf":               func(a *Artifact) { a.Leaves[0].Hash = hash(88) },
		"proof":              func(a *Artifact) { a.Leaves[0].Proof[0] = hash(88) },
		"workflow payload":   func(a *Artifact) { a.Proposal.WorkflowPayloadHash = hash(88) },
		"proposal hash":      func(a *Artifact) { a.Proposal.ProposalHash = hash(88) },
		"propose payload":    func(a *Artifact) { a.Proposal.ProposePayloadHash = hash(88) },
		"publisher signer":   func(a *Artifact) { a.PublisherAuthorization.ExpectedSigner = address(88) },
		"publisher domain":   func(a *Artifact) { a.PublisherAuthorization.OrganizationDomain = hash(88) },
		"publisher contract": func(a *Artifact) { a.PublisherAuthorization.ContractAddress = address(88) },
		"publisher chain":    func(a *Artifact) { a.PublisherAuthorization.ChainID = 8453 },
		"publisher selector": func(a *Artifact) { a.PublisherAuthorization.FunctionSelector = "0x12345678" },
		"approval calldata":  func(a *Artifact) { a.Approval.Calldata = "0x12345678" },
		"approval action":    func(a *Artifact) { a.Approval.CanonicalAction = json.RawMessage(`{}`) },
		"funding":            func(a *Artifact) { a.FundingEnabled = true },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			changed := cloneArtifact(t, base)
			mutate(&changed)
			if err := Verify(changed, deployment); err == nil {
				t.Fatal("mutated artifact accepted")
			}
		})
	}
}

func TestStrictDecodersRejectUnknownAndTrailingJSON(t *testing.T) {
	manifest, deployment := validFixture()
	raw, _ := json.Marshal(manifest)
	if _, err := DecodeManifest(append(raw, []byte(` {}`)...)); err == nil {
		t.Fatal("trailing manifest accepted")
	}
	unknown := []byte(strings.Replace(string(raw), `"fundingEnabled":false`, `"fundingEnabled":false,"unknown":1`, 1))
	if _, err := DecodeManifest(unknown); err == nil {
		t.Fatal("unknown manifest field accepted")
	}
	artifact, err := Compile(manifest, deployment)
	if err != nil {
		t.Fatal(err)
	}
	artifactRaw, _ := json.Marshal(artifact)
	if _, err := DecodeArtifact(append(artifactRaw, []byte(` null`)...)); err == nil {
		t.Fatal("trailing artifact accepted")
	}
	duplicate := []byte(strings.Replace(string(raw), `"releaseId":"`+manifest.ReleaseID+`"`,
		`"releaseId":"reviewed","releaseId":"`+manifest.ReleaseID+`"`, 1))
	if _, err := DecodeManifest(duplicate); err == nil {
		t.Fatal("duplicate manifest field accepted")
	}
	nestedDuplicate := []byte(strings.Replace(string(artifactRaw), `"expectedSigner":"`+manifest.DirectoryPublisher+`"`,
		`"expectedSigner":"`+address(88)+`","expectedSigner":"`+manifest.DirectoryPublisher+`"`, 1))
	if _, err := DecodeArtifact(nestedDuplicate); err == nil {
		t.Fatal("nested duplicate artifact field accepted")
	}
}

func validFixture() (Manifest, []byte) {
	directory := address(10)
	publisher := address(11)
	commit := strings.Repeat("a", 40)
	manifest := Manifest{SchemaVersion: SchemaVersion, ReleaseID: "ascp-directory-v1-test", Network: BaseSepoliaNetwork,
		ChainID: BaseSepoliaChainID, SourceDeployment: SourceDeployment{ReleaseID: "ascp-v4-base-sepolia-test", SourceCommit: commit},
		DirectoryContract: directory, OrganizationDomain: hash(1), DirectoryPublisher: publisher, DirectoryPublisherEpoch: 1,
		Asset: AssetBinding{Address: BaseSepoliaUSDC, Symbol: "USDC", Decimals: 6, RuntimeCodeHash: hash(9)}, VersionID: 1,
		PreviousVersion: 0, PreviousRoot: zeroHash(), ChangeClass: PayoutAuthorityChange, RequestedActivatesAt: 0,
		WorkflowID: hash(2), ProposerNonce: "7",
		Sellers: []Seller{{SellerID: hash(3), PayoutAddress: address(12), AckAuthority: address(13),
			QuoteSigningKey: address(14), KeyEpoch: 1, BaseURLOrigin: "https://seller.testnet.flowopsagent.xyz", Status: 1}},
		Resources: []Resource{
			{SellerID: hash(3), ResourceID: hash(5), Price: "1000", EscrowSupported: true, VerificationSpecHash: hash(6), DeclaredWorkTime: 300, VerificationBudgetSeconds: 120},
			{SellerID: hash(3), ResourceID: hash(7), Price: "2000", EscrowSupported: true, VerificationSpecHash: hash(8), DeclaredWorkTime: 600, VerificationBudgetSeconds: 180},
		}, FundingEnabled: false}
	ipfs, _ := CanonicalIPFSLocation(manifest)
	manifest.Locations = []string{"ar://" + base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x11}, 32)), ipfs}
	deployment := map[string]any{"releaseId": manifest.SourceDeployment.ReleaseID, "network": BaseSepoliaNetwork, "chainId": BaseSepoliaChainID,
		"sourceCommit": commit, "organizationDomain": manifest.OrganizationDomain,
		"authorities": map[string]any{"directoryPublisher": publisher}, "asset": manifest.Asset,
		"contracts": []map[string]any{{"name": "service_directory", "address": directory}}}
	raw, _ := json.Marshal(deployment)
	return manifest, raw
}

func cloneManifest(t *testing.T, value Manifest) Manifest {
	t.Helper()
	raw, _ := json.Marshal(value)
	var cloned Manifest
	if err := json.Unmarshal(raw, &cloned); err != nil {
		t.Fatal(err)
	}
	return cloned
}

func cloneArtifact(t *testing.T, value Artifact) Artifact {
	t.Helper()
	raw, _ := json.Marshal(value)
	var cloned Artifact
	if err := json.Unmarshal(raw, &cloned); err != nil {
		t.Fatal(err)
	}
	return cloned
}

func address(value int) string { return "0x" + strings.Repeat("0", 38) + fmt2(value) }
func hash(value int) string    { return "0x" + strings.Repeat("0", 62) + fmt2(value) }
func zeroHash() string         { return "0x" + strings.Repeat("0", 64) }
func fmt2(value int) string    { return string(rune('0'+value/10)) + string(rune('0'+value%10)) }
