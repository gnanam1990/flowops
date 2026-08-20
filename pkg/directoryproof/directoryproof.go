// Package directoryproof verifies ServiceDirectory leaves and converts a
// finalized observation into sellerquote.DirectoryEvidence. It performs no RPC.
package directoryproof

import (
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/gnanam1990/flowops/pkg/sellerquote"
)

const activeSellerStatus = 1

var (
	ErrInvalidObservation = errors.New("invalid ServiceDirectory observation")
	ErrInvalidProof       = errors.New("ServiceDirectory Merkle proof does not match root")
	ErrQuoteTerms         = errors.New("directory leaves do not match seller quote")
)

var (
	sellerDomain   = crypto.Keccak256Hash([]byte("ASCP_SELLER_LEAF_V1"))
	resourceDomain = crypto.Keccak256Hash([]byte("ASCP_RESOURCE_LEAF_V1"))
)

type SellerLeaf struct {
	SellerID          string
	PayoutAddress     string
	AckAuthority      string
	QuoteSigningKey   string
	KeyEpoch          uint64
	BaseURLOriginHash string
	Status            uint8
}

type ResourceLeaf struct {
	SellerID                  string
	ResourceID                string
	Price                     string
	EscrowSupported           bool
	VerificationSpecHash      string
	DeclaredWorkTime          uint64
	VerificationBudgetSeconds uint64
}

// Observation is produced by a future finalized-chain reader. BlockNumber is
// retained so callers can enforce a finality policy before accepting evidence.
type Observation struct {
	DirectoryContract string
	BlockNumber       uint64
	Version           uint64
	Root              string
	Seller            SellerLeaf
	SellerProof       []string
	Resource          ResourceLeaf
	ResourceProof     []string
	SellerPaused      bool
	QuoteKeyRevoked   bool
}

func SellerLeafHash(leaf SellerLeaf) (common.Hash, error) {
	if err := validateSeller(leaf); err != nil {
		return common.Hash{}, err
	}
	return crypto.Keccak256Hash(sellerDomain[:], hashBytes(leaf.SellerID), addressWord(leaf.PayoutAddress), addressWord(leaf.AckAuthority), addressWord(leaf.QuoteSigningKey), uintWord(new(big.Int).SetUint64(leaf.KeyEpoch)), hashBytes(leaf.BaseURLOriginHash), uintWord(new(big.Int).SetUint64(uint64(leaf.Status)))), nil
}

func ResourceLeafHash(leaf ResourceLeaf) (common.Hash, error) {
	if err := validateResource(leaf); err != nil {
		return common.Hash{}, err
	}
	price, _ := uint256(leaf.Price)
	boolWord := make([]byte, 32)
	if leaf.EscrowSupported {
		boolWord[31] = 1
	}
	return crypto.Keccak256Hash(resourceDomain[:], hashBytes(leaf.SellerID), hashBytes(leaf.ResourceID), uintWord(price), boolWord, hashBytes(leaf.VerificationSpecHash), uintWord(new(big.Int).SetUint64(leaf.DeclaredWorkTime)), uintWord(new(big.Int).SetUint64(leaf.VerificationBudgetSeconds))), nil
}

func Verify(root string, leaf common.Hash, proof []string) error {
	want, err := hash(root)
	if err != nil {
		return fmt.Errorf("%w: root", ErrInvalidObservation)
	}
	current := leaf
	for _, item := range proof {
		sibling, err := hash(item)
		if err != nil {
			return fmt.Errorf("%w: proof item", ErrInvalidObservation)
		}
		if bytesCompare(current[:], sibling[:]) <= 0 {
			current = crypto.Keccak256Hash(current[:], sibling[:])
		} else {
			current = crypto.Keccak256Hash(sibling[:], current[:])
		}
	}
	if current != want {
		return ErrInvalidProof
	}
	return nil
}

