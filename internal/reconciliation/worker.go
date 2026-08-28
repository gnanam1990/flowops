package reconciliation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

type ReceiptAndBlockSource interface {
	ReceiptQuorum(context.Context, ExpectedExecution) ReceiptResult
	CanonicalBlockQuorum(context.Context, Execution) ReorgResult
}

type EscrowReceiptAndBlockSource interface {
	EscrowReceiptQuorum(context.Context, EscrowExpectedReceipt) EscrowReceiptResult
	EscrowCanonicalBlockQuorum(context.Context, EscrowTransition) ReorgResult
}

type WorkerEngine interface {
	Status() ChainStatus
	Executions() []Execution
	LedgerTransaction(string) (LedgerTransaction, bool)
	ReconcileReceipt(context.Context, string, []ReceiptEvidence, *LedgerTransaction) (Execution, error)
	ConfirmFinality(context.Context, string, []ReorgEvidence) (Execution, error)
	ReopenReorg(context.Context, string, []ReorgEvidence, LedgerTransaction) (Execution, error)
	ReopenRevertedReorg(context.Context, string, []ReorgEvidence) (Execution, error)
	FinalityDepth() uint64
}

type EscrowWorkerEngine interface {
	EscrowCalls() []EscrowCall
	ReconcileEscrowTransition(context.Context, string, []EscrowReceiptEvidence) (EscrowCall, error)
	ConfirmEscrowFinality(context.Context, string, string, []ReorgEvidence) (EscrowCall, error)
	ReopenEscrowReorg(context.Context, string, string, []ReorgEvidence) (EscrowCall, error)
}

type WorkerConfig struct {
	Interval     time.Duration
	QueryTimeout time.Duration
	Clock        func() time.Time
	OnCycle      func(WorkerCycle)
}

type WorkerCycle struct {
	Examined          int  `json:"examined"`
	ReceiptCandidates int  `json:"receiptCandidates"`
	Settled           int  `json:"settled"`
	Reverted          int  `json:"reverted"`
	FinalityConfirmed int  `json:"finalityConfirmed"`
	ReorgsReopened    int  `json:"reorgsReopened"`
	EscrowCandidates  int  `json:"escrowCandidates"`
	EscrowConfirmed   int  `json:"escrowConfirmed"`
	EscrowReverted    int  `json:"escrowReverted"`
	EscrowFinalized   int  `json:"escrowFinalized"`
	EscrowReorgs      int  `json:"escrowReorgs"`
	Deferred          int  `json:"deferred"`
	SkippedForChain   bool `json:"skippedForChain"`
}

type Worker struct {
	source ReceiptAndBlockSource
	engine WorkerEngine
	config WorkerConfig
}

func NewWorker(source ReceiptAndBlockSource, engine WorkerEngine, config WorkerConfig) (*Worker, error) {
	if source == nil || engine == nil {
		return nil, errors.New("reconciliation worker requires a receipt source and engine")
	}
	if config.Interval <= 0 || config.QueryTimeout <= 0 || config.QueryTimeout >= config.Interval {
		return nil, errors.New("reconciliation worker interval must exceed the positive query timeout")
	}
	if engine.FinalityDepth() == 0 {
		return nil, errors.New("reconciliation worker requires a positive finality depth")
	}
	if config.Clock == nil {
		config.Clock = time.Now
	}
	return &Worker{source: source, engine: engine, config: config}, nil
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
	status := w.engine.Status()
	if status.FinalizationPaused || status.LastTrusted == nil {
		cycle.SkippedForChain = true
		return cycle, nil
	}
	executions := w.engine.Executions()
	cycle.Examined = len(executions)
	for _, execution := range executions {
		switch execution.State {
		case ExecutionBroadcast, ExecutionPendingChainRecovery:
			cycle.ReceiptCandidates++
			if err := w.reconcileReceipt(ctx, execution, &cycle); err != nil {
				return cycle, err
			}
		case ExecutionSettled, ExecutionReverted:
			if execution.FinalityCheckedAt != nil {
				continue
			}
			if status.LastTrusted.BlockNumber < execution.BlockNumber || status.LastTrusted.BlockNumber-execution.BlockNumber < w.engine.FinalityDepth() {
				continue
			}
			if err := w.checkFinality(ctx, execution, &cycle); err != nil {
				return cycle, err
			}
		}
	}
	escrowSource, sourceOK := w.source.(EscrowReceiptAndBlockSource)
	escrowEngine, engineOK := w.engine.(EscrowWorkerEngine)
	if sourceOK && engineOK {
		for _, call := range escrowEngine.EscrowCalls() {
			if call.Pending != nil {
				cycle.EscrowCandidates++
				if err := w.reconcileEscrowReceipt(ctx, escrowSource, escrowEngine, call, &cycle); err != nil {
					return cycle, err
				}
			}
			for _, transition := range call.Transitions {
				if (transition.State != EscrowTransitionConfirmed && transition.State != EscrowTransitionReverted) || transition.FinalityCheckedAt != nil || status.LastTrusted.BlockNumber < transition.BlockNumber || status.LastTrusted.BlockNumber-transition.BlockNumber < w.engine.FinalityDepth() {
					continue
				}
				reorgsBefore := cycle.EscrowReorgs
				if err := w.checkEscrowFinality(ctx, escrowSource, escrowEngine, call, transition, &cycle); err != nil {
					return cycle, err
				}
				if cycle.EscrowReorgs > reorgsBefore {
					break
				}
			}
		}
	}
	return cycle, nil
}

