// Package ascpsettlement joins ASCP bearer activation, independent Base
// receipt evidence, conservative reservation states, and classified postings.
package ascpsettlement

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gnanam1990/flowops/internal/reconciliation"
)

type OperationState string

const (
	AuthSigned           OperationState = "AUTH_SIGNED"
	LockSubmitted        OperationState = "LOCK_SUBMITTED"
	LockedSafe           OperationState = "LOCKED_SAFE"
	LockedFinalized      OperationState = "LOCKED_FINALIZED"
	ReleasedFinalized    OperationState = "RELEASED_FINALIZED"
	RefundedFinalized    OperationState = "REFUNDED_FINALIZED"
	ReorgedBack          OperationState = "REORGED_BACK"
	PendingChainRecovery OperationState = "PENDING_CHAIN_RECOVERY"
	Quarantined          OperationState = "QUARANTINED"
)

type AttemptState string

const (
	AttemptSubmitted AttemptState = "SUBMITTED"
	AttemptSafe      AttemptState = "CONFIRMED_SAFE"
	AttemptFinalized AttemptState = "FINALIZED"
	AttemptReverted  AttemptState = "REVERTED"
	AttemptReorged   AttemptState = "REORGED"
)

type Finality string

const (
	Safe      Finality = "SAFE"
	Finalized Finality = "FINALIZED"
)

var (
	ErrInvalidConfiguration = errors.New("invalid ASCP settlement configuration")
	ErrInvalidAttempt       = errors.New("invalid ASCP payment attempt")
	ErrNotFound             = errors.New("ASCP payment operation not found")
	ErrStateConflict        = errors.New("ASCP payment operation state conflict")
	ErrQuorumUnavailable    = errors.New("ASCP receipt quorum unavailable")
	ErrObserverDisagreement = errors.New("ASCP receipt observers disagree")
	ErrUnsafeFinality       = errors.New("ASCP receipt has insufficient finality")
	ErrInvalidResult        = errors.New("invalid sealed ASCP receipt result")
	ErrInvalidReorgResult   = errors.New("invalid sealed ASCP reorg result")
)

type AttemptInput struct {
	OperationID     string                           `json:"operationId"`
	Action          reconciliation.ASCPReceiptAction `json:"action"`
	TransactionHash string                           `json:"transactionHash"`
	DeliveryHash    string                           `json:"deliveryHash,omitempty"`
	EvidenceHash    string                           `json:"evidenceHash,omitempty"`
}

type Attempt struct {
	AttemptInput
	State              AttemptState `json:"state"`
	RegisteredAt       time.Time    `json:"registeredAt"`
	ResolvedAt         time.Time    `json:"resolvedAt,omitempty"`
	BlockNumber        uint64       `json:"blockNumber,omitempty"`
	BlockHash          string       `json:"blockHash,omitempty"`
	EvidenceDigest     string       `json:"evidenceDigest,omitempty"`
	CanonicalCheckedAt time.Time    `json:"canonicalCheckedAt,omitempty"`
}

type Operation struct {
	OperationID             string         `json:"operationId"`
	OrganizationID          string         `json:"organizationId"`
	AgentID                 string         `json:"agentId"`
	AuthorizationID         string         `json:"authorizationId"`
	ReservationID           string         `json:"reservationId"`
	BearerDigest            string         `json:"bearerDigest"`
	CommitmentHash          string         `json:"commitmentHash"`
	CallID                  string         `json:"callId"`
	ChainID                 uint64         `json:"chainId"`
	EscrowContract          string         `json:"escrowContract"`
	Asset                   string         `json:"asset"`
	Buyer                   string         `json:"buyer"`
	PayTo                   string         `json:"payTo"`
	AmountBaseUnits         string         `json:"amountBaseUnits"`
	SettleBy                time.Time      `json:"settleBy"`
	State                   OperationState `json:"state"`
	LockedTransactionHash   string         `json:"lockedTransactionHash,omitempty"`
	LockedBlockNumber       uint64         `json:"lockedBlockNumber,omitempty"`
	LockedBlockHash         string         `json:"lockedBlockHash,omitempty"`
	TerminalAction          string         `json:"terminalAction,omitempty"`
	TerminalTransactionHash string         `json:"terminalTransactionHash,omitempty"`
	TerminalBlockNumber     uint64         `json:"terminalBlockNumber,omitempty"`
	TerminalBlockHash       string         `json:"terminalBlockHash,omitempty"`
	CreatedAt               time.Time      `json:"createdAt"`
	UpdatedAt               time.Time      `json:"updatedAt"`
}

