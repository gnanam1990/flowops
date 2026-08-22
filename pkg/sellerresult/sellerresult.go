// Package sellerresult prevents a seller from executing the same paid call
// twice. A fresh claim is persisted as STARTED_UNKNOWN before the side effect;
// after a crash, automatic retries fail closed until an operator reconciles it.
package sellerresult

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sync"
	"time"
)

const Retention = 400 * 24 * time.Hour

var (
	ErrConflict         = errors.New("seller call binding conflicts with durable replay record")
	ErrRecoveryRequired = errors.New("seller call may have started and requires reconciliation")
	ErrResultConflict   = errors.New("seller call already has a different terminal result")
	hashPattern         = regexp.MustCompile(`^0x[0-9a-f]{64}$`)
)

type State string

const (
	StateStartedUnknown State = "STARTED_UNKNOWN"
	StateCompleted      State = "COMPLETED"
)

type Request struct {
	SellerID             string
	CallID               string
	RequestHash          string
	ResourceOperationKey string
	SettleBy             time.Time
}

type Response struct {
	StatusCode       int
	Header           http.Header
	Body             []byte
	ContentDigest    string
	SideEffectStatus string
}

type Record struct {
	Request     Request
	State       State
	Response    Response
	RetainUntil time.Time
	CreatedAt   time.Time
	CompletedAt time.Time
}

type Store interface {
	Begin(context.Context, Request, time.Time) (Record, bool, error)
	Complete(context.Context, Request, Response, time.Time) (Record, error)
}

type Service struct {
	store Store
	clock func() time.Time
}

func New(store Store, clock func() time.Time) (*Service, error) {
	if store == nil {
		return nil, errors.New("seller result store is required")
	}
	if clock == nil {
		clock = time.Now
	}
	return &Service{store: store, clock: clock}, nil
}

// Execute returns the exact stored response on replay. The effect is called
// only after a durable STARTED_UNKNOWN claim exists.
func (s *Service) Execute(ctx context.Context, request Request, effect func(context.Context) (Response, error)) (Response, bool, error) {
	if effect == nil {
		return Response{}, false, errors.New("seller effect is required")
	}
	record, execute, err := s.store.Begin(ctx, request, s.clock().UTC())
	if err != nil {
		return Response{}, false, err
	}
	if !execute {
		return cloneResponse(record.Response), true, nil
	}
	response, err := effect(ctx)
	if err != nil {
		return Response{}, false, err
	}
	response, err = normalizeResponse(response)
	if err != nil {
		return Response{}, false, err
	}
	completed, err := s.store.Complete(ctx, request, response, s.clock().UTC())
	if err != nil {
		return Response{}, false, err
	}
	return cloneResponse(completed.Response), false, nil
}

type MemoryStore struct {
	mu      sync.Mutex
	records map[string]Record
	byKey   map[string]string
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{records: make(map[string]Record), byKey: make(map[string]string)}
}

func NewMemoryStoreFromSnapshot(snapshot []Record) (*MemoryStore, error) {
	store := NewMemoryStore()
	for _, record := range snapshot {
		if err := validateRecord(record); err != nil {
			return nil, err
		}
		key := recordKey(record.Request)
		resourceKey := resourceRecordKey(record.Request)
		if _, exists := store.records[key]; exists || store.byKey[resourceKey] != "" {
			return nil, ErrConflict
		}
		store.records[key] = cloneRecord(record)
		store.byKey[resourceKey] = key
	}
	return store, nil
}

func validateRecord(record Record) error {
	if err := validateRequest(record.Request); err != nil {
		return err
	}
	wantRetention, err := retentionDeadline(record.Request.SettleBy)
	if err != nil || !record.RetainUntil.Equal(wantRetention) || record.CreatedAt.IsZero() {
		return errors.New("seller result snapshot has invalid retention metadata")
	}
	switch record.State {
	case StateStartedUnknown:
		if record.CompletedAt.IsZero() && record.Response.StatusCode == 0 && len(record.Response.Header) == 0 && len(record.Response.Body) == 0 && record.Response.ContentDigest == "" && record.Response.SideEffectStatus == "" {
			return nil
		}
	case StateCompleted:
		normalized, normalizeErr := normalizeResponse(record.Response)
		if normalizeErr == nil && !record.CompletedAt.IsZero() && sameResponse(record.Response, normalized) {
			return nil
		}
	}
	return errors.New("seller result snapshot has invalid state shape")
}

func (s *MemoryStore) Snapshot() []Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]Record, 0, len(s.records))
	for _, record := range s.records {
		result = append(result, cloneRecord(record))
	}
	return result
}