func (w *Worker) reconcileEscrowReceipt(ctx context.Context, source EscrowReceiptAndBlockSource, engine EscrowWorkerEngine, call EscrowCall, cycle *WorkerCycle) error {
	queryCtx, cancel := context.WithTimeout(ctx, w.config.QueryTimeout)
	result := source.EscrowReceiptQuorum(queryCtx, call.Pending.Expected)
	cancel()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if len(result.Evidence) == 0 {
		cycle.Deferred++
		return nil
	}
	resolved, err := engine.ReconcileEscrowTransition(ctx, call.Intent.CallID, result.Evidence)
	if err != nil {
		if errors.Is(err, ErrUnsafeFinality) || errors.Is(err, ErrChainUnavailable) {
			cycle.Deferred++
			return nil
		}
		return fmt.Errorf("reconcile escrow receipt for %s: %w", call.Intent.CallID, err)
	}
	last := resolved.Transitions[len(resolved.Transitions)-1]
	if last.State == EscrowTransitionConfirmed {
		cycle.EscrowConfirmed++
	} else if last.State == EscrowTransitionReverted {
		cycle.EscrowReverted++
	}
	return nil
}

func (w *Worker) checkEscrowFinality(ctx context.Context, source EscrowReceiptAndBlockSource, engine EscrowWorkerEngine, call EscrowCall, transition EscrowTransition, cycle *WorkerCycle) error {
	queryCtx, cancel := context.WithTimeout(ctx, w.config.QueryTimeout)
	result := source.EscrowCanonicalBlockQuorum(queryCtx, transition)
	cancel()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if len(result.Evidence) == 0 {
		cycle.Deferred++
		return nil
	}
	if result.Evidence[0].CanonicalBlockHash == transition.BlockHash {
		if _, err := engine.ConfirmEscrowFinality(ctx, call.Intent.CallID, transition.Expected.TransactionHash, result.Evidence); err != nil {
			if errors.Is(err, ErrUnsafeFinality) || errors.Is(err, ErrChainUnavailable) {
				cycle.Deferred++
				return nil
			}
			return fmt.Errorf("confirm escrow finality for %s: %w", call.Intent.CallID, err)
		}
		cycle.EscrowFinalized++
		return nil
	}
	if _, err := engine.ReopenEscrowReorg(ctx, call.Intent.CallID, transition.Expected.TransactionHash, result.Evidence); err != nil {
		if errors.Is(err, ErrUnsafeFinality) || errors.Is(err, ErrChainUnavailable) {
			cycle.Deferred++
			return nil
		}
		return fmt.Errorf("reopen escrow reorg for %s: %w", call.Intent.CallID, err)
	}
	cycle.EscrowReorgs++
	return nil
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

func (w *Worker) reconcileReceipt(ctx context.Context, execution Execution, cycle *WorkerCycle) error {
	queryCtx, cancel := context.WithTimeout(ctx, w.config.QueryTimeout)
	result := w.source.ReceiptQuorum(queryCtx, execution.Expected)
	cancel()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if len(result.Evidence) == 0 {
		cycle.Deferred++
		return nil
	}
	var settlement *LedgerTransaction
	if result.Evidence[0].Success {
		candidate := settlementTransaction(execution, result.Evidence[0], w.config.Clock().UTC())
		settlement = &candidate
	}
	resolved, err := w.engine.ReconcileReceipt(ctx, execution.Expected.ExecutionID, result.Evidence, settlement)
	if err != nil {
		if errors.Is(err, ErrUnsafeFinality) || errors.Is(err, ErrChainUnavailable) {
			cycle.Deferred++
			return nil
		}
		return fmt.Errorf("reconcile receipt for %s: %w", execution.Expected.ExecutionID, err)
	}
	if resolved.State == ExecutionSettled {
		cycle.Settled++
	} else if resolved.State == ExecutionReverted {
		cycle.Reverted++
	}
	return nil
}

func (w *Worker) checkFinality(ctx context.Context, execution Execution, cycle *WorkerCycle) error {
	queryCtx, cancel := context.WithTimeout(ctx, w.config.QueryTimeout)
	result := w.source.CanonicalBlockQuorum(queryCtx, execution)
	cancel()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if len(result.Evidence) == 0 {
		cycle.Deferred++
		return nil
	}
	if result.Evidence[0].CanonicalBlockHash == execution.BlockHash {
		_, err := w.engine.ConfirmFinality(ctx, execution.Expected.ExecutionID, result.Evidence)
		if err != nil {
			if errors.Is(err, ErrUnsafeFinality) || errors.Is(err, ErrChainUnavailable) {
				cycle.Deferred++
				return nil
			}
			return fmt.Errorf("confirm finality for %s: %w", execution.Expected.ExecutionID, err)
		}
		cycle.FinalityConfirmed++
		return nil
	}
	if execution.State == ExecutionReverted {
		if _, err := w.engine.ReopenRevertedReorg(ctx, execution.Expected.ExecutionID, result.Evidence); err != nil {
			if errors.Is(err, ErrUnsafeFinality) || errors.Is(err, ErrChainUnavailable) {
				cycle.Deferred++
				return nil
			}
			return fmt.Errorf("reopen reverted reorg for %s: %w", execution.Expected.ExecutionID, err)
		}
		cycle.ReorgsReopened++
		return nil
	}
	original, ok := w.engine.LedgerTransaction(execution.LedgerTransactionID)
	if !ok {
		return fmt.Errorf("settled execution %s has no ledger transaction", execution.Expected.ExecutionID)
	}
	correction := correctionTransaction(execution, original, result.Evidence[0], w.config.Clock().UTC())
	if _, err := w.engine.ReopenReorg(ctx, execution.Expected.ExecutionID, result.Evidence, correction); err != nil {
		if errors.Is(err, ErrUnsafeFinality) || errors.Is(err, ErrChainUnavailable) {
			cycle.Deferred++
			return nil
		}
		return fmt.Errorf("reopen reorg for %s: %w", execution.Expected.ExecutionID, err)
	}
	cycle.ReorgsReopened++
	return nil
}

func settlementTransaction(execution Execution, evidence ReceiptEvidence, recordedAt time.Time) LedgerTransaction {
	return LedgerTransaction{
		TransactionID:  derivedLedgerID("settlement", execution.Expected.ExecutionID, evidence.BlockHash),
		OrganizationID: execution.Expected.OrganizationID,
		Kind:           LedgerSettlement,
		ReferenceID:    execution.Expected.ExecutionID,
		RecordedAt:     recordedAt,
		Postings: []Posting{
			{Account: "agent_service_expense", AmountAtomic: execution.Expected.AmountAtomic},
			{Account: "pending_settlement", AmountAtomic: "-" + execution.Expected.AmountAtomic},
		},
	}
}

func correctionTransaction(execution Execution, original LedgerTransaction, evidence ReorgEvidence, recordedAt time.Time) LedgerTransaction {
	postings := make([]Posting, len(original.Postings))
	for index, posting := range original.Postings {
		amount := posting.AmountAtomic
		if amount[0] == '-' {
			amount = amount[1:]
		} else {
			amount = "-" + amount
		}
		postings[index] = Posting{Account: posting.Account, AmountAtomic: amount}
	}
	return LedgerTransaction{
		TransactionID:         derivedLedgerID("correction", execution.Expected.ExecutionID, original.TransactionID, evidence.CanonicalBlockHash),
		OrganizationID:        execution.Expected.OrganizationID,
		Kind:                  LedgerCorrection,
		ReferenceID:           execution.Expected.ExecutionID,
		ReversesTransactionID: original.TransactionID,
		Postings:              postings,
		RecordedAt:            recordedAt,
	}
}

func derivedLedgerID(prefix string, inputs ...string) string {
	digest := sha256.New()
	for _, input := range inputs {
		_, _ = digest.Write([]byte{0})
		_, _ = digest.Write([]byte(input))
	}
	return prefix + "_" + hex.EncodeToString(digest.Sum(nil))
}
