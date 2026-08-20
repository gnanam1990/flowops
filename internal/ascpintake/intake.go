// Package ascpintake owns durable ASCP operation creation and SellerQuote nonce
// consumption. It deliberately has no HTTP, policy, signer, or rail behavior.
package ascpintake

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/gnanam1990/flowops/pkg/envelope"
	"github.com/gnanam1990/flowops/pkg/purchasespec"
	"github.com/gnanam1990/flowops/pkg/sellerquote"
)

const Endpoint = "ascp.intent.create"

var (
	ErrIdempotencyConflict = errors.New("idempotency key names different ASCP intake input")
	ErrQuoteNonceConsumed  = errors.New("seller quote nonce is already owned by another operation")
	ErrPurchaseSpecBinding = errors.New("persisted purchase specification does not bind the seller quote")
)

type Request struct {
	OrganizationID        string
	ActorID               string
	IdempotencyKey        string
	DirectoryContract     string
	Quote                 sellerquote.Quote
	Signature             string
	Expected              sellerquote.ExpectedTerms
	Evidence              sellerquote.DirectoryEvidence
	CanonicalPurchaseSpec []byte
	RequestBody           []byte
}

type Operation struct {
	OperationID       string `json:"operationId"`
	OrganizationID    string `json:"organizationId"`
	ActorID           string `json:"actorId"`
	QuoteHash         string `json:"quoteHash"`
	PurchaseSpecHash  string `json:"purchaseSpecHash"`
	QuoteNonce        string `json:"quoteNonce"`
	DirectoryVersion  uint64 `json:"directoryVersion"`
	DirectoryContract string `json:"directoryContract"`
	SellerSigner      string `json:"sellerSigner"`
	CreatedAt         int64  `json:"createdAt"`
	Replayed          bool   `json:"replayed"`
}

type StoreInput struct {
	Operation          Operation
	IdempotencyKey     string
	CanonicalInputHash string
	QuoteJSON          json.RawMessage
	PurchaseSpecJSON   []byte
	RequestBody        []byte
}

// Store must create the operation and claim its quote nonce in the same durable
// transaction. It returns the stored operation and whether it is an exact
// idempotent replay.
type Store interface {
	Create(context.Context, StoreInput) (Operation, bool, error)
}

type Service struct {
	store Store
	clock func() time.Time
	newID func() (string, error)
}

func New(store Store, clock func() time.Time, random io.Reader) (*Service, error) {
	if store == nil {
		return nil, errors.New("durable intake store is required")
	}
	if clock == nil {
		clock = time.Now
	}
	if random == nil {
		random = rand.Reader
	}
	return &Service{store: store, clock: clock, newID: operationIDSource(random)}, nil
}

// Create validates a quote without mutation and then performs exactly one
// durable operation+nonce claim. An idempotent retry is returned from Store;
// therefore a newly generated operation ID never changes a successful retry.
func (s *Service) Create(ctx context.Context, request Request) (Operation, error) {
	if err := validateScope(request); err != nil {
		return Operation{}, err
	}
	if _, err := purchasespec.ValidatePersisted(request.CanonicalPurchaseSpec, request.RequestBody); err != nil {
		return Operation{}, fmt.Errorf("purchase specification: %w", err)
	}
	if purchasespec.Hash(request.CanonicalPurchaseSpec) != request.Quote.PurchaseSpecHash {
		return Operation{}, ErrPurchaseSpecBinding
	}
	quoteHash, signer, err := request.Quote.ValidateForIntake(s.clock(), request.DirectoryContract, request.Expected, request.Evidence, request.Signature)
	if err != nil {
		return Operation{}, err
	}
	quoteJSON, err := json.Marshal(request.Quote)
	if err != nil {
		return Operation{}, fmt.Errorf("encode seller quote: %w", err)
	}
	inputHash := canonicalInputHash(request, quoteHash.Hex(), strings.ToLower(signer.Hex()))
	operationID, err := s.newID()
	if err != nil {
		return Operation{}, err
	}
	now := s.clock().UTC().Truncate(time.Second)
	operation := Operation{
		OperationID: operationID, OrganizationID: request.OrganizationID, ActorID: request.ActorID,
		QuoteHash: quoteHash.Hex(), PurchaseSpecHash: request.Quote.PurchaseSpecHash, QuoteNonce: request.Quote.QuoteNonce,
		DirectoryVersion: request.Quote.DirectoryVersion, DirectoryContract: request.DirectoryContract,
		SellerSigner: strings.ToLower(signer.Hex()), CreatedAt: now.Unix(),
	}
	stored, replayed, err := s.store.Create(ctx, StoreInput{Operation: operation, IdempotencyKey: request.IdempotencyKey, CanonicalInputHash: inputHash, QuoteJSON: quoteJSON, PurchaseSpecJSON: request.CanonicalPurchaseSpec, RequestBody: request.RequestBody})
	if err != nil {
		return Operation{}, err
	}
	stored.Replayed = replayed
	return stored, nil
}

