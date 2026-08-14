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
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/gnanam1990/flowops/pkg/envelope"
	"github.com/gnanam1990/flowops/pkg/referencesigner"
)

type EscrowClefConfig struct {
	ChainID                 uint64
	Sender                  string
	Asset                   string
	Contract                string
	ReleaseWindow           uint64
	BaseRPCURL              string
	WalletRPCURL            string
	MaxGasLimit             uint64
	MaxFeePerGasWei         string
	MaxPriorityFeePerGasWei string
	RequestTimeout          time.Duration
	HTTPClient              *http.Client
}

// EscrowClefAdapter prepares only the exact CallEscrow fund transaction named
// by an escrow authorization. It verifies the live deployment tuple, allowance,
// simulation, and Clef-returned signed bytes before any broadcast is possible.
type EscrowClefAdapter struct {
	core          *ClefAdapter
	contract      common.Address
	releaseWindow uint64
}

func NewEscrowClefAdapter(cfg EscrowClefConfig) (*EscrowClefAdapter, error) {
	if cfg.ReleaseWindow == 0 || cfg.ReleaseWindow > 30*24*60*60 {
		return nil, errors.New("escrow release window is invalid")
	}
	contract, err := canonicalAddress(cfg.Contract)
	if err != nil {
		return nil, fmt.Errorf("escrow contract: %w", err)
	}
	core, err := NewClefAdapter(ClefConfig{
		ChainID: cfg.ChainID, Sender: cfg.Sender, Asset: cfg.Asset,
		BaseRPCURL: cfg.BaseRPCURL, WalletRPCURL: cfg.WalletRPCURL,
		MaxGasLimit: cfg.MaxGasLimit, MaxFeePerGasWei: cfg.MaxFeePerGasWei,
		MaxPriorityFeePerGasWei: cfg.MaxPriorityFeePerGasWei,
		RequestTimeout:          cfg.RequestTimeout, HTTPClient: cfg.HTTPClient,
	})
	if err != nil {
		return nil, err
	}
	return &EscrowClefAdapter{core: core, contract: contract, releaseWindow: cfg.ReleaseWindow}, nil
}

func (a *EscrowClefAdapter) Prepare(ctx context.Context, authorized referencesigner.Authorized) (referencesigner.PreparedTransaction, error) {
	auth := authorized.Authorization
	terms := auth.Escrow
	if auth.Rail != envelope.RailEscrow || terms == nil || auth.ChainID != a.core.chainID.Uint64() ||
		auth.Asset != strings.ToLower(a.core.asset.Hex()) || terms.Contract != strings.ToLower(a.contract.Hex()) ||
		terms.Buyer != strings.ToLower(a.core.sender.Hex()) || terms.Provider != auth.Recipient || terms.ReleaseWindow != a.releaseWindow {
		return referencesigner.PreparedTransaction{}, errors.New("authorization does not match the configured CallEscrow wallet")
	}
	if err := auth.Validate(); err != nil {
		return referencesigner.PreparedTransaction{}, errors.New("escrow authorization is invalid")
	}
	amount, err := positiveUint256(auth.AmountAtomic)
	if err != nil {
		return referencesigner.PreparedTransaction{}, errors.New("authorization amount is invalid")
	}
	if err := a.core.ensureChain(ctx); err != nil {
		return referencesigner.PreparedTransaction{}, err
	}
	if err := a.verifyDeployment(ctx); err != nil {
		return referencesigner.PreparedTransaction{}, err
	}
	if err := a.verifyAllowance(ctx, amount); err != nil {
		return referencesigner.PreparedTransaction{}, err
	}
	data := fundData(terms, amount)
	return a.core.prepareExact(ctx, a.contract, data, "fund(bytes32,address,uint256,bytes32,bytes32,uint64,uint64)", "CallEscrow fund")
}

func (a *EscrowClefAdapter) Broadcast(ctx context.Context, prepared referencesigner.PreparedTransaction) error {
	return a.core.Broadcast(ctx, prepared)
}

func (a *EscrowClefAdapter) verifyDeployment(ctx context.Context) error {
	contract := strings.ToLower(a.contract.Hex())
	var code string
	rawCode, decodeErr := []byte(nil), error(nil)
	if err := a.core.base.call(ctx, "eth_getCode", []any{contract, "latest"}, &code); err == nil {
		rawCode, decodeErr = decodeCanonicalHex(code)
	} else {
		decodeErr = err
	}
	if decodeErr != nil || len(rawCode) == 0 {
		return errors.New("configured CallEscrow deployment is unavailable")
	}
	asset, err := a.viewUint256(ctx, contract, selector("asset()"))
	if err != nil || asset.BitLen() > 160 || common.BigToAddress(asset) != a.core.asset {
		return errors.New("live CallEscrow asset does not match the configured tuple")
	}
	window, err := a.viewUint256(ctx, contract, selector("optimisticReleaseWindow()"))
	if err != nil || !window.IsUint64() || window.Uint64() != a.releaseWindow {
		return errors.New("live CallEscrow release window does not match the configured tuple")
	}
	return nil
}

func (a *EscrowClefAdapter) verifyAllowance(ctx context.Context, amount *big.Int) error {
	data := make([]byte, 4+64)
	copy(data[:4], selector("allowance(address,address)"))
	copy(data[4+12:4+32], a.core.sender.Bytes())
	copy(data[4+32+12:], a.contract.Bytes())
	allowance, err := a.viewUint256(ctx, strings.ToLower(a.core.asset.Hex()), data)
	if err != nil {
		return errors.New("USDC allowance check failed")
	}
	if allowance.Cmp(amount) != 0 {
		return errors.New("USDC allowance must equal the exact escrow funding amount")
	}
	return nil
}

func (a *EscrowClefAdapter) viewUint256(ctx context.Context, target string, data []byte) (*big.Int, error) {
	call := transactionArgs{To: target, Value: "0x0", Data: "0x" + fmt.Sprintf("%x", data)}
	var result string
	if err := a.core.base.call(ctx, "eth_call", []any{call, "latest"}, &result); err != nil {
		return nil, err
	}
	raw, err := decodeCanonicalHex(result)
	if err != nil || len(raw) != 32 {
		return nil, errors.New("contract view returned a non-canonical uint256")
	}
	return new(big.Int).SetBytes(raw), nil
}

func fundData(terms *envelope.EscrowTerms, amount *big.Int) []byte {
	data := make([]byte, 4+32*7)
	copy(data[:4], selector("fund(bytes32,address,uint256,bytes32,bytes32,uint64,uint64)"))
	copyHexWord(data[4:4+32], terms.CallID)
	provider := common.HexToAddress(terms.Provider)
	copy(data[4+32+12:4+64], provider.Bytes())
	amount.FillBytes(data[4+64 : 4+96])
	copyHexWord(data[4+96:4+128], terms.TaskDigest)
	copyHexWord(data[4+128:4+160], terms.RequestDigest)
	new(big.Int).SetUint64(terms.AcknowledgeBy).FillBytes(data[4+160 : 4+192])
	new(big.Int).SetUint64(terms.DeliverBy).FillBytes(data[4+192 : 4+224])
	return data
}

func selector(signature string) []byte { return crypto.Keccak256([]byte(signature))[:4] }

func copyHexWord(destination []byte, value string) { copy(destination, common.FromHex(value)) }
