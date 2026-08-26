package directoryrelease

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

type transactionFixtureObserver struct {
	mutate func(*[]RelayTransactionProviderObservation)
	err    error
}

func (o transactionFixtureObserver) ObserveTransaction(_ context.Context, target RelayTransactionTarget) ([]RelayTransactionProviderObservation, error) {
	if o.err != nil {
		return nil, o.err
	}
	values := make([]RelayTransactionProviderObservation, 2)
	for index, prior := range target.PreviousObservations {
		values[index] = RelayTransactionProviderObservation{ProviderID: prior.ProviderID, BlockNumber: prior.BlockNumber + 1,
			BlockHash: hash(80 + index), BlockTimestamp: uint64(presignNow.Add(time.Duration(index) * time.Second).Unix()),
			ChainID: target.ChainID, RelayerAddress: target.RelayerAddress, PendingNonce: target.ExpectedNonce,
			BaseFeePerGasWei: "100000000", MetadataOnly: true, CalldataDisclosed: false}
	}
	if o.mutate != nil {
		o.mutate(&values)
	}
	return values, nil
}

func TestPrepareAndVerifyRelayTransactionPreviewBindsPrivateTransactionWithoutBroadcast(t *testing.T) {
	presign, artifact, deployment, signed, relayRequest, relayEvidence, request := transactionFixture(t)
	preview, private, err := PrepareRelayTransactionPreview(context.Background(), relayEvidence, presign, artifact, deployment,
		signed, relayRequest, request, transactionFixtureObserver{}, func() time.Time { return presignNow })
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyRelayTransactionPreview(preview, private, relayEvidence, presign, artifact, deployment, signed,
		relayRequest, request, presignNow.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if preview.ChainID != BaseSepoliaChainID || preview.RelayerAddress != relayRequest.RelayerAddress ||
		preview.ContractAddress != presign.Authorization.ContractAddress || preview.CalldataHash != relayEvidence.CalldataHash ||
		preview.WorstCaseGasSpendWei != "2000000000000000" || !preview.PrivateArtifactRequired || preview.CalldataDisclosed ||
		!preview.SigningRequired || preview.BroadcastAuthorized || preview.FundingEnabled || !private.SigningRequired ||
		private.BroadcastAuthorized || !strings.HasPrefix(private.Data, presign.UnsignedCall.FunctionSelector) {
		t.Fatalf("unsafe preview=%+v private=%+v", preview, private)
	}
	publicRaw, _ := json.Marshal(preview)
	if bytes.Contains(publicRaw, []byte(signed.Signature)) || bytes.Contains(publicRaw, []byte(private.Data)) ||
		bytes.Contains(publicRaw, []byte(`"data"`)) {
		t.Fatalf("public preview disclosed relay capability: %s", publicRaw)
	}
}

func TestRelayTransactionPreviewRejectsRequestEvidenceAndPrivateArtifactSubstitution(t *testing.T) {
	presign, artifact, deployment, signed, relayRequest, relayEvidence, request := transactionFixture(t)
	base, private, err := PrepareRelayTransactionPreview(context.Background(), relayEvidence, presign, artifact, deployment,
		signed, relayRequest, request, transactionFixtureObserver{}, func() time.Time { return presignNow })
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*RelayTransactionPreview){
		"schema":         func(v *RelayTransactionPreview) { v.SchemaVersion++ },
		"relay evidence": func(v *RelayTransactionPreview) { v.RelayEvidenceHash = hash(71) },
		"private hash":   func(v *RelayTransactionPreview) { v.PrivateTransactionHash = hash(72) },
		"signing hash":   func(v *RelayTransactionPreview) { v.SigningHash = hash(73) },
		"relayer":        func(v *RelayTransactionPreview) { v.RelayerAddress = address(74) },
		"nonce":          func(v *RelayTransactionPreview) { v.Nonce = "8" },
		"gas":            func(v *RelayTransactionPreview) { v.GasLimit++ },
		"fee":            func(v *RelayTransactionPreview) { v.MaxFeePerGasWei = "4000000001" },
		"spend":          func(v *RelayTransactionPreview) { v.WorstCaseGasSpendWei = "1" },
		"expiry":         func(v *RelayTransactionPreview) { v.ValidUntil = v.ValidUntil.Add(time.Second) },
		"provider":       func(v *RelayTransactionPreview) { v.Observations[0].ProviderID = hash(74) },
		"block":          func(v *RelayTransactionPreview) { v.Observations[0].BlockNumber = 1 },
		"base fee":       func(v *RelayTransactionPreview) { v.Observations[0].BaseFeePerGasWei = "9999999999" },
		"metadata":       func(v *RelayTransactionPreview) { v.Observations[0].MetadataOnly = false },
		"disclosed":      func(v *RelayTransactionPreview) { v.CalldataDisclosed = true },
		"broadcast":      func(v *RelayTransactionPreview) { v.BroadcastAuthorized = true },
		"funding":        func(v *RelayTransactionPreview) { v.FundingEnabled = true },
	} {
		t.Run(name, func(t *testing.T) {
			changed := cloneTransactionPreview(t, base)
			mutate(&changed)
			if VerifyRelayTransactionPreview(changed, private, relayEvidence, presign, artifact, deployment, signed,
				relayRequest, request, presignNow) == nil {
				t.Fatal("mutated preview accepted")
			}
		})
	}
	changedPrivate := private
	changedPrivate.Data = changedPrivate.Data[:10] + "ff" + changedPrivate.Data[12:]
	if VerifyRelayTransactionPreview(base, changedPrivate, relayEvidence, presign, artifact, deployment, signed,
		relayRequest, request, presignNow) == nil {
		t.Fatal("mutated private calldata accepted")
	}
	changedPrivate = private
	changedPrivate.From = address(75)
	if VerifyRelayTransactionPreview(base, changedPrivate, relayEvidence, presign, artifact, deployment, signed,
		relayRequest, request, presignNow) == nil {
		t.Fatal("mutated transaction sender accepted")
	}

	badRequests := []RelayTransactionRequest{request, request, request, request, request, request, request}
	badRequests[0].ExpectedNonce = "01"
	badRequests[1].GasLimit = MinimumRelayGasLimit - 1
	badRequests[2].MaxFeePerGasWei = "10000000001"
	badRequests[3].MaxPriorityFeePerGasWei = "3000000000"
	badRequests[4].MaxWorstCaseGasSpendWei = "1"
	badRequests[5].ValidUntil = time.Unix(int64(presign.Authorization.ValidBefore), 0).UTC()
	badRequests[6].GasLimit = MaximumRelayGasLimit + 1
	for index, bad := range badRequests {
		if _, _, err := PrepareRelayTransactionPreview(context.Background(), relayEvidence, presign, artifact, deployment,
			signed, relayRequest, bad, transactionFixtureObserver{}, func() time.Time { return presignNow }); err == nil {
			t.Fatalf("bad request %d accepted", index)
		}
	}
}

