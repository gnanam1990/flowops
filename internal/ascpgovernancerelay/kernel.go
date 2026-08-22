// Package ascpgovernancerelay owns the fail-closed kernel between an approved
// governance outbox command, customer Safe owner signatures, and a gas-paying
// broadcaster. It never creates owner authority or infers chain outcomes from
// a write RPC response.
package ascpgovernancerelay

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/gnanam1990/flowops/internal/ascpworkflow"
	"github.com/gnanam1990/flowops/pkg/safegovernance"
)

const MaxEvidenceAge = time.Minute

var (
	ErrInvalidCommand     = errors.New("invalid governance execution command")
	ErrInvalidSnapshot    = errors.New("invalid independent Safe snapshot")
	ErrInvalidOutcome     = errors.New("invalid governance relay outcome evidence")
	ErrInsufficientQuorum = errors.New("governance relay evidence quorum unavailable")
	ErrReapprovalRequired = errors.New("governance action requires owner reapproval")
	ErrNoAutomaticRetry   = errors.New("governance action is not automatically retryable")
)

type Snapshot struct {
	ChainID             uint64    `json:"chainId"`
	SafeAddress         string    `json:"safeAddress"`
	SafeNonce           uint64    `json:"safeNonce"`
	Owners              []string  `json:"owners"`
	Threshold           int       `json:"threshold"`
	VerifiedPayloadHash string    `json:"verifiedPayloadHash"`
	BlockNumber         uint64    `json:"blockNumber"`
	BlockHash           string    `json:"blockHash"`
	BlockTimestamp      uint64    `json:"blockTimestamp"`
	ConfirmedHead       uint64    `json:"confirmedHead"`
	Observers           []string  `json:"observers"`
	EvidenceDigest      string    `json:"evidenceDigest"`
	ObservedAt          time.Time `json:"observedAt"`
}

type Prepared struct {
	WorkflowID             string                     `json:"workflowId"`
	OrganizationID         string                     `json:"organizationId"`
	PayloadHash            string                     `json:"payloadHash"`
	Transaction            safegovernance.Transaction `json:"safeTransaction"`
	Owners                 []string                   `json:"owners"`
	Threshold              int                        `json:"threshold"`
	EvidenceQuorum         int                        `json:"evidenceQuorum"`
	SafeTxHash             string                     `json:"safeTxHash"`
	ExecCalldataHash       string                     `json:"execCalldataHash"`
	SignaturesHash         string                     `json:"signaturesHash"`
	OwnerSetHash           string                     `json:"ownerSetHash"`
	SnapshotEvidenceDigest string                     `json:"snapshotEvidenceDigest"`
	SnapshotBlockNumber    uint64                     `json:"snapshotBlockNumber"`
	SnapshotBlockHash      string                     `json:"snapshotBlockHash"`
	SnapshotBlockTimestamp uint64                     `json:"snapshotBlockTimestamp"`
	SnapshotConfirmedHead  uint64                     `json:"snapshotConfirmedHead"`
	SnapshotObservers      []string                   `json:"snapshotObservers"`
	SnapshotObservedAt     time.Time                  `json:"snapshotObservedAt"`
}

type SigningRequest struct {
	WorkflowID             string                     `json:"workflowId"`
	OrganizationID         string                     `json:"organizationId"`
	PayloadHash            string                     `json:"payloadHash"`
	FunctionSelector       string                     `json:"functionSelector"`
	Calldata               string                     `json:"calldata"`
	GovernanceAction       json.RawMessage            `json:"governanceAction"`
	Transaction            safegovernance.Transaction `json:"safeTransaction"`
	Owners                 []string                   `json:"owners"`
	Threshold              int                        `json:"threshold"`
	EvidenceQuorum         int                        `json:"evidenceQuorum"`
	SafeTxHash             string                     `json:"safeTxHash"`
	OwnerSetHash           string                     `json:"ownerSetHash"`
	SnapshotEvidenceDigest string                     `json:"snapshotEvidenceDigest"`
	SnapshotBlockNumber    uint64                     `json:"snapshotBlockNumber"`
	SnapshotBlockHash      string                     `json:"snapshotBlockHash"`
	SnapshotBlockTimestamp uint64                     `json:"snapshotBlockTimestamp"`
	SnapshotConfirmedHead  uint64                     `json:"snapshotConfirmedHead"`
	SnapshotObservers      []string                   `json:"snapshotObservers"`
	SnapshotObservedAt     time.Time                  `json:"snapshotObservedAt"`
}

