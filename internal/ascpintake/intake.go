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

	"github.com/gnanam1990/flowops/internal/ascpadaptation"
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
	Adaptation            *ascpadaptation.SignedGrant
	AdaptationSigner      string
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
	AdaptationGrantID string `json:"adaptationGrantId,omitempty"`
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
	AdaptationGrantID  string
	AdaptationDigest   string
}

// Store must create the operation and claim its quote nonce in the same durable
// transaction. It returns the stored operation and whether it is an exact
// idempotent replay.
type Store interface {
	Create(context.Context, StoreInput) (Operation, bool, error)
	Lookup(context.Context, string, string, string) (Operation, string, bool, error)
}

// Reader exposes the immutable, redacted operation projection needed by
// agent-facing status APIs. Implementations must enforce both tenant and actor
// scope in the storage query rather than filtering an unscoped result later.
type Reader interface {
	Get(context.Context, string, string, string) (Operation, error)
}

var ErrNotFound = errors.New("ASCP operation not found")

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
	if replayed, found, err := s.Replay(ctx, request); err != nil || found {
		return replayed, err
	}
	spec, err := purchasespec.ValidatePersisted(request.CanonicalPurchaseSpec, request.RequestBody)
	if err != nil {
		return Operation{}, fmt.Errorf("purchase specification: %w", err)
	}
	if purchasespec.Hash(request.CanonicalPurchaseSpec) != request.Quote.PurchaseSpecHash {
		return Operation{}, ErrPurchaseSpecBinding
	}
	adaptationDigest, err := validateAdaptation(request, spec, s.clock(), true)
	if err != nil {
		return Operation{}, err
	}
	quoteHash, signer, err := request.Quote.ValidateForIntake(s.clock(), request.DirectoryContract, request.Expected, request.Evidence, request.Signature)
	if err != nil {
		return Operation{}, err
	}
	quoteJSON, err := json.Marshal(request.Quote)
	if err != nil {
		return Operation{}, fmt.Errorf("encode seller quote: %w", err)
	}
	inputHash := canonicalInputHash(request, quoteHash.Hex(), strings.ToLower(signer.Hex()), adaptationDigest)
	operationID, err := s.newID()
	if err != nil {
		return Operation{}, err
	}
	now := s.clock().UTC().Truncate(time.Second)
	operation := Operation{
		OperationID: operationID, OrganizationID: request.OrganizationID, ActorID: request.ActorID,
		QuoteHash: quoteHash.Hex(), PurchaseSpecHash: request.Quote.PurchaseSpecHash, QuoteNonce: request.Quote.QuoteNonce,
		DirectoryVersion: request.Quote.DirectoryVersion, DirectoryContract: request.DirectoryContract,
		SellerSigner: strings.ToLower(signer.Hex()), AdaptationGrantID: adaptationGrantID(request.Adaptation), CreatedAt: now.Unix(),
	}
	stored, replayed, err := s.store.Create(ctx, StoreInput{Operation: operation, IdempotencyKey: request.IdempotencyKey, CanonicalInputHash: inputHash, QuoteJSON: quoteJSON, PurchaseSpecJSON: request.CanonicalPurchaseSpec, RequestBody: request.RequestBody, AdaptationGrantID: operation.AdaptationGrantID, AdaptationDigest: adaptationDigest})
	if err != nil {
		return Operation{}, err
	}
	stored.Replayed = replayed
	return stored, nil
}

