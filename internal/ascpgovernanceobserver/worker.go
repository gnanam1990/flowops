package ascpgovernanceobserver

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gnanam1990/flowops/internal/ascpworkflow"
)

type PendingStore interface {
	Pending(context.Context, int) ([]ascpworkflow.Workflow, error)
}

type WorkflowCompleter interface {
	ObserveAndComplete(context.Context, string, string) (ascpworkflow.Workflow, error)
}

type WorkerConfig struct {
	Interval     time.Duration
	QueryTimeout time.Duration
	BatchSize    int
	OnCycle      func(WorkerCycle)
}

type WorkerCycle struct {
	Pending   int `json:"pending"`
	Completed int `json:"completed"`
	Deferred  int `json:"deferred"`
	Rejected  int `json:"rejected"`
}

type Worker struct {
	store     PendingStore
	completer WorkflowCompleter
	config    WorkerConfig
}

func NewWorker(store PendingStore, completer WorkflowCompleter, config WorkerConfig) (*Worker, error) {
	if store == nil || completer == nil || config.Interval <= 0 || config.QueryTimeout <= 0 ||
		config.QueryTimeout >= config.Interval || config.BatchSize < 1 || config.BatchSize > 1000 {
		return nil, ErrInvalidConfiguration
	}
	return &Worker{store: store, completer: completer, config: config}, nil
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
	pending, err := w.store.Pending(ctx, w.config.BatchSize+1)
	if err != nil {
		return cycle, err
	}
	if len(pending) > w.config.BatchSize {
		return cycle, errors.New("governance workflow observer capacity exceeded")
	}
	cycle.Pending = len(pending)
	for _, workflow := range pending {
		queryCtx, cancel := context.WithTimeout(ctx, w.config.QueryTimeout)
		_, err := w.completer.ObserveAndComplete(queryCtx, workflow.OrganizationID, workflow.WorkflowID)
		cancel()
		if err != nil {
			if errors.Is(err, ErrReceiptRejected) || errors.Is(err, ErrObserverDisagreement) {
				cycle.Rejected++
				continue
			}
			if deferred(err) {
				cycle.Deferred++
				continue
			}
			return cycle, fmt.Errorf("complete governance workflow %s: %w", workflow.WorkflowID, err)
		}
		cycle.Completed++
	}
	return cycle, nil
}

func (w *Worker) runCycle(ctx context.Context) error {
	cycle, err := w.RunOnce(ctx)
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

func deferred(err error) bool {
	return errors.Is(err, ErrReceiptPending) || errors.Is(err, ErrQuorumUnavailable) ||
		errors.Is(err, ErrUnsafeFinality)
}