type Outcome string

const (
	OutcomePending     Outcome = "PENDING"
	OutcomeDropped     Outcome = "DROPPED"
	OutcomeReorged     Outcome = "REORGED"
	OutcomeMinedRevert Outcome = "MINED_REVERT"
	OutcomeFinalized   Outcome = "FINALIZED"
)

type OutcomeEvidence struct {
	WorkflowID           string    `json:"workflowId"`
	OuterTransactionHash string    `json:"outerTransactionHash"`
	Outcome              Outcome   `json:"outcome"`
	PreviousCanonical    bool      `json:"previousCanonical"`
	ChainID              uint64    `json:"chainId"`
	SafeAddress          string    `json:"safeAddress"`
	CurrentSafeNonce     uint64    `json:"currentSafeNonce"`
	SafeTxHash           string    `json:"safeTxHash"`
	ExecCalldataHash     string    `json:"execCalldataHash"`
	VerifiedPayloadHash  string    `json:"verifiedPayloadHash"`
	BlockNumber          uint64    `json:"blockNumber"`
	BlockHash            string    `json:"blockHash"`
	ConfirmedHead        uint64    `json:"confirmedHead"`
	Observers            []string  `json:"observers"`
	EvidenceDigest       string    `json:"evidenceDigest"`
	ObservedAt           time.Time `json:"observedAt"`
}

type Decision string

const (
	DecisionWait       Decision = "WAIT"
	DecisionRetryExact Decision = "RETRY_EXACT_SAFE_TX"
	DecisionReapprove  Decision = "REAPPROVAL_REQUIRED"
	DecisionFinalized  Decision = "FINALIZED"
)

type DecisionResult struct {
	Decision Decision
	Reason   ascpworkflow.TerminalReason
}

func Prepare(command ascpworkflow.GovernanceExecutionCommand, safeAddress string, snapshot Snapshot, signatures []byte, quorum int, now time.Time) (Prepared, []byte, error) {
	request, err := BuildSigningRequest(command, safeAddress, snapshot, quorum, now)
	if err != nil {
		return Prepared{}, nil, err
	}
	execCalldata, err := request.Transaction.ExecCalldata(safegovernance.OwnerSnapshot{Owners: snapshot.Owners, Threshold: snapshot.Threshold}, signatures)
	if err != nil {
		return Prepared{}, nil, err
	}
	prepared := Prepared{
		WorkflowID: request.WorkflowID, OrganizationID: request.OrganizationID, PayloadHash: request.PayloadHash,
		Transaction: request.Transaction, Owners: append([]string(nil), request.Owners...), Threshold: request.Threshold,
		EvidenceQuorum: request.EvidenceQuorum,
		SafeTxHash:     request.SafeTxHash, ExecCalldataHash: hashBytes(execCalldata), SignaturesHash: hashBytes(signatures),
		OwnerSetHash: request.OwnerSetHash, SnapshotEvidenceDigest: request.SnapshotEvidenceDigest,
		SnapshotBlockNumber: request.SnapshotBlockNumber, SnapshotBlockHash: request.SnapshotBlockHash,
		SnapshotBlockTimestamp: request.SnapshotBlockTimestamp, SnapshotConfirmedHead: request.SnapshotConfirmedHead,
		SnapshotObservers:  append([]string(nil), request.SnapshotObservers...),
		SnapshotObservedAt: request.SnapshotObservedAt,
	}
	return prepared, execCalldata, nil
}

