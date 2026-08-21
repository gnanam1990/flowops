package ascpkeeper

import (
	"context"
	"errors"
	"testing"
	"time"
)

type runtimeFixture struct {
	relay      []Job
	observed   []Job
	relayErr   error
	observeErr error
}

type containedRuntimeFixture struct{ calls int }

func (f *containedRuntimeFixture) ObserveOnce(context.Context) (Job, error) { return Job{}, ErrNoWork }
func (f *containedRuntimeFixture) RunOnce(context.Context) (Job, error) {
	f.calls++
	if f.calls == 1 {
		return Job{JobID: testHash(210), State: StateAmbiguous}, ErrBroadcastAmbiguous
	}
	if f.calls == 2 {
		return Job{JobID: testHash(211), State: StateDeadLetter}, ErrFeeBumpsExhausted
	}
	return Job{}, ErrNoWork
}

func (f *runtimeFixture) RunOnce(context.Context) (Job, error) {
	if len(f.relay) == 0 {
		if f.relayErr != nil {
			return Job{}, f.relayErr
		}
		return Job{}, ErrNoWork
	}
	job := f.relay[0]
	f.relay = f.relay[1:]
	return job, nil
}

func (f *runtimeFixture) ObserveOnce(context.Context) (Job, error) {
	if len(f.observed) == 0 {
		if f.observeErr != nil {
			return Job{}, f.observeErr
		}
		return Job{}, ErrNoWork
	}
	job := f.observed[0]
	f.observed = f.observed[1:]
	return job, nil
}

type scannerFixture struct {
	created int
	err     error
	limits  []int
}

func (f *scannerFixture) Scan(_ context.Context, limit int) (int, error) {
	f.limits = append(f.limits, limit)
	return f.created, f.err
}

func TestWorkerRunsExpiryObservationBeforeRelayAndCountsStates(t *testing.T) {
	service := &runtimeFixture{
		observed: []Job{{State: StateConfirmed}, {State: StateFinalized}},
		relay:    []Job{{State: StateSubmitted}, {State: StateAmbiguous}, {State: StateDeadLetter}},
	}
	scanner := &scannerFixture{created: 2}
	worker, err := NewWorker(service, scanner, WorkerConfig{Interval: 2 * time.Second, CycleTimeout: time.Second, BatchSize: 10, ExpiryLimit: 50})
	if err != nil {
		t.Fatal(err)
	}
	cycle, err := worker.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cycle.ExpiryEnqueued != 2 || cycle.Observed != 2 || cycle.Relayed != 3 || cycle.Confirmed != 1 || cycle.Finalized != 1 || cycle.Submitted != 1 || cycle.Ambiguous != 1 || cycle.DeadLetter != 1 {
		t.Fatalf("unexpected cycle: %+v", cycle)
	}
	if len(scanner.limits) != 1 || scanner.limits[0] != 50 {
		t.Fatalf("unexpected scanner limits: %v", scanner.limits)
	}
}

func TestWorkerFailsClosedOnBoundaryError(t *testing.T) {
	want := errors.New("quorum unavailable")
	worker, err := NewWorker(&runtimeFixture{observeErr: want}, &scannerFixture{}, WorkerConfig{Interval: 2 * time.Second, CycleTimeout: time.Second, BatchSize: 1, ExpiryLimit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := worker.RunOnce(context.Background()); !errors.Is(err, want) {
		t.Fatalf("expected boundary failure, got %v", err)
	}
}

func TestWorkerPersistsOutcomeWorkBeforeExpiryFailure(t *testing.T) {
	want := errors.New("expiry quorum unavailable")
	service := &runtimeFixture{observed: []Job{{State: StateConfirmed}}}
	worker, err := NewWorker(service, &scannerFixture{err: want}, WorkerConfig{Interval: 2 * time.Second, CycleTimeout: time.Second, BatchSize: 2, ExpiryLimit: 1})
	if err != nil {
		t.Fatal(err)
	}
	cycle, err := worker.RunOnce(context.Background())
	if !errors.Is(err, want) || cycle.Observed != 1 || cycle.Confirmed != 1 {
		t.Fatalf("outcome priority lost: cycle=%+v err=%v", cycle, err)
	}
}

func TestWorkerContinuesAfterDurablyContainedRelayOutcome(t *testing.T) {
	service := &containedRuntimeFixture{}
	worker, err := NewWorker(service, &scannerFixture{}, WorkerConfig{Interval: 2 * time.Second, CycleTimeout: time.Second, BatchSize: 10, ExpiryLimit: 1})
	if err != nil {
		t.Fatal(err)
	}
	cycle, err := worker.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if service.calls != 3 || cycle.Relayed != 2 || cycle.Ambiguous != 1 || cycle.DeadLetter != 1 {
		t.Fatalf("unexpected contained cycle: calls=%d cycle=%+v", service.calls, cycle)
	}
}

func TestWorkerRejectsTypedNilAndUnsafeTiming(t *testing.T) {
	var service *runtimeFixture
	if _, err := NewWorker(service, &scannerFixture{}, WorkerConfig{Interval: 2 * time.Second, CycleTimeout: time.Second, BatchSize: 1, ExpiryLimit: 1}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("expected typed nil rejection, got %v", err)
	}
	if _, err := NewWorker(&runtimeFixture{}, &scannerFixture{}, WorkerConfig{Interval: time.Second, CycleTimeout: time.Second, BatchSize: 1, ExpiryLimit: 1}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("expected timing rejection, got %v", err)
	}
}
