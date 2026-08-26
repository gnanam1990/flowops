package directoryrelease

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

type relayFixtureSimulator struct {
	mutate func(*[]RelayProviderObservation)
	err    error
}

func (s relayFixtureSimulator) Simulate(_ context.Context, target RelaySimulationTarget) ([]RelayProviderObservation, error) {
	if s.err != nil {
		return nil, s.err
	}
	value := make([]RelayProviderObservation, 2)
	for index, prior := range target.PreviousObservations {
		value[index] = RelayProviderObservation{ProviderID: prior.ProviderID, BlockNumber: prior.BlockNumber + 2,
			BlockHash: hash(94 + index), BlockTimestamp: uint64(presignNow.Add(time.Duration(index) * time.Second).Unix()),
			ChainID: target.ChainID, ContractAddress: target.ContractAddress, OrganizationDomain: target.OrganizationDomain,
			DirectoryPublisher: target.ExpectedPublisher, PublisherEpoch: target.PublisherEpoch,
			CurrentVersion: target.PreviousVersion, CurrentRoot: target.PreviousRoot, LatestProposalHash: zeroHash(),
			AuthorizationDigest: target.AuthorizationDigest, ProposalHash: target.ProposalHash,
			WorkflowPayloadHash: target.WorkflowPayloadHash, ContractSemanticSimulation: true}
	}
	if s.mutate != nil {
		s.mutate(&value)
	}
	return value, nil
}

