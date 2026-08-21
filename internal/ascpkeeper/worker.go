package ascpkeeper

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"time"
)

type RuntimeService interface {
	RunOnce(context.Context) (Job, error)
	ObserveOnce(context.Context) (Job, error)
}

type RuntimeExpiryScanner interface {
	Scan(context.Context, int) (int, error)
}

type WorkerConfig struct {
	Interval     time.Duration
	CycleTimeout time.Duration
	BatchSize    int
	ExpiryLimit  int
	OnCycle      func(WorkerCycle)
}

type WorkerCycle struct {
	ExpiryEnqueued int `json:"expiryEnqueued"`
	Observed       int `json:"observed"`
	Relayed        int `json:"relayed"`
	Submitted      int `json:"submitted"`
	Confirmed      int `json:"confirmed"`
	Finalized      int `json:"finalized"`
	Ambiguous      int `json:"ambiguous"`
	TimedOut       int `json:"timedOut"`
	DeadLetter     int `json:"deadLetter"`
}

type Worker struct {
	service RuntimeService
	expiry  RuntimeExpiryScanner
	config  WorkerConfig
}

func NewWorker(service RuntimeService, expiry RuntimeExpiryScanner, config WorkerConfig) (*Worker, error) {
	if nilInterface(service) || nilInterface(expiry) || config.Interval < time.Second || config.Interval > 5*time.Minute ||
		config.CycleTimeout < time.Second || config.CycleTimeout >= config.Interval || config.BatchSize < 1 ||
		config.BatchSize > 100 || config.ExpiryLimit < 1 || config.ExpiryLimit > 1000 {
		return nil, ErrInvalidConfig
	}
	return &Worker{service: service, expiry: expiry, config: config}, nil
}

func (w *Worker) Run(ctx context.Context) error {
	if err := w.runCycle(ctx); err != nil {
		return err
	}
	ticker := time.NewTicker(w.config.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := w.runCycle(ctx); err != nil {
				return err
			}
		}
	}
}

func (w *Worker) RunOnce(ctx context.Context) (WorkerCycle, error) {
	cycle := WorkerCycle{}
	for index := 0; index < w.config.BatchSize; index++ {
		job, err := w.service.ObserveOnce(ctx)
		if errors.Is(err, ErrNoWork) {
			break
		}
		if err != nil {
			return cycle, fmt.Errorf("observe keeper outcome: %w", err)
		}
		cycle.Observed++
		cycle.count(job.State)
	}
	created, err := w.expiry.Scan(ctx, w.config.ExpiryLimit)
	if err != nil {
		return cycle, fmt.Errorf("scan independently proved keeper expiries: %w", err)
	}
	cycle.ExpiryEnqueued = created
	for index := 0; index < w.config.BatchSize; index++ {
		job, err := w.service.RunOnce(ctx)
		if errors.Is(err, ErrNoWork) {
			break
		}
		if err != nil {
			if !containedRelayOutcome(job) {
				return cycle, fmt.Errorf("advance keeper relay: %w", err)
			}
		}
		cycle.Relayed++
		cycle.count(job.State)
	}
	return cycle, nil
}

func containedRelayOutcome(job Job) bool {
	if !hash(job.JobID) {
		return false
	}
	switch job.State {
	case StateAmbiguous, StateTimedOut, StateDeadLetter:
		return true
	default:
		return false
	}
}

func (w *Worker) runCycle(ctx context.Context) error {
	cycleCtx, cancel := context.WithTimeout(ctx, w.config.CycleTimeout)
	cycle, err := w.RunOnce(cycleCtx)
	cancel()
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return err
	}
	if w.config.OnCycle != nil {
		w.config.OnCycle(cycle)
	}
	return nil
}

func (c *WorkerCycle) count(state State) {
	switch state {
	case StateSubmitted:
		c.Submitted++
	case StateConfirmed:
		c.Confirmed++
	case StateFinalized:
		c.Finalized++
	case StateAmbiguous:
		c.Ambiguous++
	case StateTimedOut:
		c.TimedOut++
	case StateDeadLetter:
		c.DeadLetter++
	}
}

func nilInterface(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Ptr, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

var _ RuntimeService = (*Service)(nil)
var _ RuntimeExpiryScanner = (*ExpiryScanner)(nil)
