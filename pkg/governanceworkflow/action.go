package governanceworkflow

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"io"
	"math/big"
	"reflect"
	"strings"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
)

type ActionType string

const (
	ActionCallEscrowAddVerifier    ActionType = "CALL_ESCROW_ADD_VERIFIER"
	ActionCallEscrowRevokeVerifier ActionType = "CALL_ESCROW_REVOKE_VERIFIER"
	ActionCallEscrowPause          ActionType = "CALL_ESCROW_PAUSE"
	ActionSpendAuthorizer          ActionType = "SPEND_AUTHORIZER"
	ActionSpendAllowlist           ActionType = "SPEND_ALLOWLIST"
	ActionSpendCaps                ActionType = "SPEND_CAPS"
	ActionSpendPause               ActionType = "SPEND_PAUSE"
	ActionSpendInvalidateNonces    ActionType = "SPEND_INVALIDATE_NONCES"
	ActionDirectoryApprove         ActionType = "DIRECTORY_APPROVE"
	ActionDirectoryCancel          ActionType = "DIRECTORY_CANCEL"
)

type Action struct {
	Type            ActionType `json:"type"`
	ChainID         uint64     `json:"chainId"`
	ContractAddress string     `json:"contractAddress"`

	CallEscrowAddVerifier    *CallEscrowAddVerifierAction    `json:"callEscrowAddVerifier,omitempty"`
	CallEscrowRevokeVerifier *CallEscrowRevokeVerifierAction `json:"callEscrowRevokeVerifier,omitempty"`
	CallEscrowPause          *CallEscrowPauseAction          `json:"callEscrowPause,omitempty"`
	SpendAuthorizer          *SpendAuthorizerAction          `json:"spendAuthorizer,omitempty"`
	SpendAllowlist           *SpendAllowlistAction           `json:"spendAllowlist,omitempty"`
	SpendCaps                *SpendCapsAction                `json:"spendCaps,omitempty"`
	SpendPause               *SpendPauseAction               `json:"spendPause,omitempty"`
	SpendInvalidateNonces    *SpendInvalidateNoncesAction    `json:"spendInvalidateNonces,omitempty"`
	DirectoryApprove         *DirectoryApproveAction         `json:"directoryApprove,omitempty"`
	DirectoryCancel          *DirectoryCancelAction          `json:"directoryCancel,omitempty"`
}

type CallEscrowAddVerifierAction struct {
	Key                string `json:"key"`
	ActiveEpoch        uint64 `json:"activeEpoch"`
	PendingEpoch       uint64 `json:"pendingEpoch"`
	PendingActivatesAt uint64 `json:"pendingActivatesAt"`
	Revoked            bool   `json:"revoked"`
	NextEpoch          uint64 `json:"nextEpoch"`
}

type CallEscrowRevokeVerifierAction struct {
	Key                string `json:"key"`
	ActiveEpoch        uint64 `json:"activeEpoch"`
	PendingEpoch       uint64 `json:"pendingEpoch"`
	PendingActivatesAt uint64 `json:"pendingActivatesAt"`
	Revoked            bool   `json:"revoked"`
}

type CallEscrowPauseAction struct{}

type SpendAuthorizerAction struct {
	Current      string `json:"current"`
	CurrentEpoch uint64 `json:"currentEpoch"`
	Next         string `json:"next"`
}

type SpendAllowlistAction struct {
	Target          string `json:"target"`
	CurrentCodeHash string `json:"currentCodeHash"`
	NextCodeHash    string `json:"nextCodeHash"`
}

type SpendCapsAction struct {
	Current Caps `json:"current"`
	Next    Caps `json:"next"`
}

type SpendPauseAction struct {
	Current bool `json:"current"`
	Next    bool `json:"next"`
}

type SpendInvalidateNoncesAction struct {
	Nonces []string `json:"nonces"`
}

type DirectoryApproveAction struct {
	Proposal      DirectoryProposal `json:"proposal"`
	ProposerNonce string            `json:"proposerNonce"`
}

type DirectoryCancelAction struct {
	VersionID    uint64 `json:"versionId"`
	ProposalHash string `json:"proposalHash"`
}

type BoundAction struct {
	WorkflowKind     string          `json:"workflowKind"`
	PayloadHash      string          `json:"payloadHash"`
	ChainID          uint64          `json:"chainId"`
	ContractAddress  string          `json:"contractAddress"`
	FunctionSelector string          `json:"functionSelector"`
	Calldata         string          `json:"calldata"`
	CanonicalAction  json.RawMessage `json:"canonicalAction"`
}

