package ascpreservation

import (
	"errors"
	"testing"
	"time"
)

func TestAtomicDimensionsAndLiveCannotTTLRelease(t *testing.T) {
	now := time.Unix(1800000000, 0)
	s := NewStore()
	request := Request{ReservationID: "r1", OperationID: "o1", Amount: "10", Dimensions: []Dimension{{ID: "task", Limit: "10", Refundable: true}, {ID: "lifetime", Limit: "10"}}, ExpiresAt: now.Add(15 * time.Minute)}
	r, err := s.Reserve(request, now)
	if err != nil || r.State != Reserved {
		t.Fatal(r, err)
	}
	copy := request
	copy.ReservationID = "r2"
	copy.OperationID = "o2"
	if _, err := s.Reserve(copy, now); !errors.Is(err, ErrBudgetExceeded) {
		t.Fatal(err)
	}
	if _, err := s.Transition("o1", AuthorizationLive, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Transition("o1", Released, now.Add(time.Hour)); !errors.Is(err, ErrTransition) {
		t.Fatal(err)
	}
	if r, err := s.Transition("o1", ReleasedAfterExpiry, now.Add(time.Hour)); err != nil || r.State != ReleasedAfterExpiry {
		t.Fatal(r, err)
	}
}
func TestFinalityTransitions(t *testing.T) {
	now := time.Now()
	s := NewStore()
	_, _ = s.Reserve(Request{ReservationID: "r", OperationID: "o", Amount: "1", Dimensions: []Dimension{{ID: "d", Limit: "2"}}, ExpiresAt: now.Add(time.Hour)}, now)
	for _, to := range []State{AuthorizationLive, CommittedSafe, CommittedFinalized, Restored} {
		if _, err := s.Transition("o", to, now); err != nil {
			t.Fatalf("%s %v", to, err)
		}
	}
}

func TestReservedMayReleaseOnlyAfterItsExpiry(t *testing.T) {
	now := time.Unix(1800000000, 0)
	s := NewStore()
	_, _ = s.Reserve(Request{ReservationID: "r", OperationID: "o", Amount: "1", Dimensions: []Dimension{{ID: "d", Limit: "1"}}, ExpiresAt: now.Add(time.Minute)}, now)
	if _, err := s.Transition("o", Released, now); !errors.Is(err, ErrTransition) {
		t.Fatalf("early release error=%v", err)
	}
	if output, err := s.Transition("o", Released, now.Add(time.Minute)); err != nil || output.State != Released {
		t.Fatalf("output=%+v err=%v", output, err)
	}
}
