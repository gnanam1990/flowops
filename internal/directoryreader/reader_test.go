package directoryreader

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/gnanam1990/flowops/pkg/directoryproof"
	"github.com/gnanam1990/flowops/pkg/sellerquote"
)

func TestEvidenceForQuoteRequiresExactFinalizedQuorum(t *testing.T) {
	observation, quote := fixture(t)
	observedAt := time.Unix(1800000000, 0).UTC()
	reader := newReaderAt(t, observedAt, source{name: "alpha", observation: observation}, source{name: "bravo", observation: observation})
	result, err := reader.EvidenceForQuote(context.Background(), quote)
	if err != nil || !result.Evidence.Verified || !result.Evidence.Active || len(result.Providers) != 2 || result.FinalizedBlockNumber != 100 || result.ObservationDigest == "" || !result.ObservedAt.Equal(observedAt) {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestEvidenceForQuoteRejectsAnyConflictingSnapshot(t *testing.T) {
	observation, quote := fixture(t)
	conflict := observation
	conflict.FinalizedBlockHash = testHash(99)
	reader := newReader(t, source{name: "alpha", observation: observation}, source{name: "bravo", observation: conflict})
	if _, err := reader.EvidenceForQuote(context.Background(), quote); !errors.Is(err, ErrObserverDisagreement) {
		t.Fatalf("err=%v", err)
	}
}

func TestEvidenceForQuoteRejectsWrongChainContractCodeAndBlockBinding(t *testing.T) {
	observation, quote := fixture(t)
	for name, mutate := range map[string]func(*FinalizedObservation){
		"chain": func(value *FinalizedObservation) { value.ChainID = 1 },
		"contract": func(value *FinalizedObservation) {
			value.DirectoryContract = "0x2222222222222222222222222222222222222222"
		},
		"code":  func(value *FinalizedObservation) { value.DirectoryCodeHash = testHash(88) },
		"block": func(value *FinalizedObservation) { value.Directory.BlockNumber++ },
	} {
		t.Run(name, func(t *testing.T) {
			invalid := observation
			mutate(&invalid)
			reader := newReader(t, source{name: "alpha", observation: invalid}, source{name: "bravo", observation: invalid})
			if _, err := reader.EvidenceForQuote(context.Background(), quote); !errors.Is(err, ErrQuorumUnavailable) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestEvidenceForQuoteRequiresDistinctSourceNamesAndProviderIdentity(t *testing.T) {
	observation, quote := fixture(t)
	if _, err := New(Config{ChainID: 84532, Directory: observation.DirectoryContract, DirectoryCodeHash: observation.DirectoryCodeHash, Sources: []Source{source{name: "alpha"}, source{name: "ALPHA"}}, Quorum: 2}); !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatalf("constructor err=%v", err)
	}
	spoofed := observation
	spoofed.Provider = "someone-else"
	reader := newReader(t, source{name: "alpha", observation: spoofed}, source{name: "bravo", observation: spoofed})
	if _, err := reader.EvidenceForQuote(context.Background(), quote); !errors.Is(err, ErrQuorumUnavailable) {
		t.Fatalf("err=%v", err)
	}
}

func TestEvidenceForQuoteReturnsSafeInactiveOverlay(t *testing.T) {
	observation, quote := fixture(t)
	observation.Directory.SellerPaused = true
	reader := newReader(t, source{name: "alpha", observation: observation}, source{name: "bravo", observation: observation})
	result, err := reader.EvidenceForQuote(context.Background(), quote)
	if err != nil || result.Evidence.Active || !result.Evidence.Verified {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestEvidenceForQuoteDoesNotUseFailedSourceAsQuorum(t *testing.T) {
	observation, quote := fixture(t)
	reader := newReader(t, source{name: "alpha", observation: observation}, source{name: "bravo", err: errors.New("RPC timed out")})
	if _, err := reader.EvidenceForQuote(context.Background(), quote); !errors.Is(err, ErrQuorumUnavailable) {
		t.Fatalf("err=%v", err)
	}
}

func TestEvidenceForQuoteQueriesSourcesConcurrently(t *testing.T) {
	observation, quote := fixture(t)
	var started atomic.Int32
	block := make(chan struct{})
	reader := newReader(t,
		source{name: "alpha", observation: observation, started: &started, block: block},
		source{name: "bravo", observation: observation, started: &started, block: block},
	)
	done := make(chan error, 1)
	go func() { _, err := reader.EvidenceForQuote(context.Background(), quote); done <- err }()
	for started.Load() != 2 {
	}
	close(block)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

type source struct {
	name        string
	observation FinalizedObservation
	err         error
	started     *atomic.Int32
	block       <-chan struct{}
}

func (s source) Name() string { return s.name }
func (s source) ReadFinalized(context.Context) (FinalizedObservation, error) {
	if s.started != nil {
		s.started.Add(1)
	}
	if s.block != nil {
		<-s.block
	}
	observation := s.observation
	if observation.Provider == "" {
		observation.Provider = s.name
	}
	return observation, s.err
}

func newReader(t *testing.T, sources ...Source) *Reader {
	return newReaderAt(t, time.Now(), sources...)
}

func newReaderAt(t *testing.T, observedAt time.Time, sources ...Source) *Reader {
	t.Helper()
	observation, _ := fixture(t)
	reader, err := New(Config{ChainID: 84532, Directory: observation.DirectoryContract, DirectoryCodeHash: observation.DirectoryCodeHash, Sources: sources, Quorum: 2}, func() time.Time { return observedAt })
	if err != nil {
		t.Fatal(err)
	}
	return reader
}

func fixture(t *testing.T) (FinalizedObservation, sellerquote.Quote) {
	t.Helper()
	seller := directoryproof.SellerLeaf{SellerID: testHash(1), PayoutAddress: "0x3333333333333333333333333333333333333333", AckAuthority: "0x4444444444444444444444444444444444444444", QuoteSigningKey: "0xd41c057fd1c78805aac12b0a94a405c0461a6fbb", KeyEpoch: 1, BaseURLOriginHash: testHash(2), Status: 1}
	resource := directoryproof.ResourceLeaf{SellerID: seller.SellerID, ResourceID: testHash(3), Price: "42", EscrowSupported: true, VerificationSpecHash: testHash(4), DeclaredWorkTime: 30, VerificationBudgetSeconds: 10}
	sellerHash, err := directoryproof.SellerLeafHash(seller)
	if err != nil {
		t.Fatal(err)
	}
	resourceHash, err := directoryproof.ResourceLeafHash(resource)
	if err != nil {
		t.Fatal(err)
	}
	root := merkle(sellerHash, resourceHash)
	directory := directoryproof.Observation{DirectoryContract: "0x1111111111111111111111111111111111111111", BlockNumber: 100, Version: 9, Root: root.Hex(), Seller: seller, SellerProof: []string{resourceHash.Hex()}, Resource: resource, ResourceProof: []string{sellerHash.Hex()}}
	return FinalizedObservation{Provider: "", ChainID: 84532, DirectoryContract: directory.DirectoryContract, DirectoryCodeHash: testHash(17), FinalizedBlockNumber: 100, FinalizedBlockHash: testHash(18), Directory: directory}, sellerquote.Quote{PurchaseSpecHash: testHash(10), SellerID: seller.SellerID, ResourceID: resource.ResourceID, DirectoryVersion: 9, SchemeVersion: 1, ChainID: "84532", Asset: "0x036cbd53842c5426634e7929541ec2318f3dcf7e", AmountBaseUnits: resource.Price, PayTo: seller.PayoutAddress, AckAuthority: seller.AckAuthority, VerificationSpecHash: resource.VerificationSpecHash, DeclaredWorkTime: resource.DeclaredWorkTime, VerificationBudgetSeconds: resource.VerificationBudgetSeconds, QuoteExpiresAt: 1900000000, QuoteNonce: testHash(11)}
}

func merkle(left, right common.Hash) common.Hash {
	if string(left[:]) < string(right[:]) {
		return crypto.Keccak256Hash(left[:], right[:])
	}
	return crypto.Keccak256Hash(right[:], left[:])
}
func testHash(value uint64) string { return fmt.Sprintf("0x%064x", value) }
