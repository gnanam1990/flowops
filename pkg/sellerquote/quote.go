// Package sellerquote implements the normative ASCP v4 SellerQuote digest and
// the local checks required before a quote may be bound to an operation.
package sellerquote

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

const TypeString = "SellerQuote(bytes32 purchaseSpecHash,bytes32 sellerId,bytes32 resourceId,uint64 directoryVersion,uint16 schemeVersion,uint256 chainId,address asset,uint256 amountBaseUnits,address payTo,address ackAuthority,bytes32 verificationSpecHash,uint64 declaredWorkTime,uint64 verificationBudgetSeconds,uint64 quoteExpiresAt,bytes32 quoteNonce)"

const (
	domainName    = "ASCP"
	domainVersion = "4"
)

var (
	ErrInvalidQuote      = errors.New("invalid seller quote")
	ErrInvalidSignature  = errors.New("invalid seller quote signature")
	ErrQuoteExpired      = errors.New("seller quote is expired")
	ErrDirectoryEvidence = errors.New("directory evidence does not authorize seller quote")
)

var decimalPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)$`)

var (
	typeHash       = crypto.Keccak256Hash([]byte(TypeString))
	domainTypeHash = crypto.Keccak256Hash([]byte("EIP712Domain(string name,string version,uint256 chainId,address verifyingContract)"))
	nameHash       = crypto.Keccak256Hash([]byte(domainName))
	versionHash    = crypto.Keccak256Hash([]byte(domainVersion))
)

// Quote is the wire representation of the SellerQuote EIP-712 message. All
// hashes and addresses must be lowercase 0x-hex. uint256 values are decimal
// strings so callers do not lose precision during JSON round-trips.
type Quote struct {
	PurchaseSpecHash          string `json:"purchaseSpecHash"`
	SellerID                  string `json:"sellerId"`
	ResourceID                string `json:"resourceId"`
	DirectoryVersion          uint64 `json:"directoryVersion"`
	SchemeVersion             uint16 `json:"schemeVersion"`
	ChainID                   string `json:"chainId"`
	Asset                     string `json:"asset"`
	AmountBaseUnits           string `json:"amountBaseUnits"`
	PayTo                     string `json:"payTo"`
	AckAuthority              string `json:"ackAuthority"`
	VerificationSpecHash      string `json:"verificationSpecHash"`
	DeclaredWorkTime          uint64 `json:"declaredWorkTime"`
	VerificationBudgetSeconds uint64 `json:"verificationBudgetSeconds"`
	QuoteExpiresAt            uint64 `json:"quoteExpiresAt"`
	QuoteNonce                string `json:"quoteNonce"`
}

// DirectoryEvidence is a verified leaf and overlay observation for the pinned
// directory version. It intentionally carries no proof format: a chain reader
// must establish this evidence before this package accepts it.
type DirectoryEvidence struct {
	Verified                  bool
	Version                   uint64
	SellerID                  string
	ResourceID                string
	QuoteSigningKey           string
	KeyEpoch                  uint64
	PayoutAddress             string
	AckAuthority              string
	AmountBaseUnits           string
	VerificationSpecHash      string
	DeclaredWorkTime          uint64
	VerificationBudgetSeconds uint64
	Active                    bool
	QuoteKeyRevoked           bool
}

// ExpectedTerms are the immutable values that the caller already calculated
// from the persisted purchase and verification specifications.
type ExpectedTerms struct {
	PurchaseSpecHash string
	SchemeVersion    uint16
	ChainID          string
	Asset            string
}

// Validate verifies wire-level encoding and non-zero fields. It does not use
// the clock, directory, or signature.
func (q Quote) Validate() error {
	for name, value := range map[string]string{
		"purchaseSpecHash": q.PurchaseSpecHash, "sellerId": q.SellerID, "resourceId": q.ResourceID,
		"verificationSpecHash": q.VerificationSpecHash, "quoteNonce": q.QuoteNonce,
	} {
		if _, err := nonZeroHash(value); err != nil {
			return fmt.Errorf("%w: %s: %v", ErrInvalidQuote, name, err)
		}
	}
	if q.DirectoryVersion == 0 || q.SchemeVersion == 0 || q.QuoteExpiresAt == 0 {
		return fmt.Errorf("%w: version and expiry fields must be non-zero", ErrInvalidQuote)
	}
	if _, err := uint256(q.ChainID, false); err != nil {
		return fmt.Errorf("%w: chainId: %v", ErrInvalidQuote, err)
	}
	if _, err := uint256(q.AmountBaseUnits, true); err != nil {
		return fmt.Errorf("%w: amountBaseUnits: %v", ErrInvalidQuote, err)
	}
	for name, value := range map[string]string{"asset": q.Asset, "payTo": q.PayTo, "ackAuthority": q.AckAuthority} {
		if _, err := address(value); err != nil {
			return fmt.Errorf("%w: %s: %v", ErrInvalidQuote, name, err)
		}
	}
	return nil
}

// StructHash returns hashStruct(message), excluding the EIP-712 domain.
func (q Quote) StructHash() (common.Hash, error) {
	if err := q.Validate(); err != nil {
		return common.Hash{}, err
	}
	chainID, _ := uint256(q.ChainID, false)
	amount, _ := uint256(q.AmountBaseUnits, true)
	purchaseSpecHash := mustHash(q.PurchaseSpecHash)
	sellerID := mustHash(q.SellerID)
	resourceID := mustHash(q.ResourceID)
	verificationSpecHash := mustHash(q.VerificationSpecHash)
	quoteNonce := mustHash(q.QuoteNonce)
	values := [][]byte{
		typeHash[:], purchaseSpecHash[:], sellerID[:], resourceID[:],
		uintWord(new(big.Int).SetUint64(q.DirectoryVersion)), uintWord(new(big.Int).SetUint64(uint64(q.SchemeVersion))),
		uintWord(chainID), addressWord(q.Asset), uintWord(amount), addressWord(q.PayTo), addressWord(q.AckAuthority),
		verificationSpecHash[:], uintWord(new(big.Int).SetUint64(q.DeclaredWorkTime)),
		uintWord(new(big.Int).SetUint64(q.VerificationBudgetSeconds)), uintWord(new(big.Int).SetUint64(q.QuoteExpiresAt)), quoteNonce[:],
	}
	return crypto.Keccak256Hash(values...), nil
}

// DomainSeparator returns the ASCP v4 EIP-712 domain for ServiceDirectory.
func DomainSeparator(chainID string, verifyingContract string) (common.Hash, error) {
	chain, err := uint256(chainID, false)
	if err != nil {
		return common.Hash{}, fmt.Errorf("chainId: %w", err)
	}
	if _, err := address(verifyingContract); err != nil {
		return common.Hash{}, fmt.Errorf("verifyingContract: %w", err)
	}
	return crypto.Keccak256Hash(domainTypeHash[:], nameHash[:], versionHash[:], uintWord(chain), addressWord(verifyingContract)), nil
}

// Digest returns keccak256(0x1901 || domainSeparator || structHash).
func (q Quote) Digest(verifyingContract string) (common.Hash, error) {
	structHash, err := q.StructHash()
	if err != nil {
		return common.Hash{}, err
	}
	domain, err := DomainSeparator(q.ChainID, verifyingContract)
	if err != nil {
		return common.Hash{}, err
	}
	return crypto.Keccak256Hash([]byte{0x19, 0x01}, domain[:], structHash[:]), nil
}

// RecoverSigner validates a compact 65-byte Ethereum signature and returns the
// address that signed this quote in the supplied ServiceDirectory domain.
func (q Quote) RecoverSigner(verifyingContract, signature string) (common.Address, error) {
	digest, err := q.Digest(verifyingContract)
	if err != nil {
		return common.Address{}, err
	}
	sig, err := signatureBytes(signature)
	if err != nil {
		return common.Address{}, fmt.Errorf("%w: %v", ErrInvalidSignature, err)
	}
	pub, err := crypto.SigToPub(digest[:], sig)
	if err != nil {
		return common.Address{}, fmt.Errorf("%w: %v", ErrInvalidSignature, err)
	}
	return crypto.PubkeyToAddress(*pub), nil
}

// ValidateForIntake checks expiry, pinned verified directory terms, overlays,
// and the recovered seller signing key. It performs no state mutation.
func (q Quote) ValidateForIntake(now time.Time, verifyingContract string, expected ExpectedTerms, evidence DirectoryEvidence, signature string) (common.Hash, common.Address, error) {
	if err := q.Validate(); err != nil {
		return common.Hash{}, common.Address{}, err
	}
	if now.UTC().Unix() < 0 || uint64(now.UTC().Unix()) >= q.QuoteExpiresAt {
		return common.Hash{}, common.Address{}, ErrQuoteExpired
	}
	if !evidence.Verified || !evidence.Active || evidence.QuoteKeyRevoked || evidence.KeyEpoch == 0 || expected.SchemeVersion == 0 ||
		evidence.Version != q.DirectoryVersion || evidence.SellerID != q.SellerID || evidence.ResourceID != q.ResourceID ||
		evidence.PayoutAddress != q.PayTo || evidence.AckAuthority != q.AckAuthority ||
		evidence.AmountBaseUnits != q.AmountBaseUnits || evidence.VerificationSpecHash != q.VerificationSpecHash ||
		evidence.DeclaredWorkTime != q.DeclaredWorkTime || evidence.VerificationBudgetSeconds != q.VerificationBudgetSeconds ||
		expected.PurchaseSpecHash != q.PurchaseSpecHash || expected.SchemeVersion != q.SchemeVersion ||
		expected.ChainID != q.ChainID || expected.Asset != q.Asset {
		return common.Hash{}, common.Address{}, ErrDirectoryEvidence
	}
	signer, err := q.RecoverSigner(verifyingContract, signature)
	if err != nil {
		return common.Hash{}, common.Address{}, err
	}
	want, err := address(evidence.QuoteSigningKey)
	if err != nil || signer != want {
		return common.Hash{}, common.Address{}, ErrDirectoryEvidence
	}
	digest, err := q.Digest(verifyingContract)
	if err != nil {
		return common.Hash{}, common.Address{}, err
	}
	return digest, signer, nil
}

func nonZeroHash(value string) (common.Hash, error) {
	if !strings.HasPrefix(value, "0x") || len(value) != 66 || strings.ToLower(value) != value {
		return common.Hash{}, errors.New("must be lowercase 32-byte 0x-hex")
	}
	decoded, err := hex.DecodeString(value[2:])
	if err != nil || len(decoded) != 32 {
		return common.Hash{}, errors.New("must be lowercase 32-byte 0x-hex")
	}
	result := common.BytesToHash(decoded)
	if result == (common.Hash{}) {
		return common.Hash{}, errors.New("must be non-zero")
	}
	return result, nil
}

func mustHash(value string) common.Hash { result, _ := nonZeroHash(value); return result }

func uint256(value string, positive bool) (*big.Int, error) {
	if !decimalPattern.MatchString(value) {
		return nil, errors.New("must be a canonical decimal integer")
	}
	result, ok := new(big.Int).SetString(value, 10)
	if !ok || result.Sign() < 0 || result.BitLen() > 256 || (positive && result.Sign() == 0) {
		return nil, errors.New("must fit uint256 and satisfy zero-value rule")
	}
	return result, nil
}

func address(value string) (common.Address, error) {
	if !strings.HasPrefix(value, "0x") || len(value) != 42 || strings.ToLower(value) != value || !common.IsHexAddress(value) {
		return common.Address{}, errors.New("must be a lowercase 20-byte 0x-hex address")
	}
	result := common.HexToAddress(value)
	if result == (common.Address{}) {
		return common.Address{}, errors.New("must be non-zero")
	}
	return result, nil
}

func uintWord(value *big.Int) []byte { return common.LeftPadBytes(value.Bytes(), 32) }

func addressWord(value string) []byte {
	parsed, _ := address(value)
	return common.LeftPadBytes(parsed.Bytes(), 32)
}

func signatureBytes(value string) ([]byte, error) {
	if !strings.HasPrefix(value, "0x") || len(value) != 132 || strings.ToLower(value) != value {
		return nil, errors.New("must be lowercase 65-byte 0x-hex")
	}
	decoded, err := hex.DecodeString(value[2:])
	if err != nil || len(decoded) != 65 {
		return nil, errors.New("must be lowercase 65-byte 0x-hex")
	}
	if decoded[64] == 27 || decoded[64] == 28 {
		decoded[64] -= 27
	}
	if decoded[64] > 1 || !crypto.ValidateSignatureValues(decoded[64], new(big.Int).SetBytes(decoded[:32]), new(big.Int).SetBytes(decoded[32:64]), true) {
		return nil, errors.New("has invalid recovery id or non-canonical S")
	}
	return decoded, nil
}

// ArtifactSHA256 lets a verifier compare a downloaded quote vector or schema
// against an integrity manifest without reimplementing hashing.
func ArtifactSHA256(contents []byte) string {
	sum := sha256.Sum256(contents)
	return hex.EncodeToString(sum[:])
}