type Result struct {
	Expected       reconciliation.ASCPExpectedReceipt `json:"expected"`
	Finality       Finality                           `json:"finality"`
	Success        bool                               `json:"success"`
	BlockNumber    uint64                             `json:"blockNumber"`
	BlockHash      string                             `json:"blockHash"`
	ConfirmedHead  uint64                             `json:"confirmedHead"`
	Providers      []string                           `json:"providers"`
	EvidenceDigest string                             `json:"evidenceDigest"`
	ObservedAt     time.Time                          `json:"observedAt"`
	verified       bool
	seal           [32]byte
}

// ReorgResult is produced only by Reader after independent providers agree on
// the canonical hash currently occupying a finalized receipt's block number.
// The unexported seal prevents callers from manufacturing accounting reversals.
type ReorgResult struct {
	OperationID        string                           `json:"operationId"`
	Action             reconciliation.ASCPReceiptAction `json:"action"`
	TransactionHash    string                           `json:"transactionHash"`
	BlockNumber        uint64                           `json:"blockNumber"`
	OriginalBlockHash  string                           `json:"originalBlockHash"`
	CanonicalBlockHash string                           `json:"canonicalBlockHash"`
	ObservedHead       uint64                           `json:"observedHead"`
	Providers          []string                         `json:"providers"`
	EvidenceDigest     string                           `json:"evidenceDigest"`
	ObservedAt         time.Time                        `json:"observedAt"`
	Reorged            bool                             `json:"reorged"`
	verified           bool
	seal               [32]byte
}

type ReaderConfig struct {
	Observers              *reconciliation.ObserverSet
	Quorum                 int
	SafeConfirmations      uint64
	FinalizedConfirmations uint64
	Clock                  func() time.Time
}

type Reader struct {
	observers              *reconciliation.ObserverSet
	quorum                 int
	safeConfirmations      uint64
	finalizedConfirmations uint64
	clock                  func() time.Time
}

func NewReader(cfg ReaderConfig) (*Reader, error) {
	if cfg.Observers == nil || cfg.Quorum < 2 || cfg.Quorum > 5 || cfg.SafeConfirmations == 0 ||
		cfg.FinalizedConfirmations < cfg.SafeConfirmations {
		return nil, ErrInvalidConfiguration
	}
	if cfg.Clock == nil {
		cfg.Clock = time.Now
	}
	return &Reader{cfg.Observers, cfg.Quorum, cfg.SafeConfirmations, cfg.FinalizedConfirmations, cfg.Clock}, nil
}

