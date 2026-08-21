package ascpsettlement

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/gnanam1990/flowops/internal/reconciliation"
)

type settlementTransport struct {
	canonical map[string]string
	head      map[string]uint64
}

func (t settlementTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	var call struct {
		Method string `json:"method"`
		Params []any  `json:"params"`
	}
	body, _ := io.ReadAll(request.Body)
	_ = json.Unmarshal(body, &call)
	host := request.URL.Hostname()
	var result any
	switch call.Method {
	case "eth_chainId":
		result = "0x14a34"
	case "eth_getTransactionReceipt":
		result = map[string]any{
			"transactionHash": testSettlementHash(9), "transactionIndex": "0x0", "blockNumber": "0x64",
			"blockHash": testSettlementHash(100), "status": "0x0", "logs": []any{},
		}
	case "eth_getBlockByNumber":
		tag, _ := call.Params[0].(string)
		number, hash := uint64(100), t.canonical[host]
		if tag == "latest" {
			number, hash = t.head[host], testSettlementHash(t.head[host])
		}
		result = map[string]any{"number": hexUint(number), "hash": hash, "timestamp": "0x60000000"}
	}
	envelope, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "result": result})
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}},
		Body: io.NopCloser(bytes.NewReader(envelope)), Request: request}, nil
}

func TestReaderSealsFinalizedReceiptAndRejectsFabrication(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 21, 6, 0, 0, 0, time.UTC)
	reader := testSettlementReader(t, now, map[string]string{"alpha.example": testSettlementHash(100), "beta.example": testSettlementHash(100)},
		map[string]uint64{"alpha.example": 110, "beta.example": 111})
	result, err := reader.Read(context.Background(), testSettlementExpected())
	if err != nil || result.Finality != Finalized || result.Success || result.ConfirmedHead != 110 {
		t.Fatalf("Read() = %+v, %v", result, err)
	}
	if err := validateResult(result); err != nil {
		t.Fatalf("sealed result rejected: %v", err)
	}
	result.BlockHash = testSettlementHash(999)
	if err := validateResult(result); err == nil {
		t.Fatal("tampered result was accepted")
	}
	if err := validateResult(Result{Expected: testSettlementExpected(), Finality: Finalized}); err == nil {
		t.Fatal("caller-fabricated result was accepted")
	}
}

func TestReaderRequiresProviderAgreementAndFinalityForReorg(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 21, 6, 0, 0, 0, time.UTC)
	reader := testSettlementReader(t, now, map[string]string{"alpha.example": testSettlementHash(200), "beta.example": testSettlementHash(201)},
		map[string]uint64{"alpha.example": 110, "beta.example": 111})
	if _, err := reader.CheckCanonical(context.Background(), testSettlementHash(1), reconciliation.ASCPReceiptLock,
		testSettlementHash(9), 100, testSettlementHash(100)); err != ErrObserverDisagreement {
		t.Fatalf("disagreement error = %v", err)
	}

	reader = testSettlementReader(t, now, map[string]string{"alpha.example": testSettlementHash(200), "beta.example": testSettlementHash(200)},
		map[string]uint64{"alpha.example": 110, "beta.example": 111})
	result, err := reader.CheckCanonical(context.Background(), testSettlementHash(1), reconciliation.ASCPReceiptLock,
		testSettlementHash(9), 100, testSettlementHash(100))
	if err != nil || !result.Reorged || result.CanonicalBlockHash != testSettlementHash(200) {
		t.Fatalf("CheckCanonical() = %+v, %v", result, err)
	}
	if err := validateReorgResult(result); err != nil {
		t.Fatalf("sealed reorg rejected: %v", err)
	}
	result.OriginalBlockHash = testSettlementHash(999)
	if err := validateReorgResult(result); err == nil {
		t.Fatal("tampered reorg was accepted")
	}
}

func testSettlementReader(t *testing.T, now time.Time, canonical map[string]string, heads map[string]uint64) *Reader {
	t.Helper()
	observers, err := reconciliation.NewObserverSet(84532, []reconciliation.RPCProvider{
		{Name: "rpc_alpha", URL: "https://alpha.example/v1"}, {Name: "rpc_beta", URL: "https://beta.example/v1"},
	}, &http.Client{Transport: settlementTransport{canonical: canonical, head: heads}, Timeout: time.Second}, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	reader, err := NewReader(ReaderConfig{Observers: observers, Quorum: 2, SafeConfirmations: 2, FinalizedConfirmations: 10, Clock: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	return reader
}

func testSettlementExpected() reconciliation.ASCPExpectedReceipt {
	return reconciliation.ASCPExpectedReceipt{
		Action: reconciliation.ASCPReceiptLock, TransactionHash: testSettlementHash(9), ChainID: 84532,
		Contract: "0x1111111111111111111111111111111111111111", Asset: "0x2222222222222222222222222222222222222222",
		CallID: testSettlementHash(2), OperationID: testSettlementHash(1), CommitmentHash: testSettlementHash(3),
		Buyer: "0x3333333333333333333333333333333333333333", PayTo: "0x4444444444444444444444444444444444444444",
		AmountAtomic: "10", SettleBy: 1_800_000_000,
	}
}

func testSettlementHash(value uint64) string {
	const digits = "0000000000000000000000000000000000000000000000000000000000000000"
	hex := hexUint(value)[2:]
	return "0x" + digits[:64-len(hex)] + hex
}

func hexUint(value uint64) string {
	const alphabet = "0123456789abcdef"
	if value == 0 {
		return "0x0"
	}
	var buffer [16]byte
	index := len(buffer)
	for value > 0 {
		index--
		buffer[index] = alphabet[value&15]
		value >>= 4
	}
	return "0x" + string(buffer[index:])
}
