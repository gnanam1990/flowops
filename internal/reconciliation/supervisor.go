package reconciliation

import (
	"context"
	"errors"
	"fmt"
	"time"
)

type SnapshotSource interface {
	Snapshot(context.Context) SnapshotResult
}

type ObservationSink interface {
	Observe(context.Context, []Observation) (ChainStatus, error)
}

type SupervisorConfig struct {
	Interval           time.Duration
	ObservationTimeout time.Duration
	OnResult           func(ChainStatus, SnapshotResult)
}

type Supervisor struct {
	source SnapshotSource
	sink   ObservationSink
	config SupervisorConfig
}

func NewSupervisor(source SnapshotSource, sink ObservationSink, config SupervisorConfig) (*Supervisor, error) {
	if source == nil || sink == nil {
		return nil, errors.New("observer supervisor requires a snapshot source and observation sink")
	}
	if config.Interval <= 0 || config.ObservationTimeout <= 0 || config.ObservationTimeout >= config.Interval {
		return nil, errors.New("observer interval must exceed the positive observation timeout")
	}
	return &Supervisor{source: source, sink: sink, config: config}, nil
}

func (s *Supervisor) Run(ctx context.Context) error {
	if err := s.observeOnce(ctx); err != nil {
		return err
	}
	ticker := time.NewTicker(s.config.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := s.observeOnce(ctx); err != nil {
				return err
			}
		}
	}
}

func (s *Supervisor) observeOnce(ctx context.Context) error {
	observationCtx, cancel := context.WithTimeout(ctx, s.config.ObservationTimeout)
	result := s.source.Snapshot(observationCtx)
	cancel()
	if ctx.Err() != nil {
		return nil
	}
	status, err := s.sink.Observe(ctx, result.Observations)
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return fmt.Errorf("persist Base observer result: %w", err)
	}
	if s.config.OnResult != nil {
		s.config.OnResult(status, result)
	}
	return nil
}
