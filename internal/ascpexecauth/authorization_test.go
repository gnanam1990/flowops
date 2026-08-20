package ascpexecauth

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func TestFailureInvalidatesAuthorizationWithoutChangingApproval(t *testing.T) {
	failure := errors.New("directory changed")
	s := New(check{err: failure}, reserve{})
	a, err := s.Evaluate(context.Background(), Input{AuthorizationID: testHash(1), ApprovalID: testHash(2), IntentID: testHash(3), ExecutionSnapshotHash: testHash(4), ReservationID: testHash(5)}, true)
	if !errors.Is(err, failure) || a.State != Invalidated || a.ApprovalID != testHash(2) {
		t.Fatalf("%+v %v", a, err)
	}
}
func TestReservationFailureNeverCreatesReadyAuthorization(t *testing.T) {
	s := New(check{}, reserve{err: errors.New("budget exhausted")})
	a, err := s.Evaluate(context.Background(), Input{AuthorizationID: testHash(1), ApprovalID: testHash(2), IntentID: testHash(3), ExecutionSnapshotHash: testHash(4), ReservationID: testHash(5)}, true)
	if err == nil || a.State != Invalidated {
		t.Fatalf("%+v %v", a, err)
	}
}
func TestRejectsMalformedBindingBeforeAnySideEffect(t *testing.T) {
	if _, err := New(check{}, reserve{}).Evaluate(context.Background(), Input{AuthorizationID: "bad"}, true); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("%v", err)
	}
}
func testHash(v uint64) string { return fmt.Sprintf("0x%064x", v) }

type check struct{ err error }

func (c check) Revalidate(context.Context, Input) error { return c.err }

type reserve struct{ err error }

func (r reserve) Reserve(context.Context, Input) error { return r.err }