func TestRelayTransactionPreviewRejectsProviderDriftOutageAndSlowPreparation(t *testing.T) {
	presign, artifact, deployment, signed, relayRequest, relayEvidence, request := transactionFixture(t)
	for name, mutate := range map[string]func(*[]RelayTransactionProviderObservation){
		"nonce drift": func(v *[]RelayTransactionProviderObservation) { (*v)[0].PendingNonce = "8" },
		"chain":       func(v *[]RelayTransactionProviderObservation) { (*v)[0].ChainID = 8453 },
		"relayer":     func(v *[]RelayTransactionProviderObservation) { (*v)[0].RelayerAddress = address(88) },
		"old block": func(v *[]RelayTransactionProviderObservation) {
			(*v)[0].BlockTimestamp = uint64(presignNow.Add(-3 * time.Minute).Unix())
		},
		"future block": func(v *[]RelayTransactionProviderObservation) {
			(*v)[0].BlockTimestamp = uint64(presignNow.Add(time.Minute).Unix())
		},
		"fee ceiling": func(v *[]RelayTransactionProviderObservation) { (*v)[0].BaseFeePerGasWei = "3000000000" },
		"calldata":    func(v *[]RelayTransactionProviderObservation) { (*v)[0].CalldataDisclosed = true },
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := PrepareRelayTransactionPreview(context.Background(), relayEvidence, presign, artifact, deployment,
				signed, relayRequest, request, transactionFixtureObserver{mutate: mutate}, func() time.Time { return presignNow }); err == nil {
				t.Fatal("unsafe provider observation accepted")
			}
		})
	}
	if _, _, err := PrepareRelayTransactionPreview(context.Background(), relayEvidence, presign, artifact, deployment,
		signed, relayRequest, request, transactionFixtureObserver{err: errors.New("offline")}, func() time.Time { return presignNow }); err == nil {
		t.Fatal("provider outage accepted")
	}
	calls := 0
	clock := func() time.Time {
		calls++
		if calls == 1 {
			return presignNow
		}
		return presignNow.Add(MaximumTransactionPreviewAge + time.Second)
	}
	if _, _, err := PrepareRelayTransactionPreview(context.Background(), relayEvidence, presign, artifact, deployment,
		signed, relayRequest, request, transactionFixtureObserver{}, clock); err == nil {
		t.Fatal("slow preparation accepted")
	}
}

