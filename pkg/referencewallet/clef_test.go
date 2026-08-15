package referencewallet

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/gnanam1990/flowops/pkg/envelope"
	"github.com/gnanam1990/flowops/pkg/referencesigner"
)

const (
	testAsset     = "0x036cbd53842c5426634e7929541ec2318f3dcf7e"
	testRecipient = "0x2222222222222222222222222222222222222222"
	testEscrow    = "0x86e145397f58e71c134c0e054320db929483227a"
)

type adapterHarness struct {
	t             *testing.T
	key           *ecdsa.PrivateKey
	base          *httptest.Server
	wallet        *httptest.Server
	mu            sync.Mutex
	methods       []string
	sentRaw       string
	mutate        func(*types.DynamicFeeTx)
	rpcFail       map[string]bool
	chainID       string
	escrow        bool
	allowance     *big.Int
	escrowAsset   string
	releaseWindow uint64
	code          string
}

func newAdapterHarness(t *testing.T) *adapterHarness {
	t.Helper()
	key, err := crypto.HexToECDSA(strings.Repeat("1", 64))
	if err != nil {
		t.Fatal(err)
	}
	h := &adapterHarness{t: t, key: key, rpcFail: make(map[string]bool), chainID: "0x14a34", allowance: big.NewInt(100_000), escrowAsset: testAsset, releaseWindow: 3600, code: "0x6000"}
	h.base = httptest.NewServer(http.HandlerFunc(h.handleBase))
	h.wallet = httptest.NewServer(http.HandlerFunc(h.handleWallet))
	t.Cleanup(h.base.Close)
	t.Cleanup(h.wallet.Close)
	return h
}

