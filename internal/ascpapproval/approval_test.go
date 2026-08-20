package ascpapproval

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestApprovalCASAndSnapshotBinding(t *testing.T) {
	now := time.Unix(1800000000, 0)
	service := newService(t, &now)
	approval, err := service.Request(context.Background(), "org_a", hash(1), review())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.Decide(context.Background(), approval.ApprovalID, hash(99), true, "owner_a"); !errors.Is(err, ErrSnapshotMismatch) {
		t.Fatalf("%v", err)
	}
	approved, err := service.Decide(context.Background(), approval.ApprovalID, approval.ReviewSnapshotHash, true, "owner_a")
	if err != nil || approved.State != Approved {
		t.Fatalf("%+v %v", approved, err)
	}
	later, err := service.Decide(context.Background(), approval.ApprovalID, approval.ReviewSnapshotHash, false, "owner_b")
	if !errors.Is(err, ErrNotRequested) || later.State != Approved {
		t.Fatalf("%+v %v", later, err)
	}
}
func TestApprovalExpiryCancelAndConcurrentWinners(t *testing.T) {
	now := time.Unix(1800000000, 0)
	service := newService(t, &now)
	a, err := service.Request(context.Background(), "org_a", hash(1), review())
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(ApprovalTTL)
	if expired, err := service.Decide(context.Background(), a.ApprovalID, a.ReviewSnapshotHash, true, "owner_a"); !errors.Is(err, ErrNotRequested) || expired.State != Expired {
		t.Fatalf("%+v %v", expired, err)
	}
	now = now.Add(time.Second)
	b, err := service.Request(context.Background(), "org_a", hash(2), review())
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	outcomes := make(chan State, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			v, _ := service.Decide(context.Background(), b.ApprovalID, b.ReviewSnapshotHash, i%2 == 0, "owner_a")
			outcomes <- v.State
		}(i)
	}
	wg.Wait()
	close(outcomes)
	var terminal State
	for v := range outcomes {
		if terminal == "" {
			terminal = v
		}
		if v != terminal {
			t.Fatalf("outcomes disagree %s %s", terminal, v)
		}
	}
	if _, err := service.Cancel(context.Background(), b.ApprovalID, "suspended"); !errors.Is(err, ErrNotRequested) {
		t.Fatalf("%v", err)
	}
}
func TestApprovalReplayDoesNotCreateSecondPendingApproval(t *testing.T) {
	now := time.Unix(1800000000, 0)
	service := newService(t, &now)
	first, err := service.Request(context.Background(), "org_a", hash(1), review())
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Request(context.Background(), "org_a", hash(1), review())
	if err != nil || first.ApprovalID != second.ApprovalID {
		t.Fatalf("%+v %v", second, err)
	}
	changed := review()
	changed.AmountBaseUnits = "43"
	if _, err := service.Request(context.Background(), "org_a", hash(1), changed); !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("%v", err)
	}
}
func newService(t *testing.T, now *time.Time) *Service {
	t.Helper()
	n := uint64(20)
	s, err := New(NewMemoryStore(), func() time.Time { return *now }, reader{next: &n})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

type reader struct{ next *uint64 }

func (r reader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = byte(*r.next)
		*r.next++
	}
	return len(p), nil
}
func review() Review {
	return Review{CommitmentHash: hash(1), PolicyVersion: "p1", PolicyHash: hash(2), DirectoryVersion: 1, PayTo: "0x3333333333333333333333333333333333333333", AckAuthority: "0x4444444444444444444444444444444444444444", AmountBaseUnits: "42", VerificationSpecHash: hash(3), Protection: "ESCROW", ChainID: "84532", Asset: "0x036cbd53842c5426634e7929541ec2318f3dcf7e"}
}
func hash(v uint64) string { return fmt.Sprintf("0x%064x", v) }