func TestPrepareAndVerifyRelaySimulationBindsSignatureCalldataAndCurrentState(t *testing.T) {
	presign, artifact, deployment, signed, request := relayFixture(t)
	evidence, err := PrepareRelaySimulation(context.Background(), presign, artifact, deployment, signed, request,
		relayFixtureSimulator{}, func() time.Time { return presignNow })
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyRelaySimulation(evidence, presign, artifact, deployment, signed, request, presignNow.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if evidence.RecoveredSigner != presign.ExpectedSigner || !evidence.SignatureVerified || evidence.CalldataDisclosed ||
		evidence.BroadcastAuthorized || evidence.FundingEnabled || evidence.ExpectedProposalHash != artifact.Proposal.ProposalHash ||
		len(evidence.Observations) != 2 || evidence.CalldataLength < 4 {
		t.Fatalf("unsafe relay evidence=%+v", evidence)
	}
	raw, _ := json.Marshal(evidence)
	if bytes.Contains(raw, []byte(signed.Signature)) || bytes.Contains(raw, []byte(`"signature"`)) ||
		bytes.Contains(raw, []byte(`"calldata"`)) {
		t.Fatalf("relay evidence disclosed capability: %s", raw)
	}
	if evidence.CalldataHash != "0xc520aad5acbd7e942a753e30db6eae2bba4d546cb76d7d05c0498f2c7c25c7ee" || evidence.CalldataLength != 900 {
		t.Fatalf("calldata golden hash=%s length=%d", evidence.CalldataHash, evidence.CalldataLength)
	}
}

func TestRelayRejectsSignatureRequestStateAndEvidenceSubstitution(t *testing.T) {
	presign, artifact, deployment, signed, request := relayFixture(t)
	base, err := PrepareRelaySimulation(context.Background(), presign, artifact, deployment, signed, request,
		relayFixtureSimulator{}, func() time.Time { return presignNow })
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*RelaySimulationEvidence){
		"schema":           func(v *RelaySimulationEvidence) { v.SchemaVersion++ },
		"artifact":         func(v *RelaySimulationEvidence) { v.ArtifactHash = hash(88) },
		"signer":           func(v *RelaySimulationEvidence) { v.RecoveredSigner = address(88) },
		"signature hash":   func(v *RelaySimulationEvidence) { v.SignatureHash = hash(88) },
		"relayer":          func(v *RelaySimulationEvidence) { v.RelayerAddress = address(88) },
		"calldata":         func(v *RelaySimulationEvidence) { v.CalldataHash = hash(88) },
		"proposal":         func(v *RelaySimulationEvidence) { v.ExpectedProposalHash = hash(88) },
		"disclosed":        func(v *RelaySimulationEvidence) { v.CalldataDisclosed = true },
		"broadcast":        func(v *RelaySimulationEvidence) { v.BroadcastAuthorized = true },
		"funding":          func(v *RelaySimulationEvidence) { v.FundingEnabled = true },
		"provider":         func(v *RelaySimulationEvidence) { v.Observations[0].ProviderID = hash(88) },
		"old block":        func(v *RelaySimulationEvidence) { v.Observations[0].BlockNumber = 1 },
		"used operation":   func(v *RelaySimulationEvidence) { v.Observations[0].AdminOperationUsed = true },
		"used admin nonce": func(v *RelaySimulationEvidence) { v.Observations[0].AdminNonceUsed = true },
		"used proposer":    func(v *RelaySimulationEvidence) { v.Observations[0].ProposerNonceUsed = true },
		"onchain digest":   func(v *RelaySimulationEvidence) { v.Observations[0].AuthorizationDigest = hash(88) },
		"onchain proposal": func(v *RelaySimulationEvidence) { v.Observations[0].ProposalHash = hash(88) },
		"onchain workflow": func(v *RelaySimulationEvidence) { v.Observations[0].WorkflowPayloadHash = hash(88) },
		"semantic flag":    func(v *RelaySimulationEvidence) { v.Observations[0].ContractSemanticSimulation = false },
	} {
		t.Run(name, func(t *testing.T) {
			changed := cloneRelayEvidence(t, base)
			mutate(&changed)
			if err := VerifyRelaySimulation(changed, presign, artifact, deployment, signed, request, presignNow); err == nil {
				t.Fatal("mutated relay evidence accepted")
			}
		})
	}

	wrongDigest := signed
	wrongDigest.Digest = hash(88)
	if _, err := PrepareRelaySimulation(context.Background(), presign, artifact, deployment, wrongDigest, request,
		relayFixtureSimulator{}, func() time.Time { return presignNow }); err == nil {
		t.Fatal("signature digest substitution accepted")
	}
	wrongV := signed
	wrongV.Signature = wrongV.Signature[:130] + "00"
	if _, err := PrepareRelaySimulation(context.Background(), presign, artifact, deployment, wrongV, request,
		relayFixtureSimulator{}, func() time.Time { return presignNow }); err == nil {
		t.Fatal("contract-incompatible recovery id accepted")
	}
	wrongSignerKey, _ := crypto.HexToECDSA(strings.Repeat("0", 63) + "2")
	wrongSignerBytes, _ := crypto.Sign(common.HexToHash(presign.Digest).Bytes(), wrongSignerKey)
	wrongSignerBytes[64] += 27
	wrongSigner := signed
	wrongSigner.Signature = "0x" + hex.EncodeToString(wrongSignerBytes)
	if _, err := PrepareRelaySimulation(context.Background(), presign, artifact, deployment, wrongSigner, request,
		relayFixtureSimulator{}, func() time.Time { return presignNow }); err == nil {
		t.Fatal("wrong publisher signature accepted")
	}
	highS := signed
	highSBytes, _ := hex.DecodeString(strings.TrimPrefix(highS.Signature, "0x"))
	copy(highSBytes[32:64], bytes.Repeat([]byte{0xff}, 32))
	highS.Signature = "0x" + hex.EncodeToString(highSBytes)
	if _, err := DecodePublisherSignature(mustRelayJSON(t, highS)); err == nil {
		t.Fatal("high-S signature accepted")
	}
	wrongRequest := request
	wrongRequest.RelayerAddress = strings.ToUpper(request.RelayerAddress)
	if _, err := PrepareRelaySimulation(context.Background(), presign, artifact, deployment, signed, wrongRequest,
		relayFixtureSimulator{}, func() time.Time { return presignNow }); err == nil {
		t.Fatal("noncanonical relayer accepted")
	}
	if _, err := PrepareRelaySimulation(context.Background(), presign, artifact, deployment, signed, request,
		relayFixtureSimulator{err: errors.New("offline")}, func() time.Time { return presignNow }); err == nil {
		t.Fatal("simulation outage accepted")
	}
}

func TestRelayRejectsStaleFutureAndSlowSimulationEvidence(t *testing.T) {
	presign, artifact, deployment, signed, request := relayFixture(t)
	for name, mutate := range map[string]func(*[]RelayProviderObservation){
		"stale block": func(v *[]RelayProviderObservation) {
			(*v)[0].BlockTimestamp = uint64(presignNow.Add(-MaximumRelayBlockAge - time.Second).Unix())
		},
		"future block": func(v *[]RelayProviderObservation) {
			(*v)[1].BlockTimestamp = uint64(presignNow.Add(MaximumRelayFutureBlockSkew + time.Second).Unix())
		},
		"near expiry": func(v *[]RelayProviderObservation) {
			(*v)[0].BlockTimestamp = presign.Authorization.ValidBefore - uint64(MinimumPresignRemaining/time.Second) + 1
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := PrepareRelaySimulation(context.Background(), presign, artifact, deployment, signed, request,
				relayFixtureSimulator{mutate: mutate}, func() time.Time { return presignNow }); err == nil {
				t.Fatal("unsafe block time accepted")
			}
		})
	}
	calls := 0
	clock := func() time.Time {
		calls++
		if calls == 1 {
			return presignNow
		}
		return presignNow.Add(MaximumRelayBlockAge + time.Second)
	}
	if _, err := PrepareRelaySimulation(context.Background(), presign, artifact, deployment, signed, request,
		relayFixtureSimulator{}, clock); err == nil {
		t.Fatal("simulation that outlived its evidence accepted")
	}
}