// Replay returns a previously committed exact request without reapplying
// mutable expiry, directory-head, or overlay checks. The immutable quote
// signature and PurchaseSpec/body binding are still recomputed, so a changed
// request cannot borrow an old idempotency result.
func (s *Service) Replay(ctx context.Context, request Request) (Operation, bool, error) {
	if err := validateScope(request); err != nil {
		return Operation{}, false, err
	}
	stored, storedHash, found, err := s.store.Lookup(ctx, request.OrganizationID, request.ActorID, request.IdempotencyKey)
	if err != nil || !found {
		return Operation{}, false, err
	}
	spec, err := purchasespec.ValidatePersisted(request.CanonicalPurchaseSpec, request.RequestBody)
	if err != nil {
		return Operation{}, false, fmt.Errorf("purchase specification: %w", err)
	}
	if purchasespec.Hash(request.CanonicalPurchaseSpec) != request.Quote.PurchaseSpecHash {
		return Operation{}, false, ErrPurchaseSpecBinding
	}
	// The directory contract is part of the committed operation, not public
	// request input. Replays remain stable across a later deployment change.
	request.DirectoryContract = stored.DirectoryContract
	quoteHash, err := request.Quote.Digest(stored.DirectoryContract)
	if err != nil {
		return Operation{}, false, err
	}
	signer, err := request.Quote.RecoverSigner(stored.DirectoryContract, request.Signature)
	if err != nil {
		return Operation{}, false, err
	}
	adaptationDigest, err := validateAdaptation(request, spec, time.Time{}, false)
	if err != nil {
		return Operation{}, false, err
	}
	inputHash := canonicalInputHash(request, quoteHash.Hex(), strings.ToLower(signer.Hex()), adaptationDigest)
	if storedHash != inputHash {
		return Operation{}, false, ErrIdempotencyConflict
	}
	stored.Replayed = true
	return stored, true, nil
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

func canonicalInputHash(request Request, quoteHash, signer, adaptationDigest string) string {
	input := struct {
		Version           string `json:"version"`
		OrganizationID    string `json:"organizationId"`
		ActorID           string `json:"actorId"`
		Endpoint          string `json:"endpoint"`
		IdempotencyKey    string `json:"idempotencyKey"`
		DirectoryContract string `json:"directoryContract"`
		QuoteHash         string `json:"quoteHash"`
		SellerSigner      string `json:"sellerSigner"`
		AdaptationDigest  string `json:"adaptationDigest,omitempty"`
	}{"ASCP_INTAKE_V1", request.OrganizationID, request.ActorID, Endpoint, request.IdempotencyKey, request.DirectoryContract, quoteHash, signer, adaptationDigest}
	encoded, _ := json.Marshal(input)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func validateAdaptation(request Request, spec purchasespec.Spec, now time.Time, enforceTime bool) (string, error) {
	if request.Adaptation == nil {
		return "", nil
	}
	use := ascpadaptation.Use{
		OrganizationID: request.OrganizationID, AgentID: request.ActorID, TaskID: spec.TaskID,
		Category: spec.Category, AmountAtomic: request.Quote.AmountBaseUnits, SellerID: request.Quote.SellerID,
	}
	var err error
	if enforceTime {
		err = ascpadaptation.Verify(*request.Adaptation, request.AdaptationSigner, now, use)
	} else {
		err = ascpadaptation.VerifyReplay(*request.Adaptation, request.AdaptationSigner, use)
	}
	if err != nil {
		return "", err
	}
	return ascpadaptation.DigestHex(request.Adaptation.Grant)
}

func adaptationGrantID(grant *ascpadaptation.SignedGrant) string {
	if grant == nil {
		return ""
	}
	return grant.Grant.GrantID
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
	mu               sync.Mutex
	byScope          map[string]memoryRecord
	byNonce          map[string]string
	grants           map[string]ascpadaptation.Record
	byOriginalIntent map[string]string
}

type memoryRecord struct {
	hash      string
	operation Operation
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{byScope: make(map[string]memoryRecord), byNonce: make(map[string]string), grants: make(map[string]ascpadaptation.Record), byOriginalIntent: make(map[string]string)}
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
	if input.AdaptationGrantID != "" {
		grant, exists := s.grants[input.AdaptationGrantID]
		if !exists || grant.Digest != input.AdaptationDigest {
			return Operation{}, false, ascpadaptation.ErrInvalidGrant
		}
		if grant.ConsumedOperationID != "" {
			return Operation{}, false, ascpadaptation.ErrGrantConsumed
		}
		grant.ConsumedOperationID = input.Operation.OperationID
		s.grants[input.AdaptationGrantID] = grant
	}
	stored := input.Operation
	s.byScope[scope] = memoryRecord{hash: input.CanonicalInputHash, operation: stored}
	s.byNonce[input.Operation.QuoteNonce] = input.Operation.OperationID
	return stored, false, nil
}

func (s *MemoryStore) Issue(ctx context.Context, record ascpadaptation.Record) (ascpadaptation.Record, bool, error) {
	if err := ctx.Err(); err != nil {
		return ascpadaptation.Record{}, false, err
	}
	if err := ascpadaptation.ValidateRecord(record); err != nil {
		return ascpadaptation.Record{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	original := record.Artifact.Grant.OriginalIntentID
	if grantID, exists := s.byOriginalIntent[original]; exists {
		existing := s.grants[grantID]
		if existing.CanonicalRequestHash != record.CanonicalRequestHash || existing.ReasonClass != record.ReasonClass {
			return ascpadaptation.Record{}, false, ascpadaptation.ErrIssueConflict
		}
		return existing, true, nil
	}
	s.grants[record.Artifact.Grant.GrantID] = record
	s.byOriginalIntent[original] = record.Artifact.Grant.GrantID
	return record, false, nil
}

func (s *MemoryStore) GetGrant(ctx context.Context, organizationID, agentID, grantID string) (ascpadaptation.Record, error) {
	if err := ctx.Err(); err != nil {
		return ascpadaptation.Record{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	record, exists := s.grants[grantID]
	if !exists || record.Artifact.Grant.OrganizationID != organizationID || record.Artifact.Grant.AgentID != agentID {
		return ascpadaptation.Record{}, ascpadaptation.ErrGrantNotFound
	}
	return record, nil
}

func (s *MemoryStore) GetByOriginalIntent(ctx context.Context, organizationID, agentID, originalIntentID string) (ascpadaptation.Record, error) {
	if err := ctx.Err(); err != nil {
		return ascpadaptation.Record{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	grantID, exists := s.byOriginalIntent[originalIntentID]
	record := s.grants[grantID]
	if !exists || record.Artifact.Grant.OrganizationID != organizationID || record.Artifact.Grant.AgentID != agentID {
		return ascpadaptation.Record{}, ascpadaptation.ErrGrantNotFound
	}
	return record, nil
}

func (s *MemoryStore) Lookup(ctx context.Context, organizationID, actorID, idempotencyKey string) (Operation, string, bool, error) {
	if err := ctx.Err(); err != nil {
		return Operation{}, "", false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	record, found := s.byScope[organizationID+"\x00"+actorID+"\x00"+Endpoint+"\x00"+idempotencyKey]
	if !found {
		return Operation{}, "", false, nil
	}
	return record.operation, record.hash, true, nil
}

func (s *MemoryStore) Get(ctx context.Context, organizationID, actorID, operationID string) (Operation, error) {
	if err := ctx.Err(); err != nil {
		return Operation{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, record := range s.byScope {
		operation := record.operation
		if operation.OperationID == operationID && operation.OrganizationID == organizationID && operation.ActorID == actorID {
			return operation, nil
		}
	}
	return Operation{}, ErrNotFound
}
