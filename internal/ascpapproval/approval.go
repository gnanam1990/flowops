// Package ascpapproval implements the durable decision state machine for an
// escalated ASCP intent. It intentionally does not reserve budget.
package ascpapproval

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
)

const ApprovalTTL = 24 * time.Hour

type State string

const (
	Requested State = "REQUESTED"
	Approved  State = "APPROVED"
	Rejected  State = "REJECTED"
	Expired   State = "EXPIRED"
	Cancelled State = "CANCELLED"
)

var (
	ErrSnapshotMismatch = errors.New("approval review snapshot mismatch")
	ErrNotRequested     = errors.New("approval is no longer requested")
	ErrAlreadyExists    = errors.New("approval already exists for intent")
	ErrNotFound         = errors.New("approval not found")
)

// Review describes every mutable/economic field shown to an approver. All
// strings use their already-canonical wire representation.
type Review struct {
	CommitmentHash       string `json:"commitmentHash"`
	PolicyVersion        string `json:"policyVersion"`
	PolicyHash           string `json:"policyHash"`
	DirectoryVersion     uint64 `json:"directoryVersion"`
	PayTo                string `json:"payTo"`
	AckAuthority         string `json:"ackAuthority"`
	AmountBaseUnits      string `json:"amountBaseUnits"`
	VerificationSpecHash string `json:"verificationSpecHash"`
	Protection           string `json:"protection"`
	ChainID              string `json:"chainId"`
	Asset                string `json:"asset"`
}

type Approval struct {
	ApprovalID         string `json:"approvalId"`
	OrganizationID     string `json:"organizationId"`
	IntentID           string `json:"intentId"`
	State              State  `json:"state"`
	ReviewSnapshotHash string `json:"reviewSnapshotHash"`
	RequestedAt        int64  `json:"requestedAt"`
	ExpiresAt          int64  `json:"expiresAt"`
	DecidedAt          int64  `json:"decidedAt,omitempty"`
	DecidedBy          string `json:"decidedBy,omitempty"`
	CancelReason       string `json:"cancelReason,omitempty"`
}

type Store interface {
	Create(ctx context.Context, input Approval) (Approval, bool, error)
	Decide(ctx context.Context, approvalID, snapshot string, target State, actor string, now time.Time) (Approval, error)
	Cancel(ctx context.Context, approvalID, reason string, now time.Time) (Approval, error)
}

type Service struct {
	store Store
	clock func() time.Time
	newID func() (string, error)
}

func New(store Store, clock func() time.Time, random io.Reader) (*Service, error) {
	if store == nil {
		return nil, errors.New("durable approval store is required")
	}
	if clock == nil {
		clock = time.Now
	}
	if random == nil {
		random = rand.Reader
	}
	return &Service{store: store, clock: clock, newID: idSource(random)}, nil
}

func (s *Service) Request(ctx context.Context, organizationID, intentID string, review Review) (Approval, error) {
	if !envelope.ValidIdentifier(organizationID) || !validHash(intentID) {
		return Approval{}, errors.New("approval scope is invalid")
	}
	snapshot, err := reviewHash(review)
	if err != nil {
		return Approval{}, err
	}
	id, err := s.newID()
	if err != nil {
		return Approval{}, err
	}
	now := s.clock().UTC().Truncate(time.Second)
	approval, replay, err := s.store.Create(ctx, Approval{ApprovalID: id, OrganizationID: organizationID, IntentID: intentID, State: Requested, ReviewSnapshotHash: snapshot, RequestedAt: now.Unix(), ExpiresAt: now.Add(ApprovalTTL).Unix()})
	if err != nil {
		return Approval{}, err
	}
	if replay && approval.ReviewSnapshotHash != snapshot {
		return Approval{}, ErrAlreadyExists
	}
	return approval, nil
}

func (s *Service) Decide(ctx context.Context, approvalID, snapshot string, approved bool, actor string) (Approval, error) {
	if !validHash(approvalID) || !validHash(snapshot) || !envelope.ValidIdentifier(actor) {
		return Approval{}, errors.New("approval decision is invalid")
	}
	target := Rejected
	if approved {
		target = Approved
	}
	return s.store.Decide(ctx, approvalID, snapshot, target, actor, s.clock().UTC().Truncate(time.Second))
}