func (r *Reader) Read(ctx context.Context, expected reconciliation.ASCPExpectedReceipt) (Result, error) {
	if err := expected.Validate(); err != nil {
		return Result{}, ErrInvalidAttempt
	}
	raw := r.observers.ASCPReceiptQuorum(ctx, expected)
	if len(raw.Evidence) < r.quorum {
		return Result{}, fmt.Errorf("%w: got %d, need %d", ErrQuorumUnavailable, len(raw.Evidence), r.quorum)
	}
	canonical := raw.Evidence[0]
	providers := make([]string, 0, len(raw.Evidence))
	seen := make(map[string]struct{}, len(raw.Evidence))
	confirmedHead := canonical.ConfirmedHead
	for _, evidence := range raw.Evidence {
		if _, duplicate := seen[evidence.Provider]; duplicate {
			return Result{}, ErrObserverDisagreement
		}
		seen[evidence.Provider] = struct{}{}
		providers = append(providers, evidence.Provider)
		if evidence.Action != canonical.Action || evidence.ChainID != canonical.ChainID ||
			evidence.TransactionHash != canonical.TransactionHash || evidence.BlockNumber != canonical.BlockNumber ||
			evidence.BlockHash != canonical.BlockHash || evidence.Success != canonical.Success ||
			evidence.CallID != canonical.CallID || evidence.OperationID != canonical.OperationID {
			return Result{}, ErrObserverDisagreement
		}
		if evidence.ConfirmedHead < confirmedHead {
			confirmedHead = evidence.ConfirmedHead
		}
	}
	if canonical.BlockNumber == 0 || confirmedHead < canonical.BlockNumber {
		return Result{}, ErrUnsafeFinality
	}
	confirmations := confirmedHead - canonical.BlockNumber + 1
	finality := Safe
	if confirmations >= r.finalizedConfirmations {
		finality = Finalized
	} else if confirmations < r.safeConfirmations {
		return Result{}, ErrUnsafeFinality
	}
	result := Result{
		Expected: expected, Finality: finality, Success: canonical.Success,
		BlockNumber: canonical.BlockNumber, BlockHash: canonical.BlockHash, ConfirmedHead: confirmedHead,
		Providers: providers, ObservedAt: r.clock().UTC(), verified: true,
	}
	result.EvidenceDigest = resultDigest(result)
	result.seal = resultSeal(result)
	return result, nil
}

func (r *Reader) CheckCanonical(ctx context.Context, operationID string, action reconciliation.ASCPReceiptAction, transactionHash string, blockNumber uint64, blockHash string) (ReorgResult, error) {
	if !hash(operationID) || (action != reconciliation.ASCPReceiptLock && action != reconciliation.ASCPReceiptRelease && action != reconciliation.ASCPReceiptRefund) ||
		!hash(transactionHash) || blockNumber == 0 || !hash(blockHash) {
		return ReorgResult{}, ErrInvalidAttempt
	}
	raw := r.observers.ASCPCanonicalBlockQuorum(ctx, transactionHash, blockNumber, blockHash)
	if len(raw.Evidence) < r.quorum {
		return ReorgResult{}, fmt.Errorf("%w: got %d, need %d", ErrQuorumUnavailable, len(raw.Evidence), r.quorum)
	}
	canonical := raw.Evidence[0]
	providers := make([]string, 0, len(raw.Evidence))
	seen := make(map[string]struct{}, len(raw.Evidence))
	head := canonical.ObservedHead
	for _, evidence := range raw.Evidence {
		if _, duplicate := seen[evidence.Provider]; duplicate {
			return ReorgResult{}, ErrObserverDisagreement
		}
		seen[evidence.Provider] = struct{}{}
		providers = append(providers, evidence.Provider)
		if evidence.ChainID != canonical.ChainID || evidence.TransactionHash != transactionHash ||
			evidence.OriginalBlockNumber != blockNumber || evidence.OriginalBlockHash != blockHash ||
			evidence.CanonicalBlockHash != canonical.CanonicalBlockHash {
			return ReorgResult{}, ErrObserverDisagreement
		}
		if evidence.ObservedHead < head {
			head = evidence.ObservedHead
		}
	}
	if head < blockNumber || head-blockNumber+1 < r.finalizedConfirmations || !hash(canonical.CanonicalBlockHash) {
		return ReorgResult{}, ErrUnsafeFinality
	}
	result := ReorgResult{
		OperationID: operationID, Action: action, TransactionHash: transactionHash,
		BlockNumber: blockNumber, OriginalBlockHash: blockHash, CanonicalBlockHash: canonical.CanonicalBlockHash,
		ObservedHead: head, Providers: providers, ObservedAt: r.clock().UTC(),
		Reorged: canonical.CanonicalBlockHash != blockHash, verified: true,
	}
	result.EvidenceDigest = reorgDigest(result)
	result.seal = reorgSeal(result)
	return result, nil
}