func validateScope(request Request) error {
	for name, value := range map[string]string{"organizationId": request.OrganizationID, "actorId": request.ActorID, "idempotencyKey": request.IdempotencyKey} {
		if !envelope.ValidIdentifier(value) {
			return fmt.Errorf("%s is invalid", name)
		}
	}
	if len(request.IdempotencyKey) > 128 {
		return errors.New("idempotency key is too long")
	}
	return nil
}

func canonicalInputHash(request Request, quoteHash, signer string) string {
	input := struct {
		Version           string `json:"version"`
		OrganizationID    string `json:"organizationId"`
		ActorID           string `json:"actorId"`
		Endpoint          string `json:"endpoint"`
		IdempotencyKey    string `json:"idempotencyKey"`
		DirectoryContract string `json:"directoryContract"`
		QuoteHash         string `json:"quoteHash"`
		SellerSigner      string `json:"sellerSigner"`
	}{"ASCP_INTAKE_V1", request.OrganizationID, request.ActorID, Endpoint, request.IdempotencyKey, request.DirectoryContract, quoteHash, signer}
	encoded, _ := json.Marshal(input)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func operationIDSource(random io.Reader) func() (string, error) {
	return func() (string, error) {
		bytes := make([]byte, 32)
		if _, err := io.ReadFull(random, bytes); err != nil {
			return "", fmt.Errorf("generate operation ID: %w", err)
		}
		allZero := true
		for _, value := range bytes {
			allZero = allZero && value == 0
		}
		if allZero {
			return "", errors.New("generated zero operation ID")
		}
		return "0x" + hex.EncodeToString(bytes), nil
	}
}

// MemoryStore models the required serializable transaction for focused tests.
// It is not durable and must never be selected by a production runtime.
type MemoryStore struct {
	mu      sync.Mutex
	byScope map[string]memoryRecord
	byNonce map[string]string
}

type memoryRecord struct {
	hash      string
	operation Operation
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{byScope: make(map[string]memoryRecord), byNonce: make(map[string]string)}
}

func (s *MemoryStore) Create(ctx context.Context, input StoreInput) (Operation, bool, error) {
	if err := ctx.Err(); err != nil {
		return Operation{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	scope := input.Operation.OrganizationID + "\x00" + input.Operation.ActorID + "\x00" + Endpoint + "\x00" + input.IdempotencyKey
	if existing, found := s.byScope[scope]; found {
		if existing.hash != input.CanonicalInputHash {
			return Operation{}, false, ErrIdempotencyConflict
		}
		return existing.operation, true, nil
	}
	if _, claimed := s.byNonce[input.Operation.QuoteNonce]; claimed {
		return Operation{}, false, ErrQuoteNonceConsumed
	}
	stored := input.Operation
	s.byScope[scope] = memoryRecord{hash: input.CanonicalInputHash, operation: stored}
	s.byNonce[input.Operation.QuoteNonce] = input.Operation.OperationID
	return stored, false, nil
}
