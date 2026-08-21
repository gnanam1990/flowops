package ascpexecauth

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/gnanam1990/flowops/internal/ascpapproval"
	"github.com/gnanam1990/flowops/internal/ascpreservation"
)

func TestFailureInvalidatesAuthorizationWithoutChangingApproval(t *testing.T) {
	failure := errors.New("directory changed")
	s := New(check{err: failure}, reserve{})
	a, err := s.Evaluate(context.Background(), testInput(), true)
	if !errors.Is(err, failure) || a.State != Invalidated || a.ApprovalID != testHash(2) {
		t.Fatalf("%+v %v", a, err)
	}
}
func TestReservationFailureNeverCreatesReadyAuthorization(t *testing.T) {
	s := New(check{}, reserve{err: errors.New("budget exhausted")})
	a, err := s.Evaluate(context.Background(), testInput(), true)
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

func testInput() Input {
	now := time.Unix(1800000000, 0)
	review := ascpapproval.Review{
		CommitmentHash: testHash(7), PolicyVersion: "policy_1", PolicyHash: testHash(8), DirectoryVersion: 9,
		PayTo: "0x3333333333333333333333333333333333333333", AckAuthority: "0x4444444444444444444444444444444444444444",
		AmountBaseUnits: "10", VerificationSpecHash: testHash(9), Protection: "ESCROW", ChainID: "84532",
		Asset: "0x036cbd53842c5426634e7929541ec2318f3dcf7e",
	}
	snapshot, err := ascpapproval.ReviewHash(review)
	if err != nil {
		panic(err)
	}
	return Input{
		AuthorizationID: testHash(1), ApprovalID: testHash(2), ApprovalSnapshotHash: snapshot,
		IntentID: testHash(3), ExecutionSnapshotHash: testHash(4), Review: review,
		Reservation: ascpreservation.Request{
			ReservationID: testHash(5), OperationID: testHash(3), Amount: "10",
			Dimensions: []ascpreservation.Dimension{{ID: "org:day:2030-01-15", Limit: "100", Refundable: true}},
			ExpiresAt:  now.Add(15 * time.Minute),
		},
	}
}

type check struct{ err error }

func (c check) Revalidate(context.Context, Input) error { return c.err }

type reserve struct{ err error }

func (r reserve) Reserve(context.Context, Input) error { return r.err }
