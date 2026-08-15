package x402experiment

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/gnanam1990/flowops/internal/x402adapter"
)

var transferTopic = common.HexToHash("0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef")

type ChainClient interface {
	TransactionByHash(context.Context, common.Hash) (*types.Transaction, bool, error)
	TransactionReceipt(context.Context, common.Hash) (*types.Receipt, error)
}

type ChainProof struct {
	TransactionHash   string                          `json:"transactionHash"`
	TransactionSender string                          `json:"transactionSender"`
	BlockNumber       uint64                          `json:"blockNumber"`
	BlockHash         string                          `json:"blockHash"`
	CompleteCalldata  string                          `json:"completeCalldata"`
	TransferVerified  bool                            `json:"usdcTransferVerified"`
	Attribution       x402adapter.AttributionEvidence `json:"attribution"`
	CanonicalRPCs     int                             `json:"canonicalRpcs"`
}

func Inspect(ctx context.Context, hash common.Hash, expectedSigner string, preparation Preparation, clients ...ChainClient) (ChainProof, error) {
	if len(clients) < 2 {
		return ChainProof{}, errors.New("two independent Base Sepolia RPC clients are required")
	}
	if err := Validate(preparation, preparation.CreatedAt); err != nil {
		return ChainProof{}, fmt.Errorf("preparation: %w", err)
	}
	if !common.IsHexAddress(expectedSigner) {
		return ChainProof{}, errors.New("facilitator signer is invalid")
	}
	var canonicalTx *types.Transaction
	var canonicalReceipt *types.Receipt
	for index, client := range clients {
		tx, pending, err := client.TransactionByHash(ctx, hash)
		if err != nil || pending || tx == nil {
			return ChainProof{}, fmt.Errorf("RPC %d transaction is not canonically included: %w", index+1, err)
		}
		receipt, err := client.TransactionReceipt(ctx, hash)
		if err != nil || receipt == nil {
			return ChainProof{}, fmt.Errorf("RPC %d receipt unavailable: %w", index+1, err)
		}
		if receipt.Status != types.ReceiptStatusSuccessful || receipt.BlockHash == (common.Hash{}) || receipt.TxHash != hash {
			return ChainProof{}, fmt.Errorf("RPC %d receipt is not a successful canonical receipt", index+1)
		}
		if index == 0 {
			canonicalTx, canonicalReceipt = tx, receipt
			continue
		}
		if tx.Hash() != canonicalTx.Hash() || !bytesEqual(tx.Data(), canonicalTx.Data()) || !receiptsEqual(receipt, canonicalReceipt) {
			return ChainProof{}, fmt.Errorf("RPC %d disagrees with RPC 1", index+1)
		}
	}
	if canonicalTx.ChainId().Cmp(big.NewInt(ChainID)) != 0 || canonicalTx.To() == nil || *canonicalTx.To() != common.HexToAddress(Asset) {
		return ChainProof{}, errors.New("settlement transaction is not the fixed Base Sepolia USDC call")
	}
	sender, err := types.Sender(types.LatestSignerForChainID(canonicalTx.ChainId()), canonicalTx)
	if err != nil || sender != common.HexToAddress(expectedSigner) {
		return ChainProof{}, errors.New("transaction sender is not the advertised facilitator signer")
	}
	transferVerified := false
	for _, log := range canonicalReceipt.Logs {
		if log.Address != common.HexToAddress(Asset) || len(log.Topics) != 3 || log.Topics[0] != transferTopic || len(log.Data) != 32 {
			continue
		}
		from := common.BytesToAddress(log.Topics[1].Bytes()[12:])
		to := common.BytesToAddress(log.Topics[2].Bytes()[12:])
		amount := new(big.Int).SetBytes(log.Data)
		if from == common.HexToAddress(preparation.Payer) && to == common.HexToAddress(preparation.Payee) && amount.Cmp(big.NewInt(1000)) == 0 {
			transferVerified = true
			break
		}
	}
	if !transferVerified {
		return ChainProof{}, errors.New("canonical receipt lacks the exact payer-to-payee 1000-unit USDC Transfer")
	}
	calldata := "0x" + common.Bytes2Hex(canonicalTx.Data())
	attribution := x402adapter.ClassifyCalldata(calldata, preparation.AppCode, []string{preparation.ServiceCode}, true)
	if attribution.State != x402adapter.AttributionVerifiedSuffix {
		return ChainProof{}, fmt.Errorf("builder-code calldata proof failed: %s", attribution.Reason)
	}
	return ChainProof{
		TransactionHash: hash.Hex(), TransactionSender: sender.Hex(), BlockNumber: canonicalReceipt.BlockNumber.Uint64(),
		BlockHash: canonicalReceipt.BlockHash.Hex(), CompleteCalldata: calldata, TransferVerified: true,
		Attribution: attribution, CanonicalRPCs: len(clients),
	}, nil
}

func bytesEqual(left, right []byte) bool {
	return strings.EqualFold(common.Bytes2Hex(left), common.Bytes2Hex(right))
}

func receiptsEqual(left, right *types.Receipt) bool {
	if left.TxHash != right.TxHash || left.BlockHash != right.BlockHash || left.Status != right.Status || left.BlockNumber.Cmp(right.BlockNumber) != 0 || len(left.Logs) != len(right.Logs) {
		return false
	}
	for index := range left.Logs {
		leftLog, rightLog := left.Logs[index], right.Logs[index]
		if leftLog.Address != rightLog.Address || leftLog.Removed != rightLog.Removed || !bytesEqual(leftLog.Data, rightLog.Data) || len(leftLog.Topics) != len(rightLog.Topics) {
			return false
		}
		for topic := range leftLog.Topics {
			if leftLog.Topics[topic] != rightLog.Topics[topic] {
				return false
			}
		}
	}
	return true
}
