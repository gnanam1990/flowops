package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gnanam1990/flowops/pkg/directoryrelease"
)

var cliNow = time.Date(2026, 8, 26, 14, 0, 0, 0, time.UTC)

type cliFetcher struct {
	body []byte
	err  error
}

type cliLiveObserver struct{}

func (cliLiveObserver) Observe(_ context.Context, target directoryrelease.LivePresignTarget) (directoryrelease.LivePresignEvidence, error) {
	return directoryrelease.LivePresignEvidence{SchemaVersion: 1, Observations: []directoryrelease.LiveProviderObservation{
		{ProviderID: cliHash(90), BlockNumber: 100, BlockHash: cliHash(92), BlockTimestamp: uint64(cliNow.Unix()), ChainID: target.ChainID,
			ContractAddress: target.ContractAddress, OrganizationDomain: target.OrganizationDomain, DirectoryPublisher: target.ExpectedPublisher,
			PublisherEpoch: target.PublisherEpoch, CurrentVersion: target.PreviousVersion, CurrentRoot: target.PreviousRoot, LatestProposalHash: "0x" + strings.Repeat("0", 64)},
		{ProviderID: cliHash(91), BlockNumber: 101, BlockHash: cliHash(93), BlockTimestamp: uint64(cliNow.Add(time.Second).Unix()), ChainID: target.ChainID,
			ContractAddress: target.ContractAddress, OrganizationDomain: target.OrganizationDomain, DirectoryPublisher: target.ExpectedPublisher,
			PublisherEpoch: target.PublisherEpoch, CurrentVersion: target.PreviousVersion, CurrentRoot: target.PreviousRoot, LatestProposalHash: "0x" + strings.Repeat("0", 64)},
	}}, nil
}

func (f cliFetcher) Fetch(_ context.Context, rawURL string, _ int64) (directoryrelease.FetchedBlob, error) {
	if f.err != nil {
		return directoryrelease.FetchedBlob{}, f.err
	}
	return directoryrelease.FetchedBlob{URL: rawURL, FetchedAt: cliNow.Add(-time.Second), StatusCode: http.StatusOK,
		ContentType: "application/json", ContentLength: int64(len(f.body)), Body: append([]byte(nil), f.body...)}, nil
}

func TestCLIProducesRemoteEvidenceAndUnsignedPresignThenVerifies(t *testing.T) {
	paths, artifact := cliFixture(t)
	fetcher := cliFetcher{body: artifact.CanonicalBlob}
	var remote bytes.Buffer
	if err := run(context.Background(), []string{"verify-remote", paths.artifact, paths.deployment, paths.gateways}, &remote, fetcher, nil, func() time.Time { return cliNow }); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(remote.String(), `"copies": [`) || !strings.Contains(remote.String(), artifact.BlobContentHash) {
		t.Fatalf("remote output=%s", remote.String())
	}
	var output bytes.Buffer
	if err := run(context.Background(), []string{"prepare", paths.artifact, paths.deployment, paths.gateways, paths.request}, &output, fetcher, cliLiveObserver{}, func() time.Time { return cliNow }); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), `"signature":`) || !strings.Contains(output.String(), `"broadcastAuthorized": false`) ||
		!strings.Contains(output.String(), `"fundingEnabled": false`) {
		t.Fatalf("unsafe output=%s", output.String())
	}
	if err := os.WriteFile(paths.presign, output.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	var verified bytes.Buffer
	if err := run(context.Background(), []string{"verify", paths.presign, paths.artifact, paths.deployment}, &verified, nil, nil, func() time.Time { return cliNow }); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(verified.String(), "signature=absent broadcastAuthorized=false fundingEnabled=false") {
		t.Fatalf("verify output=%s", verified.String())
	}
}