func (h *adapterHarness) escrowAdapter(t *testing.T) *EscrowClefAdapter {
	t.Helper()
	h.escrow = true
	adapter, err := NewEscrowClefAdapter(EscrowClefConfig{
		ChainID: 84532, Sender: strings.ToLower(crypto.PubkeyToAddress(h.key.PublicKey).Hex()), Asset: testAsset,
		Contract: testEscrow, ReleaseWindow: 3600, BaseRPCURL: h.base.URL, WalletRPCURL: h.wallet.URL,
		MaxGasLimit: 500_000, MaxFeePerGasWei: "10000000000", MaxPriorityFeePerGasWei: "2000000000",
		RequestTimeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	return adapter
}

func (h *adapterHarness) adapter(t *testing.T) *ClefAdapter {
	t.Helper()
	adapter, err := NewClefAdapter(ClefConfig{
		ChainID: 84532, Sender: strings.ToLower(crypto.PubkeyToAddress(h.key.PublicKey).Hex()), Asset: testAsset,
		BaseRPCURL: h.base.URL, WalletRPCURL: h.wallet.URL, MaxGasLimit: 100_000,
		MaxFeePerGasWei: "10000000000", MaxPriorityFeePerGasWei: "2000000000",
		RequestTimeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	return adapter
}

func (h *adapterHarness) handleBase(w http.ResponseWriter, r *http.Request) {
	request := decodeRPCRequest(h.t, r)
	h.mu.Lock()
	h.methods = append(h.methods, request.Method)
	h.mu.Unlock()
	if h.rpcFail[request.Method] {
		writeRPC(w, map[string]any{"jsonrpc": "2.0", "id": 1, "error": map[string]any{"code": -32000, "message": "secret provider detail"}})
		return
	}
	var result any
	switch request.Method {
	case "eth_chainId":
		result = h.chainID
	case "eth_call":
		result = h.callResult(request)
	case "eth_getCode":
		result = h.code
	case "eth_getTransactionCount":
		result = "0x7"
	case "eth_maxPriorityFeePerGas":
		result = "0x3b9aca00"
	case "eth_getBlockByNumber":
		result = map[string]any{"baseFeePerGas": "0x3b9aca00"}
	case "eth_estimateGas":
		result = "0xc350"
	case "eth_sendRawTransaction":
		if len(request.Params) != 1 {
			h.t.Errorf("broadcast params = %d", len(request.Params))
		}
		_ = json.Unmarshal(request.Params[0], &h.sentRaw)
		var tx types.Transaction
		if err := tx.UnmarshalBinary(common.FromHex(h.sentRaw)); err != nil {
			h.t.Errorf("decode broadcast: %v", err)
		}
		result = strings.ToLower(tx.Hash().Hex())
	default:
		h.t.Errorf("unexpected Base method %s", request.Method)
		result = "0x0"
	}
	writeRPC(w, map[string]any{"jsonrpc": "2.0", "id": 1, "result": result})
}

func (h *adapterHarness) callResult(request rawRPCRequest) string {
	if !h.escrow {
		return "0x" + strings.Repeat("0", 63) + "1"
	}
	var args transactionArgs
	if len(request.Params) == 0 || json.Unmarshal(request.Params[0], &args) != nil {
		h.t.Fatal("invalid eth_call arguments")
	}
	switch strings.TrimPrefix(args.Data, "0x") {
	case common.Bytes2Hex(selector("asset()")):
		return "0x" + strings.Repeat("0", 24) + strings.TrimPrefix(h.escrowAsset, "0x")
	case common.Bytes2Hex(selector("optimisticReleaseWindow()")):
		return "0x" + common.Bytes2Hex(common.LeftPadBytes(new(big.Int).SetUint64(h.releaseWindow).Bytes(), 32))
	default:
		if strings.HasPrefix(strings.TrimPrefix(args.Data, "0x"), common.Bytes2Hex(selector("allowance(address,address)"))) {
			return "0x" + common.Bytes2Hex(common.LeftPadBytes(h.allowance.Bytes(), 32))
		}
		return "0x"
	}
}

func (h *adapterHarness) handleWallet(w http.ResponseWriter, r *http.Request) {
	request := decodeRPCRequest(h.t, r)
	if request.Method != "account_signTransaction" || len(request.Params) != 2 {
		h.t.Errorf("wallet request = %s params=%d", request.Method, len(request.Params))
	}
	var args transactionArgs
	if err := json.Unmarshal(request.Params[0], &args); err != nil {
		h.t.Fatal(err)
	}
	if args.From != common.HexToAddress(args.From).Hex() {
		h.t.Errorf("wallet from address is not EIP-55 checksummed: %s", args.From)
	}
	if args.To != common.HexToAddress(args.To).Hex() {
		h.t.Errorf("wallet to address is not EIP-55 checksummed: %s", args.To)
	}
	nonce, _ := parseQuantity(args.Nonce)
	gas, _ := parseQuantity(args.Gas)
	fee, _ := parseQuantity(args.MaxFeePerGas)
	priority, _ := parseQuantity(args.MaxPriorityFeePerGas)
	chainID, _ := parseQuantity(args.ChainID)
	txData, _ := decodeCanonicalHex(args.Data)
	tx := &types.DynamicFeeTx{
		ChainID: chainID, Nonce: nonce.Uint64(), GasTipCap: priority, GasFeeCap: fee,
		Gas: gas.Uint64(), To: ptrAddress(common.HexToAddress(args.To)), Value: new(big.Int), Data: txData,
	}
	if h.mutate != nil {
		h.mutate(tx)
	}
	signed, err := types.SignNewTx(h.key, types.LatestSignerForChainID(chainID), tx)
	if err != nil {
		h.t.Fatal(err)
	}
	raw, err := signed.MarshalBinary()
	if err != nil {
		h.t.Fatal(err)
	}
	writeRPC(w, map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"raw": "0x" + common.Bytes2Hex(raw), "tx": map[string]any{"ignored": true}}})
}

func TestClefAdapterPreparesValidatesAndBroadcastsExactTransaction(t *testing.T) {
	h := newAdapterHarness(t)
	adapter := h.adapter(t)
	prepared, err := adapter.Prepare(context.Background(), directAuthorization())
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Sender != strings.ToLower(crypto.PubkeyToAddress(h.key.PublicKey).Hex()) || prepared.TransactionHash == "" {
		t.Fatalf("prepared = %+v", prepared)
	}
	if err := adapter.Broadcast(context.Background(), prepared); err != nil {
		t.Fatal(err)
	}
	if h.sentRaw != "0x"+common.Bytes2Hex(prepared.RawTransaction) {
		t.Fatal("broadcast did not use exact prepared bytes")
	}
	want := []string{"eth_chainId", "eth_call", "eth_getTransactionCount", "eth_maxPriorityFeePerGas", "eth_getBlockByNumber", "eth_estimateGas", "eth_sendRawTransaction"}
	if strings.Join(h.methods, ",") != strings.Join(want, ",") {
		t.Fatalf("methods = %v", h.methods)
	}
}

func TestClefAdapterRejectsWalletMutationBeforeBroadcast(t *testing.T) {
	tests := map[string]func(*types.DynamicFeeTx){
		"recipient": func(tx *types.DynamicFeeTx) { tx.Data[len(tx.Data)-1]++ },
		"asset":     func(tx *types.DynamicFeeTx) { tx.To = ptrAddress(common.HexToAddress(testRecipient)) },
		"value":     func(tx *types.DynamicFeeTx) { tx.Value = big.NewInt(1) },
		"nonce":     func(tx *types.DynamicFeeTx) { tx.Nonce++ },
		"gas":       func(tx *types.DynamicFeeTx) { tx.Gas++ },
		"fee":       func(tx *types.DynamicFeeTx) { tx.GasFeeCap.Add(tx.GasFeeCap, big.NewInt(1)) },
		"accessList": func(tx *types.DynamicFeeTx) {
			tx.AccessList = types.AccessList{{Address: common.HexToAddress(testRecipient)}}
		},
	}
	for name, mutation := range tests {
		t.Run(name, func(t *testing.T) {
			h := newAdapterHarness(t)
			h.mutate = mutation
			_, err := h.adapter(t).Prepare(context.Background(), directAuthorization())
			if err == nil {
				t.Fatal("expected mutated transaction to be refused")
			}
			if h.sentRaw != "" {
				t.Fatal("mutation reached broadcast")
			}
		})
	}
}

func TestClefAdapterRejectsWrongSignerAndSimulationFailure(t *testing.T) {
	t.Run("wrong chain", func(t *testing.T) {
		h := newAdapterHarness(t)
		h.chainID = "0x1"
		_, err := h.adapter(t).Prepare(context.Background(), directAuthorization())
		if err == nil || !strings.Contains(err.Error(), "wrong chain") {
			t.Fatalf("err = %v", err)
		}
		if len(h.methods) != 1 {
			t.Fatalf("methods after chain mismatch = %v", h.methods)
		}
	})
	t.Run("wrong signer", func(t *testing.T) {
		h := newAdapterHarness(t)
		adapter, err := NewClefAdapter(ClefConfig{
			ChainID: 84532, Sender: "0x3333333333333333333333333333333333333333", Asset: testAsset,
			BaseRPCURL: h.base.URL, WalletRPCURL: h.wallet.URL, MaxGasLimit: 100_000,
			MaxFeePerGasWei: "10000000000", MaxPriorityFeePerGasWei: "2000000000", RequestTimeout: time.Second,
		})
		if err != nil {
			t.Fatal(err)
		}
		_, err = adapter.Prepare(context.Background(), directAuthorization())
		if err == nil || !strings.Contains(err.Error(), "wrong account") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("simulation", func(t *testing.T) {
		h := newAdapterHarness(t)
		h.rpcFail["eth_call"] = true
		_, err := h.adapter(t).Prepare(context.Background(), directAuthorization())
		if err == nil || strings.Contains(err.Error(), "secret provider detail") {
			t.Fatalf("unsafe or missing error: %v", err)
		}
		if len(h.methods) != 2 {
			t.Fatalf("methods after simulation failure = %v", h.methods)
		}
	})
}

func TestClefAdapterBroadcastHashMismatchIsAmbiguous(t *testing.T) {
	h := newAdapterHarness(t)
	adapter := h.adapter(t)
	prepared, err := adapter.Prepare(context.Background(), directAuthorization())
	if err != nil {
		t.Fatal(err)
	}
	prepared.TransactionHash = "0x" + strings.Repeat("0", 64)
	if err := adapter.Broadcast(context.Background(), prepared); err == nil {
		t.Fatal("expected hash mismatch")
	}
}

func TestClefAdapterURLAndCapValidation(t *testing.T) {
	base := ClefConfig{
		ChainID: 84532, Sender: "0x3333333333333333333333333333333333333333", Asset: testAsset,
		BaseRPCURL: "https://base.example", WalletRPCURL: "http://127.0.0.1:8550",
		MaxGasLimit: 100_000, MaxFeePerGasWei: "100", MaxPriorityFeePerGasWei: "10", RequestTimeout: time.Second,
	}
	tests := []ClefConfig{
		func() ClefConfig { c := base; c.BaseRPCURL = "http://base.example"; return c }(),
		func() ClefConfig { c := base; c.WalletRPCURL = "https://wallet.example"; return c }(),
		func() ClefConfig { c := base; c.WalletRPCURL = "http://user@127.0.0.1:8550"; return c }(),
		func() ClefConfig { c := base; c.MaxPriorityFeePerGasWei = "101"; return c }(),
	}
	for i, cfg := range tests {
		if _, err := NewClefAdapter(cfg); err == nil {
			t.Fatalf("case %d accepted", i)
		}
	}
	withProviderPath := base
	withProviderPath.BaseRPCURL = "https://base.example/v2/customer-project"
	if _, err := NewClefAdapter(withProviderPath); err != nil {
		t.Fatalf("Base provider credential path was rejected: %v", err)
	}
}

func TestEscrowClefAdapterPreparesValidatesAndBroadcastsExactFund(t *testing.T) {
	h := newAdapterHarness(t)
	adapter := h.escrowAdapter(t)
	prepared, err := adapter.Prepare(context.Background(), escrowAuthorization(h))
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Sender != strings.ToLower(crypto.PubkeyToAddress(h.key.PublicKey).Hex()) {
		t.Fatalf("prepared sender = %s", prepared.Sender)
	}
	var tx types.Transaction
	if err := tx.UnmarshalBinary(prepared.RawTransaction); err != nil {
		t.Fatal(err)
	}
	if tx.To() == nil || strings.ToLower(tx.To().Hex()) != testEscrow || !strings.HasPrefix(common.Bytes2Hex(tx.Data()), common.Bytes2Hex(selector("fund(bytes32,address,uint256,bytes32,bytes32,uint64,uint64)"))) {
		t.Fatalf("fund transaction = to %v data %x", tx.To(), tx.Data())
	}
	if err := adapter.Broadcast(context.Background(), prepared); err != nil {
		t.Fatal(err)
	}
	want := []string{"eth_chainId", "eth_getCode", "eth_call", "eth_call", "eth_call", "eth_call", "eth_getTransactionCount", "eth_maxPriorityFeePerGas", "eth_getBlockByNumber", "eth_estimateGas", "eth_sendRawTransaction"}
	if strings.Join(h.methods, ",") != strings.Join(want, ",") {
		t.Fatalf("methods = %v", h.methods)
	}
}

func TestEscrowClefAdapterFailsClosedBeforeWallet(t *testing.T) {
	tests := map[string]func(*adapterHarness, *referencesigner.Authorized){
		"wrong live asset":       func(h *adapterHarness, _ *referencesigner.Authorized) { h.escrowAsset = testRecipient },
		"wrong release window":   func(h *adapterHarness, _ *referencesigner.Authorized) { h.releaseWindow = 7200 },
		"missing code":           func(h *adapterHarness, _ *referencesigner.Authorized) { h.code = "0x" },
		"insufficient allowance": func(h *adapterHarness, _ *referencesigner.Authorized) { h.allowance = big.NewInt(99) },
		"excess allowance":       func(h *adapterHarness, _ *referencesigner.Authorized) { h.allowance = big.NewInt(100_001) },
		"wrong configured contract": func(_ *adapterHarness, a *referencesigner.Authorized) {
			a.Authorization.Escrow.Contract = testRecipient
		},
		"wrong buyer": func(_ *adapterHarness, a *referencesigner.Authorized) {
			a.Authorization.Escrow.Buyer = "0x3333333333333333333333333333333333333333"
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			h := newAdapterHarness(t)
			auth := escrowAuthorization(h)
			mutate(h, &auth)
			_, err := h.escrowAdapter(t).Prepare(context.Background(), auth)
			if err == nil {
				t.Fatal("expected escrow preparation to fail")
			}
			if h.sentRaw != "" {
				t.Fatal("failed escrow preparation reached broadcast")
			}
		})
	}
}

func TestEscrowClefAdapterRejectsWalletMutation(t *testing.T) {
	h := newAdapterHarness(t)
	h.mutate = func(tx *types.DynamicFeeTx) { tx.Data[len(tx.Data)-1]++ }
	_, err := h.escrowAdapter(t).Prepare(context.Background(), escrowAuthorization(h))
	if err == nil || !strings.Contains(err.Error(), "changed the CallEscrow fund") {
		t.Fatalf("err = %v", err)
	}
}

func TestFundDataMatchesSolidityABI(t *testing.T) {
	h := newAdapterHarness(t)
	authorized := escrowAuthorization(h)
	terms := authorized.Authorization.Escrow
	amount := big.NewInt(100_000)
	contractABI, err := abi.JSON(strings.NewReader(`[{"type":"function","name":"fund","stateMutability":"nonpayable","inputs":[{"name":"callId","type":"bytes32"},{"name":"provider","type":"address"},{"name":"amount","type":"uint256"},{"name":"taskDigest","type":"bytes32"},{"name":"requestDigest","type":"bytes32"},{"name":"acknowledgeBy","type":"uint64"},{"name":"deliverBy","type":"uint64"}],"outputs":[]}]`))
	if err != nil {
		t.Fatal(err)
	}
	want, err := contractABI.Pack("fund", common.HexToHash(terms.CallID), common.HexToAddress(terms.Provider), amount,
		common.HexToHash(terms.TaskDigest), common.HexToHash(terms.RequestDigest), terms.AcknowledgeBy, terms.DeliverBy)
	if err != nil {
		t.Fatal(err)
	}
	if got := fundData(terms, amount); !bytes.Equal(got, want) {
		t.Fatalf("fund calldata mismatch\ngot  %x\nwant %x", got, want)
	}
}

func directAuthorization() referencesigner.Authorized {
	return referencesigner.Authorized{Authorization: envelope.Authorization{
		Version: envelope.Version, AuthorizationID: "auth-1", OrganizationID: "org-1", CustomerID: "customer-1",
		AgentID: "agent-1", TaskID: "task-1", ActionID: "action-1", Rail: envelope.RailDirect,
		ChainID: 84532, Recipient: testRecipient, Asset: testAsset, AmountAtomic: "1250000", Resource: "invoice-1",
		PolicyVersion: "policy-1", Nonce: "0x" + strings.Repeat("a", 64), IssuedAt: 1, ExpiresAt: 2,
	}}
}

func escrowAuthorization(h *adapterHarness) referencesigner.Authorized {
	buyer := strings.ToLower(crypto.PubkeyToAddress(h.key.PublicKey).Hex())
	taskDigest := "0x" + strings.Repeat("a", 64)
	requestDigest := "0x" + strings.Repeat("b", 64)
	callID, err := envelope.DeriveEscrowCallID(84532, testEscrow, buyer, taskDigest, requestDigest)
	if err != nil {
		h.t.Fatal(err)
	}
	return referencesigner.Authorized{Authorization: envelope.Authorization{
		Version: envelope.Version, AuthorizationID: "auth-escrow-1", OrganizationID: "org-1", CustomerID: "customer-1",
		AgentID: "agent-1", TaskID: "task-1", ActionID: "action-1", Rail: envelope.RailEscrow,
		ChainID: 84532, Recipient: testRecipient, Asset: testAsset, AmountAtomic: "100000", Resource: "evidence-fetch",
		PolicyVersion: "policy-1", Nonce: "0x" + strings.Repeat("c", 64), IssuedAt: 1, ExpiresAt: 2,
		Escrow: &envelope.EscrowTerms{Contract: testEscrow, Buyer: buyer, Provider: testRecipient, CallID: callID,
			TaskDigest: taskDigest, RequestDigest: requestDigest, AcknowledgeBy: 3, DeliverBy: 4, ReleaseWindow: 3600},
	}}
}

type rawRPCRequest struct {
	JSONRPC string            `json:"jsonrpc"`
	ID      int               `json:"id"`
	Method  string            `json:"method"`
	Params  []json.RawMessage `json:"params"`
}

func decodeRPCRequest(t *testing.T, r *http.Request) rawRPCRequest {
	t.Helper()
	var request rawRPCRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		t.Fatal(err)
	}
	if request.JSONRPC != "2.0" || request.ID != 1 {
		t.Fatalf("RPC envelope = %+v", request)
	}
	return request
}

func writeRPC(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

func ptrAddress(value common.Address) *common.Address { return &value }
