package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/gnanam1990/flowops/internal/reconciliation"
	"github.com/gnanam1990/flowops/internal/rpcadmission"
	"github.com/gnanam1990/flowops/pkg/broadcastreceipt"
	"github.com/gnanam1990/flowops/pkg/envelope"
)

func TestInitReceiptKeyCreatesPrivateFileWithoutLeakingKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "receipt.key")
	var output bytes.Buffer
	if err := run(context.Background(), []string{"init-receipt-key", path}, strings.NewReader(""), &output, nil); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o", info.Mode().Perm())
	}
	raw, _ := os.ReadFile(path)
	privateB64 := strings.TrimSpace(string(raw))
	if strings.Contains(output.String(), privateB64) {
		t.Fatal("private receipt key leaked to output")
	}
	if err := run(context.Background(), []string{"init-receipt-key", path}, strings.NewReader(""), io.Discard, nil); err == nil {
		t.Fatal("existing key was overwritten")
	}
}

func TestReferenceSignerNoFundsEndToEnd(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	flowPublic, flowPrivate, _ := ed25519.GenerateKey(nil)
	_, receiptPrivate, _ := ed25519.GenerateKey(nil)
	walletKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	receiptPath := filepath.Join(dir, "receipt.key")
	freezePath := filepath.Join(dir, "freeze.json")
	writePrivate(t, receiptPath, base64.StdEncoding.EncodeToString(receiptPrivate)+"\n")
	writePrivate(t, freezePath, `{"version":"flowops.freeze.v1","organizationFrozen":false,"frozenAgents":[],"frozenTasks":[]}`)
	config := validConfig(dir, flowPublic, walletKey, receiptPath, freezePath)
	configPath := filepath.Join(dir, "config.json")
	rawConfig, _ := json.Marshal(config)
	writePrivate(t, configPath, string(rawConfig))

	authorization := envelope.Authorization{
		Version: envelope.Version, AuthorizationID: "auth-e2e", OrganizationID: "org-1", CustomerID: "customer-1",
		AgentID: "agent-1", TaskID: "task-1", ActionID: "action-1", Rail: envelope.RailDirect,
		ChainID: 84532, Recipient: config.AllowedRecipients[0], Asset: config.Asset, AmountAtomic: "1250000",
		Resource: "invoice-1", PolicyVersion: "policy-1", Nonce: "0x" + strings.Repeat("a", 64),
		IssuedAt: now.Add(-time.Second).Unix(), ExpiresAt: now.Add(2 * time.Minute).Unix(),
	}
	signed, err := envelope.Sign(authorization, "flowops-1", flowPrivate)
	if err != nil {
		t.Fatal(err)
	}
	input, _ := json.Marshal(signed)
	transport := &signerTransport{t: t, now: now, walletKey: walletKey}
	client := &http.Client{Transport: transport, Timeout: 2 * time.Second}
	var output bytes.Buffer
	if err := run(context.Background(), []string{"execute", configPath}, bytes.NewReader(input), &output, client); err != nil {
		t.Fatal(err)
	}
	var summary attemptSummary
	if err := json.Unmarshal(output.Bytes(), &summary); err != nil {
		t.Fatal(err)
	}
	if summary.State != "REGISTERED" || !summary.Registered || summary.Outcome != broadcastreceipt.OutcomeSubmitted || summary.TransactionHash == "" {
		t.Fatalf("summary = %+v", summary)
	}
	if strings.Contains(output.String(), "rawTransaction") || strings.Contains(output.String(), base64.StdEncoding.EncodeToString(receiptPrivate)) {
		t.Fatal("sensitive signer material leaked to output")
	}
	transport.mu.Lock()
	if transport.signCalls != 1 || transport.broadcastCalls != 1 || transport.registrationCalls != 1 {
		t.Fatalf("calls sign=%d broadcast=%d registration=%d", transport.signCalls, transport.broadcastCalls, transport.registrationCalls)
	}
	transport.mu.Unlock()

	output.Reset()
	if err := run(context.Background(), []string{"execute", configPath}, bytes.NewReader(input), &output, client); err != nil {
		t.Fatal(err)
	}
	transport.mu.Lock()
	if transport.signCalls != 1 || transport.broadcastCalls != 1 || transport.registrationCalls != 1 {
		t.Fatalf("replay crossed boundary: sign=%d broadcast=%d registration=%d", transport.signCalls, transport.broadcastCalls, transport.registrationCalls)
	}
	transport.mu.Unlock()
}