func BindAction(workflowID string, action Action) (BoundAction, error) {
	if !address(action.ContractAddress) || (action.ChainID != 8453 && action.ChainID != 84532) || selectedActions(action) != 1 {
		return BoundAction{}, ErrInvalidPayload
	}
	var workflowKind, signature string
	var digest common.Hash
	var call []byte
	var err error
	switch action.Type {
	case ActionCallEscrowAddVerifier:
		if action.CallEscrowAddVerifier == nil {
			return BoundAction{}, ErrInvalidPayload
		}
		value := action.CallEscrowAddVerifier
		workflowKind, signature = "VERIFIER_GOVERNANCE", "addVerifier(address,uint64,bytes32,bytes32)"
		digest, err = CallEscrowAddVerifier(action.ChainID, action.ContractAddress, workflowID, value.Key,
			value.ActiveEpoch, value.PendingEpoch, value.PendingActivatesAt, value.Revoked, value.NextEpoch)
		if err == nil {
			call, err = packCall(signature, []abi.Type{addressType, uint64Type, bytes32Type, bytes32Type}, common.HexToAddress(value.Key), value.NextEpoch, common.HexToHash(workflowID), digest)
		}
	case ActionCallEscrowRevokeVerifier:
		if action.CallEscrowRevokeVerifier == nil {
			return BoundAction{}, ErrInvalidPayload
		}
		value := action.CallEscrowRevokeVerifier
		workflowKind, signature = "VERIFIER_GOVERNANCE", "revokeVerifier(address,bytes32,bytes32)"
		digest, err = CallEscrowRevokeVerifier(action.ChainID, action.ContractAddress, workflowID, value.Key,
			value.ActiveEpoch, value.PendingEpoch, value.PendingActivatesAt, value.Revoked)
		if err == nil {
			call, err = packCall(signature, []abi.Type{addressType, bytes32Type, bytes32Type}, common.HexToAddress(value.Key), common.HexToHash(workflowID), digest)
		}
	case ActionCallEscrowPause:
		if action.CallEscrowPause == nil {
			return BoundAction{}, ErrInvalidPayload
		}
		workflowKind, signature = "BREAK_GLASS", "setEmergencyPause(bytes32,bytes32)"
		digest, err = CallEscrowPause(action.ChainID, action.ContractAddress, workflowID)
		if err == nil {
			call, err = packCall(signature, []abi.Type{bytes32Type, bytes32Type}, common.HexToHash(workflowID), digest)
		}
	case ActionSpendAuthorizer:
		if action.SpendAuthorizer == nil {
			return BoundAction{}, ErrInvalidPayload
		}
		value := action.SpendAuthorizer
		workflowKind, signature = "MODULE_GOVERNANCE", "setSpendAuthorizer(address,bytes32,bytes32)"
		digest, err = SpendAuthorizer(action.ChainID, action.ContractAddress, workflowID, value.Current, value.CurrentEpoch, value.Next)
		if err == nil {
			call, err = packCall(signature, []abi.Type{addressType, bytes32Type, bytes32Type}, common.HexToAddress(value.Next), common.HexToHash(workflowID), digest)
		}
	case ActionSpendAllowlist:
		if action.SpendAllowlist == nil {
			return BoundAction{}, ErrInvalidPayload
		}
		value := action.SpendAllowlist
		workflowKind, signature = "MODULE_GOVERNANCE", "setEscrowAllowlist(address,bytes32,bytes32,bytes32)"
		digest, err = SpendAllowlist(action.ChainID, action.ContractAddress, workflowID, value.Target, value.CurrentCodeHash, value.NextCodeHash)
		if err == nil {
			call, err = packCall(signature, []abi.Type{addressType, bytes32Type, bytes32Type, bytes32Type}, common.HexToAddress(value.Target), common.HexToHash(value.NextCodeHash), common.HexToHash(workflowID), digest)
		}
	case ActionSpendCaps:
		if action.SpendCaps == nil {
			return BoundAction{}, ErrInvalidPayload
		}
		value := action.SpendCaps
		workflowKind, signature = "SIGNER_CAPS", "scheduleCaps((uint256,uint256,uint256),bytes32,bytes32)"
		digest, err = SpendCaps(action.ChainID, action.ContractAddress, workflowID, value.Current, value.Next)
		if err == nil {
			next, parseErr := capsValues(value.Next)
			if parseErr != nil {
				err = parseErr
			} else {
				call, err = packCall(signature, []abi.Type{uint256Type, uint256Type, uint256Type, bytes32Type, bytes32Type}, next[0], next[1], next[2], common.HexToHash(workflowID), digest)
			}
		}
	case ActionSpendPause:
		if action.SpendPause == nil {
			return BoundAction{}, ErrInvalidPayload
		}
		value := action.SpendPause
		workflowKind, signature = "BREAK_GLASS", "setEmergencyPause(bool,bytes32,bytes32)"
		digest, err = SpendPause(action.ChainID, action.ContractAddress, workflowID, value.Current, value.Next)
		if err == nil {
			call, err = packCall(signature, []abi.Type{boolType, bytes32Type, bytes32Type}, value.Next, common.HexToHash(workflowID), digest)
		}
	case ActionSpendInvalidateNonces:
		if action.SpendInvalidateNonces == nil {
			return BoundAction{}, ErrInvalidPayload
		}
		value := action.SpendInvalidateNonces
		workflowKind, signature = "MODULE_GOVERNANCE", "invalidateNonces(uint256[],bytes32,bytes32)"
		digest, err = SpendInvalidateNonces(action.ChainID, action.ContractAddress, workflowID, value.Nonces)
		if err == nil {
			nonces := make([]*big.Int, len(value.Nonces))
			for index, nonce := range value.Nonces {
				nonces[index], err = decimal(nonce)
				if err != nil {
					break
				}
			}
			if err == nil {
				call, err = packCall(signature, []abi.Type{uint256ArrayType, bytes32Type, bytes32Type}, nonces, common.HexToHash(workflowID), digest)
			}
		}
	case ActionDirectoryApprove:
		if action.DirectoryApprove == nil {
			return BoundAction{}, ErrInvalidPayload
		}
		value := action.DirectoryApprove
		workflowKind, signature = "PAYOUT_CHANGE", "approveVersion(uint64,bytes32)"
		digest, err = DirectoryPublish(action.ChainID, action.ContractAddress, workflowID, value.Proposal)
		if err == nil {
			var proposalHash common.Hash
			proposalHash, err = DirectoryProposalHash(action.ChainID, action.ContractAddress, workflowID, value.Proposal, value.ProposerNonce)
			if err == nil {
				call, err = packCall(signature, []abi.Type{uint64Type, bytes32Type}, value.Proposal.VersionID, proposalHash)
			}
		}
	case ActionDirectoryCancel:
		if action.DirectoryCancel == nil {
			return BoundAction{}, ErrInvalidPayload
		}
		value := action.DirectoryCancel
		workflowKind, signature = "DIRECTORY_CANCEL", "cancelVersion(uint64,bytes32,bytes32,bytes32)"
		digest, err = DirectoryCancel(action.ChainID, action.ContractAddress, workflowID, value.VersionID, value.ProposalHash)
		if err == nil {
			call, err = packCall(signature, []abi.Type{uint64Type, bytes32Type, bytes32Type, bytes32Type}, value.VersionID, common.HexToHash(value.ProposalHash), common.HexToHash(workflowID), digest)
		}
	default:
		return BoundAction{}, ErrInvalidPayload
	}
	if err != nil {
		return BoundAction{}, ErrInvalidPayload
	}
	canonical, err := json.Marshal(action)
	if err != nil {
		return BoundAction{}, ErrInvalidPayload
	}
	return BoundAction{WorkflowKind: workflowKind, PayloadHash: strings.ToLower(digest.Hex()), ChainID: action.ChainID,
		ContractAddress: action.ContractAddress, FunctionSelector: "0x" + hex.EncodeToString(call[:4]),
		Calldata: "0x" + hex.EncodeToString(call), CanonicalAction: canonical}, nil
}

