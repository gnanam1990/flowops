package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gnanam1990/flowops/pkg/directoryrelease"
)

func TestCommandCompilesAndVerifiesWithoutSigning(t *testing.T) {
	manifest, deployment := commandFixture()
	directory := t.TempDir()
	manifestPath := filepath.Join(directory, "manifest.json")
	deploymentPath := filepath.Join(directory, "deployment.json")
	artifactPath := filepath.Join(directory, "artifact.json")
	writeJSON(t, manifestPath, manifest)
	if err := os.WriteFile(deploymentPath, deployment, 0o600); err != nil {
		t.Fatal(err)
	}
	compiled, err := captureOutput(t, func() error { return run([]string{"compile", manifestPath, deploymentPath}) })
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(compiled, "signature") || !strings.Contains(compiled, `"fundingEnabled": false`) {
		t.Fatalf("unsafe output=%s", compiled)
	}
	if err := os.WriteFile(artifactPath, []byte(compiled), 0o600); err != nil {
		t.Fatal(err)
	}
	verified, err := captureOutput(t, func() error { return run([]string{"verify", artifactPath, deploymentPath}) })
	if err != nil || !strings.Contains(verified, "fundingEnabled=false") {
		t.Fatalf("verify=%q err=%v", verified, err)
	}

	var artifact directoryrelease.Artifact
	if err := json.Unmarshal([]byte(compiled), &artifact); err != nil {
		t.Fatal(err)
	}
	artifact.Approval.Calldata = "0x12345678"
	writeJSON(t, artifactPath, artifact)
	if _, err := captureOutput(t, func() error { return run([]string{"verify", artifactPath, deploymentPath}) }); err == nil {
		t.Fatal("command accepted substituted approval calldata")
	}
}

func TestCommandRejectsBadModeAndMissingFiles(t *testing.T) {
	if err := run([]string{"sign", "a", "b"}); err == nil {
		t.Fatal("sign mode unexpectedly exists")
	}
	if err := run([]string{"compile", "/missing", "/missing"}); err == nil {
		t.Fatal("missing input accepted")
	}
}

func commandFixture() (directoryrelease.Manifest, []byte) {
	addr := func(value int) string { return "0x" + strings.Repeat("0", 38) + two(value) }
	digest := func(value int) string { return "0x" + strings.Repeat("0", 62) + two(value) }
	manifest := directoryrelease.Manifest{SchemaVersion: 1, ReleaseID: "directory-command-test", Network: "base-sepolia", ChainID: 84532,
		SourceDeployment:  directoryrelease.SourceDeployment{ReleaseID: "ascp-source-test", SourceCommit: strings.Repeat("a", 40)},
		DirectoryContract: addr(10), OrganizationDomain: digest(1), DirectoryPublisher: addr(11), DirectoryPublisherEpoch: 1,
		Asset:     directoryrelease.AssetBinding{Address: directoryrelease.BaseSepoliaUSDC, Symbol: "USDC", Decimals: 6, RuntimeCodeHash: digest(9)},
		VersionID: 1, PreviousVersion: 0, PreviousRoot: "0x" + strings.Repeat("0", 64), ChangeClass: 2,
		WorkflowID: digest(2), ProposerNonce: "9",
		Sellers:   []directoryrelease.Seller{{SellerID: digest(3), PayoutAddress: addr(12), AckAuthority: addr(13), QuoteSigningKey: addr(14), KeyEpoch: 1, BaseURLOrigin: "https://seller.testnet.flowopsagent.xyz", Status: 1}},
		Resources: []directoryrelease.Resource{{SellerID: digest(3), ResourceID: digest(5), Price: "1000", EscrowSupported: true, VerificationSpecHash: digest(6), DeclaredWorkTime: 300, VerificationBudgetSeconds: 120}}}
	ipfs, _ := directoryrelease.CanonicalIPFSLocation(manifest)
	manifest.Locations = []string{"ar://" + base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x22}, 32)), ipfs}
	deployment := map[string]any{"releaseId": manifest.SourceDeployment.ReleaseID, "network": manifest.Network, "chainId": manifest.ChainID,
		"sourceCommit": manifest.SourceDeployment.SourceCommit, "organizationDomain": manifest.OrganizationDomain,
		"authorities": map[string]any{"directoryPublisher": manifest.DirectoryPublisher}, "asset": manifest.Asset,
		"contracts": []map[string]any{{"name": "service_directory", "address": manifest.DirectoryContract}}}
	raw, _ := json.Marshal(deployment)
	return manifest, raw
}

func writeJSON(t *testing.T, path string, value any) {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func captureOutput(t *testing.T, action func() error) (string, error) {
	t.Helper()
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	original := os.Stdout
	os.Stdout = write
	done := make(chan []byte, 1)
	go func() { raw, _ := io.ReadAll(read); done <- raw }()
	actionErr := action()
	os.Stdout = original
	_ = write.Close()
	output := <-done
	_ = read.Close()
	return string(output), actionErr
}

func two(value int) string { return string(rune('0'+value/10)) + string(rune('0'+value%10)) }
