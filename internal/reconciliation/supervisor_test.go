package reconciliation

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type supervisorSource struct {
	mu      sync.Mutex
	results []SnapshotResult
	calls   int
}

func (s *supervisorSource) Snapshot(context.Context) SnapshotResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	index := s.calls
	s.calls++
	if index >= len(s.results) {
		index = len(s.results) - 1
	}
	return s.results[index]
}

type supervisorSink struct {
	mu           sync.Mutex
	observations [][]Observation
	err          error
	onObserve    func()
}

func (s *supervisorSink) Observe(_ context.Context, observations []Observation) (ChainStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.observations = append(s.observations, cloneObservations(observations))
	if s.onObserve != nil {
		s.onObserve()
	}
	return ChainStatus{State: StateRecovering}, s.err
}

func TestSupervisorObservesImmediatelyThenOnInterval(t *testing.T) {
	t.Parallel()
	source := &supervisorSource{results: []SnapshotResult{{Observations: []Observation{{Provider: "rpc_alpha"}}}}}
	sink := &supervisorSink{}
	results := make(chan struct{}, 2)
	supervisor, err := NewSupervisor(source, sink, SupervisorConfig{
		Interval: 20 * time.Millisecond, ObservationTimeout: 10 * time.Millisecond,
		OnResult: func(ChainStatus, SnapshotResult) { results <- struct{}{} },
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- supervisor.Run(ctx) }()
	for range 2 {
		select {
		case <-results:
		case <-time.After(time.Second):
			t.Fatal("observer supervisor did not poll")
		}
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.observations) < 2 || len(sink.observations[0]) != 1 {
		t.Fatalf("observations = %+v", sink.observations)
	}
}

func TestSupervisorPersistsEmptyFailureSnapshotAndStopsOnJournalFailure(t *testing.T) {
	t.Parallel()
	source := &supervisorSource{results: []SnapshotResult{{Failures: map[string]string{"rpc_alpha": "unavailable"}}}}
	sink := &supervisorSink{err: errors.New("journal sync failed")}
	supervisor, err := NewSupervisor(source, sink, SupervisorConfig{Interval: time.Second, ObservationTimeout: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	err = supervisor.Run(context.Background())
	if err == nil || !errors.Is(err, sink.err) {
		t.Fatalf("Run() error = %v", err)
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.observations) != 1 || len(sink.observations[0]) != 0 {
		t.Fatalf("failure observation = %+v", sink.observations)
	}
}

func TestSupervisorRejectsOverlappingOrInvalidTiming(t *testing.T) {
	t.Parallel()
	source, sink := &supervisorSource{}, &supervisorSink{}
	for _, config := range []SupervisorConfig{
		{},
		{Interval: time.Second, ObservationTimeout: time.Second},
		{Interval: time.Second, ObservationTimeout: 2 * time.Second},
	} {
		if _, err := NewSupervisor(source, sink, config); err == nil {
			t.Fatalf("accepted config %+v", config)
		}
	}
}

func TestSupervisorTreatsSinkCancellationAsCleanShutdown(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	source := &supervisorSource{results: []SnapshotResult{{Observations: []Observation{{Provider: "rpc_alpha"}}}}}
	sink := &supervisorSink{err: context.Canceled, onObserve: cancel}
	supervisor, err := NewSupervisor(source, sink, SupervisorConfig{Interval: time.Second, ObservationTimeout: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if err := supervisor.Run(ctx); err != nil {
		t.Fatalf("shutdown returned an operational error: %v", err)
	}
}
