package ascpsettlement

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gnanam1990/flowops/internal/reconciliation"
)

type WorkerStore interface {
	Pending(context.Context, int) ([]Attempt, error)
	FinalizedUnchecked(context.Context, int) ([]Attempt, error)
	Expected(context.Context, string, reconciliation.ASCPReceiptAction) (reconciliation.ASCPExpectedReceipt, error)
	Apply(context.Context, Result) (Operation, error)
	ApplyReorg(context.Context, ReorgResult) (Operation, error)
	ConfirmCanonical(context.Context, ReorgResult) error
}

type EvidenceReader interface {
	Read(context.Context, reconciliation.ASCPExpectedReceipt) (Result, error)
	CheckCanonical(context.Context, string, reconciliation.ASCPReceiptAction, string, uint64, string) (ReorgResult, error)
}

type WorkerConfig struct {
	Interval     time.Duration
	QueryTimeout time.Duration
	BatchSize    int
	OnCycle      func(WorkerCycle)
}

type WorkerCycle struct {
	Pending            int `json:"pending"`
	Applied            int `json:"applied"`
	FinalityChecks     int `json:"finalityChecks"`
	CanonicalConfirmed int `json:"canonicalConfirmed"`
	ReorgsRecovered    int `json:"reorgsRecovered"`
	Deferred           int `json:"deferred"`
}

type Worker struct {
	store  WorkerStore
	reader EvidenceReader
	config WorkerConfig
}

func NewWorker(store WorkerStore, reader EvidenceReader, config WorkerConfig) (*Worker, error) {
	if store == nil || reader == nil || config.Interval <= 0 || config.QueryTimeout <= 0 ||
		config.QueryTimeout >= config.Interval || config.BatchSize < 1 || config.BatchSize > 1000 {
		return nil, errors.New("invalid ASCP settlement worker configuration")
	}
	return &Worker{store: store, reader: reader, config: config}, nil
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
	pending, err := w.store.Pending(ctx, w.config.BatchSize)
	if err != nil {
		return cycle, err
	}
	cycle.Pending = len(pending)
	for _, attempt := range pending {
		expected, err := w.store.Expected(ctx, attempt.OperationID, attempt.Action)
		if err != nil {
			return cycle, err
		}
		queryCtx, cancel := context.WithTimeout(ctx, w.config.QueryTimeout)
		result, err := w.reader.Read(queryCtx, expected)
		cancel()
		if err != nil {
			if deferredEvidence(err) {
				cycle.Deferred++
				continue
			}
			return cycle, fmt.Errorf("read ASCP %s receipt for %s: %w", attempt.Action, attempt.OperationID, err)
		}
		if _, err := w.store.Apply(ctx, result); err != nil {
			return cycle, fmt.Errorf("apply ASCP %s receipt for %s: %w", attempt.Action, attempt.OperationID, err)
		}
		cycle.Applied++
	}
	finalized, err := w.store.FinalizedUnchecked(ctx, w.config.BatchSize)
	if err != nil {
		return cycle, err
	}
	for _, attempt := range finalized {
		cycle.FinalityChecks++
		queryCtx, cancel := context.WithTimeout(ctx, w.config.QueryTimeout)
		result, err := w.reader.CheckCanonical(queryCtx, attempt.OperationID, attempt.Action,
			attempt.TransactionHash, attempt.BlockNumber, attempt.BlockHash)
		cancel()
		if err != nil {
			if deferredEvidence(err) {
				cycle.Deferred++
				continue
			}
			return cycle, fmt.Errorf("check ASCP %s canonical block for %s: %w", attempt.Action, attempt.OperationID, err)
		}
		if result.Reorged {
			if _, err := w.store.ApplyReorg(ctx, result); err != nil {
				return cycle, fmt.Errorf("recover ASCP %s reorg for %s: %w", attempt.Action, attempt.OperationID, err)
			}
			cycle.ReorgsRecovered++
		} else {
			if err := w.store.ConfirmCanonical(ctx, result); err != nil {
				return cycle, err
			}
			cycle.CanonicalConfirmed++
		}
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

func deferredEvidence(err error) bool {
	return errors.Is(err, ErrQuorumUnavailable) || errors.Is(err, ErrUnsafeFinality) || errors.Is(err, ErrObserverDisagreement)
}
