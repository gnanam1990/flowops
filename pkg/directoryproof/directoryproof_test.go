package directoryproof

import (
	"errors"
	"fmt"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/gnanam1990/flowops/pkg/sellerquote"
)

func TestEvidenceForQuoteVerifiesSharedRootAndExactTerms(t *testing.T) {
	observation, quote := fixture(t)
	evidence, err := EvidenceForQuote(observation, quote)
	if err != nil || !evidence.Verified || !evidence.Active || evidence.QuoteKeyRevoked || evidence.AmountBaseUnits != quote.AmountBaseUnits {
		t.Fatalf("evidence=%+v err=%v", evidence, err)
	}
}

func TestEvidenceForQuoteRejectsProofTermAndOverlayChanges(t *testing.T) {
	observation, quote := fixture(t)
	changed := observation
	changed.ResourceProof = []string{testHash(999)}
	if _, err := EvidenceForQuote(changed, quote); !errors.Is(err, ErrInvalidProof) {
		t.Fatalf("proof error = %v", err)
	}
	changed = observation
	changed.Resource.Price = "43"
	resourceHash, err := ResourceLeafHash(changed.Resource)
	if err != nil {
		t.Fatal(err)
	}
	sellerHash, _ := SellerLeafHash(changed.Seller)
	changed.Root = merkle(sellerHash, resourceHash).Hex()
	changed.SellerProof = []string{resourceHash.Hex()}
	changed.ResourceProof = []string{sellerHash.Hex()}
	if _, err := EvidenceForQuote(changed, quote); !errors.Is(err, ErrQuoteTerms) {
		t.Fatalf("price mismatch error = %v", err)
	}
	changed = observation
	changed.SellerPaused = true
	evidence, err := EvidenceForQuote(changed, quote)
	if err != nil || evidence.Active {
		t.Fatalf("paused evidence=%+v err=%v", evidence, err)
	}
	changed = observation
	changed.QuoteKeyRevoked = true
	evidence, err = EvidenceForQuote(changed, quote)
	if err != nil || !evidence.QuoteKeyRevoked {
		t.Fatalf("revoked evidence=%+v err=%v", evidence, err)
	}
}

func TestLeafHashesMatchSolidityStaticABIWords(t *testing.T) {
	observation, _ := fixture(t)
	sellerHash, err := SellerLeafHash(observation.Seller)
	if err != nil {
		t.Fatal(err)
	}
	resourceHash, err := ResourceLeafHash(observation.Resource)
	if err != nil {
		t.Fatal(err)
	}
	if sellerHash == resourceHash || sellerHash == (common.Hash{}) || resourceHash == (common.Hash{}) {
		t.Fatalf("invalid leaf hashes seller=%s resource=%s", sellerHash, resourceHash)
	}
}

func fixture(t *testing.T) (Observation, sellerquote.Quote) {
	t.Helper()
	seller := SellerLeaf{SellerID: testHash(1), PayoutAddress: "0x3333333333333333333333333333333333333333", AckAuthority: "0x4444444444444444444444444444444444444444", QuoteSigningKey: "0xd41c057fd1c78805aac12b0a94a405c0461a6fbb", KeyEpoch: 1, BaseURLOriginHash: testHash(2), Status: activeSellerStatus}
	resource := ResourceLeaf{SellerID: seller.SellerID, ResourceID: testHash(3), Price: "42", EscrowSupported: true, VerificationSpecHash: testHash(4), DeclaredWorkTime: 30, VerificationBudgetSeconds: 10}
	sellerHash, err := SellerLeafHash(seller)
	if err != nil {
		t.Fatal(err)
	}
	resourceHash, err := ResourceLeafHash(resource)
	if err != nil {
		t.Fatal(err)
	}
	root := merkle(sellerHash, resourceHash)
	quote := sellerquote.Quote{PurchaseSpecHash: testHash(10), SellerID: seller.SellerID, ResourceID: resource.ResourceID, DirectoryVersion: 9, SchemeVersion: 1, ChainID: "84532", Asset: "0x036cbd53842c5426634e7929541ec2318f3dcf7e", AmountBaseUnits: resource.Price, PayTo: seller.PayoutAddress, AckAuthority: seller.AckAuthority, VerificationSpecHash: resource.VerificationSpecHash, DeclaredWorkTime: resource.DeclaredWorkTime, VerificationBudgetSeconds: resource.VerificationBudgetSeconds, QuoteExpiresAt: 1900000000, QuoteNonce: testHash(11)}
	return Observation{DirectoryContract: "0x1111111111111111111111111111111111111111", BlockNumber: 100, Version: 9, Root: root.Hex(), Seller: seller, SellerProof: []string{resourceHash.Hex()}, Resource: resource, ResourceProof: []string{sellerHash.Hex()}}, quote
}

func merkle(left, right common.Hash) common.Hash {
	if bytesCompare(left[:], right[:]) <= 0 {
		return crypto.Keccak256Hash(left[:], right[:])
	}
	return crypto.Keccak256Hash(right[:], left[:])
}
func testHash(value uint64) string { return fmt.Sprintf("0x%064x", value) }
