// Package adminauthorization implements the shared ASCP v4 EIP-712 digest
// used by keeper-relayed hot administrative actions.
package adminauthorization

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
	TypeString                  = "AdminActionAuthorization(bytes32 orgDomain,address contractAddress,uint256 chainId,bytes32 authorityRole,bytes4 functionSelector,bytes32 payloadHash,bytes32 adminOperationId,uint256 adminNonce,uint64 adminEpoch,uint64 validAfter,uint64 validBefore,bytes32 workflowId)"
	MaximumWindowSeconds uint64 = 600
)

var (
	ErrInvalidAuthorization = errors.New("invalid admin action authorization")
	decimalPattern          = regexp.MustCompile(`^(0|[1-9][0-9]*)$`)
	domainTypeHash          = crypto.Keccak256Hash([]byte("EIP712Domain(string name,string version,uint256 chainId,address verifyingContract)"))
	nameHash                = crypto.Keccak256Hash([]byte("ASCP"))
	versionHash             = crypto.Keccak256Hash([]byte("4"))
	typeHash                = crypto.Keccak256Hash([]byte(TypeString))
)

type Authorization struct {
	OrgDomain        string `json:"orgDomain"`
	ContractAddress  string `json:"contractAddress"`
	ChainID          string `json:"chainId"`
	AuthorityRole    string `json:"authorityRole"`
	FunctionSelector string `json:"functionSelector"`
	PayloadHash      string `json:"payloadHash"`
	AdminOperationID string `json:"adminOperationId"`
	AdminNonce       string `json:"adminNonce"`
	AdminEpoch       uint64 `json:"adminEpoch"`
	ValidAfter       uint64 `json:"validAfter"`
	ValidBefore      uint64 `json:"validBefore"`
	WorkflowID       string `json:"workflowId"`
}

func (a Authorization) StructHash() (common.Hash, error) {
	chain, nonce, err := a.validate()
	if err != nil {
		return common.Hash{}, err
	}
	return crypto.Keccak256Hash(
		typeHash[:], hashWord(a.OrgDomain), addressWord(a.ContractAddress), uintWord(chain),
		hashWord(a.AuthorityRole), selectorWord(a.FunctionSelector), hashWord(a.PayloadHash),
		hashWord(a.AdminOperationID), uintWord(nonce), uint64Word(a.AdminEpoch),
		uint64Word(a.ValidAfter), uint64Word(a.ValidBefore), hashWord(a.WorkflowID),
	), nil
}

func (a Authorization) Digest() (common.Hash, error) {
	structHash, err := a.StructHash()
	if err != nil {
		return common.Hash{}, err
	}
	domain, err := DomainSeparator(a.ChainID, a.ContractAddress)
	if err != nil {
		return common.Hash{}, err
	}
	return crypto.Keccak256Hash([]byte{0x19, 0x01}, domain[:], structHash[:]), nil
}

func DomainSeparator(chainID, contractAddress string) (common.Hash, error) {
	chain, err := decimal(chainID)
	if err != nil || chain.Sign() == 0 || !validAddress(contractAddress) {
		return common.Hash{}, ErrInvalidAuthorization
	}
	return crypto.Keccak256Hash(
		domainTypeHash[:], nameHash[:], versionHash[:], uintWord(chain), addressWord(contractAddress),
	), nil
}

func (a Authorization) validate() (*big.Int, *big.Int, error) {
	if !validNonZeroHash(a.OrgDomain) || !validAddress(a.ContractAddress) ||
		!validNonZeroHash(a.AuthorityRole) || !validSelector(a.FunctionSelector) ||
		!validNonZeroHash(a.PayloadHash) || !validNonZeroHash(a.AdminOperationID) ||
		!validHash(a.WorkflowID) || a.AdminEpoch == 0 || a.ValidBefore <= a.ValidAfter ||
		a.ValidBefore-a.ValidAfter > MaximumWindowSeconds {
		return nil, nil, ErrInvalidAuthorization
	}
	chain, err := decimal(a.ChainID)
	if err != nil || chain.Sign() == 0 {
		return nil, nil, ErrInvalidAuthorization
	}
	nonce, err := decimal(a.AdminNonce)
	if err != nil {
		return nil, nil, ErrInvalidAuthorization
	}
	return chain, nonce, nil
}

func validNonZeroHash(value string) bool {
	return validHash(value) && common.HexToHash(value) != (common.Hash{})
}

func validHash(value string) bool {
	if len(value) != 66 || !strings.HasPrefix(value, "0x") || value != strings.ToLower(value) {
		return false
	}
	decoded, err := hex.DecodeString(value[2:])
	return err == nil && len(decoded) == 32
}

func validAddress(value string) bool {
	return len(value) == 42 && strings.HasPrefix(value, "0x") && value == strings.ToLower(value) &&
		common.IsHexAddress(value) && common.HexToAddress(value) != (common.Address{})
}

func validSelector(value string) bool {
	if len(value) != 10 || !strings.HasPrefix(value, "0x") || value != strings.ToLower(value) {
		return false
	}
	decoded, err := hex.DecodeString(value[2:])
	return err == nil && len(decoded) == 4 && decoded[0]|decoded[1]|decoded[2]|decoded[3] != 0
}

func decimal(value string) (*big.Int, error) {
	if !decimalPattern.MatchString(value) {
		return nil, ErrInvalidAuthorization
	}
	result, ok := new(big.Int).SetString(value, 10)
	if !ok || result.BitLen() > 256 {
		return nil, ErrInvalidAuthorization
	}
	return result, nil
}

func hashWord(value string) []byte { return common.HexToHash(value).Bytes() }
func addressWord(value string) []byte {
	return common.LeftPadBytes(common.HexToAddress(value).Bytes(), 32)
}
func selectorWord(value string) []byte {
	decoded, _ := hex.DecodeString(value[2:])
	return common.RightPadBytes(decoded, 32)
}
func uint64Word(value uint64) []byte { return uintWord(new(big.Int).SetUint64(value)) }
func uintWord(value *big.Int) []byte { return common.LeftPadBytes(value.Bytes(), 32) }
