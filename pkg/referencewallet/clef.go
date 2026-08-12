// Package referencewallet provides customer-boundary wallet adapters for the
// FlowOps reference signer. It never accepts or stores wallet private keys.
package referencewallet

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/gnanam1990/flowops/pkg/envelope"
	"github.com/gnanam1990/flowops/pkg/referencesigner"
)

const (
	transferSelector = "a9059cbb"
	defaultGasBuffer = uint64(20)
)

type ClefConfig struct {
	ChainID                 uint64
	Sender                  string
	Asset                   string
	BaseRPCURL              string
	WalletRPCURL            string
	MaxGasLimit             uint64
	MaxFeePerGasWei         string
	MaxPriorityFeePerGasWei string
	RequestTimeout          time.Duration
	HTTPClient              *http.Client
}

// ClefAdapter prepares a direct-USDC EIP-1559 transaction through a
// customer-run Clef-compatible account_signTransaction endpoint, validates the
// returned signed bytes independently, and broadcasts only those exact bytes.
type ClefAdapter struct {
	chainID              *big.Int
	sender               common.Address
	asset                common.Address
	maxGasLimit          uint64
	maxFeePerGas         *big.Int
	maxPriorityFeePerGas *big.Int
	base                 *rpcClient
	wallet               *rpcClient
}

func NewClefAdapter(cfg ClefConfig) (*ClefAdapter, error) {
	if cfg.ChainID != 8453 && cfg.ChainID != 84532 {
		return nil, errors.New("wallet adapter supports Base mainnet or Base Sepolia only")
	}
	sender, err := canonicalAddress(cfg.Sender)
	if err != nil {
		return nil, fmt.Errorf("sender: %w", err)
	}
	asset, err := canonicalAddress(cfg.Asset)
	if err != nil {
		return nil, fmt.Errorf("asset: %w", err)
	}
	if cfg.MaxGasLimit < 21_000 || cfg.MaxGasLimit > 2_000_000 {
		return nil, errors.New("max gas limit must be between 21000 and 2000000")
	}
	maxFee, err := positiveUint256(cfg.MaxFeePerGasWei)
	if err != nil {
		return nil, fmt.Errorf("max fee per gas: %w", err)
	}
	maxPriority, err := positiveUint256(cfg.MaxPriorityFeePerGasWei)
	if err != nil {
		return nil, fmt.Errorf("max priority fee per gas: %w", err)
	}
	if maxPriority.Cmp(maxFee) > 0 {
		return nil, errors.New("max priority fee cannot exceed max fee")
	}
	if cfg.RequestTimeout <= 0 || cfg.RequestTimeout > time.Minute {
		return nil, errors.New("request timeout must be between 1ns and 1m")
	}
	base, err := newRPCClient(cfg.BaseRPCURL, rpcRemoteBase, cfg.RequestTimeout, cfg.HTTPClient)
	if err != nil {
		return nil, fmt.Errorf("Base RPC: %w", err)
	}
	wallet, err := newRPCClient(cfg.WalletRPCURL, rpcLocalWallet, cfg.RequestTimeout, cfg.HTTPClient)
	if err != nil {
		return nil, fmt.Errorf("wallet RPC: %w", err)
	}
	return &ClefAdapter{
		chainID: big.NewInt(int64(cfg.ChainID)), sender: sender, asset: asset,
		maxGasLimit: cfg.MaxGasLimit, maxFeePerGas: maxFee,
		maxPriorityFeePerGas: maxPriority, base: base, wallet: wallet,
	}, nil
}