func TestBaseTransactionObserverUsesOnlyMetadataRPCsAndRechecksNonce(t *testing.T) {
	_, _, _, _, relayRequest, relayEvidence, request := transactionFixture(t)
	target := RelayTransactionTarget{ChainID: BaseSepoliaChainID, RelayerAddress: relayRequest.RelayerAddress,
		ExpectedNonce: request.ExpectedNonce, MaxFeePerGasWei: request.MaxFeePerGasWei,
		MaxPriorityFeeWei: request.MaxPriorityFeePerGasWei, ValidUntil: request.ValidUntil,
		PreviousObservations: relayEvidence.Observations}
	var lock sync.Mutex
	calls := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		raw := new(bytes.Buffer)
		_, _ = raw.ReadFrom(request.Body)
		if strings.Contains(raw.String(), "eth_call") || strings.Contains(raw.String(), "eth_send") ||
			strings.Contains(raw.String(), relayEvidence.CalldataHash) {
			t.Errorf("unsafe RPC payload=%s", raw.String())
		}
		var envelope struct {
			JSONRPC string            `json:"jsonrpc"`
			ID      int               `json:"id"`
			Method  string            `json:"method"`
			Params  []json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(raw.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		lock.Lock()
		calls[envelope.Method]++
		lock.Unlock()
		var result any
		switch envelope.Method {
		case "eth_chainId":
			result = "0x14a34"
		case "eth_getTransactionCount":
			result = "0x7"
		case "eth_getBlockByNumber":
			result = map[string]string{"number": "0x70", "hash": hash(87),
				"timestamp": fmt.Sprintf("0x%x", presignNow.Add(time.Second).Unix()), "baseFeePerGas": "0x5f5e100"}
		default:
			t.Errorf("unexpected RPC method %s", envelope.Method)
		}
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(map[string]any{"jsonrpc": "2.0", "id": envelope.ID, "result": result})
	}))
	defer server.Close()
	first, _ := url.Parse(server.URL + "/first")
	second, _ := url.Parse(server.URL + "/second")
	observer := &BaseSepoliaTransactionObserver{providers: []liveRPCProvider{
		{id: relayEvidence.Observations[0].ProviderID, endpoint: first, client: server.Client()},
		{id: relayEvidence.Observations[1].ProviderID, endpoint: second, client: server.Client()},
	}}
	values, err := observer.ObserveTransaction(context.Background(), target)
	if err != nil || verifyRelayTransactionObservations(target, values, presignNow) != nil {
		t.Fatalf("values=%+v err=%v", values, err)
	}
	if calls["eth_chainId"] != 2 || calls["eth_getTransactionCount"] != 4 || calls["eth_getBlockByNumber"] != 4 || len(calls) != 3 {
		t.Fatalf("calls=%v", calls)
	}
}

func transactionFixture(t *testing.T) (PresignPackage, Artifact, []byte, PublisherSignature, RelaySimulationRequest,
	RelaySimulationEvidence, RelayTransactionRequest,
) {
	t.Helper()
	presign, artifact, deployment, signed, relayRequest := relayFixture(t)
	relayEvidence, err := PrepareRelaySimulation(context.Background(), presign, artifact, deployment, signed, relayRequest,
		relayFixtureSimulator{}, func() time.Time { return presignNow })
	if err != nil {
		t.Fatal(err)
	}
	request := RelayTransactionRequest{SchemaVersion: TransactionPreviewSchemaVersion, ExpectedNonce: "7", GasLimit: 500_000,
		MaxFeePerGasWei: "4000000000", MaxPriorityFeePerGasWei: "1000000000", MaxWorstCaseGasSpendWei: "2000000000000000",
		ValidUntil: presignNow.Add(time.Minute)}
	return presign, artifact, deployment, signed, relayRequest, relayEvidence, request
}

func cloneTransactionPreview(t *testing.T, value RelayTransactionPreview) RelayTransactionPreview {
	t.Helper()
	raw, _ := json.Marshal(value)
	decoded, err := DecodeRelayTransactionPreview(raw)
	if err != nil {
		t.Fatal(err)
	}
	return decoded
}