func (s *MemoryStore) Begin(ctx context.Context, request Request, now time.Time) (Record, bool, error) {
	if err := ctx.Err(); err != nil {
		return Record{}, false, err
	}
	if err := validateRequest(request); err != nil {
		return Record{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := recordKey(request)
	if existing, found := s.records[key]; found {
		if !sameRequest(existing.Request, request) {
			return Record{}, false, ErrConflict
		}
		if existing.State != StateCompleted {
			return Record{}, false, ErrRecoveryRequired
		}
		return cloneRecord(existing), false, nil
	}
	if owner := s.byKey[resourceRecordKey(request)]; owner != "" {
		return Record{}, false, ErrConflict
	}
	retainUntil, err := retentionDeadline(request.SettleBy)
	if err != nil {
		return Record{}, false, err
	}
	record := Record{Request: request, State: StateStartedUnknown, RetainUntil: retainUntil, CreatedAt: now.UTC()}
	s.records[key] = record
	s.byKey[resourceRecordKey(request)] = key
	return cloneRecord(record), true, nil
}

func (s *MemoryStore) Complete(ctx context.Context, request Request, response Response, now time.Time) (Record, error) {
	if err := ctx.Err(); err != nil {
		return Record{}, err
	}
	normalized, err := normalizeResponse(response)
	if err != nil {
		return Record{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := recordKey(request)
	record, found := s.records[key]
	if !found || !sameRequest(record.Request, request) {
		return Record{}, ErrConflict
	}
	if record.State == StateCompleted {
		if !sameResponse(record.Response, normalized) {
			return Record{}, ErrResultConflict
		}
		return cloneRecord(record), nil
	}
	record.State = StateCompleted
	record.Response = cloneResponse(normalized)
	record.CompletedAt = now.UTC()
	s.records[key] = record
	return cloneRecord(record), nil
}

func validateRequest(request Request) error {
	if len(request.SellerID) == 0 || len(request.SellerID) > 128 {
		return errors.New("sellerId is invalid")
	}
	if !hashPattern.MatchString(request.CallID) || !hashPattern.MatchString(request.RequestHash) {
		return errors.New("callId and requestHash must be canonical hashes")
	}
	if len(request.ResourceOperationKey) == 0 || len(request.ResourceOperationKey) > 128 {
		return errors.New("resource operation key is required")
	}
	if request.SettleBy.IsZero() {
		return errors.New("settleBy is required")
	}
	_, err := retentionDeadline(request.SettleBy)
	return err
}

func retentionDeadline(settleBy time.Time) (time.Time, error) {
	deadline := settleBy.UTC().Add(Retention)
	if !deadline.After(settleBy.UTC()) {
		return time.Time{}, errors.New("seller result retention deadline overflow")
	}
	return deadline, nil
}

func normalizeResponse(response Response) (Response, error) {
	if response.StatusCode < 100 || response.StatusCode > 599 {
		return Response{}, errors.New("response status is invalid")
	}
	if len(response.Body) > 16<<20 {
		return Response{}, errors.New("response body exceeds 16 MiB")
	}
	encodedHeaders, err := json.Marshal(response.Header)
	if err != nil {
		return Response{}, fmt.Errorf("encode response headers: %w", err)
	}
	if len(encodedHeaders) > 64<<10 {
		return Response{}, errors.New("response headers exceed 64 KiB")
	}
	sum := sha256.Sum256(response.Body)
	digest := "0x" + hex.EncodeToString(sum[:])
	if response.ContentDigest != "" && response.ContentDigest != digest {
		return Response{}, errors.New("content digest does not bind response body")
	}
	response.ContentDigest = digest
	response.SideEffectStatus = "COMPLETED"
	response.Header = response.Header.Clone()
	response.Body = append([]byte(nil), response.Body...)
	return response, nil
}

func sameRequest(left, right Request) bool {
	return left.SellerID == right.SellerID && left.CallID == right.CallID && left.RequestHash == right.RequestHash &&
		left.ResourceOperationKey == right.ResourceOperationKey && left.SettleBy.Equal(right.SettleBy)
}

func sameResponse(left, right Response) bool {
	leftJSON, _ := json.Marshal(left.Header)
	rightJSON, _ := json.Marshal(right.Header)
	return left.StatusCode == right.StatusCode && string(leftJSON) == string(rightJSON) &&
		string(left.Body) == string(right.Body) && left.ContentDigest == right.ContentDigest && left.SideEffectStatus == right.SideEffectStatus
}

func recordKey(request Request) string { return request.SellerID + "\x00" + request.CallID }
func resourceRecordKey(request Request) string {
	return request.SellerID + "\x00" + request.ResourceOperationKey
}

func cloneResponse(response Response) Response {
	response.Header = response.Header.Clone()
	response.Body = append([]byte(nil), response.Body...)
	return response
}

func cloneRecord(record Record) Record {
	record.Response = cloneResponse(record.Response)
	return record
}

func (r Record) String() string {
	return fmt.Sprintf("seller result %s/%s (%s)", r.Request.SellerID, r.Request.CallID, r.State)
}
