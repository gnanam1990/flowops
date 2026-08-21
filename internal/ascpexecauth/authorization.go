// Package ascpexecauth turns an approved ASCP intent into an execution
// authorization only after current-state revalidation and budget reservation.
package ascpexecauth

import (
	"context"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/gnanam1990/flowops/internal/ascpapproval"
	"github.com/gnanam1990/flowops/internal/ascpreservation"
)

type State string

const (
	NotEvaluated         State = "NOT_EVALUATED"
	ValidatedAndReserved State = "VALIDATED_AND_RESERVED"
	Invalidated          State = "INVALIDATED"
)

var (
	ErrApprovalNotApproved = errors.New("approval is not approved")
	ErrApprovalExpired     = errors.New("approval is expired")
	ErrApprovalSnapshot    = errors.New("approval snapshot does not match")
	ErrAlreadyEvaluated    = errors.New("execution authorization is already evaluated")
	ErrRevalidationFailed  = errors.New("execution authorization revalidation failed")
	ErrInvalidInput        = errors.New("execution authorization input is invalid")
)

type Input struct {
	AuthorizationID       string
	ApprovalID            string
	ApprovalSnapshotHash  string
	IntentID              string
	ExecutionSnapshotHash string
	Review                ascpapproval.Review
	Reservation           ascpreservation.Request
}
type Authorization struct {
	Input
	State              State
	InvalidationReason string
}
type Revalidator interface {
	Revalidate(context.Context, Input) error
}
type Reserver interface {
	Reserve(context.Context, Input) error
}

// Service serializes an authorization's decision. Production adapters must
// implement the same logic with revalidation, reservation, and state update in
// one SQL transaction; this model makes the no-rewrite invariant executable.
type Service struct {
	mu          sync.Mutex
	records     map[string]Authorization
	revalidator Revalidator
	reserver    Reserver
}

func New(revalidator Revalidator, reserver Reserver) *Service {
	return &Service{records: map[string]Authorization{}, revalidator: revalidator, reserver: reserver}
}
func (s *Service) Evaluate(ctx context.Context, input Input, approved bool) (Authorization, error) {
	if !validInput(input) || s.revalidator == nil || s.reserver == nil {
		return Authorization{}, ErrInvalidInput
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if current, ok := s.records[input.AuthorizationID]; ok {
		return current, ErrAlreadyEvaluated
	}
	a := Authorization{Input: input, State: NotEvaluated}
	if !approved {
		a.State = Invalidated
		a.InvalidationReason = ErrApprovalNotApproved.Error()
		s.records[input.AuthorizationID] = a
		return a, ErrApprovalNotApproved
	}
	if err := s.revalidator.Revalidate(ctx, input); err != nil {
		a.State = Invalidated
		a.InvalidationReason = err.Error()
		s.records[input.AuthorizationID] = a
		return a, err
	}
	if err := s.reserver.Reserve(ctx, input); err != nil {
		a.State = Invalidated
		a.InvalidationReason = err.Error()
		s.records[input.AuthorizationID] = a
		return a, err
	}
	a.State = ValidatedAndReserved
	s.records[input.AuthorizationID] = a
	return a, nil
}

func validInput(input Input) bool {
	reviewHash, reviewErr := ascpapproval.ReviewHash(input.Review)
	return hash(input.AuthorizationID) && hash(input.IntentID) && hash(input.ExecutionSnapshotHash) &&
		input.Reservation.OperationID == input.IntentID && hash(input.Reservation.ReservationID) &&
		!input.Reservation.ExpiresAt.IsZero() &&
		((input.ApprovalID != "" && hash(input.ApprovalID) && hash(input.ApprovalSnapshotHash) &&
			reviewErr == nil && reviewHash == input.ApprovalSnapshotHash) ||
			(input.ApprovalID == "" && input.ApprovalSnapshotHash == ""))
}

func reservationID(input Input) string { return input.Reservation.ReservationID }

func reservationExpiresAfter(input Input, now time.Time) bool {
	return input.Reservation.ExpiresAt.After(now)
}

func hash(value string) bool {
	if len(value) != 66 || !strings.HasPrefix(value, "0x") || strings.ToLower(value) != value || value == "0x"+strings.Repeat("0", 64) {
		return false
	}
	_, err := hex.DecodeString(value[2:])
	return err == nil
}
