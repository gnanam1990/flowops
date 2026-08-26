package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/gnanam1990/flowops/pkg/directoryrelease"
)

var relayCLINow = time.Date(2026, 8, 26, 16, 0, 0, 0, time.UTC)

type relayCLIFetcher struct{ body []byte }

func (f relayCLIFetcher) Fetch(_ context.Context, rawURL string, _ int64) (directoryrelease.FetchedBlob, error) {
	return directoryrelease.FetchedBlob{URL: rawURL, FetchedAt: relayCLINow.Add(-time.Second), StatusCode: http.StatusOK,
		ContentType: "application/json", ContentLength: int64(len(f.body)), Body: append([]byte(nil), f.body...)}, nil
}

type relayCLILiveObserver struct{}

func (relayCLILiveObserver) Observe(_ context.Context, target directoryrelease.LivePresignTarget) (directoryrelease.LivePresignEvidence, error) {
	return directoryrelease.LivePresignEvidence{SchemaVersion: 1, Observations: []directoryrelease.LiveProviderObservation{
		{ProviderID: relayCLIHash(90), BlockNumber: 100, BlockHash: relayCLIHash(92), BlockTimestamp: uint64(relayCLINow.Unix()),
			ChainID: target.ChainID, ContractAddress: target.ContractAddress, OrganizationDomain: target.OrganizationDomain,
			DirectoryPublisher: target.ExpectedPublisher, PublisherEpoch: target.PublisherEpoch,
			CurrentVersion: target.PreviousVersion, CurrentRoot: target.PreviousRoot, LatestProposalHash: relayCLIZeroHash()},
		{ProviderID: relayCLIHash(91), BlockNumber: 101, BlockHash: relayCLIHash(93), BlockTimestamp: uint64(relayCLINow.Add(time.Second).Unix()),
			ChainID: target.ChainID, ContractAddress: target.ContractAddress, OrganizationDomain: target.OrganizationDomain,
			DirectoryPublisher: target.ExpectedPublisher, PublisherEpoch: target.PublisherEpoch,
			CurrentVersion: target.PreviousVersion, CurrentRoot: target.PreviousRoot, LatestProposalHash: relayCLIZeroHash()},
	}}, nil
}

type relayCLISimulator struct{ err error }

func (s relayCLISimulator) Simulate(_ context.Context, target directoryrelease.RelaySimulationTarget) ([]directoryrelease.RelayProviderObservation, error) {
	if s.err != nil {
		return nil, s.err
	}
	values := make([]directoryrelease.RelayProviderObservation, 2)
	for index, prior := range target.PreviousObservations {
		values[index] = directoryrelease.RelayProviderObservation{ProviderID: prior.ProviderID, BlockNumber: prior.BlockNumber + 1,
			BlockHash: relayCLIHash(94 + index), BlockTimestamp: uint64(relayCLINow.Add(time.Duration(index) * time.Second).Unix()),
			ChainID: target.ChainID, ContractAddress: target.ContractAddress, OrganizationDomain: target.OrganizationDomain,
			DirectoryPublisher: target.ExpectedPublisher, PublisherEpoch: target.PublisherEpoch,
			CurrentVersion: target.PreviousVersion, CurrentRoot: target.PreviousRoot, LatestProposalHash: relayCLIZeroHash(),
			AuthorizationDigest: target.AuthorizationDigest, ProposalHash: target.ProposalHash,
			WorkflowPayloadHash: target.WorkflowPayloadHash, ContractSemanticSimulation: true}
	}
	return values, nil
}

func TestRelayCLIProducesNonDisclosingEvidenceAndVerifies(t *testing.T) {
	paths, signed := relayCLIFixture(t)
	var output bytes.Buffer
	if err := run(context.Background(), []string{"simulate", paths.presign, paths.artifact, paths.deployment, paths.signature, paths.request},
		&output, relayCLISimulator{}, func() time.Time { return relayCLINow }); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), signed.Signature) || strings.Contains(output.String(), `"signature":`) ||
		strings.Contains(output.String(), `"calldata":`) || !strings.Contains(output.String(), `"broadcastAuthorized": false`) ||
		!strings.Contains(output.String(), `"fundingEnabled": false`) {
		t.Fatalf("unsafe CLI output=%s", output.String())
	}
	if err := os.WriteFile(paths.evidence, output.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	var verified bytes.Buffer
	if err := run(context.Background(), []string{"verify", paths.evidence, paths.presign, paths.artifact, paths.deployment, paths.signature, paths.request},
		&verified, nil, func() time.Time { return relayCLINow.Add(time.Second) }); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(verified.String(), "signature=private calldata=withheld broadcastAuthorized=false fundingEnabled=false") ||
		strings.Contains(verified.String(), signed.Signature) {
		t.Fatalf("verify output=%s", verified.String())
	}
}

