package ascprails

import (
	"context"
	"errors"
	"fmt"
	"time"
)

type WorkerService interface {
	DispatchOne(context.Context) (Job, error)
	FinalizeOne(context.Context) (Job, error)
}

type WorkerConfig struct {
	Interval     time.Duration
	CycleTimeout time.Duration
	BatchSize    int
	OnCycle      func(WorkerCycle)
}

type WorkerCycle struct {
	Finalized      int `json:"finalized"`
	Dispatched     int `json:"dispatched"`
	ResponseStored int `json:"responseStored"`
	RetryWait      int `json:"retryWait"`
	Captured       int `json:"captured"`
	Missing        int `json:"missing"`
	DeadLetter     int `json:"deadLetter"`
}

type Worker struct {
	service WorkerService
	config  WorkerConfig
}

func NewWorker(service WorkerService, config WorkerConfig) (*Worker, error) {
	if service == nil || config.Interval < time.Second || config.Interval > time.Minute ||
		config.CycleTimeout < time.Second || config.CycleTimeout >= config.Interval || config.BatchSize < 1 || config.BatchSize > 100 {
		return nil, ErrInvalidConfig
	}
	return &Worker{service: service, config: config}, nil
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
		job, err := w.service.FinalizeOne(ctx)
		if errors.Is(err, ErrNoWork) {
			break
		}
		if err != nil {
			return cycle, fmt.Errorf("finalize seller response: %w", err)
		}
		cycle.Finalized++
		cycle.count(job.State)
	}
	for index := 0; index < w.config.BatchSize; index++ {
		job, err := w.service.DispatchOne(ctx)
		if errors.Is(err, ErrNoWork) {
			break
		}
		if err != nil {
			return cycle, fmt.Errorf("dispatch seller request: %w", err)
		}
		cycle.Dispatched++
		cycle.count(job.State)
	}
	return cycle, nil
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
	case StateResponseStored:
		c.ResponseStored++
	case StateRetryWait:
		c.RetryWait++
	case StateCaptured:
		c.Captured++
	case StateMissing:
		c.Missing++
	case StateDeadLetter:
		c.DeadLetter++
	}
}

var _ WorkerService = (*Service)(nil)
