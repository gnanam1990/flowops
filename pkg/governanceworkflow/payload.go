// Package governanceworkflow computes the exact payload hashes required by
// FlowOps governance contracts. These hashes bind an approved workflow to one
// chain, contract, function selector, current-state precondition, and change.
package governanceworkflow

import (
	"encoding/hex"
	"errors"
	"math/big"
	"regexp"
	"strings"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

const (
	CallEscrowDomain       = "ASCP_CALL_ESCROW_GOVERNANCE_V1"
	SpendModuleDomain      = "ASCP_SPEND_MODULE_GOVERNANCE_V1"
	ServiceDirectoryDomain = "SERVICE_DIRECTORY_GOVERNANCE_V1"
)

var (
	ErrInvalidPayload = errors.New("invalid governance workflow payload")
	decimalPattern    = regexp.MustCompile(`^(0|[1-9][0-9]*)$`)
	addressType       = mustType("address")
	uint64Type        = mustType("uint64")
	uint256Type       = mustType("uint256")
	uint8Type         = mustType("uint8")
	boolType          = mustType("bool")
	bytes32Type       = mustType("bytes32")
	bytes32ArrayType  = mustType("bytes32[]")
)

type Caps struct {
	PerTransaction   string
	PerDay           string
	AllowanceCeiling string
}

type DirectoryProposal struct {
	VersionID            uint64
	PreviousVersion      uint64
	PreviousRoot         string
	NewRoot              string
	BlobContentHash      string
	LocationsHash        string
	ChangeClass          uint8
	RequestedActivatesAt uint64
}

func CallEscrowAddVerifier(
	chainID uint64,
	contractAddress, workflowID, key string,
	activeEpoch, pendingEpoch, pendingActivatesAt, nextEpoch uint64,
) (common.Hash, error) {
	if !address(key) || nextEpoch == 0 || nextEpoch <= activeEpoch ||
		(pendingEpoch == 0) != (pendingActivatesAt == 0) || (pendingEpoch != 0 && nextEpoch <= pendingEpoch) {
		return common.Hash{}, ErrInvalidPayload
	}
	return bind(CallEscrowDomain, chainID, contractAddress, workflowID, selector("addVerifier(address,uint64,bytes32,bytes32)"),
		[]abi.Argument{{Type: addressType}, {Type: uint64Type}, {Type: uint64Type}, {Type: uint64Type},
			{Type: boolType}, {Type: uint64Type}},
		common.HexToAddress(key), activeEpoch, pendingEpoch, pendingActivatesAt, false, nextEpoch)
}

func CallEscrowRevokeVerifier(
	chainID uint64,
	contractAddress, workflowID, key string,
	activeEpoch, pendingEpoch, pendingActivatesAt uint64,
) (common.Hash, error) {
	if !address(key) || (activeEpoch == 0 && pendingEpoch == 0) ||
		(pendingEpoch == 0) != (pendingActivatesAt == 0) {
		return common.Hash{}, ErrInvalidPayload
	}
	return bind(CallEscrowDomain, chainID, contractAddress, workflowID, selector("revokeVerifier(address,bytes32,bytes32)"),
		[]abi.Argument{{Type: addressType}, {Type: uint64Type}, {Type: uint64Type}, {Type: uint64Type},
			{Type: boolType}, {Type: boolType}},
		common.HexToAddress(key), activeEpoch, pendingEpoch, pendingActivatesAt, false, true)
}

func CallEscrowPause(chainID uint64, contractAddress, workflowID string) (common.Hash, error) {
	return bind(CallEscrowDomain, chainID, contractAddress, workflowID, selector("setEmergencyPause(bytes32,bytes32)"),
		[]abi.Argument{{Type: boolType}, {Type: boolType}}, false, true)
}

func SpendAuthorizer(chainID uint64, contractAddress, workflowID, current string, currentEpoch uint64, next string) (common.Hash, error) {
	if !address(current) || !address(next) || currentEpoch == 0 {
		return common.Hash{}, ErrInvalidPayload
	}
	return bind(SpendModuleDomain, chainID, contractAddress, workflowID, selector("setSpendAuthorizer(address,bytes32,bytes32)"),
		[]abi.Argument{{Type: addressType}, {Type: uint64Type}, {Type: addressType}},
		common.HexToAddress(current), currentEpoch, common.HexToAddress(next))
}

func SpendAllowlist(chainID uint64, contractAddress, workflowID, target, currentCodeHash, nextCodeHash string) (common.Hash, error) {
	if !address(target) || !hash(currentCodeHash, true) || !hash(nextCodeHash, true) || currentCodeHash == nextCodeHash {
		return common.Hash{}, ErrInvalidPayload
	}
	return bind(SpendModuleDomain, chainID, contractAddress, workflowID, selector("setEscrowAllowlist(address,bytes32,bytes32,bytes32)"),
		[]abi.Argument{{Type: addressType}, {Type: bytes32Type}, {Type: bytes32Type}},
		common.HexToAddress(target), common.HexToHash(currentCodeHash), common.HexToHash(nextCodeHash))
}

func SpendCaps(chainID uint64, contractAddress, workflowID string, current, next Caps) (common.Hash, error) {
	values := make([]*big.Int, 0, 6)
	for _, raw := range []string{current.PerTransaction, current.PerDay, current.AllowanceCeiling,
		next.PerTransaction, next.PerDay, next.AllowanceCeiling} {
		value, err := decimal(raw)
		if err != nil {
			return common.Hash{}, err
		}
		values = append(values, value)
	}
	if !validCaps(values[0], values[1], values[2]) || !validCaps(values[3], values[4], values[5]) {
		return common.Hash{}, ErrInvalidPayload
	}
	if values[0].Cmp(values[3]) == 0 && values[1].Cmp(values[4]) == 0 && values[2].Cmp(values[5]) == 0 {
		return common.Hash{}, ErrInvalidPayload
	}
	return bind(SpendModuleDomain, chainID, contractAddress, workflowID,
		selector("scheduleCaps((uint256,uint256,uint256),bytes32,bytes32)"),
		[]abi.Argument{{Type: uint256Type}, {Type: uint256Type}, {Type: uint256Type},
			{Type: uint256Type}, {Type: uint256Type}, {Type: uint256Type}},
		values[0], values[1], values[2], values[3], values[4], values[5])
}

func SpendPause(chainID uint64, contractAddress, workflowID string, current, next bool) (common.Hash, error) {
	if current == next {
		return common.Hash{}, ErrInvalidPayload
	}
	return bind(SpendModuleDomain, chainID, contractAddress, workflowID, selector("setEmergencyPause(bool,bytes32,bytes32)"),
		[]abi.Argument{{Type: boolType}, {Type: boolType}}, current, next)
}

func SpendInvalidateNonces(chainID uint64, contractAddress, workflowID string, nonces []string) (common.Hash, error) {
	if len(nonces) == 0 || len(nonces) > 100 {
		return common.Hash{}, ErrInvalidPayload
	}
	values := make([][32]byte, len(nonces))
	seen := make(map[string]struct{}, len(nonces))
	for index, nonce := range nonces {
		if !hash(nonce, false) {
			return common.Hash{}, ErrInvalidPayload
		}
		if _, duplicate := seen[nonce]; duplicate {
			return common.Hash{}, ErrInvalidPayload
		}
		seen[nonce] = struct{}{}
		values[index] = common.HexToHash(nonce)
	}
	return bind(SpendModuleDomain, chainID, contractAddress, workflowID, selector("invalidateNonces(bytes32[],bytes32,bytes32)"),
		[]abi.Argument{{Type: bytes32ArrayType}}, values)
}

func DirectoryPublish(chainID uint64, contractAddress, workflowID string, proposal DirectoryProposal) (common.Hash, error) {
	if proposal.VersionID == 0 || !hash(proposal.PreviousRoot, true) || !hash(proposal.NewRoot, false) || !hash(proposal.BlobContentHash, false) ||
		!hash(proposal.LocationsHash, false) || (proposal.ChangeClass != 1 && proposal.ChangeClass != 2) {
		return common.Hash{}, ErrInvalidPayload
	}
	return bind(ServiceDirectoryDomain, chainID, contractAddress, workflowID, selector("approveVersion(uint64,bytes32)"),
		[]abi.Argument{{Type: uint64Type}, {Type: uint64Type}, {Type: bytes32Type}, {Type: bytes32Type},
			{Type: bytes32Type}, {Type: bytes32Type}, {Type: uint8Type}, {Type: uint64Type}},
		proposal.VersionID, proposal.PreviousVersion, common.HexToHash(proposal.PreviousRoot), common.HexToHash(proposal.NewRoot),
		common.HexToHash(proposal.BlobContentHash), common.HexToHash(proposal.LocationsHash), proposal.ChangeClass,
		proposal.RequestedActivatesAt)
}

func DirectoryCancel(chainID uint64, contractAddress, workflowID string, versionID uint64, proposalHash string) (common.Hash, error) {
	if versionID == 0 || !hash(proposalHash, false) {
		return common.Hash{}, ErrInvalidPayload
	}
	return bind(ServiceDirectoryDomain, chainID, contractAddress, workflowID,
		selector("cancelVersion(uint64,bytes32,bytes32,bytes32)"),
		[]abi.Argument{{Type: uint64Type}, {Type: bytes32Type}}, versionID, common.HexToHash(proposalHash))
}

func bind(domain string, chainID uint64, contractAddress, workflowID string, functionSelector [4]byte, types []abi.Argument, values ...any) (common.Hash, error) {
	argumentsHash, err := arguments(types, values...)
	if err != nil {
		return common.Hash{}, err
	}
	return payload(domain, chainID, contractAddress, workflowID, functionSelector, argumentsHash)
}

func payload(domain string, chainID uint64, contractAddress, workflowID string, functionSelector [4]byte, argumentsHash common.Hash) (common.Hash, error) {
	if (chainID != 8453 && chainID != 84532) || !address(contractAddress) || !hash(workflowID, false) {
		return common.Hash{}, ErrInvalidPayload
	}
	return crypto.Keccak256Hash(
		crypto.Keccak256([]byte(domain)), uintWord(new(big.Int).SetUint64(chainID)),
		common.LeftPadBytes(common.HexToAddress(contractAddress).Bytes(), 32), common.HexToHash(workflowID).Bytes(),
		common.RightPadBytes(functionSelector[:], 32),
		argumentsHash[:],
	), nil
}

func arguments(types []abi.Argument, values ...any) (common.Hash, error) {
	encoded, err := abi.Arguments(types).Pack(values...)
	if err != nil {
		return common.Hash{}, ErrInvalidPayload
	}
	return crypto.Keccak256Hash(encoded), nil
}

func selector(signature string) [4]byte {
	var result [4]byte
	copy(result[:], crypto.Keccak256([]byte(signature))[:4])
	return result
}

func decimal(raw string) (*big.Int, error) {
	if !decimalPattern.MatchString(raw) {
		return nil, ErrInvalidPayload
	}
	value, ok := new(big.Int).SetString(raw, 10)
	if !ok || value.BitLen() > 256 {
		return nil, ErrInvalidPayload
	}
	return value, nil
}

func address(value string) bool {
	return len(value) == 42 && strings.HasPrefix(value, "0x") && value == strings.ToLower(value) &&
		common.IsHexAddress(value) && common.HexToAddress(value) != (common.Address{})
}

func hash(value string, zeroAllowed bool) bool {
	if len(value) != 66 || !strings.HasPrefix(value, "0x") || value != strings.ToLower(value) {
		return false
	}
	decoded, err := hex.DecodeString(value[2:])
	return err == nil && len(decoded) == 32 && (zeroAllowed || common.BytesToHash(decoded) != (common.Hash{}))
}

func uintWord(value *big.Int) []byte { return common.LeftPadBytes(value.Bytes(), 32) }

func validCaps(perTransaction, perDay, allowanceCeiling *big.Int) bool {
	return perTransaction.Sign() > 0 && perDay.Sign() > 0 && perTransaction.Cmp(perDay) <= 0 &&
		allowanceCeiling.Sign() > 0
}

func mustType(name string) abi.Type {
	type_, err := abi.NewType(name, "", nil)
	if err != nil {
		panic(err)
	}
	return type_
}