func TestCLIRejectsRemoteFailureMutationExpiryAndSignMode(t *testing.T) {
	paths, artifact := cliFixture(t)
	fetcher := cliFetcher{body: artifact.CanonicalBlob}
	var output bytes.Buffer
	if err := run(context.Background(), []string{"prepare", paths.artifact, paths.deployment, paths.gateways, paths.request}, &output, fetcher, cliLiveObserver{}, func() time.Time { return cliNow }); err != nil {
		t.Fatal(err)
	}
	var value directoryrelease.PresignPackage
	if err := json.Unmarshal(output.Bytes(), &value); err != nil {
		t.Fatal(err)
	}
	value.Digest = strings.Repeat("0", 66)
	writeCLIJSON(t, paths.presign, value)
	if err := run(context.Background(), []string{"verify", paths.presign, paths.artifact, paths.deployment}, &bytes.Buffer{}, nil, nil, func() time.Time { return cliNow }); err == nil {
		t.Fatal("mutated package accepted")
	}
	if err := run(context.Background(), []string{"prepare", paths.artifact, paths.deployment, paths.gateways, paths.request}, &bytes.Buffer{}, cliFetcher{err: errors.New("offline")}, cliLiveObserver{}, func() time.Time { return cliNow }); err == nil {
		t.Fatal("remote failure accepted")
	}
	if err := run(context.Background(), []string{"sign", paths.presign}, &bytes.Buffer{}, nil, nil, func() time.Time { return cliNow }); err == nil {
		t.Fatal("sign mode unexpectedly exists")
	}
	if err := os.WriteFile(paths.presign, output.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	expired := cliNow.Add(10 * time.Minute)
	if err := run(context.Background(), []string{"verify", paths.presign, paths.artifact, paths.deployment}, &bytes.Buffer{}, nil, nil, func() time.Time { return expired }); err == nil {
		t.Fatal("expired package accepted")
	}
}

type cliPaths struct{ artifact, deployment, gateways, request, presign string }

func cliFixture(t *testing.T) (cliPaths, directoryrelease.Artifact) {
	t.Helper()
	directory := cliAddress(10)
	publisher := cliAddress(11)
	commit := strings.Repeat("a", 40)
	manifest := directoryrelease.Manifest{SchemaVersion: 1, ReleaseID: "directory-presign-command-test", Network: "base-sepolia", ChainID: 84532,
		SourceDeployment:  directoryrelease.SourceDeployment{ReleaseID: "ascp-source-test", SourceCommit: commit},
		DirectoryContract: directory, OrganizationDomain: cliHash(1), DirectoryPublisher: publisher, DirectoryPublisherEpoch: 1,
		Asset:     directoryrelease.AssetBinding{Address: directoryrelease.BaseSepoliaUSDC, Symbol: "USDC", Decimals: 6, RuntimeCodeHash: cliHash(9)},
		VersionID: 1, PreviousVersion: 0, PreviousRoot: "0x" + strings.Repeat("0", 64), ChangeClass: 2,
		WorkflowID: cliHash(2), ProposerNonce: "7",
		Sellers: []directoryrelease.Seller{{SellerID: cliHash(3), PayoutAddress: cliAddress(12), AckAuthority: cliAddress(13),
			QuoteSigningKey: cliAddress(14), KeyEpoch: 1, BaseURLOrigin: "https://seller.testnet.flowopsagent.xyz", Status: 1}},
		Resources: []directoryrelease.Resource{{SellerID: cliHash(3), ResourceID: cliHash(5), Price: "1000", EscrowSupported: true,
			VerificationSpecHash: cliHash(6), DeclaredWorkTime: 300, VerificationBudgetSeconds: 120}}}
	ipfs, err := directoryrelease.CanonicalIPFSLocation(manifest)
	if err != nil {
		t.Fatal(err)
	}
	manifest.Locations = []string{"ar://" + base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x22}, 32)), ipfs}
	deploymentValue := map[string]any{"releaseId": manifest.SourceDeployment.ReleaseID, "network": manifest.Network, "chainId": manifest.ChainID,
		"sourceCommit": commit, "organizationDomain": manifest.OrganizationDomain,
		"authorities": map[string]any{"directoryPublisher": publisher}, "asset": manifest.Asset,
		"contracts": []map[string]any{{"name": "service_directory", "address": directory}}}
	deployment, _ := json.Marshal(deploymentValue)
	artifact, err := directoryrelease.Compile(manifest, deployment)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	paths := cliPaths{artifact: filepath.Join(root, "artifact.json"), deployment: filepath.Join(root, "deployment.json"),
		gateways: filepath.Join(root, "gateways.json"), request: filepath.Join(root, "request.json"), presign: filepath.Join(root, "presign.json")}
	writeCLIJSON(t, paths.artifact, artifact)
	if err := os.WriteFile(paths.deployment, deployment, 0o600); err != nil {
		t.Fatal(err)
	}
	writeCLIJSON(t, paths.gateways, directoryrelease.GatewayConfig{SchemaVersion: 1,
		IPFSGatewayOrigin: "https://ipfs.example.com", ArweaveGatewayOrigin: "https://arweave.example.com"})
	writeCLIJSON(t, paths.request, directoryrelease.PresignRequest{SchemaVersion: 1, AdminNonce: "41"})
	return paths, artifact
}

func writeCLIJSON(t *testing.T, path string, value any) {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func cliAddress(value int) string { return "0x" + strings.Repeat("0", 38) + cliTwo(value) }
func cliHash(value int) string    { return "0x" + strings.Repeat("0", 62) + cliTwo(value) }
func cliTwo(value int) string     { return string(rune('0'+value/10)) + string(rune('0'+value%10)) }