func (a *ClefAdapter) Prepare(ctx context.Context, authorized referencesigner.Authorized) (referencesigner.PreparedTransaction, error) {
	auth := authorized.Authorization
	if auth.Rail != envelope.RailDirect || auth.ChainID != a.chainID.Uint64() || auth.Asset != strings.ToLower(a.asset.Hex()) {
		return referencesigner.PreparedTransaction{}, errors.New("authorization does not match the configured direct-USDC wallet")
	}
	recipient, err := canonicalAddress(auth.Recipient)
	if err != nil {
		return referencesigner.PreparedTransaction{}, errors.New("authorization recipient is invalid")
	}
	amount, err := positiveUint256(auth.AmountAtomic)
	if err != nil {
		return referencesigner.PreparedTransaction{}, errors.New("authorization amount is invalid")
	}
	data := transferData(recipient, amount)

	var chainHex string
	if err := a.base.call(ctx, "eth_chainId", []any{}, &chainHex); err != nil {
		return referencesigner.PreparedTransaction{}, err
	}
	chainID, err := parseQuantity(chainHex)
	if err != nil || chainID.Cmp(a.chainID) != 0 {
		return referencesigner.PreparedTransaction{}, errors.New("Base RPC returned the wrong chain ID")
	}

	from := strings.ToLower(a.sender.Hex())
	to := strings.ToLower(a.asset.Hex())
	call := transactionArgs{From: from, To: to, Value: "0x0", Data: "0x" + fmt.Sprintf("%x", data)}
	var simulation string
	if err := a.base.call(ctx, "eth_call", []any{call, "latest"}, &simulation); err != nil {
		return referencesigner.PreparedTransaction{}, errors.New("direct-USDC simulation failed")
	}
	if simulation != "0x" && simulation != "0x1" && simulation != "0x"+strings.Repeat("0", 63)+"1" {
		return referencesigner.PreparedTransaction{}, errors.New("direct-USDC simulation returned an unexpected result")
	}

	var nonceHex, priorityHex string
	if err := a.base.call(ctx, "eth_getTransactionCount", []any{from, "pending"}, &nonceHex); err != nil {
		return referencesigner.PreparedTransaction{}, err
	}
	if err := a.base.call(ctx, "eth_maxPriorityFeePerGas", []any{}, &priorityHex); err != nil {
		return referencesigner.PreparedTransaction{}, err
	}
	var block struct {
		BaseFeePerGas string `json:"baseFeePerGas"`
	}
	if err := a.base.call(ctx, "eth_getBlockByNumber", []any{"latest", false}, &block); err != nil {
		return referencesigner.PreparedTransaction{}, err
	}
	nonce, err := parseQuantity(nonceHex)
	if err != nil || !nonce.IsUint64() {
		return referencesigner.PreparedTransaction{}, errors.New("Base RPC returned an invalid pending nonce")
	}
	priority, err := parseQuantity(priorityHex)
	if err != nil || priority.Sign() <= 0 || priority.Cmp(a.maxPriorityFeePerGas) > 0 {
		return referencesigner.PreparedTransaction{}, errors.New("Base priority fee exceeds the customer cap")
	}
	baseFee, err := parseQuantity(block.BaseFeePerGas)
	if err != nil || baseFee.Sign() < 0 {
		return referencesigner.PreparedTransaction{}, errors.New("Base RPC returned an invalid base fee")
	}
	feeCap := new(big.Int).Add(new(big.Int).Mul(baseFee, big.NewInt(2)), priority)
	if feeCap.Cmp(a.maxFeePerGas) > 0 {
		return referencesigner.PreparedTransaction{}, errors.New("Base fee cap exceeds the customer cap")
	}

	estimateArgs := call
	estimateArgs.MaxFeePerGas = quantity(feeCap)
	estimateArgs.MaxPriorityFeePerGas = quantity(priority)
	var gasHex string
	if err := a.base.call(ctx, "eth_estimateGas", []any{estimateArgs}, &gasHex); err != nil {
		return referencesigner.PreparedTransaction{}, errors.New("direct-USDC gas estimation failed")
	}
	gasEstimate, err := parseQuantity(gasHex)
	if err != nil || !gasEstimate.IsUint64() || gasEstimate.Sign() <= 0 {
		return referencesigner.PreparedTransaction{}, errors.New("Base RPC returned an invalid gas estimate")
	}
	gasLimit, ok := bufferedGas(gasEstimate.Uint64(), defaultGasBuffer, a.maxGasLimit)
	if !ok {
		return referencesigner.PreparedTransaction{}, errors.New("direct-USDC gas estimate exceeds the customer cap")
	}

	signRequest := transactionArgs{
		From: from, To: to, Value: "0x0", Data: call.Data,
		Nonce: quantity(nonce), Gas: quantity(new(big.Int).SetUint64(gasLimit)),
		MaxFeePerGas: quantity(feeCap), MaxPriorityFeePerGas: quantity(priority),
		ChainID: quantity(a.chainID),
	}
	var signed struct {
		Raw string `json:"raw"`
	}
	if err := a.wallet.call(ctx, "account_signTransaction", []any{signRequest, "transfer(address,uint256)"}, &signed); err != nil {
		return referencesigner.PreparedTransaction{}, errors.New("customer wallet refused transaction signing")
	}
	return a.validateSigned(signed.Raw, recipient, amount, nonce.Uint64(), gasLimit, feeCap, priority)
}

func (a *ClefAdapter) Broadcast(ctx context.Context, prepared referencesigner.PreparedTransaction) error {
	if len(prepared.RawTransaction) == 0 {
		return errors.New("prepared transaction is empty")
	}
	var returned string
	if err := a.base.call(ctx, "eth_sendRawTransaction", []any{"0x" + fmt.Sprintf("%x", prepared.RawTransaction)}, &returned); err != nil {
		return errors.New("Base broadcast result is unknown")
	}
	if strings.ToLower(returned) != prepared.TransactionHash {
		return errors.New("Base RPC returned a different transaction hash")
	}
	return nil
}

