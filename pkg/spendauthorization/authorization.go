// Package spendauthorization implements the exact ASCPSpendModule EIP-712
// digests produced by the isolated signer and verified on-chain.
package spendauthorization

import (
	"encoding/hex"
	"errors"
	"math/big"
	"regexp"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

const (
	LockTypeString              = "LockAuthorization(bytes32 orgDomain,address safe,bytes32 operationId,bytes32 commitmentHash,bytes32 calldataHash,address escrow,uint256 amount,uint64 validAfter,uint64 validBefore,bytes32 nonce,uint64 leadershipEpoch,uint64 authorizerEpoch)"
	AllowanceTypeString         = "AllowanceAuthorization(bytes32 orgDomain,address safe,bytes32 adminOperationId,address token,address spender,uint256 expectedCurrentAllowance,uint256 newAllowance,uint64 validAfter,uint64 validBefore,bytes32 nonce,uint64 authorizerEpoch)"
	MaximumWindowSeconds uint64 = 600
)

var (
	ErrInvalidAuthorization = errors.New("invalid spend authorization")
	decimalPattern          = regexp.MustCompile(`^(0|[1-9][0-9]*)$`)
	domainTypeHash          = crypto.Keccak256Hash([]byte("EIP712Domain(string name,string version,uint256 chainId,address verifyingContract)"))
	nameHash                = crypto.Keccak256Hash([]byte("ASCP"))
	versionHash             = crypto.Keccak256Hash([]byte("3"))
	lockTypeHash            = crypto.Keccak256Hash([]byte(LockTypeString))
	allowanceTypeHash       = crypto.Keccak256Hash([]byte(AllowanceTypeString))
)

type LockAuthorization struct {
	OrgDomain       string
	Safe            string
	OperationID     string
	CommitmentHash  string
	CalldataHash    string
	Escrow          string
	Amount          string
	ValidAfter      uint64
	ValidBefore     uint64
	Nonce           string
	LeadershipEpoch uint64
	AuthorizerEpoch uint64
}

type AllowanceAuthorization struct {
	OrgDomain                string
	Safe                     string
	AdminOperationID         string
	Token                    string
	Spender                  string
	ExpectedCurrentAllowance string
	NewAllowance             string
	ValidAfter               uint64
	ValidBefore              uint64
	Nonce                    string
	AuthorizerEpoch          uint64
}

func (a LockAuthorization) StructHash() (common.Hash, error) {
	amount, err := validateLock(a)
	if err != nil {
		return common.Hash{}, err
	}
	return crypto.Keccak256Hash(
		lockTypeHash[:], hashWord(a.OrgDomain), addressWord(a.Safe), hashWord(a.OperationID),
		hashWord(a.CommitmentHash), hashWord(a.CalldataHash), addressWord(a.Escrow), uintWord(amount),
		uint64Word(a.ValidAfter), uint64Word(a.ValidBefore), hashWord(a.Nonce),
		uint64Word(a.LeadershipEpoch), uint64Word(a.AuthorizerEpoch),
	), nil
}

func (a LockAuthorization) Digest(chainID, module string) (common.Hash, error) {
	structHash, err := a.StructHash()
	if err != nil {
		return common.Hash{}, err
	}
	return digest(chainID, module, structHash)
}

func (a AllowanceAuthorization) StructHash() (common.Hash, error) {
	expected, next, err := validateAllowance(a)
	if err != nil {
		return common.Hash{}, err
	}
	return crypto.Keccak256Hash(
		allowanceTypeHash[:], hashWord(a.OrgDomain), addressWord(a.Safe), hashWord(a.AdminOperationID),
		addressWord(a.Token), addressWord(a.Spender), uintWord(expected), uintWord(next),
		uint64Word(a.ValidAfter), uint64Word(a.ValidBefore), hashWord(a.Nonce), uint64Word(a.AuthorizerEpoch),
	), nil
}

func (a AllowanceAuthorization) Digest(chainID, module string) (common.Hash, error) {
	structHash, err := a.StructHash()
	if err != nil {
		return common.Hash{}, err
	}
	return digest(chainID, module, structHash)
}

func DomainSeparator(chainID, module string) (common.Hash, error) {
	chain, err := decimal(chainID, true)
	if err != nil || !validAddress(module) {
		return common.Hash{}, ErrInvalidAuthorization
	}
	return crypto.Keccak256Hash(
		domainTypeHash[:], nameHash[:], versionHash[:], uintWord(chain), addressWord(module),
	), nil
}

func digest(chainID, module string, structHash common.Hash) (common.Hash, error) {
	domain, err := DomainSeparator(chainID, module)
	if err != nil {
		return common.Hash{}, err
	}
	return crypto.Keccak256Hash([]byte{0x19, 0x01}, domain[:], structHash[:]), nil
}

func validateLock(a LockAuthorization) (*big.Int, error) {
	if !validHash(a.OrgDomain) || !validHash(a.OperationID) || !validHash(a.CommitmentHash) ||
		!validHash(a.CalldataHash) || !validHash(a.Nonce) || !validAddress(a.Safe) || !validAddress(a.Escrow) ||
		!validWindow(a.ValidAfter, a.ValidBefore) || a.LeadershipEpoch == 0 || a.AuthorizerEpoch == 0 {
		return nil, ErrInvalidAuthorization
	}
	amount, err := decimal(a.Amount, true)
	if err != nil {
		return nil, ErrInvalidAuthorization
	}
	return amount, nil
}

func validateAllowance(a AllowanceAuthorization) (*big.Int, *big.Int, error) {
	if !validHash(a.OrgDomain) || !validHash(a.AdminOperationID) || !validHash(a.Nonce) ||
		!validAddress(a.Safe) || !validAddress(a.Token) || !validAddress(a.Spender) ||
		!validWindow(a.ValidAfter, a.ValidBefore) || a.AuthorizerEpoch == 0 {
		return nil, nil, ErrInvalidAuthorization
	}
	expected, err := decimal(a.ExpectedCurrentAllowance, false)
	if err != nil {
		return nil, nil, ErrInvalidAuthorization
	}
	next, err := decimal(a.NewAllowance, false)
	if err != nil {
		return nil, nil, ErrInvalidAuthorization
	}
	return expected, next, nil
}

func validWindow(after, before uint64) bool {
	return before > after && before-after <= MaximumWindowSeconds
}

func validHash(value string) bool {
	if len(value) != 66 || !strings.HasPrefix(value, "0x") || value != strings.ToLower(value) {
		return false
	}
	decoded, err := hex.DecodeString(value[2:])
	return err == nil && len(decoded) == 32 && common.BytesToHash(decoded) != (common.Hash{})
}

func validAddress(value string) bool {
	return len(value) == 42 && strings.HasPrefix(value, "0x") && value == strings.ToLower(value) &&
		common.IsHexAddress(value) && common.HexToAddress(value) != (common.Address{})
}

func decimal(value string, positive bool) (*big.Int, error) {
	if !decimalPattern.MatchString(value) {
		return nil, ErrInvalidAuthorization
	}
	result, ok := new(big.Int).SetString(value, 10)
	if !ok || result.BitLen() > 256 || positive && result.Sign() == 0 {
		return nil, ErrInvalidAuthorization
	}
	return result, nil
}

func hashWord(value string) []byte { return common.HexToHash(value).Bytes() }
func addressWord(value string) []byte {
	return common.LeftPadBytes(common.HexToAddress(value).Bytes(), 32)
}
func uint64Word(value uint64) []byte { return uintWord(new(big.Int).SetUint64(value)) }
func uintWord(value *big.Int) []byte { return common.LeftPadBytes(value.Bytes(), 32) }
