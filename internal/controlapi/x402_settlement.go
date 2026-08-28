package controlapi

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/gnanam1990/flowops/internal/controlplane"
	"github.com/gnanam1990/flowops/internal/reconciliation"
	"github.com/gnanam1990/flowops/internal/x402adapter"
	"github.com/gnanam1990/flowops/pkg/broadcastreceipt"
	"github.com/gnanam1990/flowops/pkg/envelope"
	x402 "github.com/x402-foundation/x402/go/v2"
)

var (
	ErrX402SettlementBinding = errors.New("x402 settlement does not match an issued authorization")
	ErrX402SettlementResult  = errors.New("x402 settlement result is invalid")
)

type X402SettlementReconciler interface {
	RegisterX402Settlement(context.Context, reconciliation.ExpectedExecution, reconciliation.X402SettlementClaim) (reconciliation.Execution, error)
}

type X402SettlementRegistrar struct {
	lifecycle  *controlplane.Lifecycle
	keys       *StaticBroadcastKeys
	reconciler X402SettlementReconciler
	clock      func() time.Time
}

type X402SettlementRequest struct {
	Settlement    x402.SettleResponse            `json:"settlement"`
	SignedReceipt broadcastreceipt.SignedReceipt `json:"signedReceipt"`
}

func NewX402SettlementRegistrar(lifecycle *controlplane.Lifecycle, keys *StaticBroadcastKeys, reconciler X402SettlementReconciler, clock func() time.Time) (*X402SettlementRegistrar, error) {
	if lifecycle == nil || keys == nil || reconciler == nil {
		return nil, errors.New("lifecycle, broadcast keys, and x402 settlement reconciler are required")
	}
	if clock == nil {
		clock = time.Now
	}
	return &X402SettlementRegistrar{lifecycle: lifecycle, keys: keys, reconciler: reconciler, clock: clock}, nil
}

func (r *X402SettlementRegistrar) Validate(organizationID, agentID, authorizationID string, request X402SettlementRequest) error {
	_, _, err := r.prepare(organizationID, agentID, authorizationID, request)
	return err
}

func (r *X402SettlementRegistrar) Register(ctx context.Context, organizationID, agentID, authorizationID string, request X402SettlementRequest) (reconciliation.Execution, error) {
	expected, claim, err := r.prepare(organizationID, agentID, authorizationID, request)
	if err != nil {
		return reconciliation.Execution{}, err
	}
	return r.reconciler.RegisterX402Settlement(ctx, expected, claim)
}

func (r *X402SettlementRegistrar) prepare(organizationID, agentID, authorizationID string, request X402SettlementRequest) (reconciliation.ExpectedExecution, reconciliation.X402SettlementClaim, error) {
	record, authorization, publicKey, err := validateBroadcastBinding(r.lifecycle, r.keys, r.clock, request.SignedReceipt)
	if err != nil {
		return reconciliation.ExpectedExecution{}, reconciliation.X402SettlementClaim{}, err
	}
	if authorization.AuthorizationID != authorizationID || record.Intent.OrganizationID != organizationID || (agentID != "" && record.Intent.AgentID != agentID) {
		return reconciliation.ExpectedExecution{}, reconciliation.X402SettlementClaim{}, ErrX402SettlementBinding
	}
	if authorization.Rail != envelope.RailX402 || authorization.Escrow != nil {
		return reconciliation.ExpectedExecution{}, reconciliation.X402SettlementClaim{}, ErrX402SettlementBinding
	}
	settled := request.Settlement
	wantNetwork := x402adapter.BaseMainnetNetwork
	if authorization.ChainID == x402adapter.BaseSepoliaChainID {
		wantNetwork = x402adapter.BaseSepoliaNetwork
	}
	payer, payerErr := envelope.NormalizeAddress(settled.Payer)
	transaction := strings.ToLower(strings.TrimSpace(settled.Transaction))
	if !settled.Success || payerErr != nil || payer != settled.Payer || settled.Network != x402.Network(wantNetwork) ||
		len(transaction) != 66 || !strings.HasPrefix(transaction, "0x") || transaction != settled.Transaction ||
		(settled.Amount != "" && settled.Amount != authorization.AmountAtomic) {
		return reconciliation.ExpectedExecution{}, reconciliation.X402SettlementClaim{}, ErrX402SettlementResult
	}
	receipt := request.SignedReceipt.Receipt
	if receipt.TransactionHash != transaction || receipt.Sender != payer {
		return reconciliation.ExpectedExecution{}, reconciliation.X402SettlementClaim{}, ErrX402SettlementBinding
	}
	if _, err := hex.DecodeString(transaction[2:]); err != nil {
		return reconciliation.ExpectedExecution{}, reconciliation.X402SettlementClaim{}, ErrX402SettlementResult
	}
	expected := reconciliation.ExpectedExecution{
		ExecutionID: controlplane.ExecutionID(authorization.AuthorizationID), OrganizationID: authorization.OrganizationID,
		AgentID: authorization.AgentID, TaskID: authorization.TaskID, IntentDigest: record.IntentDigest,
		TransactionHash: transaction, ChainID: authorization.ChainID, Sender: payer,
		Asset: authorization.Asset, Recipient: authorization.Recipient, AmountAtomic: authorization.AmountAtomic,
	}
	claim := reconciliation.X402SettlementClaim{
		Authorization: authorization, Success: settled.Success, Payer: payer, Transaction: transaction,
		Network: string(settled.Network), Amount: settled.Amount, SignedReceipt: request.SignedReceipt,
		PublicKeyB64: base64.StdEncoding.EncodeToString(publicKey),
	}
	return expected, claim, nil
}