func BuildSigningRequest(command ascpworkflow.GovernanceExecutionCommand, safeAddress string, snapshot Snapshot, quorum int, now time.Time) (SigningRequest, error) {
	if err := ascpworkflow.ValidateExecutionCommand(command); err != nil {
		return SigningRequest{}, errors.Join(ErrInvalidCommand, err)
	}
	if err := validateSnapshot(command, safeAddress, snapshot, quorum, now); err != nil {
		return SigningRequest{}, err
	}
	if snapshot.VerifiedPayloadHash != command.PayloadHash {
		return SigningRequest{}, ErrInvalidSnapshot
	}
	data, err := hex.DecodeString(strings.TrimPrefix(command.Calldata, "0x"))
	if err != nil {
		return SigningRequest{}, errors.Join(ErrInvalidCommand, err)
	}
	tx, err := safegovernance.NewTransaction(command.ChainID, safeAddress, command.ContractAddress, data, snapshot.SafeNonce)
	if err != nil {
		return SigningRequest{}, err
	}
	safeTxHash, err := tx.Hash()
	if err != nil {
		return SigningRequest{}, err
	}
	return SigningRequest{
		WorkflowID: command.WorkflowID, OrganizationID: command.OrganizationID, PayloadHash: command.PayloadHash,
		FunctionSelector: command.FunctionSelector, Calldata: command.Calldata,
		GovernanceAction: append(json.RawMessage(nil), command.GovernanceAction...),
		Transaction:      tx, Owners: append([]string(nil), snapshot.Owners...), Threshold: snapshot.Threshold, EvidenceQuorum: quorum,
		SafeTxHash: safeTxHash, OwnerSetHash: ownerSetHash(snapshot.Owners, snapshot.Threshold),
		SnapshotEvidenceDigest: snapshot.EvidenceDigest, SnapshotBlockNumber: snapshot.BlockNumber,
		SnapshotBlockHash: snapshot.BlockHash, SnapshotBlockTimestamp: snapshot.BlockTimestamp,
		SnapshotConfirmedHead: snapshot.ConfirmedHead, SnapshotObservers: append([]string(nil), snapshot.Observers...),
		SnapshotObservedAt: snapshot.ObservedAt.UTC(),
	}, nil
}

func VerifyPrepared(command ascpworkflow.GovernanceExecutionCommand, prepared Prepared, execCalldata []byte) error {
	commandData, decodeErr := hex.DecodeString(strings.TrimPrefix(command.Calldata, "0x"))
	if err := ascpworkflow.ValidateExecutionCommand(command); err != nil || prepared.WorkflowID != command.WorkflowID ||
		prepared.OrganizationID != command.OrganizationID || prepared.PayloadHash != command.PayloadHash ||
		prepared.Transaction.ChainID != command.ChainID || prepared.Transaction.To != command.ContractAddress ||
		decodeErr != nil || !bytes.Equal(prepared.Transaction.Data, commandData) ||
		prepared.ExecCalldataHash != hashBytes(execCalldata) || !canonicalHash(prepared.SafeTxHash) ||
		!canonicalHash(prepared.SignaturesHash) || !canonicalHash(prepared.OwnerSetHash) ||
		!canonicalHash(prepared.SnapshotEvidenceDigest) || !canonicalHash(prepared.SnapshotBlockHash) ||
		prepared.SnapshotBlockNumber == 0 || prepared.SnapshotConfirmedHead < prepared.SnapshotBlockNumber ||
		prepared.SnapshotBlockTimestamp < uint64(command.ExecuteAfter) || prepared.SnapshotObservedAt.IsZero() ||
		prepared.EvidenceQuorum < 2 || prepared.EvidenceQuorum > 5 {
		return errors.Join(ErrInvalidCommand, err, decodeErr)
	}
	digest, err := prepared.Transaction.Hash()
	if err != nil || digest != prepared.SafeTxHash {
		return errors.Join(ErrInvalidCommand, err)
	}
	if ownerSetHash(prepared.Owners, prepared.Threshold) != prepared.OwnerSetHash ||
		!canonicalOwnerSnapshot(prepared.Owners, prepared.Threshold) {
		return ErrInvalidCommand
	}
	if _, err := observerQuorum(prepared.SnapshotObservers, prepared.EvidenceQuorum); err != nil {
		return ErrInvalidCommand
	}
	if err := prepared.Transaction.VerifyExecCalldata(
		safegovernance.OwnerSnapshot{Owners: prepared.Owners, Threshold: prepared.Threshold}, execCalldata,
	); err != nil {
		return err
	}
	signatures, err := safegovernance.SignaturesFromExecCalldata(execCalldata)
	if err != nil || hashBytes(signatures) != prepared.SignaturesHash {
		return errors.Join(ErrInvalidCommand, err)
	}
	return nil
}