func TestRelayStrictDecodersRejectUnknownDuplicateAndMalformedFields(t *testing.T) {
	for name, decode := range map[string]func([]byte) error{
		"signature duplicate": func(raw []byte) error { _, err := DecodePublisherSignature(raw); return err },
		"request duplicate":   func(raw []byte) error { _, err := DecodeRelaySimulationRequest(raw); return err },
		"evidence duplicate":  func(raw []byte) error { _, err := DecodeRelaySimulationEvidence(raw); return err },
	} {
		var raw []byte
		switch name {
		case "signature duplicate":
			raw = []byte(`{"schemaVersion":1,"digest":"` + hash(1) + `","digest":"` + hash(2) + `","signature":"0x` + strings.Repeat("11", 64) + `1b"}`)
		case "request duplicate":
			raw = []byte(`{"schemaVersion":1,"relayerAddress":"` + address(11) + `","relayerAddress":"` + address(12) + `"}`)
		default:
			raw = []byte(`{"schemaVersion":1,"releaseId":"a","releaseId":"b"}`)
		}
		if err := decode(raw); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
}

func TestBaseRelaySimulatorPinsBlocksAndNeverSendsSignedCalldata(t *testing.T) {
	presign, artifact, _, signed, _ := relayFixture(t)
	target := relayTarget(presign, artifact.Proposal.ProposalHash)
	signatureHex := strings.TrimPrefix(signed.Signature, "0x")
	var lock sync.Mutex
	calls := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		raw := new(bytes.Buffer)
		_, _ = raw.ReadFrom(request.Body)
		if strings.Contains(raw.String(), signatureHex) {
			t.Error("signed proposeVersion calldata was disclosed to an RPC provider")
		}
		var envelope struct {
			JSONRPC string            `json:"jsonrpc"`
			ID      int               `json:"id"`
			Method  string            `json:"method"`
			Params  []json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(raw.Bytes(), &envelope); err != nil {
			t.Errorf("decode RPC: %v", err)
			return
		}
		lock.Lock()
		calls[envelope.Method]++
		lock.Unlock()
		var result any
		switch envelope.Method {
		case "eth_chainId":
			result = "0x14a34"
		case "eth_getBlockByNumber":
			result = map[string]string{"number": "0x66", "hash": hash(95), "timestamp": fmt.Sprintf("0x%x", presignNow.Unix())}
		case "eth_call":
			var call map[string]string
			if err := json.Unmarshal(envelope.Params[0], &call); err != nil || string(envelope.Params[1]) != `"0x66"` {
				t.Errorf("unbound call params=%s", envelope.Params)
			}
			if strings.HasPrefix(call["data"], presign.UnsignedCall.FunctionSelector) {
				t.Error("full signed proposeVersion call was sent to an RPC provider")
			}
			result = relayCallResult(t, target, call["data"])
		default:
			t.Errorf("unexpected method %s", envelope.Method)
		}
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(map[string]any{"jsonrpc": "2.0", "id": envelope.ID, "result": result})
	}))
	defer server.Close()
	first, _ := url.Parse(server.URL + "/primary")
	second, _ := url.Parse(server.URL + "/secondary")
	simulator := &BaseSepoliaRelaySimulator{providers: []liveRPCProvider{
		{id: target.PreviousObservations[0].ProviderID, endpoint: first, client: server.Client()},
		{id: target.PreviousObservations[1].ProviderID, endpoint: second, client: server.Client()},
	}}
	observations, err := simulator.Simulate(context.Background(), target)
	if err != nil || len(observations) != 2 {
		t.Fatalf("observations=%+v err=%v", observations, err)
	}
	if calls["eth_chainId"] != 2 || calls["eth_getBlockByNumber"] != 4 || calls["eth_call"] != 24 {
		t.Fatalf("calls=%v", calls)
	}
}