func TestReferenceSignerEscrowNoFundsEndToEnd(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	flowPublic, flowPrivate, _ := ed25519.GenerateKey(nil)
	_, receiptPrivate, _ := ed25519.GenerateKey(nil)
	walletKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	receiptPath := filepath.Join(dir, "receipt.key")
	freezePath := filepath.Join(dir, "freeze.json")
	writePrivate(t, receiptPath, base64.StdEncoding.EncodeToString(receiptPrivate)+"\n")
	writePrivate(t, freezePath, `{"version":"flowops.freeze.v1","organizationFrozen":false,"frozenAgents":[],"frozenTasks":[]}`)
	config := validConfig(dir, flowPublic, walletKey, receiptPath, freezePath)
	config.Rail = envelope.RailEscrow
	config.EscrowContract = "0x4444444444444444444444444444444444444444"
	config.EscrowReleaseWindow = 3600
	config.MaxGasLimit = 500_000
	configPath := filepath.Join(dir, "config.json")
	rawConfig, _ := json.Marshal(config)
	writePrivate(t, configPath, string(rawConfig))

	buyer := config.Sender
	taskDigest := "0x" + strings.Repeat("c", 64)
	requestDigest := "0x" + strings.Repeat("d", 64)
	callID, err := envelope.DeriveEscrowCallID(84532, config.EscrowContract, buyer, taskDigest, requestDigest)
	if err != nil {
		t.Fatal(err)
	}
	authorization := envelope.Authorization{
		Version: envelope.Version, AuthorizationID: "auth-escrow-e2e", OrganizationID: "org-1", CustomerID: "customer-1",
		AgentID: "agent-1", TaskID: "task-1", ActionID: "action-1", Rail: envelope.RailEscrow,
		ChainID: 84532, Recipient: config.AllowedRecipients[0], Asset: config.Asset, AmountAtomic: "100000",
		Resource: "evidence-fetch", PolicyVersion: "policy-1", Nonce: "0x" + strings.Repeat("e", 64),
		IssuedAt: now.Add(-time.Second).Unix(), ExpiresAt: now.Add(2 * time.Minute).Unix(),
		Escrow: &envelope.EscrowTerms{Contract: config.EscrowContract, Buyer: buyer, Provider: config.AllowedRecipients[0], CallID: callID,
			TaskDigest: taskDigest, RequestDigest: requestDigest, AcknowledgeBy: uint64(now.Add(3 * time.Minute).Unix()),
			DeliverBy: uint64(now.Add(10 * time.Minute).Unix()), ReleaseWindow: 3600},
	}
	signed, err := envelope.Sign(authorization, "flowops-1", flowPrivate)
	if err != nil {
		t.Fatal(err)
	}
	input, _ := json.Marshal(signed)
	transport := &signerTransport{t: t, now: now, walletKey: walletKey}
	client := &http.Client{Transport: transport, Timeout: 2 * time.Second}
	var output bytes.Buffer
	if err := run(context.Background(), []string{"execute", configPath}, bytes.NewReader(input), &output, client); err != nil {
		t.Fatal(err)
	}
	var summary attemptSummary
	if err := json.Unmarshal(output.Bytes(), &summary); err != nil || summary.State != "REGISTERED" || !summary.Registered {
		t.Fatalf("summary=%+v err=%v", summary, err)
	}
	transport.mu.Lock()
	defer transport.mu.Unlock()
	if transport.signCalls != 1 || transport.broadcastCalls != 1 || transport.registrationCalls != 1 {
		t.Fatalf("calls sign=%d broadcast=%d registration=%d", transport.signCalls, transport.broadcastCalls, transport.registrationCalls)
	}
}