// EvidenceForQuote verifies both leaves against the same finalized root and
// returns the exact evidence accepted by SellerQuote intake.
func EvidenceForQuote(observation Observation, quote sellerquote.Quote) (sellerquote.DirectoryEvidence, error) {
	if observation.BlockNumber == 0 || observation.Version == 0 || observation.Version != quote.DirectoryVersion || !isAddress(observation.DirectoryContract) {
		return sellerquote.DirectoryEvidence{}, ErrInvalidObservation
	}
	sellerHash, err := SellerLeafHash(observation.Seller)
	if err != nil {
		return sellerquote.DirectoryEvidence{}, err
	}
	if err := Verify(observation.Root, sellerHash, observation.SellerProof); err != nil {
		return sellerquote.DirectoryEvidence{}, err
	}
	resourceHash, err := ResourceLeafHash(observation.Resource)
	if err != nil {
		return sellerquote.DirectoryEvidence{}, err
	}
	if err := Verify(observation.Root, resourceHash, observation.ResourceProof); err != nil {
		return sellerquote.DirectoryEvidence{}, err
	}
	if observation.Seller.SellerID != quote.SellerID || observation.Resource.SellerID != quote.SellerID || observation.Resource.ResourceID != quote.ResourceID ||
		observation.Seller.PayoutAddress != quote.PayTo || observation.Seller.AckAuthority != quote.AckAuthority || observation.Resource.Price != quote.AmountBaseUnits ||
		observation.Resource.VerificationSpecHash != quote.VerificationSpecHash || observation.Resource.DeclaredWorkTime != quote.DeclaredWorkTime ||
		observation.Resource.VerificationBudgetSeconds != quote.VerificationBudgetSeconds {
		return sellerquote.DirectoryEvidence{}, ErrQuoteTerms
	}
	return sellerquote.DirectoryEvidence{Verified: true, Version: observation.Version, SellerID: observation.Seller.SellerID, ResourceID: observation.Resource.ResourceID,
		QuoteSigningKey: observation.Seller.QuoteSigningKey, KeyEpoch: observation.Seller.KeyEpoch, PayoutAddress: observation.Seller.PayoutAddress, AckAuthority: observation.Seller.AckAuthority,
		AmountBaseUnits: observation.Resource.Price, VerificationSpecHash: observation.Resource.VerificationSpecHash, DeclaredWorkTime: observation.Resource.DeclaredWorkTime,
		VerificationBudgetSeconds: observation.Resource.VerificationBudgetSeconds, Active: observation.Seller.Status == activeSellerStatus && observation.Resource.EscrowSupported && !observation.SellerPaused,
		QuoteKeyRevoked: observation.QuoteKeyRevoked}, nil
}

func validateSeller(leaf SellerLeaf) error {
	if leaf.KeyEpoch == 0 || !isHash(leaf.SellerID) || !isHash(leaf.BaseURLOriginHash) || !isAddress(leaf.PayoutAddress) || !isAddress(leaf.AckAuthority) || !isAddress(leaf.QuoteSigningKey) {
		return ErrInvalidObservation
	}
	return nil
}

func validateResource(leaf ResourceLeaf) error {
	if !isHash(leaf.SellerID) || !isHash(leaf.ResourceID) || !isHash(leaf.VerificationSpecHash) {
		return ErrInvalidObservation
	}
	if _, err := uint256(leaf.Price); err != nil {
		return ErrInvalidObservation
	}
	return nil
}

func hash(value string) (common.Hash, error) {
	if !isHash(value) {
		return common.Hash{}, errors.New("invalid hash")
	}
	decoded, _ := hex.DecodeString(value[2:])
	return common.BytesToHash(decoded), nil
}
func hashBytes(value string) []byte { result, _ := hash(value); return result[:] }
func isHash(value string) bool {
	return len(value) == 66 && strings.HasPrefix(value, "0x") && strings.ToLower(value) == value && value != "0x"+strings.Repeat("0", 64) && validHex(value[2:])
}
func isAddress(value string) bool {
	return len(value) == 42 && strings.HasPrefix(value, "0x") && strings.ToLower(value) == value && value != "0x"+strings.Repeat("0", 40) && common.IsHexAddress(value)
}
func uint256(value string) (*big.Int, error) {
	if value == "" || (len(value) > 1 && value[0] == '0') {
		return nil, errors.New("invalid uint256")
	}
	result, ok := new(big.Int).SetString(value, 10)
	if !ok || result.Sign() <= 0 || result.BitLen() > 256 {
		return nil, errors.New("invalid uint256")
	}
	return result, nil
}
func uintWord(value *big.Int) []byte { return common.LeftPadBytes(value.Bytes(), 32) }
func addressWord(value string) []byte {
	return common.LeftPadBytes(common.HexToAddress(value).Bytes(), 32)
}
func validHex(value string) bool { _, err := hex.DecodeString(value); return err == nil }
func bytesCompare(left, right []byte) int {
	for i := range left {
		if left[i] < right[i] {
			return -1
		}
		if left[i] > right[i] {
			return 1
		}
	}
	return 0
}