func (a *ClefAdapter) validateSigned(rawHex string, recipient common.Address, amount *big.Int, nonce, gas uint64, feeCap, priority *big.Int) (referencesigner.PreparedTransaction, error) {
	raw, err := decodeCanonicalHex(rawHex)
	if err != nil {
		return referencesigner.PreparedTransaction{}, errors.New("customer wallet returned invalid signed bytes")
	}
	var tx types.Transaction
	if err := tx.UnmarshalBinary(raw); err != nil {
		return referencesigner.PreparedTransaction{}, errors.New("customer wallet returned an undecodable transaction")
	}
	if tx.Type() != types.DynamicFeeTxType || tx.ChainId().Cmp(a.chainID) != 0 || tx.Nonce() != nonce || tx.Gas() != gas || tx.Value().Sign() != 0 || tx.GasFeeCap().Cmp(feeCap) != 0 || tx.GasTipCap().Cmp(priority) != 0 || len(tx.AccessList()) != 0 {
		return referencesigner.PreparedTransaction{}, errors.New("customer wallet changed transaction authority or fee fields")
	}
	if tx.To() == nil || *tx.To() != a.asset || string(tx.Data()) != string(transferData(recipient, amount)) {
		return referencesigner.PreparedTransaction{}, errors.New("customer wallet changed the direct-USDC transfer")
	}
	sender, err := types.Sender(types.LatestSignerForChainID(a.chainID), &tx)
	if err != nil || sender != a.sender {
		return referencesigner.PreparedTransaction{}, errors.New("customer wallet signed with the wrong account")
	}
	return referencesigner.PreparedTransaction{
		RawTransaction: raw, TransactionHash: strings.ToLower(tx.Hash().Hex()), Sender: strings.ToLower(sender.Hex()),
	}, nil
}

type transactionArgs struct {
	From                 string `json:"from,omitempty"`
	To                   string `json:"to"`
	Gas                  string `json:"gas,omitempty"`
	MaxFeePerGas         string `json:"maxFeePerGas,omitempty"`
	MaxPriorityFeePerGas string `json:"maxPriorityFeePerGas,omitempty"`
	Value                string `json:"value"`
	Data                 string `json:"data"`
	Nonce                string `json:"nonce,omitempty"`
	ChainID              string `json:"chainId,omitempty"`
}

func transferData(recipient common.Address, amount *big.Int) []byte {
	data := make([]byte, 4+32+32)
	copy(data[:4], common.FromHex("0x"+transferSelector))
	copy(data[4+12:4+32], recipient.Bytes())
	amount.FillBytes(data[4+32:])
	return data
}

func bufferedGas(estimate, percentage, cap uint64) (uint64, bool) {
	if estimate == 0 || estimate > cap || estimate > ^uint64(0)/(100+percentage) {
		return 0, false
	}
	buffered := (estimate*(100+percentage) + 99) / 100
	return buffered, buffered <= cap
}

func canonicalAddress(value string) (common.Address, error) {
	if !common.IsHexAddress(value) || value != strings.ToLower(value) || value != strings.ToLower(common.HexToAddress(value).Hex()) {
		return common.Address{}, errors.New("must be a canonical lowercase 20-byte EVM address")
	}
	return common.HexToAddress(value), nil
}

func positiveUint256(value string) (*big.Int, error) {
	if value == "" || value[0] == '0' {
		return nil, errors.New("must be a canonical positive integer")
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			return nil, errors.New("must contain decimal digits only")
		}
	}
	n, ok := new(big.Int).SetString(value, 10)
	if !ok || n.Sign() <= 0 || n.BitLen() > 256 {
		return nil, errors.New("must fit uint256")
	}
	return n, nil
}

func parseQuantity(value string) (*big.Int, error) {
	if value == "0x0" {
		return new(big.Int), nil
	}
	if len(value) < 3 || !strings.HasPrefix(value, "0x") || value[2] == '0' || strings.ToLower(value) != value {
		return nil, errors.New("invalid canonical JSON-RPC quantity")
	}
	n, ok := new(big.Int).SetString(value[2:], 16)
	if !ok || n.Sign() < 0 || n.BitLen() > 256 {
		return nil, errors.New("invalid JSON-RPC quantity")
	}
	return n, nil
}

func quantity(value *big.Int) string { return "0x" + value.Text(16) }
