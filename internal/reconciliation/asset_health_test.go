package reconciliation

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/crypto"
)

const (
	assetHealthUSDC   = "0x036cbd53842c5426634e7929541ec2318f3dcf7e"
	assetHealthImpl   = "0x1111111111111111111111111111111111111111"
	assetHealthBuyer  = "0x2222222222222222222222222222222222222222"
	assetHealthEscrow = "0x3333333333333333333333333333333333333333"
)

type assetHealthTransport struct{}

func (assetHealthTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	body, _ := io.ReadAll(request.Body)
	var call rpcRequest
	if err := json.Unmarshal(body, &call); err != nil {
		return nil, err
	}
	var result any
	switch call.Method {
	case "eth_chainId":
		result = "0x14a34"
	case "eth_getBlockByNumber":
		result = rpcBlock{Number: "0x64", Hash: testHash(100), Timestamp: "0x64"}
	case "eth_getStorageAt":
		result = "0x" + strings.Repeat("0", 24) + assetHealthImpl[2:]
	case "eth_getCode":
		result = "0x60016000"
	case "eth_call":
		object := call.Params[0].(map[string]any)
		data := object["data"].(string)
		if strings.HasPrefix(data, "0x"+hex.EncodeToString(crypto.Keccak256([]byte("transfer(address,uint256)"))[:4])) {
			result = "0x" + strings.Repeat("0", 63) + "1"
		} else {
			result = "0x" + strings.Repeat("0", 64)
		}
	default:
		return nil, fmt.Errorf("unexpected method %s", call.Method)
	}
	encoded, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "result": result})
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(encoded)), Request: request}, nil
}

func TestAssetHealthQuorumReadsPinnedProxyPauseBlacklistAndTransfer(t *testing.T) {
	client := &http.Client{Transport: assetHealthTransport{}, Timeout: time.Second}
	observers, err := NewObserverSet(84532, observerProviders(), client, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	result := observers.AssetHealthQuorum(context.Background(), AssetHealthRequest{Asset: assetHealthUSDC, Buyer: assetHealthBuyer, Escrow: assetHealthEscrow})
	if len(result.Failures) != 0 || len(result.Evidence) != 2 {
		t.Fatalf("result=%+v", result)
	}
	wantCodeHash := strings.ToLower(crypto.Keccak256Hash([]byte{0x60, 0x01, 0x60, 0x00}).Hex())
	for _, evidence := range result.Evidence {
		if evidence.ProxyImplementation != assetHealthImpl || evidence.RuntimeCodeHash != wantCodeHash || evidence.Paused ||
			evidence.BuyerBlacklisted || evidence.EscrowBlacklisted || evidence.TransferFailure || evidence.FinalizedBlock != 100 {
			t.Fatalf("evidence=%+v", evidence)
		}
	}
}