// RebindAction decodes a persisted JSON/JSONB action under the closed schema
// and derives its exact executable binding again. JSONB key reordering and
// whitespace are harmless; unknown fields, explicit omitted variants, and
// trailing values are rejected.
func RebindAction(workflowID string, raw json.RawMessage) (BoundAction, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var action Action
	if err := decoder.Decode(&action); err != nil {
		return BoundAction{}, ErrInvalidPayload
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return BoundAction{}, ErrInvalidPayload
	}
	bound, err := BindAction(workflowID, action)
	if err != nil || !sameJSON(raw, bound.CanonicalAction) {
		return BoundAction{}, ErrInvalidPayload
	}
	return bound, nil
}

func sameJSON(left, right []byte) bool {
	decode := func(value []byte) (any, error) {
		decoder := json.NewDecoder(bytes.NewReader(value))
		decoder.UseNumber()
		var decoded any
		if err := decoder.Decode(&decoded); err != nil {
			return nil, err
		}
		return decoded, nil
	}
	leftValue, leftErr := decode(left)
	rightValue, rightErr := decode(right)
	return leftErr == nil && rightErr == nil && reflect.DeepEqual(leftValue, rightValue)
}

func selectedActions(action Action) int {
	count := 0
	for _, selected := range []bool{
		action.CallEscrowAddVerifier != nil, action.CallEscrowRevokeVerifier != nil, action.CallEscrowPause != nil,
		action.SpendAuthorizer != nil, action.SpendAllowlist != nil, action.SpendCaps != nil,
		action.SpendPause != nil, action.SpendInvalidateNonces != nil, action.DirectoryApprove != nil,
		action.DirectoryCancel != nil,
	} {
		if selected {
			count++
		}
	}
	return count
}

func capsValues(next Caps) ([3]*big.Int, error) {
	var values [3]*big.Int
	for index, raw := range []string{next.PerTransaction, next.PerDay, next.AllowanceCeiling} {
		value, err := decimal(raw)
		if err != nil {
			return values, err
		}
		values[index] = value
	}
	return values, nil
}

func packCall(signature string, types []abi.Type, values ...any) ([]byte, error) {
	arguments := make(abi.Arguments, len(types))
	for index, type_ := range types {
		arguments[index] = abi.Argument{Type: type_}
	}
	encoded, err := arguments.Pack(values...)
	if err != nil {
		return nil, ErrInvalidPayload
	}
	selector := selector(signature)
	return append(selector[:], encoded...), nil
}