// ValidateRelaySnapshot is the last read-side gate before an outer transaction
// is prepared. A changed Safe nonce, owner set, threshold, or action
// precondition cannot be repaired by the relayer.
func ValidateRelaySnapshot(command ascpworkflow.GovernanceExecutionCommand, prepared Prepared, snapshot Snapshot, quorum int, now time.Time) (DecisionResult, error) {
	if quorum != prepared.EvidenceQuorum {
		return DecisionResult{Decision: DecisionReapprove, Reason: ascpworkflow.PreconditionChanged}, nil
	}
	if err := validateSnapshot(command, prepared.Transaction.Safe, snapshot, quorum, now); err != nil {
		return DecisionResult{}, err
	}
	if len(snapshot.Observers) < quorum || quorum < 2 || quorum > 5 {
		return DecisionResult{}, ErrInsufficientQuorum
	}
	if snapshot.SafeNonce != prepared.Transaction.Nonce {
		return DecisionResult{Decision: DecisionReapprove, Reason: ascpworkflow.SafeNonceConflict}, nil
	}
	if snapshot.VerifiedPayloadHash != prepared.PayloadHash {
		return DecisionResult{Decision: DecisionReapprove, Reason: ascpworkflow.PreconditionChanged}, nil
	}
	if ownerSetHash(snapshot.Owners, snapshot.Threshold) != prepared.OwnerSetHash {
		return DecisionResult{Decision: DecisionReapprove, Reason: ascpworkflow.PreconditionChanged}, nil
	}
	return DecisionResult{Decision: DecisionRetryExact}, nil
}

func DecideRetry(prepared Prepared, outerTransactionHash string, evidence OutcomeEvidence, quorum int, now time.Time) (DecisionResult, error) {
	if err := validateOutcome(prepared, outerTransactionHash, evidence, quorum, now); err != nil {
		return DecisionResult{}, err
	}
	if evidence.SafeTxHash != prepared.SafeTxHash || evidence.ExecCalldataHash != prepared.ExecCalldataHash {
		return DecisionResult{}, ErrInvalidOutcome
	}
	switch evidence.Outcome {
	case OutcomePending:
		if evidence.CurrentSafeNonce != prepared.Transaction.Nonce {
			return DecisionResult{Decision: DecisionReapprove, Reason: ascpworkflow.SafeNonceConflict}, nil
		}
		if evidence.VerifiedPayloadHash != prepared.PayloadHash {
			return DecisionResult{Decision: DecisionReapprove, Reason: ascpworkflow.PreconditionChanged}, nil
		}
		return DecisionResult{Decision: DecisionWait}, nil
	case OutcomeDropped, OutcomeReorged:
		if evidence.CurrentSafeNonce != prepared.Transaction.Nonce {
			return DecisionResult{Decision: DecisionReapprove, Reason: ascpworkflow.SafeNonceConflict}, nil
		}
		if evidence.VerifiedPayloadHash != prepared.PayloadHash {
			return DecisionResult{Decision: DecisionReapprove, Reason: ascpworkflow.PreconditionChanged}, nil
		}
		if evidence.PreviousCanonical {
			return DecisionResult{}, ErrInvalidOutcome
		}
		return DecisionResult{Decision: DecisionRetryExact}, nil
	case OutcomeMinedRevert:
		// A reverted outer transaction may fail before Safe consumes its nonce,
		// or Safe may consume the nonce and emit an execution failure. Neither
		// shape is relay-safe: both require an owner-visible fresh ceremony.
		if evidence.CurrentSafeNonce < prepared.Transaction.Nonce {
			return DecisionResult{}, ErrInvalidOutcome
		}
		return DecisionResult{Decision: DecisionReapprove, Reason: ascpworkflow.MinedRevert}, nil
	case OutcomeFinalized:
		if evidence.CurrentSafeNonce <= prepared.Transaction.Nonce || evidence.VerifiedPayloadHash != prepared.PayloadHash {
			return DecisionResult{}, ErrInvalidOutcome
		}
		return DecisionResult{Decision: DecisionFinalized}, nil
	default:
		return DecisionResult{}, ErrInvalidOutcome
	}
}

