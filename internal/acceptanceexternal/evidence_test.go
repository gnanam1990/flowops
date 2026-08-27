package acceptanceexternal

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/crypto"
)

type evidenceFixture struct {
	root    string
	now     time.Time
	profile Profile
	bundle  Bundle
	keys    []string
}

func TestVerifyAcceptsCompleteQuorumSignedBundle(t *testing.T) {
	fixture := newEvidenceFixture(t)
	if err := Verify(fixture.bundle, fixture.profile, fixture.root, fixture.now); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyRejectsEveryLoadBearingMutation(t *testing.T) {
	tests := map[string]func(*evidenceFixture){
		"profile mismatch": func(f *evidenceFixture) { f.bundle.ProfileID = "different-profile" },
		"stale completion": func(f *evidenceFixture) {
			f.bundle.CompletedAt = f.now.Add(-25 * time.Hour).Format(time.RFC3339)
		},
		"artifact digest": func(f *evidenceFixture) { f.bundle.Artifacts[0].SHA256 = strings.Repeat("0", 64) },
		"artifact contents": func(f *evidenceFixture) {
			if err := os.WriteFile(filepath.Join(f.root, f.bundle.Artifacts[0].Path), []byte("mutated"), 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"missing event export": func(f *evidenceFixture) { f.bundle.Artifacts[0].Kind = "operator-record" },
		"failed assertion":     func(f *evidenceFixture) { f.bundle.Criteria[0].Assertions[0].Passed = false },
		"unknown assertion evidence": func(f *evidenceFixture) {
			f.bundle.Criteria[0].Assertions[0].EvidenceRefs = []string{"missing"}
		},
		"same RPC host": func(f *evidenceFixture) {
			f.bundle.ProviderObservations[1].RPCURL = "https://sepolia.base.org/alternate"
		},
		"untrusted owner": func(f *evidenceFixture) {
			key, err := crypto.HexToECDSA(strings.Repeat("04", 32))
			if err != nil {
				t.Fatal(err)
			}
			f.bundle.Signatures[0].Owner = strings.ToLower(crypto.PubkeyToAddress(key.PublicKey).Hex())
		},
		"signed payload mutation": func(f *evidenceFixture) { f.bundle.RunID = "external-run-mutated" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := newEvidenceFixture(t)
			mutate(&fixture)
			if err := Verify(fixture.bundle, fixture.profile, fixture.root, fixture.now); err == nil {
				t.Fatal("mutated external acceptance evidence was accepted")
			}
		})
	}
}

func TestVerifyRejectsIncompleteCriterionAndSignatureQuorums(t *testing.T) {
	fixture := newEvidenceFixture(t)
	fixture.bundle.Criteria = fixture.bundle.Criteria[1:]
	fixture.bundle.Signatures = fixture.bundle.Signatures[:1]
	if err := Verify(fixture.bundle, fixture.profile, fixture.root, fixture.now); err == nil {
		t.Fatal("incomplete external acceptance quorums were accepted")
	}
}

func TestVerifyRejectsPinnedDeploymentMutation(t *testing.T) {
	fixture := newEvidenceFixture(t)
	if err := os.WriteFile(filepath.Join(fixture.root, fixture.profile.Deployment.Path), []byte("changed deployment"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Verify(fixture.bundle, fixture.profile, fixture.root, fixture.now); err == nil {
		t.Fatal("mutated pinned deployment was accepted")
	}
}

func TestLoadRejectsUnknownAndTrailingJSON(t *testing.T) {
	tests := map[string]string{
		"unknown field": `{"schemaVersion":1,"unknown":true}`,
		"trailing JSON": `{}` + "\n{}",
	}
	for name, contents := range tests {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "input.json")
			if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadBundle(path); err == nil {
				t.Fatal("malformed JSON was accepted")
			}
		})
	}
}

func TestProfileRequiresExactExternalCriterionInventory(t *testing.T) {
	fixture := newEvidenceFixture(t)
	fixture.profile.RequiredCriteria = fixture.profile.RequiredCriteria[1:]
	if err := fixture.profile.Validate(); err == nil {
		t.Fatal("incomplete external criterion inventory was accepted")
	}
}

func TestTemplateIsCompleteButCannotClaimSuccess(t *testing.T) {
	fixture := newEvidenceFixture(t)
	template, err := Template(fixture.profile, "acceptance-run-template", fixture.now)
	if err != nil {
		t.Fatal(err)
	}
	if len(template.Criteria) != 14 || len(template.Signatures) != 0 || len(template.Artifacts) != 0 || template.CompletedAt != "" {
		t.Fatalf("unexpected template shape: %+v", template)
	}
	for _, criterion := range template.Criteria {
		for _, assertion := range criterion.Assertions {
			if assertion.Passed || len(assertion.EvidenceRefs) != 0 {
				t.Fatal("template pre-claimed an external assertion")
			}
		}
	}
	if err := Verify(template, fixture.profile, fixture.root, fixture.now); err == nil {
		t.Fatal("incomplete template was accepted as evidence")
	}
}

func TestRepositoryProfileLoads(t *testing.T) {
	profile, err := LoadProfile(filepath.Join("..", "..", "deployments", "base-sepolia-ascp-external-acceptance-profile-v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	if profile.ChainID != 84532 || profile.Safe.Threshold != 2 || len(profile.RequiredCriteria) != 14 {
		t.Fatalf("unexpected repository profile: %+v", profile)
	}
}

func newEvidenceFixture(t *testing.T) evidenceFixture {
	t.Helper()
	root := t.TempDir()
	write := func(path, contents string) string {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, path), []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256([]byte(contents))
		return hex.EncodeToString(digest[:])
	}
	deployment := DigestFile{Path: "deployment.json", SHA256: write("deployment.json", `{"chainId":84532}`)}
	artifacts := []Artifact{
		{ID: "event-chain", Kind: "event-chain-export", Path: "event-chain.json", SHA256: write("event-chain.json", `{"head":"event"}`)},
		{ID: "run-manifest", Kind: "manifest", Path: "manifest.json", SHA256: write("manifest.json", `{"run":"manifest"}`)},
		{ID: "rpc-primary", Kind: "rpc-observation", Path: "rpc-primary.json", SHA256: write("rpc-primary.json", `{"provider":"primary"}`)},
		{ID: "rpc-secondary", Kind: "rpc-observation", Path: "rpc-secondary.json", SHA256: write("rpc-secondary.json", `{"provider":"secondary"}`)},
		{ID: "operator-proof", Kind: "operator-record", Path: "operator.json", SHA256: write("operator.json", `{"result":"pass"}`)},
	}
	keyHexes := []string{strings.Repeat("01", 32), strings.Repeat("02", 32), strings.Repeat("03", 32)}
	owners := make([]string, 0, len(keyHexes))
	for _, keyHex := range keyHexes {
		key, err := crypto.HexToECDSA(keyHex)
		if err != nil {
			t.Fatal(err)
		}
		owners = append(owners, strings.ToLower(crypto.PubkeyToAddress(key.PublicKey).Hex()))
	}
	criteriaIDs := make([]string, 0, len(requiredAssertions))
	criteria := make([]CriterionResult, 0, len(requiredAssertions))
	for id, names := range RequiredAssertions() {
		criteriaIDs = append(criteriaIDs, id)
		assertions := make([]Assertion, 0, len(names))
		for _, name := range names {
			assertions = append(assertions, Assertion{Name: name, Passed: true, EvidenceRefs: []string{"operator-proof"}})
		}
		criteria = append(criteria, CriterionResult{ID: id, Assertions: assertions})
	}
	sort.Strings(criteriaIDs)
	sort.Slice(criteria, func(i, j int) bool { return criteria[i].ID < criteria[j].ID })
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	profile := Profile{
		SchemaVersion: SchemaVersion, ProfileID: "base-sepolia-ascp-external-v1", Network: "base-sepolia", ChainID: 84532,
		SourceCommit: strings.Repeat("a", 40), Deployment: deployment, RequiredCriteria: criteriaIDs,
		Safe:             SafeTrust{Address: "0xf6ac2af2c441ff8886b250233a7adfc206ab0b57", Owners: owners, Threshold: 2},
		MinimumProviders: 2, MaximumEvidenceAgeS: 24 * 60 * 60,
	}
	bundle := Bundle{
		SchemaVersion: SchemaVersion, RunID: "external-run-20260827", ProfileID: profile.ProfileID,
		Network: profile.Network, ChainID: profile.ChainID, SourceCommit: profile.SourceCommit, Deployment: deployment,
		StartedAt: now.Add(-2 * time.Hour).Format(time.RFC3339), CompletedAt: now.Add(-time.Minute).Format(time.RFC3339),
		Artifacts: artifacts, Criteria: criteria,
		ProviderObservations: []ProviderObservation{
			{Provider: "base-public", RPCURL: "https://sepolia.base.org", ObservedAt: now.Add(-10 * time.Minute).Format(time.RFC3339), HeadNumber: 100, HeadHash: "0x" + strings.Repeat("1", 64), EvidenceRef: "rpc-primary"},
			{Provider: "publicnode", RPCURL: "https://base-sepolia-rpc.publicnode.com", ObservedAt: now.Add(-9 * time.Minute).Format(time.RFC3339), HeadNumber: 100, HeadHash: "0x" + strings.Repeat("1", 64), EvidenceRef: "rpc-secondary"},
		},
	}
	message, err := SigningMessage(bundle)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		key, err := crypto.HexToECDSA(keyHexes[i])
		if err != nil {
			t.Fatal(err)
		}
		signature, err := crypto.Sign(accounts.TextHash([]byte(message)), key)
		if err != nil {
			t.Fatal(err)
		}
		signature[64] += 27
		bundle.Signatures = append(bundle.Signatures, OwnerSignature{Owner: owners[i], SignatureHex: "0x" + hex.EncodeToString(signature)})
	}
	return evidenceFixture{root: root, now: now, profile: profile, bundle: bundle, keys: keyHexes}
}

func cloneBundle(t *testing.T, input Bundle) Bundle {
	t.Helper()
	encoded, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	var output Bundle
	if err := json.Unmarshal(encoded, &output); err != nil {
		t.Fatal(err)
	}
	return output
}