func relayCallResult(t *testing.T, target RelaySimulationTarget, data string) string {
	t.Helper()
	decoded, err := hex.DecodeString(strings.TrimPrefix(data, "0x"))
	if err != nil || len(decoded) < 4 {
		t.Fatalf("invalid call data %q", data)
	}
	selectorHex := "0x" + hex.EncodeToString(decoded[:4])
	parsedABI, _ := abiForRelayTest()
	word := make([]byte, 32)
	switch selectorHex {
	case methodSelector(parsedABI, "orgDomain"):
		word = common.HexToHash(target.OrganizationDomain).Bytes()
	case methodSelector(parsedABI, "directoryPublisher"):
		word = common.LeftPadBytes(common.HexToAddress(target.ExpectedPublisher).Bytes(), 32)
	case methodSelector(parsedABI, "directoryPublisherEpoch"):
		word = common.LeftPadBytes(new(big.Int).SetUint64(target.PublisherEpoch).Bytes(), 32)
	case methodSelector(parsedABI, "currentVersion"):
		word = common.LeftPadBytes(new(big.Int).SetUint64(target.PreviousVersion).Bytes(), 32)
	case methodSelector(parsedABI, "currentRoot"), methodSelector(parsedABI, "latestProposalHash"),
		methodSelector(parsedABI, "usedAdminOperationIds"), methodSelector(parsedABI, "usedAdminNonces"),
		methodSelector(parsedABI, "usedProposerNonces"):
	case methodSelector(parsedABI, "adminAuthorizationDigest"):
		word = common.HexToHash(target.AuthorizationDigest).Bytes()
	case methodSelector(parsedABI, "hashProposal"):
		word = common.HexToHash(target.ProposalHash).Bytes()
	case methodSelector(parsedABI, "directoryProposalWorkflowPayloadHash"):
		word = common.HexToHash(target.WorkflowPayloadHash).Bytes()
	default:
		t.Fatalf("unexpected selector %s", selectorHex)
	}
	return "0x" + hex.EncodeToString(word)
}

func relayFixture(t *testing.T) (PresignPackage, Artifact, []byte, PublisherSignature, RelaySimulationRequest) {
	t.Helper()
	key, err := crypto.HexToECDSA(strings.Repeat("0", 63) + "1")
	if err != nil {
		t.Fatal(err)
	}
	manifest, deployment := validFixture()
	manifest.DirectoryPublisher = strings.ToLower(crypto.PubkeyToAddress(key.PublicKey).Hex())
	var deploymentValue map[string]any
	if err := json.Unmarshal(deployment, &deploymentValue); err != nil {
		t.Fatal(err)
	}
	deploymentValue["authorities"].(map[string]any)["directoryPublisher"] = manifest.DirectoryPublisher
	deployment, _ = json.Marshal(deploymentValue)
	artifact, err := Compile(manifest, deployment)
	if err != nil {
		t.Fatal(err)
	}
	gateways := GatewayConfig{SchemaVersion: 1, IPFSGatewayOrigin: "https://ipfs.example.com", ArweaveGatewayOrigin: "https://arweave.example.com"}
	responses := make(map[string]FetchedBlob, 2)
	for _, location := range artifact.Manifest.Locations {
		gateway, _ := gatewayURL(gateways, location)
		responses[gateway] = FetchedBlob{URL: gateway, FetchedAt: presignNow.Add(-time.Second), StatusCode: http.StatusOK,
			ContentType: "application/json", ContentLength: int64(len(artifact.CanonicalBlob)), Body: append([]byte(nil), artifact.CanonicalBlob...)}
	}
	presign, err := BuildPresign(context.Background(), artifact, deployment, gateways,
		PresignRequest{SchemaVersion: 1, AdminNonce: "41"}, blobFixtureFetcher{responses: responses},
		liveFixtureObserver{}, func() time.Time { return presignNow })
	if err != nil {
		t.Fatal(err)
	}
	signature, err := crypto.Sign(common.HexToHash(presign.Digest).Bytes(), key)
	if err != nil {
		t.Fatal(err)
	}
	signature[64] += 27
	signed := PublisherSignature{SchemaVersion: 1, Digest: presign.Digest, Signature: "0x" + hex.EncodeToString(signature)}
	return presign, artifact, deployment, signed, RelaySimulationRequest{SchemaVersion: 1, RelayerAddress: address(20)}
}

func cloneRelayEvidence(t *testing.T, value RelaySimulationEvidence) RelaySimulationEvidence {
	t.Helper()
	raw, _ := json.Marshal(value)
	decoded, err := DecodeRelaySimulationEvidence(raw)
	if err != nil {
		t.Fatal(err)
	}
	return decoded
}

func mustRelayJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func abiForRelayTest() (abi.ABI, error) { return abi.JSON(strings.NewReader(serviceDirectoryRelayABI)) }
func methodSelector(parsed abi.ABI, method string) string {
	return "0x" + hex.EncodeToString(parsed.Methods[method].ID)
}