func validateSnapshot(command ascpworkflow.GovernanceExecutionCommand, safeAddress string, snapshot Snapshot, quorum int, now time.Time) error {
	observers, err := observerQuorum(snapshot.Observers, quorum)
	if err != nil || snapshot.ChainID != command.ChainID || snapshot.SafeAddress != safeAddress ||
		!canonicalAddress(safeAddress) || !canonicalHash(snapshot.VerifiedPayloadHash) ||
		!canonicalHash(snapshot.BlockHash) || !canonicalHash(snapshot.EvidenceDigest) || snapshot.BlockNumber == 0 ||
		snapshot.ConfirmedHead < snapshot.BlockNumber || snapshot.BlockTimestamp < uint64(command.ExecuteAfter) ||
		snapshot.ObservedAt.IsZero() || snapshot.ObservedAt.After(now.Add(time.Minute)) || now.Sub(snapshot.ObservedAt) > MaxEvidenceAge ||
		len(observers) < quorum {
		return errors.Join(ErrInvalidSnapshot, err)
	}
	if !canonicalOwnerSnapshot(snapshot.Owners, snapshot.Threshold) {
		return ErrInvalidSnapshot
	}
	return nil
}

func validateOutcome(prepared Prepared, outerTransactionHash string, evidence OutcomeEvidence, quorum int, now time.Time) error {
	observers, err := observerQuorum(evidence.Observers, quorum)
	if err != nil || quorum < 2 || quorum > 5 || evidence.WorkflowID != prepared.WorkflowID ||
		evidence.OuterTransactionHash != outerTransactionHash || !canonicalHash(outerTransactionHash) ||
		evidence.ChainID != prepared.Transaction.ChainID || evidence.SafeAddress != prepared.Transaction.Safe ||
		!canonicalHash(evidence.SafeTxHash) || !canonicalHash(evidence.ExecCalldataHash) ||
		!canonicalHash(evidence.VerifiedPayloadHash) || !canonicalHash(evidence.BlockHash) ||
		!canonicalHash(evidence.EvidenceDigest) || evidence.BlockNumber == 0 || evidence.ConfirmedHead < evidence.BlockNumber ||
		evidence.ObservedAt.IsZero() || evidence.ObservedAt.After(now.Add(time.Minute)) || now.Sub(evidence.ObservedAt) > MaxEvidenceAge ||
		len(observers) < quorum {
		return errors.Join(ErrInvalidOutcome, err)
	}
	if (evidence.Outcome == OutcomePending || evidence.Outcome == OutcomeMinedRevert || evidence.Outcome == OutcomeFinalized) && !evidence.PreviousCanonical {
		return ErrInvalidOutcome
	}
	return nil
}

func canonicalOwnerSnapshot(owners []string, threshold int) bool {
	// The agreed FlowOps governance authority is always two of exactly three
	// customer-controlled owners. Accepting a structurally valid 1-of-1 or
	// 2-of-2 Safe would silently weaken INV-G1 under directory misconfiguration.
	if threshold != 2 || len(owners) != 3 {
		return false
	}
	for index, owner := range owners {
		if !canonicalAddress(owner) || index > 0 && owners[index-1] >= owner {
			return false
		}
	}
	return true
}

func observerQuorum(values []string, minimum int) ([]string, error) {
	if minimum < 2 || minimum > 5 || len(values) < minimum || len(values) > 5 {
		return nil, ErrInsufficientQuorum
	}
	result := append([]string(nil), values...)
	sort.Strings(result)
	for index, value := range result {
		if !identifierPattern.MatchString(value) || index > 0 && result[index-1] == value {
			return nil, ErrInsufficientQuorum
		}
	}
	return result, nil
}

func ownerSetHash(owners []string, threshold int) string {
	parts := []byte(fmt.Sprintf("ASCP_SAFE_OWNER_SET_V1\x00%d", threshold))
	for _, owner := range owners {
		parts = append(parts, 0)
		parts = append(parts, owner...)
	}
	return hashBytes(parts)
}

func hashBytes(value []byte) string { return crypto.Keccak256Hash(value).Hex() }

func canonicalHash(value string) bool {
	if len(value) != 66 || !strings.HasPrefix(value, "0x") || value != strings.ToLower(value) || value == "0x"+strings.Repeat("0", 64) {
		return false
	}
	_, err := hex.DecodeString(value[2:])
	return err == nil
}

func canonicalAddress(value string) bool {
	return len(value) == 42 && strings.HasPrefix(value, "0x") && value == strings.ToLower(value) &&
		common.IsHexAddress(value) && common.HexToAddress(value) != (common.Address{})
}