// Cancel only applies while an approval is pending (for intent withdrawal or
// a current agent/seller suspension). It never rewrites an approval outcome.
func (s *Service) Cancel(ctx context.Context, approvalID, reason string) (Approval, error) {
	if !validHash(approvalID) || strings.TrimSpace(reason) == "" || len(reason) > 256 {
		return Approval{}, errors.New("approval cancellation is invalid")
	}
	return s.store.Cancel(ctx, approvalID, reason, s.clock().UTC().Truncate(time.Second))
}

func reviewHash(review Review) (string, error) {
	if !validHash(review.CommitmentHash) || !envelope.ValidIdentifier(review.PolicyVersion) || !validHash(review.PolicyHash) || review.DirectoryVersion == 0 || !address(review.PayTo) || !address(review.AckAuthority) || !positiveDecimal(review.AmountBaseUnits) || !validHash(review.VerificationSpecHash) || strings.TrimSpace(review.Protection) == "" || len(review.Protection) > 64 || !positiveDecimal(review.ChainID) || !address(review.Asset) {
		return "", errors.New("approval review is invalid")
	}
	encoded, err := json.Marshal(review)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(append([]byte("ASCP_APPROVAL_REVIEW_V1\n"), encoded...))
	return "0x" + hex.EncodeToString(sum[:]), nil
}

func idSource(random io.Reader) func() (string, error) {
	return func() (string, error) {
		b := make([]byte, 32)
		if _, err := io.ReadFull(random, b); err != nil {
			return "", err
		}
		if allZero(b) {
			return "", errors.New("generated zero approval ID")
		}
		return "0x" + hex.EncodeToString(b), nil
	}
}
func allZero(b []byte) bool {
	for _, v := range b {
		if v != 0 {
			return false
		}
	}
	return true
}
func validHash(value string) bool {
	if len(value) != 66 || !strings.HasPrefix(value, "0x") || strings.ToLower(value) != value || value == "0x"+strings.Repeat("0", 64) {
		return false
	}
	_, err := hex.DecodeString(value[2:])
	return err == nil
}
func address(value string) bool {
	if len(value) != 42 || !strings.HasPrefix(value, "0x") || strings.ToLower(value) != value || value == "0x"+strings.Repeat("0", 40) {
		return false
	}
	_, err := hex.DecodeString(value[2:])
	return err == nil
}
func positiveDecimal(value string) bool {
	if value == "" || value[0] == '0' {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// MemoryStore is only a concurrency model for tests; production must provide
// a transactional durable Store implementation.
type MemoryStore struct {
	mu       sync.Mutex
	byID     map[string]Approval
	byIntent map[string]string
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{byID: map[string]Approval{}, byIntent: map[string]string{}}
}
func (s *MemoryStore) Create(ctx context.Context, input Approval) (Approval, bool, error) {
	if err := ctx.Err(); err != nil {
		return Approval{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if id, ok := s.byIntent[input.OrganizationID+"\x00"+input.IntentID]; ok {
		return s.byID[id], true, nil
	}
	s.byID[input.ApprovalID] = input
	s.byIntent[input.OrganizationID+"\x00"+input.IntentID] = input.ApprovalID
	return input, false, nil
}
func (s *MemoryStore) Decide(ctx context.Context, id, snapshot string, target State, actor string, now time.Time) (Approval, error) {
	if err := ctx.Err(); err != nil {
		return Approval{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.byID[id]
	if !ok {
		return Approval{}, ErrNotFound
	}
	if a.ReviewSnapshotHash != snapshot {
		return Approval{}, ErrSnapshotMismatch
	}
	if a.State != Requested {
		return a, ErrNotRequested
	}
	if !now.Before(time.Unix(a.ExpiresAt, 0)) {
		a.State = Expired
		s.byID[id] = a
		return a, ErrNotRequested
	}
	a.State = target
	a.DecidedAt = now.Unix()
	a.DecidedBy = actor
	s.byID[id] = a
	return a, nil
}
func (s *MemoryStore) Cancel(ctx context.Context, id, reason string, now time.Time) (Approval, error) {
	if err := ctx.Err(); err != nil {
		return Approval{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.byID[id]
	if !ok {
		return Approval{}, ErrNotFound
	}
	if a.State != Requested {
		return a, ErrNotRequested
	}
	if !now.Before(time.Unix(a.ExpiresAt, 0)) {
		a.State = Expired
		s.byID[id] = a
		return a, ErrNotRequested
	}
	a.State = Cancelled
	a.DecidedAt = now.Unix()
	a.CancelReason = reason
	s.byID[id] = a
	return a, nil
}

func (a Approval) String() string { return fmt.Sprintf("%s:%s", a.ApprovalID, a.State) }
