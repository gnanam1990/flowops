package sellerquote

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

var (
	ErrNonceConsumed       = errors.New("seller quote nonce is already consumed")
	ErrIdempotencyConflict = errors.New("idempotency key names different intake input")
	ErrOperationClaimed    = errors.New("operation ID already owns a different quote intake")
)

// IntakeRequest is accepted only after a caller has pinned the directory
// contract that supplied Evidence. OperationID must be a non-zero bytes32 hex
// value; IdempotencyKey is deliberately opaque and must be scoped by the API.
type IntakeRequest struct {
	OperationID       string
	IdempotencyKey    string
	VerifyingContract string
	Quote             Quote
	Signature         string
	Expected          ExpectedTerms
	Evidence          DirectoryEvidence
}

type IntakeResult struct {
	OperationID string `json:"operationId"`
	QuoteHash   string `json:"quoteHash"`
	Signer      string `json:"signer"`
	Replayed    bool   `json:"replayed"`
}

// ClaimStore is the transaction boundary for quote nonce consumption. A
// production implementation MUST claim the quote nonce and insert the intent
// in one SQL transaction. MemoryClaimStore is for tests and local simulations.
type ClaimStore interface {
	Claim(operationID, idempotencyKey, quoteNonce, inputHash string, result IntakeResult) (IntakeResult, bool, error)
}

type Intake struct {
	store ClaimStore
	clock func() time.Time
}

func NewIntake(store ClaimStore, clock func() time.Time) (*Intake, error) {
	if store == nil {
		return nil, errors.New("claim store is required")
	}
	if clock == nil {
		clock = time.Now
	}
	return &Intake{store: store, clock: clock}, nil
}

// Accept validates all non-mutating terms before one atomic Claim call.
func (i *Intake) Accept(request IntakeRequest) (IntakeResult, error) {
	if _, err := nonZeroHash(request.OperationID); err != nil {
		return IntakeResult{}, fmt.Errorf("operationId: %w", err)
	}
	if len(request.IdempotencyKey) == 0 || len(request.IdempotencyKey) > 256 {
		return IntakeResult{}, errors.New("idempotency key must contain 1 to 256 bytes")
	}
	digest, signer, err := request.Quote.ValidateForIntake(i.clock(), request.VerifyingContract, request.Expected, request.Evidence, request.Signature)
	if err != nil {
		return IntakeResult{}, err
	}
	canonicalSigner := strings.ToLower(signer.Hex())
	result := IntakeResult{OperationID: request.OperationID, QuoteHash: digest.Hex(), Signer: canonicalSigner}
	inputHash := intakeInputHash(request.OperationID, request.IdempotencyKey, digest.Hex(), canonicalSigner)
	claimed, replayed, err := i.store.Claim(request.OperationID, request.IdempotencyKey, request.Quote.QuoteNonce, inputHash, result)
	if err != nil {
		return IntakeResult{}, err
	}
	claimed.Replayed = replayed
	return claimed, nil
}

func intakeInputHash(operationID, idempotencyKey, quoteHash, signer string) string {
	return ArtifactSHA256([]byte("ASCP_SELLER_QUOTE_INTAKE_V1\x00" + operationID + "\x00" + idempotencyKey + "\x00" + quoteHash + "\x00" + signer))
}

// MemoryClaimStore models a serializable transaction for deterministic tests.
// It is never a substitute for the required durable SQL transaction.
type MemoryClaimStore struct {
	mu            sync.Mutex
	byIdempotency map[string]memoryClaim
	byNonce       map[string]string
	byOperation   map[string]string
}

type memoryClaim struct {
	inputHash string
	result    IntakeResult
}

func NewMemoryClaimStore() *MemoryClaimStore {
	return &MemoryClaimStore{byIdempotency: make(map[string]memoryClaim), byNonce: make(map[string]string), byOperation: make(map[string]string)}
}

func (s *MemoryClaimStore) Claim(operationID, idempotencyKey, quoteNonce, inputHash string, result IntakeResult) (IntakeResult, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if prior, exists := s.byIdempotency[idempotencyKey]; exists {
		if prior.inputHash != inputHash {
			return IntakeResult{}, false, ErrIdempotencyConflict
		}
		return prior.result, true, nil
	}
	if existingOperation, exists := s.byNonce[quoteNonce]; exists {
		if existingOperation != operationID {
			return IntakeResult{}, false, ErrNonceConsumed
		}
		return IntakeResult{}, false, ErrOperationClaimed
	}
	if existingNonce, exists := s.byOperation[operationID]; exists && existingNonce != quoteNonce {
		return IntakeResult{}, false, ErrOperationClaimed
	}
	s.byNonce[quoteNonce] = operationID
	s.byOperation[operationID] = quoteNonce
	s.byIdempotency[idempotencyKey] = memoryClaim{inputHash: inputHash, result: result}
	return result, false, nil
}
