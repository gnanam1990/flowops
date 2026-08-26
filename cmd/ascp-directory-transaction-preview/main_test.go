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

var transactionCLINow = time.Date(2026, 8, 26, 18, 0, 0, 0, time.UTC)

type transactionCLIFetcher struct{ body []byte }

func (f transactionCLIFetcher) Fetch(_ context.Context, rawURL string, _ int64) (directoryrelease.FetchedBlob, error) {
	return directoryrelease.FetchedBlob{URL: rawURL, FetchedAt: transactionCLINow.Add(-time.Second), StatusCode: http.StatusOK,
		ContentType: "application/json", ContentLength: int64(len(f.body)), Body: append([]byte(nil), f.body...)}, nil
}

type transactionCLILiveObserver struct{}

func (transactionCLILiveObserver) Observe(_ context.Context, target directoryrelease.LivePresignTarget) (directoryrelease.LivePresignEvidence, error) {
	return directoryrelease.LivePresignEvidence{SchemaVersion: 1, Observations: []directoryrelease.LiveProviderObservation{
		{ProviderID: transactionCLIHash(90), BlockNumber: 100, BlockHash: transactionCLIHash(92), BlockTimestamp: uint64(transactionCLINow.Unix()),
			ChainID: target.ChainID, ContractAddress: target.ContractAddress, OrganizationDomain: target.OrganizationDomain,
			DirectoryPublisher: target.ExpectedPublisher, PublisherEpoch: target.PublisherEpoch,
			CurrentVersion: target.PreviousVersion, CurrentRoot: target.PreviousRoot, LatestProposalHash: transactionCLIZeroHash()},
		{ProviderID: transactionCLIHash(91), BlockNumber: 101, BlockHash: transactionCLIHash(93), BlockTimestamp: uint64(transactionCLINow.Add(time.Second).Unix()),
			ChainID: target.ChainID, ContractAddress: target.ContractAddress, OrganizationDomain: target.OrganizationDomain,
			DirectoryPublisher: target.ExpectedPublisher, PublisherEpoch: target.PublisherEpoch,
			CurrentVersion: target.PreviousVersion, CurrentRoot: target.PreviousRoot, LatestProposalHash: transactionCLIZeroHash()},
	}}, nil
}

type transactionCLIRelaySimulator struct{}

func (transactionCLIRelaySimulator) Simulate(_ context.Context, target directoryrelease.RelaySimulationTarget) ([]directoryrelease.RelayProviderObservation, error) {
	values := make([]directoryrelease.RelayProviderObservation, 2)
	for index, prior := range target.PreviousObservations {
		values[index] = directoryrelease.RelayProviderObservation{ProviderID: prior.ProviderID, BlockNumber: prior.BlockNumber + 2,
			BlockHash: transactionCLIHash(94 + index), BlockTimestamp: uint64(transactionCLINow.Add(time.Duration(index) * time.Second).Unix()),
			ChainID: target.ChainID, ContractAddress: target.ContractAddress, OrganizationDomain: target.OrganizationDomain,
			DirectoryPublisher: target.ExpectedPublisher, PublisherEpoch: target.PublisherEpoch,
			CurrentVersion: target.PreviousVersion, CurrentRoot: target.PreviousRoot, LatestProposalHash: transactionCLIZeroHash(),
			AuthorizationDigest: target.AuthorizationDigest, ProposalHash: target.ProposalHash,
			WorkflowPayloadHash: target.WorkflowPayloadHash, ContractSemanticSimulation: true}
	}
	return values, nil
}

type transactionCLIObserver struct{ err error }

func (o transactionCLIObserver) ObserveTransaction(_ context.Context, target directoryrelease.RelayTransactionTarget) ([]directoryrelease.RelayTransactionProviderObservation, error) {
	if o.err != nil {
		return nil, o.err
	}
	values := make([]directoryrelease.RelayTransactionProviderObservation, 2)
	for index, prior := range target.PreviousObservations {
		values[index] = directoryrelease.RelayTransactionProviderObservation{ProviderID: prior.ProviderID,
			BlockNumber: prior.BlockNumber + 1, BlockHash: transactionCLIHash(80 + index),
			BlockTimestamp: uint64(transactionCLINow.Add(time.Duration(index) * time.Second).Unix()),
			ChainID:        target.ChainID, RelayerAddress: target.RelayerAddress, PendingNonce: target.ExpectedNonce,
			BaseFeePerGasWei: "100000000", MetadataOnly: true, CalldataDisclosed: false}
	}
	return values, nil
}

