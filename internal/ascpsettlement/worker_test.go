package ascpsettlement

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gnanam1990/flowops/internal/reconciliation"
)

type workerStoreFixture struct {
	pending, finalized          []Attempt
	applied, reorged, confirmed int
}

func (s *workerStoreFixture) Pending(context.Context, int) ([]Attempt, error) { return s.pending, nil }
func (s *workerStoreFixture) FinalizedUnchecked(context.Context, int) ([]Attempt, error) {
	return s.finalized, nil
}
func (s *workerStoreFixture) Expected(_ context.Context, operationID string, action reconciliation.ASCPReceiptAction) (reconciliation.ASCPExpectedReceipt, error) {
	return reconciliation.ASCPExpectedReceipt{OperationID: operationID, Action: action}, nil
}
func (s *workerStoreFixture) Apply(context.Context, Result) (Operation, error) {
	s.applied++
	return Operation{}, nil
}
func (s *workerStoreFixture) ApplyReorg(context.Context, ReorgResult) (Operation, error) {
	s.reorged++
	return Operation{}, nil
}
func (s *workerStoreFixture) ConfirmCanonical(context.Context, ReorgResult) error {
	s.confirmed++
	return nil
}

type workerReaderFixture struct{ readError error }

func (r workerReaderFixture) Read(context.Context, reconciliation.ASCPExpectedReceipt) (Result, error) {
	return Result{}, r.readError
}
func (r workerReaderFixture) CheckCanonical(_ context.Context, _ string, action reconciliation.ASCPReceiptAction, _ string, _ uint64, _ string) (ReorgResult, error) {
	return ReorgResult{Reorged: action == reconciliation.ASCPReceiptRefund}, nil
}

func TestWorkerReconcilesPendingAndRoutesCanonicalOrReorgedFinality(t *testing.T) {
	t.Parallel()
	store := &workerStoreFixture{
		pending: []Attempt{{AttemptInput: AttemptInput{OperationID: settlementHash(1), Action: reconciliation.ASCPReceiptLock}}},
		finalized: []Attempt{
			{AttemptInput: AttemptInput{OperationID: settlementHash(1), Action: reconciliation.ASCPReceiptLock, TransactionHash: settlementHash(2)}, BlockNumber: 10, BlockHash: settlementHash(10)},
			{AttemptInput: AttemptInput{OperationID: settlementHash(3), Action: reconciliation.ASCPReceiptRefund, TransactionHash: settlementHash(4)}, BlockNumber: 11, BlockHash: settlementHash(11)},
		},
	}
	worker, err := NewWorker(store, workerReaderFixture{}, WorkerConfig{Interval: time.Second, QueryTimeout: time.Millisecond, BatchSize: 10})
	if err != nil {
		t.Fatal(err)
	}
	cycle, err := worker.RunOnce(context.Background())
	if err != nil || cycle.Pending != 1 || cycle.Applied != 1 || cycle.FinalityChecks != 2 ||
		cycle.CanonicalConfirmed != 1 || cycle.ReorgsRecovered != 1 || store.applied != 1 || store.confirmed != 1 || store.reorged != 1 {
		t.Fatalf("RunOnce() cycle=%+v store=%+v err=%v", cycle, store, err)
	}
}

func TestWorkerDefersUnavailableEvidenceWithoutMutatingState(t *testing.T) {
	t.Parallel()
	store := &workerStoreFixture{pending: []Attempt{{AttemptInput: AttemptInput{OperationID: settlementHash(1), Action: reconciliation.ASCPReceiptLock}}}}
	worker, err := NewWorker(store, workerReaderFixture{readError: ErrQuorumUnavailable}, WorkerConfig{Interval: time.Second, QueryTimeout: time.Millisecond, BatchSize: 10})
	if err != nil {
		t.Fatal(err)
	}
	cycle, err := worker.RunOnce(context.Background())
	if err != nil || cycle.Deferred != 1 || store.applied != 0 {
		t.Fatalf("RunOnce() cycle=%+v applied=%d err=%v", cycle, store.applied, err)
	}

	worker, _ = NewWorker(store, workerReaderFixture{readError: errors.New("database corrupt")}, WorkerConfig{Interval: time.Second, QueryTimeout: time.Millisecond, BatchSize: 10})
	if _, err := worker.RunOnce(context.Background()); err == nil {
		t.Fatal("non-evidence failure was deferred")
	}
}