func resultDigest(result Result) string {
	copy := result
	copy.EvidenceDigest, copy.verified, copy.seal = "", false, [32]byte{}
	encoded, _ := json.Marshal(copy)
	digest := sha256.Sum256(append([]byte("ASCP_CHAIN_OBSERVATION_V1\n"), encoded...))
	return "0x" + hex.EncodeToString(digest[:])
}

func resultSeal(result Result) [32]byte {
	copy := result
	copy.verified, copy.seal = false, [32]byte{}
	encoded, _ := json.Marshal(copy)
	return sha256.Sum256(append([]byte("ASCP_RECEIPT_RESULT_V1\n"), encoded...))
}

func validateResult(result Result) error {
	if !result.verified || result.seal != resultSeal(result) || result.EvidenceDigest != resultDigest(result) ||
		result.Expected.Validate() != nil || (result.Finality != Safe && result.Finality != Finalized) ||
		result.BlockNumber == 0 || result.ConfirmedHead < result.BlockNumber || !hash(result.BlockHash) ||
		result.ObservedAt.IsZero() || len(result.Providers) < 2 || len(result.Providers) > 5 {
		return ErrInvalidResult
	}
	seen := make(map[string]struct{}, len(result.Providers))
	for _, provider := range result.Providers {
		if !identifier(provider) {
			return ErrInvalidResult
		}
		if _, duplicate := seen[provider]; duplicate {
			return ErrInvalidResult
		}
		seen[provider] = struct{}{}
	}
	return nil
}

func reorgDigest(result ReorgResult) string {
	copy := result
	copy.EvidenceDigest, copy.verified, copy.seal = "", false, [32]byte{}
	encoded, _ := json.Marshal(copy)
	digest := sha256.Sum256(append([]byte("ASCP_REORG_OBSERVATION_V1\n"), encoded...))
	return "0x" + hex.EncodeToString(digest[:])
}

func reorgSeal(result ReorgResult) [32]byte {
	copy := result
	copy.verified, copy.seal = false, [32]byte{}
	encoded, _ := json.Marshal(copy)
	return sha256.Sum256(append([]byte("ASCP_REORG_RESULT_V1\n"), encoded...))
}

func validateReorgResult(result ReorgResult) error {
	if err := validateCanonicalResult(result); err != nil || !result.Reorged || result.OriginalBlockHash == result.CanonicalBlockHash {
		return ErrInvalidReorgResult
	}
	return nil
}

func validateCanonicalResult(result ReorgResult) error {
	if !result.verified || result.seal != reorgSeal(result) || result.EvidenceDigest != reorgDigest(result) ||
		!hash(result.OperationID) || !hash(result.TransactionHash) || result.BlockNumber == 0 ||
		!hash(result.OriginalBlockHash) || !hash(result.CanonicalBlockHash) || result.Reorged != (result.OriginalBlockHash != result.CanonicalBlockHash) ||
		result.ObservedHead < result.BlockNumber || result.ObservedAt.IsZero() || len(result.Providers) < 2 || len(result.Providers) > 5 ||
		(result.Action != reconciliation.ASCPReceiptLock && result.Action != reconciliation.ASCPReceiptRelease && result.Action != reconciliation.ASCPReceiptRefund) {
		return ErrInvalidReorgResult
	}
	seen := make(map[string]struct{}, len(result.Providers))
	for _, provider := range result.Providers {
		if !identifier(provider) {
			return ErrInvalidReorgResult
		}
		if _, duplicate := seen[provider]; duplicate {
			return ErrInvalidReorgResult
		}
		seen[provider] = struct{}{}
	}
	return nil
}

func hash(value string) bool {
	if len(value) != 66 || !strings.HasPrefix(value, "0x") || strings.ToLower(value) != value || value == "0x"+strings.Repeat("0", 64) {
		return false
	}
	_, err := hex.DecodeString(value[2:])
	return err == nil
}

func identifier(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for index, character := range value {
		if !(character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || index > 0 && strings.ContainsRune("._:-", character)) {
			return false
		}
	}
	return true
}