func TestValidateConfigRejectsNestedDuplicateAndUnsafePermissions(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	writePrivate(t, configPath, `{"version":"flowops.reference-signer.v1","trustKeys":[{"keyId":"a","keyId":"b"}]}`)
	if _, err := loadConfig(configPath); err == nil {
		t.Fatal("nested duplicate field accepted")
	}
	writePrivate(t, configPath, `{"version":"flowops.reference-signer.v1"}`)
	if err := os.Chmod(configPath, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadConfig(configPath); err == nil {
		t.Fatal("world-readable config accepted")
	}
}

func TestLoadConfigPinsInitialBaseMainnetPilotLimits(t *testing.T) {
	dir := t.TempDir()
	config := fileConfig{
		Version: configVersion, OrganizationID: "org-1", CustomerID: "customer-1", ReceiptKeyID: "receipt-1",
		ChainID: 8453, Rail: envelope.RailDirect, MaxAmountAtomic: "1000000", MaxOutstandingAtomic: "10000001",
		MaxTTLSeconds: 300, MaxFutureSkewSeconds: 5, StallThresholdSeconds: 120,
		MaxFutureBlockSkewSeconds: 5, RequestTimeoutSeconds: 2, ObserverQuorum: 2,
		NonceJournalPath: filepath.Join(dir, "nonces.log"), AttemptJournalPath: filepath.Join(dir, "attempts.log"),
	}
	raw, _ := json.Marshal(config)
	path := filepath.Join(dir, "config.json")
	writePrivate(t, path, string(raw))
	if _, err := loadConfig(path); err == nil || !strings.Contains(err.Error(), "initial Base mainnet profile") {
		t.Fatalf("changed mainnet pilot profile error = %v", err)
	}
}

func TestLoadConfigRejectsLegacyV1AfterRequiredLimitMigration(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	writePrivate(t, path, `{"version":"flowops.reference-signer.v1","organizationId":"org-1","customerId":"customer-1","receiptKeyId":"receipt-1","maxAmountAtomic":"1000000"}`)
	if _, err := loadConfig(path); err == nil || !strings.Contains(err.Error(), "config version") {
		t.Fatalf("legacy config error = %v", err)
	}
	writePrivate(t, path, `{"version":"flowops.reference-signer.v2","organizationId":"org-1","customerId":"customer-1","receiptKeyId":"receipt-1","maxAmountAtomic":"1000000","maxOutstandingAtomic":"10000000"}`)
	if _, err := loadConfig(path); err == nil || !strings.Contains(err.Error(), "config version") {
		t.Fatalf("legacy v2 config error = %v", err)
	}
	writePrivate(t, path, `{"version":"flowops.reference-signer.v3","organizationId":"org-1","customerId":"customer-1","receiptKeyId":"receipt-1","maxAmountAtomic":"1000000","maxOutstandingAtomic":"10000000","rail":"direct_usdc"}`)
	if _, err := loadConfig(path); err == nil || !strings.Contains(err.Error(), "config version") {
		t.Fatalf("legacy v3 config error = %v", err)
	}
}

func TestLoadConfigRequiresProductionRPCAdmissionOnBaseMainnet(t *testing.T) {
	flowPublic, _, _ := ed25519.GenerateKey(nil)
	walletKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	config := validConfig(dir, flowPublic, walletKey, filepath.Join(dir, "receipt.key"), filepath.Join(dir, "freeze.json"))
	config.ChainID = 8453
	config.MaxAmountAtomic = "1000000"
	config.MaxOutstandingAtomic = "10000000"
	config.BaseRPCProviders = []reconciliation.RPCProvider{{Name: "rpc_alpha", URL: "https://alpha.vendor.example/v1/secret"}, {Name: "rpc_beta", URL: "https://beta.vendor.example/v1/secret"}}
	write := func(name string, value fileConfig) error {
		raw, _ := json.Marshal(value)
		path := filepath.Join(dir, name+".json")
		writePrivate(t, path, string(raw))
		_, loadErr := loadConfig(path)
		return loadErr
	}
	if err := write("missing-admission", config); err == nil || !strings.Contains(err.Error(), "requires production RPC admission") {
		t.Fatalf("missing admission error = %v", err)
	}
	config.BaseRPCAdmission = &rpcadmission.ProductionAdmission{SchemaVersion: 1, Providers: []rpcadmission.ProviderAdmission{
		{Name: "rpc_alpha", Operator: "vendor_alpha", FailureDomain: "vendor_alpha_global", ServiceTier: "paid", ProductionEligible: true},
		{Name: "rpc_beta", Operator: "vendor_beta", FailureDomain: "vendor_beta_global", ServiceTier: "paid", ProductionEligible: true},
	}}
	if err := write("valid-admission", config); err != nil {
		t.Fatal(err)
	}
	canonical, _ := json.Marshal(config)
	ambiguous := strings.Replace(string(canonical), `"operator":"vendor_alpha"`, `"Operator":"shadow","operator":"vendor_alpha"`, 1)
	ambiguousPath := filepath.Join(dir, "ambiguous-admission.json")
	writePrivate(t, ambiguousPath, ambiguous)
	if _, err := loadConfig(ambiguousPath); err == nil {
		t.Fatal("case-variant embedded admission field was accepted")
	}
	config.BaseRPCProviders[0].URL = "https://mainnet.base.org"
	if err := write("public-endpoint", config); err == nil || !strings.Contains(err.Error(), "known public endpoint") {
		t.Fatalf("public endpoint error = %v", err)
	}
}

func TestLoadConfigRejectsProductionRPCAdmissionOnBaseSepolia(t *testing.T) {
	flowPublic, _, _ := ed25519.GenerateKey(nil)
	walletKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	config := validConfig(dir, flowPublic, walletKey, filepath.Join(dir, "receipt.key"), filepath.Join(dir, "freeze.json"))
	config.BaseRPCAdmission = &rpcadmission.ProductionAdmission{SchemaVersion: 1}
	raw, _ := json.Marshal(config)
	path := filepath.Join(dir, "sepolia-with-admission.json")
	writePrivate(t, path, string(raw))
	if _, err := loadConfig(path); err == nil || !strings.Contains(err.Error(), "must not contain") {
		t.Fatalf("Sepolia admission error = %v", err)
	}
}

func TestLoadConfigRequiresOneExplicitRailAndEscrowTuple(t *testing.T) {
	dir := t.TempDir()
	base := fileConfig{
		Version: configVersion, OrganizationID: "org-1", CustomerID: "customer-1", ReceiptKeyID: "receipt-1",
		ChainID: 84532, MaxAmountAtomic: "1000000", MaxOutstandingAtomic: "10000000",
		MaxTTLSeconds: 300, MaxFutureSkewSeconds: 5, StallThresholdSeconds: 120,
		MaxFutureBlockSkewSeconds: 5, RequestTimeoutSeconds: 2, ObserverQuorum: 2,
		NonceJournalPath: filepath.Join(dir, "nonces.log"), AttemptJournalPath: filepath.Join(dir, "attempts.log"),
	}
	for name, mutate := range map[string]func(*fileConfig){
		"missing rail":         func(*fileConfig) {},
		"escrow missing tuple": func(c *fileConfig) { c.Rail = envelope.RailEscrow },
		"direct with tuple": func(c *fileConfig) {
			c.Rail = envelope.RailDirect
			c.EscrowContract = "0x4444444444444444444444444444444444444444"
			c.EscrowReleaseWindow = 3600
		},
	} {
		t.Run(name, func(t *testing.T) {
			config := base
			mutate(&config)
			raw, _ := json.Marshal(config)
			path := filepath.Join(dir, strings.ReplaceAll(name, " ", "-")+".json")
			writePrivate(t, path, string(raw))
			if _, err := loadConfig(path); err == nil {
				t.Fatal("unsafe rail configuration accepted")
			}
		})
	}
}

func validConfig(dir string, flowPublic ed25519.PublicKey, walletKey *ecdsa.PrivateKey, receiptPath, freezePath string) fileConfig {
	return fileConfig{
		Version: configVersion, OrganizationID: "org-1", CustomerID: "customer-1",
		TrustKeys: []trustKeyConfig{{KeyID: "flowops-1", PublicKeyB64: base64.StdEncoding.EncodeToString(flowPublic)}},
		ChainID:   84532, Rail: envelope.RailDirect, Asset: "0x1111111111111111111111111111111111111111",
		AllowedRecipients: []string{"0x2222222222222222222222222222222222222222"}, MaxAmountAtomic: "10000000",
		MaxOutstandingAtomic: "50000000",
		MaxTTLSeconds:        300, MaxFutureSkewSeconds: 5,
		BaseRPCProviders: []reconciliation.RPCProvider{{Name: "alpha", URL: "https://alpha.rpc.example"}, {Name: "beta", URL: "https://beta.rpc.example"}},
		PrimaryBaseRPC:   "alpha", ObserverQuorum: 2, MaxHeadSkew: 2, StallThresholdSeconds: 120, MaxFutureBlockSkewSeconds: 5,
		WalletRPCURL: "http://127.0.0.1:8550", Sender: strings.ToLower(crypto.PubkeyToAddress(walletKey.PublicKey).Hex()),
		MaxGasLimit: 100_000, MaxFeePerGasWei: "10000000000", MaxPriorityFeePerGasWei: "2000000000",
		NonceJournalPath: filepath.Join(dir, "nonces.log"), AttemptJournalPath: filepath.Join(dir, "attempts.log"),
		FreezeFilePath: freezePath, ReceiptKeyID: "receipt-1", ReceiptPrivateKeyPath: receiptPath,
		ControlAPIURL: "https://control.example", RequestTimeoutSeconds: 2,
	}
}

type signerTransport struct {
	t                 *testing.T
	now               time.Time
	walletKey         *ecdsa.PrivateKey
	mu                sync.Mutex
	signCalls         int
	broadcastCalls    int
	registrationCalls int
}

func (s *signerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.URL.Hostname() == "control.example" {
		return s.register(request), nil
	}
	var call struct {
		JSONRPC string            `json:"jsonrpc"`
		ID      int               `json:"id"`
		Method  string            `json:"method"`
		Params  []json.RawMessage `json:"params"`
	}
	if err := json.NewDecoder(request.Body).Decode(&call); err != nil {
		s.t.Fatal(err)
	}
	var result any
	switch call.Method {
	case "eth_chainId":
		result = "0x14a34"
	case "eth_getBlockByNumber":
		result = map[string]any{"number": "0x64", "hash": "0x" + strings.Repeat("1", 64), "timestamp": "0x" + big.NewInt(s.now.Unix()).Text(16), "baseFeePerGas": "0x3b9aca00"}
	case "eth_call":
		result = s.ethCallResult(call.Params)
	case "eth_getCode":
		result = "0x6000"
	case "eth_getTransactionCount":
		result = "0x1"
	case "eth_maxPriorityFeePerGas":
		result = "0x3b9aca00"
	case "eth_estimateGas":
		result = "0xc350"
	case "account_signTransaction":
		s.mu.Lock()
		s.signCalls++
		s.mu.Unlock()
		result = s.sign(call.Params[0])
	case "eth_sendRawTransaction":
		s.mu.Lock()
		s.broadcastCalls++
		s.mu.Unlock()
		var rawHex string
		_ = json.Unmarshal(call.Params[0], &rawHex)
		var tx types.Transaction
		if err := tx.UnmarshalBinary(common.FromHex(rawHex)); err != nil {
			s.t.Fatal(err)
		}
		result = strings.ToLower(tx.Hash().Hex())
	default:
		s.t.Fatalf("unexpected RPC method %s", call.Method)
	}
	return jsonResponse(request, map[string]any{"jsonrpc": "2.0", "id": 1, "result": result}), nil
}

func (s *signerTransport) ethCallResult(params []json.RawMessage) string {
	if len(params) == 0 {
		s.t.Fatal("eth_call missing transaction")
	}
	var args struct {
		Data string `json:"data"`
	}
	if err := json.Unmarshal(params[0], &args); err != nil {
		s.t.Fatal(err)
	}
	selector := func(signature string) string { return hex.EncodeToString(crypto.Keccak256([]byte(signature))[:4]) }
	switch strings.TrimPrefix(args.Data, "0x") {
	case selector("asset()"):
		return "0x" + strings.Repeat("0", 24) + strings.Repeat("1", 40)
	case selector("optimisticReleaseWindow()"):
		return "0x" + hex.EncodeToString(common.LeftPadBytes(big.NewInt(3600).Bytes(), 32))
	default:
		if strings.HasPrefix(strings.TrimPrefix(args.Data, "0x"), selector("allowance(address,address)")) {
			return "0x" + hex.EncodeToString(common.LeftPadBytes(big.NewInt(100_000).Bytes(), 32))
		}
		return "0x"
	}
}

func (s *signerTransport) sign(raw json.RawMessage) map[string]any {
	var args struct {
		To, Gas, MaxFeePerGas, MaxPriorityFeePerGas, Value, Data, Nonce, ChainID string
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		s.t.Fatal(err)
	}
	chainID := hexQuantity(args.ChainID)
	nonce := hexQuantity(args.Nonce)
	gas := hexQuantity(args.Gas)
	fee := hexQuantity(args.MaxFeePerGas)
	priority := hexQuantity(args.MaxPriorityFeePerGas)
	data, _ := hex.DecodeString(strings.TrimPrefix(args.Data, "0x"))
	to := common.HexToAddress(args.To)
	tx := &types.DynamicFeeTx{ChainID: chainID, Nonce: nonce.Uint64(), GasTipCap: priority, GasFeeCap: fee, Gas: gas.Uint64(), To: &to, Value: new(big.Int), Data: data}
	signed, err := types.SignNewTx(s.walletKey, types.LatestSignerForChainID(chainID), tx)
	if err != nil {
		s.t.Fatal(err)
	}
	encoded, _ := signed.MarshalBinary()
	return map[string]any{"raw": "0x" + hex.EncodeToString(encoded)}
}

func (s *signerTransport) register(request *http.Request) *http.Response {
	s.mu.Lock()
	s.registrationCalls++
	s.mu.Unlock()
	var receipt broadcastreceipt.SignedReceipt
	if err := json.NewDecoder(request.Body).Decode(&receipt); err != nil {
		s.t.Fatal(err)
	}
	if request.URL.Path == "/v1/signer/escrow-broadcasts" {
		return jsonResponse(request, map[string]any{"call": map[string]any{"pending": map[string]any{"expected": map[string]any{"transactionHash": receipt.Receipt.TransactionHash}, "broadcastAttestation": map[string]any{"signedReceipt": receipt}}}})
	}
	return jsonResponse(request, map[string]any{"execution": map[string]any{"expected": map[string]any{"transactionHash": receipt.Receipt.TransactionHash}, "broadcastAttestation": map[string]any{"signedReceipt": receipt}}})
}

func jsonResponse(request *http.Request, value any) *http.Response {
	raw, _ := json.Marshal(value)
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(bytes.NewReader(raw)), Request: request}
}

func hexQuantity(value string) *big.Int {
	n := new(big.Int)
	n.SetString(strings.TrimPrefix(value, "0x"), 16)
	return n
}

func writePrivate(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
}
