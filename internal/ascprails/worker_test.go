package ascprails

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestWorkerFinalizesBeforeBoundedDispatch(t *testing.T) {
	service := &sequenceWorkerService{
		finalized:  []workerResult{{job: Job{State: StateCaptured}}, {err: ErrNoWork}},
		dispatched: []workerResult{{job: Job{State: StateResponseStored}}, {job: Job{State: StateRetryWait}}, {err: ErrNoWork}},
	}
	worker, err := NewWorker(service, WorkerConfig{Interval: 2 * time.Second, CycleTimeout: time.Second, BatchSize: 3})
	if err != nil {
		t.Fatal(err)
	}
	cycle, err := worker.RunOnce(t.Context())
	if err != nil || cycle.Finalized != 1 || cycle.Dispatched != 2 || cycle.Captured != 1 || cycle.ResponseStored != 1 || cycle.RetryWait != 1 {
		t.Fatalf("cycle=%+v err=%v", cycle, err)
	}
	if len(service.order) < 2 || service.order[0] != "finalize" || service.order[1] != "finalize" {
		t.Fatalf("worker did not drain finalization first: %v", service.order)
	}
}

func TestWorkerStopsAtBatchBoundary(t *testing.T) {
	service := &sequenceWorkerService{
		finalized:  []workerResult{{err: ErrNoWork}},
		dispatched: []workerResult{{job: Job{State: StateMissing}}, {job: Job{State: StateMissing}}},
	}
	worker, _ := NewWorker(service, WorkerConfig{Interval: 2 * time.Second, CycleTimeout: time.Second, BatchSize: 1})
	cycle, err := worker.RunOnce(t.Context())
	if err != nil || cycle.Dispatched != 1 || cycle.Missing != 1 || service.dispatchCalls != 1 {
		t.Fatalf("cycle=%+v calls=%d err=%v", cycle, service.dispatchCalls, err)
	}
}

func TestWorkerReturnsOperationalFailureWithoutStartingDispatch(t *testing.T) {
	sentinel := errors.New("chain clock unavailable")
	service := &sequenceWorkerService{finalized: []workerResult{{err: sentinel}}}
	worker, _ := NewWorker(service, WorkerConfig{Interval: 2 * time.Second, CycleTimeout: time.Second, BatchSize: 2})
	if _, err := worker.RunOnce(t.Context()); !errors.Is(err, sentinel) || service.dispatchCalls != 0 {
		t.Fatalf("dispatchCalls=%d err=%v", service.dispatchCalls, err)
	}
}

func TestNewWorkerRejectsUnboundedConfiguration(t *testing.T) {
	service := &sequenceWorkerService{}
	for _, config := range []WorkerConfig{
		{},
		{Interval: time.Second, CycleTimeout: time.Second, BatchSize: 1},
		{Interval: 2 * time.Second, CycleTimeout: time.Second, BatchSize: 101},
	} {
		if _, err := NewWorker(service, config); !errors.Is(err, ErrInvalidConfig) {
			t.Fatalf("config=%+v err=%v", config, err)
		}
	}
}

func TestNewWorkerRejectsTypedNilService(t *testing.T) {
	var service *sequenceWorkerService
	if _, err := NewWorker(service, WorkerConfig{Interval: 2 * time.Second, CycleTimeout: time.Second, BatchSize: 1}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("typed nil service error=%v", err)
	}
}

type workerResult struct {
	job Job
	err error
}

type sequenceWorkerService struct {
	finalized     []workerResult
	dispatched    []workerResult
	finalizeCalls int
	dispatchCalls int
	order         []string
}

func (s *sequenceWorkerService) FinalizeOne(context.Context) (Job, error) {
	s.order = append(s.order, "finalize")
	index := s.finalizeCalls
	s.finalizeCalls++
	if index >= len(s.finalized) {
		return Job{}, ErrNoWork
	}
	return s.finalized[index].job, s.finalized[index].err
}

func (s *sequenceWorkerService) DispatchOne(context.Context) (Job, error) {
	s.order = append(s.order, "dispatch")
	index := s.dispatchCalls
	s.dispatchCalls++
	if index >= len(s.dispatched) {
		return Job{}, ErrNoWork
	}
	return s.dispatched[index].job, s.dispatched[index].err
}