func TestTransactionPreviewCLIWritesPrivateArtifactAndPublicNonDisclosingPreview(t *testing.T) {
	paths, signed := transactionCLIFixture(t)
	var output bytes.Buffer
	if err := run(context.Background(), []string{"prepare", paths.relay, paths.presign, paths.artifact, paths.deployment,
		paths.signature, paths.relayRequest, paths.transactionRequest, paths.private}, &output, transactionCLIObserver{},
		func() time.Time { return transactionCLINow }); err != nil {
		t.Fatal(err)
	}
	privateRaw, err := os.ReadFile(paths.private)
	if err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(paths.private)
	if info.Mode().Perm() != 0o600 || !bytes.Contains(privateRaw, []byte(`"data": "0x`)) ||
		bytes.Contains(output.Bytes(), []byte(signed.Signature)) || bytes.Contains(output.Bytes(), []byte(`"data"`)) ||
		!bytes.Contains(output.Bytes(), []byte(`"broadcastAuthorized": false`)) ||
		!bytes.Contains(output.Bytes(), []byte(`"fundingEnabled": false`)) {
		t.Fatalf("mode=%o public=%s private=%s", info.Mode().Perm(), output.Bytes(), privateRaw)
	}
	if err := os.WriteFile(paths.preview, output.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	var verified bytes.Buffer
	if err := run(context.Background(), []string{"verify", paths.preview, paths.private, paths.relay, paths.presign,
		paths.artifact, paths.deployment, paths.signature, paths.relayRequest, paths.transactionRequest}, &verified, nil,
		func() time.Time { return transactionCLINow.Add(time.Second) }); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(verified.String(), "privateArtifact=required signingRequired=true broadcastAuthorized=false fundingEnabled=false") ||
		strings.Contains(verified.String(), signed.Signature) {
		t.Fatalf("verify output=%s", verified.String())
	}
}

func TestTransactionPreviewCLIRejectsOverwriteSymlinkOutageAndUnsafeModes(t *testing.T) {
	paths, _ := transactionCLIFixture(t)
	if err := os.WriteFile(paths.private, []byte("occupied"), 0o600); err != nil {
		t.Fatal(err)
	}
	args := []string{"prepare", paths.relay, paths.presign, paths.artifact, paths.deployment,
		paths.signature, paths.relayRequest, paths.transactionRequest, paths.private}
	if err := run(context.Background(), args, &bytes.Buffer{}, transactionCLIObserver{}, func() time.Time { return transactionCLINow }); err == nil {
		t.Fatal("existing private output overwritten")
	}
	if err := os.Remove(paths.private); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(filepath.Dir(paths.private), "target.json")
	if err := os.WriteFile(target, []byte("target"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, paths.private); err != nil {
		t.Fatal(err)
	}
	if err := run(context.Background(), args, &bytes.Buffer{}, transactionCLIObserver{}, func() time.Time { return transactionCLINow }); err == nil {
		t.Fatal("symlink private output accepted")
	}
	if err := os.Remove(paths.private); err != nil {
		t.Fatal(err)
	}
	if err := run(context.Background(), args, &bytes.Buffer{}, transactionCLIObserver{err: errors.New("offline")},
		func() time.Time { return transactionCLINow }); err == nil {
		t.Fatal("RPC outage accepted")
	}
	if err := os.Chmod(paths.signature, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := run(context.Background(), args, &bytes.Buffer{}, transactionCLIObserver{}, func() time.Time { return transactionCLINow }); err == nil {
		t.Fatal("public signature file accepted")
	}
	for _, mode := range []string{"sign", "submit", "broadcast", "fund"} {
		if err := run(context.Background(), []string{mode}, &bytes.Buffer{}, nil, func() time.Time { return transactionCLINow }); err == nil {
			t.Fatalf("unsafe mode %s exists", mode)
		}
	}
}

type transactionCLIPaths struct {
	relay, presign, artifact, deployment, signature, relayRequest, transactionRequest, private, preview string
}

func transactionCLIFixture(t *testing.T) (transactionCLIPaths, directoryrelease.PublisherSignature) {
	t.Helper()
	key, err := crypto.HexToECDSA(strings.Repeat("0", 63) + "1")
	if err != nil {
		t.Fatal(err)
	}
	directory := transactionCLIAddress(10)
	publisher := strings.ToLower(crypto.PubkeyToAddress(key.PublicKey).Hex())
	commit := strings.Repeat("a", 40)
	manifest := directoryrelease.Manifest{SchemaVersion: 1, ReleaseID: "directory-transaction-cli-test", Network: "base-sepolia", ChainID: 84532,
		SourceDeployment:  directoryrelease.SourceDeployment{ReleaseID: "ascp-source-test", SourceCommit: commit},
		DirectoryContract: directory, OrganizationDomain: transactionCLIHash(1), DirectoryPublisher: publisher, DirectoryPublisherEpoch: 1,
		Asset:     directoryrelease.AssetBinding{Address: directoryrelease.BaseSepoliaUSDC, Symbol: "USDC", Decimals: 6, RuntimeCodeHash: transactionCLIHash(9)},
		VersionID: 1, PreviousVersion: 0, PreviousRoot: transactionCLIZeroHash(), ChangeClass: 2,
		WorkflowID: transactionCLIHash(2), ProposerNonce: "7",
		Sellers: []directoryrelease.Seller{{SellerID: transactionCLIHash(3), PayoutAddress: transactionCLIAddress(12),
			AckAuthority: transactionCLIAddress(13), QuoteSigningKey: transactionCLIAddress(14), KeyEpoch: 1,
			BaseURLOrigin: "https://seller.testnet.flowopsagent.xyz", Status: 1}},
		Resources: []directoryrelease.Resource{{SellerID: transactionCLIHash(3), ResourceID: transactionCLIHash(5), Price: "1000",
			EscrowSupported: true, VerificationSpecHash: transactionCLIHash(6), DeclaredWorkTime: 300, VerificationBudgetSeconds: 120}}}
	ipfs, err := directoryrelease.CanonicalIPFSLocation(manifest)
	if err != nil {
		t.Fatal(err)
	}
	manifest.Locations = []string{"ar://" + base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x22}, 32)), ipfs}
	deploymentValue := map[string]any{"releaseId": manifest.SourceDeployment.ReleaseID, "network": manifest.Network,
		"chainId": manifest.ChainID, "sourceCommit": commit, "organizationDomain": manifest.OrganizationDomain,
		"authorities": map[string]any{"directoryPublisher": publisher}, "asset": manifest.Asset,
		"contracts": []map[string]any{{"name": "service_directory", "address": directory}}}
	deployment, _ := json.Marshal(deploymentValue)
	artifact, err := directoryrelease.Compile(manifest, deployment)
	if err != nil {
		t.Fatal(err)
	}
	gateways := directoryrelease.GatewayConfig{SchemaVersion: 1, IPFSGatewayOrigin: "https://ipfs.example.com",
		ArweaveGatewayOrigin: "https://arweave.example.com"}
	presign, err := directoryrelease.BuildPresign(context.Background(), artifact, deployment, gateways,
		directoryrelease.PresignRequest{SchemaVersion: 1, AdminNonce: "41"}, transactionCLIFetcher{body: artifact.CanonicalBlob},
		transactionCLILiveObserver{}, func() time.Time { return transactionCLINow })
	if err != nil {
		t.Fatal(err)
	}
	signature, err := crypto.Sign(common.HexToHash(presign.Digest).Bytes(), key)
	if err != nil {
		t.Fatal(err)
	}
	signature[64] += 27
	signed := directoryrelease.PublisherSignature{SchemaVersion: 1, Digest: presign.Digest,
		Signature: "0x" + hex.EncodeToString(signature)}
	relayRequest := directoryrelease.RelaySimulationRequest{SchemaVersion: 1, RelayerAddress: transactionCLIAddress(20)}
	relay, err := directoryrelease.PrepareRelaySimulation(context.Background(), presign, artifact, deployment, signed, relayRequest,
		transactionCLIRelaySimulator{}, func() time.Time { return transactionCLINow })
	if err != nil {
		t.Fatal(err)
	}
	transactionRequest := directoryrelease.RelayTransactionRequest{SchemaVersion: 1, ExpectedNonce: "7", GasLimit: 500000,
		MaxFeePerGasWei: "4000000000", MaxPriorityFeePerGasWei: "1000000000", MaxWorstCaseGasSpendWei: "2000000000000000",
		ValidUntil: transactionCLINow.Add(time.Minute)}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	paths := transactionCLIPaths{relay: filepath.Join(root, "relay.json"), presign: filepath.Join(root, "presign.json"),
		artifact: filepath.Join(root, "artifact.json"), deployment: filepath.Join(root, "deployment.json"),
		signature: filepath.Join(root, "signature.json"), relayRequest: filepath.Join(root, "relay-request.json"),
		transactionRequest: filepath.Join(root, "transaction-request.json"), private: filepath.Join(root, "private-transaction.json"),
		preview: filepath.Join(root, "preview.json")}
	transactionCLIWrite(t, paths.relay, relay)
	transactionCLIWrite(t, paths.presign, presign)
	transactionCLIWrite(t, paths.artifact, artifact)
	if err := os.WriteFile(paths.deployment, deployment, 0o600); err != nil {
		t.Fatal(err)
	}
	transactionCLIWrite(t, paths.signature, signed)
	transactionCLIWrite(t, paths.relayRequest, relayRequest)
	transactionCLIWrite(t, paths.transactionRequest, transactionRequest)
	return paths, signed
}

func transactionCLIWrite(t *testing.T, path string, value any) {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func transactionCLIAddress(value int) string {
	return "0x" + strings.Repeat("0", 38) + transactionCLITwo(value)
}
func transactionCLIHash(value int) string {
	return "0x" + strings.Repeat("0", 62) + transactionCLITwo(value)
}
func transactionCLIZeroHash() string { return "0x" + strings.Repeat("0", 64) }
func transactionCLITwo(value int) string {
	return string(rune('0'+value/10)) + string(rune('0'+value%10))
}
