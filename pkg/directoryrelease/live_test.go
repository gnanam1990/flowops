package directoryrelease

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
)

func TestBaseSepoliaLiveObserverPinsExactBlocksAndUnusedReplayState(t *testing.T) {
	var lock sync.Mutex
	calls := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var envelope struct {
			JSONRPC string            `json:"jsonrpc"`
			ID      int               `json:"id"`
			Method  string            `json:"method"`
			Params  []json.RawMessage `json:"params"`
		}
		if err := json.NewDecoder(request.Body).Decode(&envelope); err != nil {
			t.Errorf("decode request: %v", err)
			http.Error(writer, "bad request", http.StatusBadRequest)
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
			result = map[string]string{"number": "0x64", "hash": hash(92), "timestamp": fmt.Sprintf("0x%x", presignNow.Unix())}
		case "eth_call":
			var call map[string]string
			if err := json.Unmarshal(envelope.Params[0], &call); err != nil || string(envelope.Params[1]) != `"0x64"` {
				t.Errorf("call is not pinned to observed block: %s", envelope.Params)
			}
			result = liveCallResult(t, call["data"])
		default:
			t.Errorf("unexpected method %s", envelope.Method)
			result = nil
		}
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(map[string]any{"jsonrpc": "2.0", "id": envelope.ID, "result": result})
	}))
	defer server.Close()
	first, _ := url.Parse(server.URL + "/primary")
	second, _ := url.Parse(server.URL + "/secondary")
	observer := &BaseSepoliaLiveObserver{providers: []liveRPCProvider{
		{id: hash(90), endpoint: first, client: server.Client()},
		{id: hash(91), endpoint: second, client: server.Client()},
	}}
	target := liveTargetFixture()
	evidence, err := observer.Observe(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyLiveEvidence(target, evidence); err != nil || len(evidence.Observations) != 2 {
		t.Fatalf("evidence=%+v err=%v", evidence, err)
	}
	if calls["eth_chainId"] != 2 || calls["eth_getBlockByNumber"] != 4 || calls["eth_call"] != 16 {
		t.Fatalf("calls=%v", calls)
	}
}

func TestLiveObserverAndEvidenceRejectProviderAndReplaySubstitution(t *testing.T) {
	if _, err := NewBaseSepoliaLiveObserver("http://rpc.example.com", "https://rpc2.example.com", time.Second); err == nil {
		t.Fatal("HTTP provider accepted")
	}
	if _, err := NewBaseSepoliaLiveObserver("https://rpc.example.com/a", "https://rpc.example.com/b", time.Second); err == nil {
		t.Fatal("same provider origin accepted")
	}
	if _, err := NewBaseSepoliaLiveObserver("https://rpc1.example.com", "https://rpc2.example.com", 31*time.Second); err == nil {
		t.Fatal("long provider timeout accepted")
	}
	target := liveTargetFixture()
	base := LivePresignEvidence{SchemaVersion: 1, Observations: []LiveProviderObservation{
		liveObservationFixture(target, hash(90), hash(92), 100), liveObservationFixture(target, hash(91), hash(93), 101),
	}}
	mutations := map[string]func(*LivePresignEvidence){
		"same provider":     func(v *LivePresignEvidence) { v.Observations[1].ProviderID = v.Observations[0].ProviderID },
		"wrong chain":       func(v *LivePresignEvidence) { v.Observations[0].ChainID = 8453 },
		"wrong contract":    func(v *LivePresignEvidence) { v.Observations[0].ContractAddress = address(88) },
		"wrong publisher":   func(v *LivePresignEvidence) { v.Observations[0].DirectoryPublisher = address(88) },
		"wrong epoch":       func(v *LivePresignEvidence) { v.Observations[0].PublisherEpoch++ },
		"existing proposal": func(v *LivePresignEvidence) { v.Observations[0].LatestProposalHash = hash(88) },
		"used operation":    func(v *LivePresignEvidence) { v.Observations[0].AdminOperationUsed = true },
		"used nonce":        func(v *LivePresignEvidence) { v.Observations[0].AdminNonceUsed = true },
		"expired block":     func(v *LivePresignEvidence) { v.Observations[0].BlockTimestamp = target.ValidBefore },
		"timestamp skew":    func(v *LivePresignEvidence) { v.Observations[1].BlockTimestamp += 121 },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			changed := base
			changed.Observations = append([]LiveProviderObservation(nil), base.Observations...)
			mutate(&changed)
			if err := verifyLiveEvidence(target, changed); err == nil {
				t.Fatal("unsafe live evidence accepted")
			}
		})
	}
}

func liveCallResult(t *testing.T, data string) string {
	t.Helper()
	decoded, err := hex.DecodeString(strings.TrimPrefix(data, "0x"))
	if err != nil || len(decoded) < 4 {
		t.Fatalf("invalid call data %q", data)
	}
	selectorHex := "0x" + hex.EncodeToString(decoded[:4])
	var word []byte
	switch selectorHex {
	case selector("orgDomain()"):
		word = common.HexToHash(hash(1)).Bytes()
	case selector("directoryPublisher()"):
		word = common.LeftPadBytes(common.HexToAddress(address(11)).Bytes(), 32)
	case selector("directoryPublisherEpoch()"):
		word = common.LeftPadBytes([]byte{1}, 32)
	case selector("currentVersion()"), selector("currentRoot()"), selector("latestProposalHash(uint64)"),
		selector("usedAdminOperationIds(bytes32)"), selector("usedAdminNonces(bytes32,uint256)"):
		word = make([]byte, 32)
	default:
		t.Fatalf("unexpected selector %s", selectorHex)
	}
	return "0x" + hex.EncodeToString(word)
}

func liveTargetFixture() LivePresignTarget {
	return LivePresignTarget{ChainID: 84532, ContractAddress: address(10), OrganizationDomain: hash(1),
		ExpectedPublisher: address(11), PublisherEpoch: 1, VersionID: 1, PreviousVersion: 0, PreviousRoot: zeroHash(),
		AuthorityRole: hash(2), AdminOperationID: hash(3), AdminNonce: "41",
		ValidAfter: uint64(presignNow.Add(-30 * time.Second).Unix()), ValidBefore: uint64(presignNow.Add(570 * time.Second).Unix())}
}

func liveObservationFixture(target LivePresignTarget, providerID, blockHash string, blockNumber uint64) LiveProviderObservation {
	return LiveProviderObservation{ProviderID: providerID, BlockNumber: blockNumber, BlockHash: blockHash,
		BlockTimestamp: uint64(presignNow.Unix()), ChainID: target.ChainID, ContractAddress: target.ContractAddress,
		OrganizationDomain: target.OrganizationDomain, DirectoryPublisher: target.ExpectedPublisher,
		PublisherEpoch: target.PublisherEpoch, CurrentVersion: target.PreviousVersion, CurrentRoot: target.PreviousRoot,
		LatestProposalHash: zeroHash()}
}
