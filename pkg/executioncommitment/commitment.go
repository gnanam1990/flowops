// Package executioncommitment implements the normative ASCP v4
// ExecutionCommitment EIP-712 digest. The resulting digest is the immutable
// value every money-moving boundary must bind to.
package executioncommitment

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

const TypeString = "ExecutionCommitment(bytes32 orgDomain,bytes32 operationId,uint8 rail,uint16 schemeVersion,uint8 protection,address escrowContract,bytes32 purchaseSpecHash,bytes32 quoteHash,bytes32 verificationSpecHash,uint64 declaredWorkTime,uint64 verificationBudgetSeconds,uint64 directoryVersion,bytes32 sellerId,bytes32 resourceId,address payTo,address ackAuthority,uint256 amount,uint256 chainId,address asset,uint64 quoteExpiresAt,uint64 acceptBy,uint64 deliverBy,uint64 settleBy)"

const (
	RailEscrow       uint8  = 1
	SchemeVersionV1  uint16 = 1
	ProtectionEscrow uint8  = 1
	domainName              = "ASCP"
	domainVersion           = "4"
)

var (
	ErrInvalidCommitment = errors.New("invalid execution commitment")
	ErrDomainMismatch    = errors.New("execution commitment domain mismatch")

	decimalPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)$`)
	typeHash       = crypto.Keccak256Hash([]byte(TypeString))
	domainTypeHash = crypto.Keccak256Hash([]byte("EIP712Domain(string name,string version,uint256 chainId,address verifyingContract)"))
	nameHash       = crypto.Keccak256Hash([]byte(domainName))
	versionHash    = crypto.Keccak256Hash([]byte(domainVersion))
)

// Commitment is the canonical JSON-safe representation. bytes32 and address
// fields are lowercase 0x-hex; uint256 fields are canonical decimal strings.
type Commitment struct {
	OrgDomain                 string `json:"orgDomain"`
	OperationID               string `json:"operationId"`
	Rail                      uint8  `json:"rail"`
	SchemeVersion             uint16 `json:"schemeVersion"`
	Protection                uint8  `json:"protection"`
	EscrowContract            string `json:"escrowContract"`
	PurchaseSpecHash          string `json:"purchaseSpecHash"`
	QuoteHash                 string `json:"quoteHash"`
	VerificationSpecHash      string `json:"verificationSpecHash"`
	DeclaredWorkTime          uint64 `json:"declaredWorkTime"`
	VerificationBudgetSeconds uint64 `json:"verificationBudgetSeconds"`
	DirectoryVersion          uint64 `json:"directoryVersion"`
	SellerID                  string `json:"sellerId"`
	ResourceID                string `json:"resourceId"`
	PayTo                     string `json:"payTo"`
	AckAuthority              string `json:"ackAuthority"`
	Amount                    string `json:"amount"`
	ChainID                   string `json:"chainId"`
	Asset                     string `json:"asset"`
	QuoteExpiresAt            uint64 `json:"quoteExpiresAt"`
	AcceptBy                  uint64 `json:"acceptBy"`
	DeliverBy                 uint64 `json:"deliverBy"`
	SettleBy                  uint64 `json:"settleBy"`
}

// Validate rejects ambiguous encodings and values that cannot represent the
// only v1 escrow protection scheme. Clock-relative deadline checks belong at
// the boundary that has an authoritative clock.
func (c Commitment) Validate() error {
	for name, value := range map[string]string{
		"orgDomain": c.OrgDomain, "operationId": c.OperationID, "purchaseSpecHash": c.PurchaseSpecHash,
		"quoteHash": c.QuoteHash, "verificationSpecHash": c.VerificationSpecHash, "sellerId": c.SellerID,
		"resourceId": c.ResourceID,
	} {
		if _, err := nonZeroHash(value); err != nil {
			return fmt.Errorf("%w: %s: %v", ErrInvalidCommitment, name, err)
		}
	}
	if c.Rail != RailEscrow || c.SchemeVersion != SchemeVersionV1 || c.Protection != ProtectionEscrow {
		return fmt.Errorf("%w: unsupported rail, scheme version, or protection", ErrInvalidCommitment)
	}
	if c.DirectoryVersion == 0 || c.DeclaredWorkTime == 0 || c.VerificationBudgetSeconds == 0 ||
		c.QuoteExpiresAt == 0 || c.AcceptBy == 0 || c.DeliverBy == 0 || c.SettleBy == 0 ||
		c.AcceptBy >= c.DeliverBy || c.DeliverBy >= c.SettleBy {
		return fmt.Errorf("%w: invalid versions, timing budget, or deadline order", ErrInvalidCommitment)
	}
	for name, value := range map[string]string{
		"escrowContract": c.EscrowContract, "payTo": c.PayTo, "ackAuthority": c.AckAuthority, "asset": c.Asset,
	} {
		if _, err := address(value); err != nil {
			return fmt.Errorf("%w: %s: %v", ErrInvalidCommitment, name, err)
		}
	}
	if _, err := uint256(c.Amount, true); err != nil {
		return fmt.Errorf("%w: amount: %v", ErrInvalidCommitment, err)
	}
	if _, err := uint256(c.ChainID, true); err != nil {
		return fmt.Errorf("%w: chainId: %v", ErrInvalidCommitment, err)
	}
	return nil
}

// StructHash returns hashStruct(commitment), excluding its EIP-712 domain.
func (c Commitment) StructHash() (common.Hash, error) {
	if err := c.Validate(); err != nil {
		return common.Hash{}, err
	}
	amount, _ := uint256(c.Amount, true)
	chainID, _ := uint256(c.ChainID, true)
	values := [][]byte{
		typeHash[:], hashWord(c.OrgDomain), hashWord(c.OperationID), uint64Word(uint64(c.Rail)), uint64Word(uint64(c.SchemeVersion)), uint64Word(uint64(c.Protection)),
		addressWord(c.EscrowContract), hashWord(c.PurchaseSpecHash), hashWord(c.QuoteHash), hashWord(c.VerificationSpecHash),
		uint64Word(c.DeclaredWorkTime), uint64Word(c.VerificationBudgetSeconds), uint64Word(c.DirectoryVersion),
		hashWord(c.SellerID), hashWord(c.ResourceID), addressWord(c.PayTo), addressWord(c.AckAuthority), uintWord(amount), uintWord(chainID), addressWord(c.Asset),
		uint64Word(c.QuoteExpiresAt), uint64Word(c.AcceptBy), uint64Word(c.DeliverBy), uint64Word(c.SettleBy),
	}
	return crypto.Keccak256Hash(values...), nil
}

// DomainSeparator returns the ASCP v4 EIP-712 domain for an escrow contract.
func DomainSeparator(chainID, verifyingContract string) (common.Hash, error) {
	chain, err := uint256(chainID, true)
	if err != nil {
		return common.Hash{}, fmt.Errorf("chainId: %w", err)
	}
	if _, err := address(verifyingContract); err != nil {
		return common.Hash{}, fmt.Errorf("verifyingContract: %w", err)
	}
	return crypto.Keccak256Hash(domainTypeHash[:], nameHash[:], versionHash[:], uintWord(chain), addressWord(verifyingContract)), nil
}

// Digest implements executionCommitmentDigest(c, escrow, domainChainId). It
// deliberately requires both independently supplied boundary values to match
// the message, preventing a caller from signing for a different chain or
// escrow contract.
func (c Commitment) Digest(escrow, domainChainID string) (common.Hash, error) {
	if err := c.Validate(); err != nil {
		return common.Hash{}, err
	}
	if escrow != c.EscrowContract || domainChainID != c.ChainID {
		return common.Hash{}, ErrDomainMismatch
	}
	structHash, err := c.StructHash()
	if err != nil {
		return common.Hash{}, err
	}
	domain, err := DomainSeparator(domainChainID, escrow)
	if err != nil {
		return common.Hash{}, err
	}
	return crypto.Keccak256Hash([]byte{0x19, 0x01}, domain[:], structHash[:]), nil
}

func nonZeroHash(value string) (common.Hash, error) {
	if !strings.HasPrefix(value, "0x") || len(value) != 66 || strings.ToLower(value) != value {
		return common.Hash{}, errors.New("must be lowercase non-zero 32-byte 0x-hex")
	}
	decoded, err := hex.DecodeString(value[2:])
	if err != nil || len(decoded) != 32 {
		return common.Hash{}, errors.New("must be lowercase non-zero 32-byte 0x-hex")
	}
	result := common.BytesToHash(decoded)
	if result == (common.Hash{}) {
		return common.Hash{}, errors.New("must be lowercase non-zero 32-byte 0x-hex")
	}
	return result, nil
}

func uint256(value string, positive bool) (*big.Int, error) {
	if !decimalPattern.MatchString(value) {
		return nil, errors.New("must be a canonical decimal integer")
	}
	result, ok := new(big.Int).SetString(value, 10)
	if !ok || result.BitLen() > 256 || (positive && result.Sign() == 0) {
		return nil, errors.New("must fit uint256 and satisfy zero-value rule")
	}
	return result, nil
}

func address(value string) (common.Address, error) {
	if !strings.HasPrefix(value, "0x") || len(value) != 42 || strings.ToLower(value) != value || !common.IsHexAddress(value) {
		return common.Address{}, errors.New("must be lowercase non-zero 20-byte 0x-hex")
	}
	result := common.HexToAddress(value)
	if result == (common.Address{}) {
		return common.Address{}, errors.New("must be lowercase non-zero 20-byte 0x-hex")
	}
	return result, nil
}

func hashWord(value string) []byte   { hash, _ := nonZeroHash(value); return hash[:] }
func uint64Word(value uint64) []byte { return uintWord(new(big.Int).SetUint64(value)) }
func uintWord(value *big.Int) []byte { return common.LeftPadBytes(value.Bytes(), 32) }
func addressWord(value string) []byte {
	parsed, _ := address(value)
	return common.LeftPadBytes(parsed.Bytes(), 32)
}

// ArtifactSHA256 lets a verifier compare a published schema or vector without
// reimplementing the EIP-712 encoding.
func ArtifactSHA256(contents []byte) string {
	sum := sha256.Sum256(contents)
	return hex.EncodeToString(sum[:])
}
