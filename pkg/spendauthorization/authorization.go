// Package spendauthorization implements the exact ASCPSpendModule EIP-712
// digests produced by the isolated signer and verified on-chain.
package spendauthorization

import (
	"bytes"
	"encoding/hex"
	"errors"
	"math/big"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

const (
	LockTypeString              = "LockAuthorization(bytes32 orgDomain,address safe,address module,bytes32 operationId,bytes32 commitmentHash,bytes32 calldataHash,address escrow,uint256 amount,uint256 nonce,uint64 validAfter,uint64 validBefore,uint64 leadershipEpoch,uint64 authorizerEpoch)"
	AllowanceTypeString         = "AllowanceAuthorization(bytes32 orgDomain,address safe,address module,bytes32 adminOperationId,address token,address spender,uint256 expectedAllowance,uint256 newAllowance,uint256 nonce,uint64 validAfter,uint64 validBefore,uint64 leadershipEpoch,uint64 authorizerEpoch)"
	MaximumWindowSeconds uint64 = 600
)

var (
	ErrInvalidAuthorization = errors.New("invalid spend authorization")
	decimalPattern          = regexp.MustCompile(`^(0|[1-9][0-9]*)$`)
	domainTypeHash          = crypto.Keccak256Hash([]byte("EIP712Domain(string name,string version,uint256 chainId,address verifyingContract)"))
	nameHash                = crypto.Keccak256Hash([]byte("ASCP"))
	versionHash             = crypto.Keccak256Hash([]byte("4"))
	lockTypeHash            = crypto.Keccak256Hash([]byte(LockTypeString))
	allowanceTypeHash       = crypto.Keccak256Hash([]byte(AllowanceTypeString))
)

type LockAuthorization struct {
	OrgDomain       string `json:"orgDomain"`
	Safe            string `json:"safe"`
	Module          string `json:"module"`
	OperationID     string `json:"operationId"`
	CommitmentHash  string `json:"commitmentHash"`
	CalldataHash    string `json:"calldataHash"`
	Escrow          string `json:"escrow"`
	Amount          string `json:"amount"`
	Nonce           string `json:"nonce"`
	ValidAfter      uint64 `json:"validAfter"`
	ValidBefore     uint64 `json:"validBefore"`
	LeadershipEpoch uint64 `json:"leadershipEpoch"`
	AuthorizerEpoch uint64 `json:"authorizerEpoch"`
}

type AllowanceAuthorization struct {
	OrgDomain         string `json:"orgDomain"`
	Safe              string `json:"safe"`
	Module            string `json:"module"`
	AdminOperationID  string `json:"adminOperationId"`
	Token             string `json:"token"`
	Spender           string `json:"spender"`
	ExpectedAllowance string `json:"expectedAllowance"`
	NewAllowance      string `json:"newAllowance"`
	Nonce             string `json:"nonce"`
	ValidAfter        uint64 `json:"validAfter"`
	ValidBefore       uint64 `json:"validBefore"`
	LeadershipEpoch   uint64 `json:"leadershipEpoch"`
	AuthorizerEpoch   uint64 `json:"authorizerEpoch"`
}

func (a LockAuthorization) EncodedData() ([]byte, error) {
	amount, nonce, err := validateLock(a)
	if err != nil {
		return nil, err
	}
	return bytes.Join([][]byte{
		lockTypeHash[:], hashWord(a.OrgDomain), addressWord(a.Safe), addressWord(a.Module), hashWord(a.OperationID),
		hashWord(a.CommitmentHash), hashWord(a.CalldataHash), addressWord(a.Escrow), uintWord(amount),
		uintWord(nonce), uint64Word(a.ValidAfter), uint64Word(a.ValidBefore),
		uint64Word(a.LeadershipEpoch), uint64Word(a.AuthorizerEpoch),
	}, nil), nil
}

func (a LockAuthorization) StructHash() (common.Hash, error) {
	encoded, err := a.EncodedData()
	if err != nil {
		return common.Hash{}, err
	}
	return crypto.Keccak256Hash(encoded), nil
}

func (a LockAuthorization) Digest(chainID, module string) (common.Hash, error) {
	structHash, err := a.StructHash()
	if err != nil {
		return common.Hash{}, err
	}
	if !strings.EqualFold(module, a.Module) {
		return common.Hash{}, ErrInvalidAuthorization
	}
	return digest(chainID, module, structHash)
}

func (a AllowanceAuthorization) EncodedData() ([]byte, error) {
	expected, next, nonce, err := validateAllowance(a)
	if err != nil {
		return nil, err
	}
	return bytes.Join([][]byte{
		allowanceTypeHash[:], hashWord(a.OrgDomain), addressWord(a.Safe), addressWord(a.Module), hashWord(a.AdminOperationID),
		addressWord(a.Token), addressWord(a.Spender), uintWord(expected), uintWord(next),
		uintWord(nonce), uint64Word(a.ValidAfter), uint64Word(a.ValidBefore),
		uint64Word(a.LeadershipEpoch), uint64Word(a.AuthorizerEpoch),
	}, nil), nil
}

func (a AllowanceAuthorization) StructHash() (common.Hash, error) {
	encoded, err := a.EncodedData()
	if err != nil {
		return common.Hash{}, err
	}
	return crypto.Keccak256Hash(encoded), nil
}

func (a AllowanceAuthorization) Digest(chainID, module string) (common.Hash, error) {
	structHash, err := a.StructHash()
	if err != nil {
		return common.Hash{}, err
	}
	if !strings.EqualFold(module, a.Module) {
		return common.Hash{}, ErrInvalidAuthorization
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

func validateLock(a LockAuthorization) (*big.Int, *big.Int, error) {
	if !validHash(a.OrgDomain) || !validHash(a.OperationID) || !validHash(a.CommitmentHash) ||
		!validHash(a.CalldataHash) || !validAddress(a.Safe) || !validAddress(a.Module) || !validAddress(a.Escrow) ||
		!validWindow(a.ValidAfter, a.ValidBefore) || a.LeadershipEpoch == 0 || a.AuthorizerEpoch == 0 {
		return nil, nil, ErrInvalidAuthorization
	}
	amount, err := decimal(a.Amount, true)
	if err != nil {
		return nil, nil, ErrInvalidAuthorization
	}
	nonce, err := decimal(a.Nonce, true)
	if err != nil {
		return nil, nil, ErrInvalidAuthorization
	}
	return amount, nonce, nil
}

func validateAllowance(a AllowanceAuthorization) (*big.Int, *big.Int, *big.Int, error) {
	if !validHash(a.OrgDomain) || !validHash(a.AdminOperationID) ||
		!validAddress(a.Safe) || !validAddress(a.Module) || !validAddress(a.Token) || !validAddress(a.Spender) ||
		!validWindow(a.ValidAfter, a.ValidBefore) || a.LeadershipEpoch == 0 || a.AuthorizerEpoch == 0 {
		return nil, nil, nil, ErrInvalidAuthorization
	}
	expected, err := decimal(a.ExpectedAllowance, false)
	if err != nil {
		return nil, nil, nil, ErrInvalidAuthorization
	}
	next, err := decimal(a.NewAllowance, false)
	if err != nil {
		return nil, nil, nil, ErrInvalidAuthorization
	}
	nonce, err := decimal(a.Nonce, true)
	if err != nil {
		return nil, nil, nil, ErrInvalidAuthorization
	}
	return expected, next, nonce, nil
}

func (a LockAuthorization) CanonicalJSON() ([]byte, error) {
	if _, _, err := validateLock(a); err != nil {
		return nil, err
	}
	return canonicalObject(map[string]string{
		"amount": quote(a.Amount), "authorizerEpoch": strconv.FormatUint(a.AuthorizerEpoch, 10),
		"calldataHash": quote(a.CalldataHash), "commitmentHash": quote(a.CommitmentHash),
		"escrow": quote(a.Escrow), "leadershipEpoch": strconv.FormatUint(a.LeadershipEpoch, 10),
		"module": quote(a.Module), "nonce": quote(a.Nonce), "operationId": quote(a.OperationID),
		"orgDomain": quote(a.OrgDomain), "safe": quote(a.Safe), "validAfter": strconv.FormatUint(a.ValidAfter, 10),
		"validBefore": strconv.FormatUint(a.ValidBefore, 10),
	}), nil
}

func (a AllowanceAuthorization) CanonicalJSON() ([]byte, error) {
	if _, _, _, err := validateAllowance(a); err != nil {
		return nil, err
	}
	return canonicalObject(map[string]string{
		"adminOperationId": quote(a.AdminOperationID), "authorizerEpoch": strconv.FormatUint(a.AuthorizerEpoch, 10),
		"expectedAllowance": quote(a.ExpectedAllowance), "leadershipEpoch": strconv.FormatUint(a.LeadershipEpoch, 10),
		"module": quote(a.Module), "newAllowance": quote(a.NewAllowance), "nonce": quote(a.Nonce),
		"orgDomain": quote(a.OrgDomain), "safe": quote(a.Safe), "spender": quote(a.Spender), "token": quote(a.Token),
		"validAfter": strconv.FormatUint(a.ValidAfter, 10), "validBefore": strconv.FormatUint(a.ValidBefore, 10),
	}), nil
}

func canonicalObject(fields map[string]string) []byte {
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var output strings.Builder
	output.WriteByte('{')
	for index, key := range keys {
		if index != 0 {
			output.WriteByte(',')
		}
		output.WriteString(quote(key))
		output.WriteByte(':')
		output.WriteString(fields[key])
	}
	output.WriteByte('}')
	return []byte(output.String())
}

func quote(value string) string { return strconv.Quote(value) }

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