func TestRelayCLIRejectsInsecureSignatureFilesOutageAndBroadcastModes(t *testing.T) {
	paths, _ := relayCLIFixture(t)
	if err := os.Chmod(paths.signature, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := run(context.Background(), []string{"simulate", paths.presign, paths.artifact, paths.deployment, paths.signature, paths.request},
		&bytes.Buffer{}, relayCLISimulator{}, func() time.Time { return relayCLINow }); err == nil {
		t.Fatal("world-readable signature file accepted")
	}
	if err := os.Chmod(paths.signature, 0o600); err != nil {
		t.Fatal(err)
	}
	symlink := filepath.Join(filepath.Dir(paths.signature), "signature-link.json")
	if err := os.Symlink(paths.signature, symlink); err != nil {
		t.Fatal(err)
	}
	if _, err := readPrivate(symlink, maximumSignatureBytes); err == nil {
		t.Fatal("signature symlink accepted")
	}
	if _, err := readPrivate("relative-signature.json", maximumSignatureBytes); err == nil {
		t.Fatal("relative signature path accepted")
	}
	if err := run(context.Background(), []string{"simulate", paths.presign, paths.artifact, paths.deployment, paths.signature, paths.request},
		&bytes.Buffer{}, relayCLISimulator{err: errors.New("offline")}, func() time.Time { return relayCLINow }); err == nil {
		t.Fatal("simulation outage accepted")
	}
	for _, mode := range []string{"submit", "broadcast", "sign"} {
		if err := run(context.Background(), []string{mode}, &bytes.Buffer{}, nil, func() time.Time { return relayCLINow }); err == nil {
			t.Fatalf("unsafe mode %s exists", mode)
		}
	}
}

type relayCLIPaths struct{ presign, artifact, deployment, signature, request, evidence string }

func relayCLIFixture(t *testing.T) (relayCLIPaths, directoryrelease.PublisherSignature) {
	t.Helper()
	key, err := crypto.HexToECDSA(strings.Repeat("0", 63) + "1")
	if err != nil {
		t.Fatal(err)
	}
	directory := relayCLIAddress(10)
	publisher := strings.ToLower(crypto.PubkeyToAddress(key.PublicKey).Hex())
	commit := strings.Repeat("a", 40)
	manifest := directoryrelease.Manifest{SchemaVersion: 1, ReleaseID: "directory-relay-command-test", Network: "base-sepolia", ChainID: 84532,
		SourceDeployment:  directoryrelease.SourceDeployment{ReleaseID: "ascp-source-test", SourceCommit: commit},
		DirectoryContract: directory, OrganizationDomain: relayCLIHash(1), DirectoryPublisher: publisher, DirectoryPublisherEpoch: 1,
		Asset:     directoryrelease.AssetBinding{Address: directoryrelease.BaseSepoliaUSDC, Symbol: "USDC", Decimals: 6, RuntimeCodeHash: relayCLIHash(9)},
		VersionID: 1, PreviousVersion: 0, PreviousRoot: relayCLIZeroHash(), ChangeClass: 2,
		WorkflowID: relayCLIHash(2), ProposerNonce: "7",
		Sellers: []directoryrelease.Seller{{SellerID: relayCLIHash(3), PayoutAddress: relayCLIAddress(12), AckAuthority: relayCLIAddress(13),
			QuoteSigningKey: relayCLIAddress(14), KeyEpoch: 1, BaseURLOrigin: "https://seller.testnet.flowopsagent.xyz", Status: 1}},
		Resources: []directoryrelease.Resource{{SellerID: relayCLIHash(3), ResourceID: relayCLIHash(5), Price: "1000", EscrowSupported: true,
			VerificationSpecHash: relayCLIHash(6), DeclaredWorkTime: 300, VerificationBudgetSeconds: 120}}}
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
	gateways := directoryrelease.GatewayConfig{SchemaVersion: 1, IPFSGatewayOrigin: "https://ipfs.example.com", ArweaveGatewayOrigin: "https://arweave.example.com"}
	presign, err := directoryrelease.BuildPresign(context.Background(), artifact, deployment, gateways,
		directoryrelease.PresignRequest{SchemaVersion: 1, AdminNonce: "41"}, relayCLIFetcher{body: artifact.CanonicalBlob},
		relayCLILiveObserver{}, func() time.Time { return relayCLINow })
	if err != nil {
		t.Fatal(err)
	}
	signature, err := crypto.Sign(common.HexToHash(presign.Digest).Bytes(), key)
	if err != nil {
		t.Fatal(err)
	}
	signature[64] += 27
	signed := directoryrelease.PublisherSignature{SchemaVersion: 1, Digest: presign.Digest, Signature: "0x" + hex.EncodeToString(signature)}
	request := directoryrelease.RelaySimulationRequest{SchemaVersion: 1, RelayerAddress: relayCLIAddress(20)}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	paths := relayCLIPaths{presign: filepath.Join(root, "presign.json"), artifact: filepath.Join(root, "artifact.json"),
		deployment: filepath.Join(root, "deployment.json"), signature: filepath.Join(root, "signature.json"),
		request: filepath.Join(root, "request.json"), evidence: filepath.Join(root, "evidence.json")}
	relayCLIWrite(t, paths.presign, presign, 0o600)
	relayCLIWrite(t, paths.artifact, artifact, 0o600)
	if err := os.WriteFile(paths.deployment, deployment, 0o600); err != nil {
		t.Fatal(err)
	}
	relayCLIWrite(t, paths.signature, signed, 0o600)
	relayCLIWrite(t, paths.request, request, 0o600)
	return paths, signed
}

func relayCLIWrite(t *testing.T, path string, value any, mode os.FileMode) {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, mode); err != nil {
		t.Fatal(err)
	}
}

func relayCLIAddress(value int) string { return "0x" + strings.Repeat("0", 38) + relayCLITwo(value) }
func relayCLIHash(value int) string    { return "0x" + strings.Repeat("0", 62) + relayCLITwo(value) }
func relayCLIZeroHash() string         { return "0x" + strings.Repeat("0", 64) }
func relayCLITwo(value int) string     { return string(rune('0'+value/10)) + string(rune('0'+value%10)) }
