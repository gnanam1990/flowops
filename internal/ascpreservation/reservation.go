// Package ascpreservation conservatively accounts for each ASCP operation
// from pre-signature reservation through terminal chain truth.
package ascpreservation

import (
	"errors"
	"math/big"
	"sync"
	"time"
)

type State string

const (
	Reserved            State = "RESERVED"
	AuthorizationLive   State = "AUTHORIZATION_LIVE"
	CommittedSafe       State = "COMMITTED_SAFE"
	CommittedFinalized  State = "COMMITTED_FINALIZED"
	Consumed            State = "CONSUMED_ON_RELEASE"
	Restored            State = "RESTORED_ON_REFUND"
	Released            State = "RELEASED"
	ReleasedAfterExpiry State = "RELEASED_AFTER_EXPIRY_PROOF"
	ReorgedBack         State = "REORGED_BACK"
)

var (
	ErrBudgetExceeded = errors.New("budget reservation exceeds a dimension limit")
	ErrTransition     = errors.New("invalid reservation state transition")
	ErrDuplicate      = errors.New("reservation already exists for operation")
)

type Dimension struct {
	ID         string
	Limit      string
	Refundable bool
}
type Request struct {
	ReservationID, OperationID, Amount string
	Dimensions                         []Dimension
	ExpiresAt                          time.Time
}
type Reservation struct {
	Request
	State     State
	CreatedAt time.Time
}
type Store struct {
	mu          sync.Mutex
	byOperation map[string]Reservation
}

func NewStore() *Store { return &Store{byOperation: map[string]Reservation{}} }
func (s *Store) Reserve(request Request, now time.Time) (Reservation, error) {
	amount, ok := number(request.Amount)
	if !ok || request.OperationID == "" || request.ReservationID == "" || len(request.Dimensions) == 0 || !request.ExpiresAt.After(now) {
		return Reservation{}, ErrTransition
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if current, exists := s.byOperation[request.OperationID]; exists {
		return current, ErrDuplicate
	}
	for _, d := range request.Dimensions {
		limit, ok := number(d.Limit)
		if !ok || d.ID == "" || amount.Cmp(limit) > 0 {
			return Reservation{}, ErrBudgetExceeded
		}
		for _, current := range s.byOperation {
			if current.State == Reserved || current.State == AuthorizationLive || current.State == CommittedSafe || current.State == CommittedFinalized || current.State == Consumed {
				if includes(current.Dimensions, d.ID) {
					used, _ := number(current.Amount)
					limit.Sub(limit, used)
				}
			}
		}
		if limit.Cmp(amount) < 0 {
			return Reservation{}, ErrBudgetExceeded
		}
	}
	r := Reservation{Request: request, State: Reserved, CreatedAt: now.UTC()}
	s.byOperation[request.OperationID] = r
	return r, nil
}
func (s *Store) Transition(operation string, target State, now time.Time) (Reservation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.byOperation[operation]
	if !ok {
		return Reservation{}, ErrTransition
	}
	if !allowed(r.State, target, now, r.ExpiresAt) {
		return r, ErrTransition
	}
	r.State = target
	s.byOperation[operation] = r
	return r, nil
}
func includes(ds []Dimension, id string) bool {
	for _, d := range ds {
		if d.ID == id {
			return true
		}
	}
	return false
}
func allowed(from, to State, now, expiry time.Time) bool {
	switch from {
	case Reserved:
		return to == AuthorizationLive || to == Released && !now.Before(expiry)
	case AuthorizationLive:
		return to == CommittedSafe || to == ReleasedAfterExpiry
	case CommittedSafe:
		return to == CommittedFinalized || to == ReorgedBack
	case CommittedFinalized:
		return to == Consumed || to == Restored || to == ReorgedBack
	case ReorgedBack:
		return to == AuthorizationLive || to == Reserved
	}
	return false
}
func number(v string) (*big.Int, bool) {
	if v == "" || v[0] == '0' {
		return nil, false
	}
	n, ok := new(big.Int).SetString(v, 10)
	return n, ok && n.Sign() > 0 && n.BitLen() <= 256
}
