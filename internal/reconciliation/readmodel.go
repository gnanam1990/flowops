package reconciliation

import (
	"math/big"
	"sort"
	"time"
)

// OrganizationView is a tenant-scoped, read-only projection of the durable
// reconciliation journal. It deliberately does not claim wallet balances or
// spendable funds: the operational ledger only proves FlowOps-recognized
// settlement effects.
type OrganizationView struct {
	Available    bool                 `json:"available"`
	Chain        ChainStatus          `json:"chain"`
	Recovery     RecoveryProgress     `json:"recovery"`
	Exceptions   []Exception          `json:"exceptions"`
	Assets       []AssetLedgerSummary `json:"assets"`
	Unclassified int                  `json:"unclassifiedLedgerTransactions"`
	GeneratedAt  time.Time            `json:"generatedAt"`
}

type RecoveryProgress struct {
	CheckpointBlock      uint64 `json:"checkpointBlock,omitempty"`
	ObservedThroughBlock uint64 `json:"observedThroughBlock,omitempty"`
	TotalCandidates      int    `json:"totalCandidates"`
	ResolvedCandidates   int    `json:"resolvedCandidates"`
	UnresolvedOutcomes   int    `json:"unresolvedOutcomes"`
	QuarantinedOutcomes  int    `json:"quarantinedOutcomes"`
	PendingFinality      int    `json:"pendingFinality"`
	ReadyForManualResume bool   `json:"readyForManualResume"`
	Complete             bool   `json:"complete"`
}

type Exception struct {
	ID                   string    `json:"id"`
	Kind                 string    `json:"kind"`
	State                string    `json:"state"`
	TransactionHash      string    `json:"transactionHash,omitempty"`
	Asset                string    `json:"asset"`
	AmountAtomic         string    `json:"amountAtomic"`
	FirstObservedAt      time.Time `json:"firstObservedAt"`
	Reason               string    `json:"reason"`
	OperatorActionNeeded bool      `json:"operatorActionNeeded"`
	RecoveryOutcome      string    `json:"recoveryOutcome,omitempty"`
	TransactionNonce     uint64    `json:"transactionNonce,omitempty"`
	ReplacementHash      string    `json:"replacementHash,omitempty"`
	ReplacementRecipient string    `json:"replacementRecipient,omitempty"`
	ReplacementAmount    string    `json:"replacementAmountAtomic,omitempty"`
}

type AssetLedgerSummary struct {
	Asset                   string `json:"asset"`
	EscrowLockedAtomic      string `json:"escrowLockedAtomic"`
	RecognizedExpenseAtomic string `json:"recognizedExpenseAtomic"`
	SpentTodayAtomic        string `json:"spentTodayAtomic"`
	SpentMonthAtomic        string `json:"spentMonthAtomic"`
	UnresolvedAtomic        string `json:"unresolvedAtomic"`
}

type mutableAssetSummary struct {
	escrowLocked      *big.Int
	recognizedExpense *big.Int
	spentToday        *big.Int
	spentMonth        *big.Int
	unresolved        *big.Int
}

func newMutableAssetSummary() *mutableAssetSummary {
	return &mutableAssetSummary{
		escrowLocked: new(big.Int), recognizedExpense: new(big.Int),
		spentToday: new(big.Int), spentMonth: new(big.Int), unresolved: new(big.Int),
	}
}

func (e *Engine) OrganizationView(organizationID string) OrganizationView {
	e.mu.Lock()
	defer e.mu.Unlock()
	now := e.config.Clock().UTC()
	view := OrganizationView{
		Available: true, Chain: e.statusAt(now), GeneratedAt: now,
		Exceptions: make([]Exception, 0), Assets: make([]AssetLedgerSummary, 0),
	}
	assets := make(map[string]*mutableAssetSummary)
	assetSummary := func(asset string) *mutableAssetSummary {
		if assets[asset] == nil {
			assets[asset] = newMutableAssetSummary()
		}
		return assets[asset]
	}

	for _, execution := range e.executions {
		if execution.Expected.OrganizationID != organizationID {
			continue
		}
		view.Recovery.TotalCandidates++
		switch execution.State {
		case ExecutionBroadcast, ExecutionPendingChainRecovery:
			view.Recovery.UnresolvedOutcomes++
			assetSummary(execution.Expected.Asset).unresolved.Add(assetSummary(execution.Expected.Asset).unresolved, mustInteger(execution.Expected.AmountAtomic))
			view.Exceptions = append(view.Exceptions, Exception{
				ID: execution.Expected.ExecutionID, Kind: "DIRECT_EXECUTION", State: string(execution.State),
				TransactionHash: execution.Expected.TransactionHash, Asset: execution.Expected.Asset,
				AmountAtomic: execution.Expected.AmountAtomic, FirstObservedAt: execution.BroadcastAt,
				Reason: execution.Resolution, OperatorActionNeeded: execution.State == ExecutionPendingChainRecovery,
			})
		case ExecutionQuarantined:
			view.Recovery.QuarantinedOutcomes++
			assetSummary(execution.Expected.Asset)
			exception := Exception{
				ID: execution.Expected.ExecutionID, Kind: "DIRECT_EXECUTION", State: string(execution.State),
				TransactionHash: execution.Expected.TransactionHash, Asset: execution.Expected.Asset,
				AmountAtomic: execution.Expected.AmountAtomic, FirstObservedAt: execution.BroadcastAt,
				Reason: execution.Resolution, OperatorActionNeeded: true,
			}
			if execution.TransactionRecovery != nil {
				exception.RecoveryOutcome = string(execution.TransactionRecovery.Outcome)
				exception.TransactionNonce = execution.TransactionRecovery.Nonce
				exception.ReplacementHash = execution.TransactionRecovery.ReplacementTransaction
				exception.ReplacementRecipient = execution.TransactionRecovery.ReplacementRecipient
				exception.ReplacementAmount = execution.TransactionRecovery.ReplacementAmountAtomic
			}
			view.Exceptions = append(view.Exceptions, exception)
		default:
			view.Recovery.ResolvedCandidates++
		}
		if execution.State == ExecutionSettled && execution.FinalityCheckedAt == nil {
			view.Recovery.PendingFinality++
		}
	}
	for _, call := range e.escrowCalls {
		if call.Intent.OrganizationID != organizationID {
			continue
		}
		view.Recovery.TotalCandidates += len(call.Transitions)
		view.Recovery.ResolvedCandidates += len(call.Transitions)
		for _, transition := range call.Transitions {
			if (transition.State == EscrowTransitionConfirmed || transition.State == EscrowTransitionReverted) && transition.FinalityCheckedAt == nil {
				view.Recovery.PendingFinality++
			}
		}
		if call.Pending != nil {
			view.Recovery.TotalCandidates++
			view.Recovery.UnresolvedOutcomes++
			assetSummary(call.Intent.Asset).unresolved.Add(assetSummary(call.Intent.Asset).unresolved, mustInteger(call.Intent.AmountAtomic))
			view.Exceptions = append(view.Exceptions, Exception{
				ID: call.Intent.CallID, Kind: "ESCROW_TRANSITION", State: string(call.Pending.State),
				TransactionHash: call.Pending.Expected.TransactionHash, Asset: call.Intent.Asset,
				AmountAtomic: call.Intent.AmountAtomic, FirstObservedAt: call.Pending.RegisteredAt,
				Reason: call.Pending.Resolution, OperatorActionNeeded: call.Pending.State == EscrowTransitionPendingRecovery,
			})
		}
		if call.State == EscrowPositionQuarantined {
			view.Recovery.QuarantinedOutcomes++
			view.Exceptions = append(view.Exceptions, Exception{
				ID: call.Intent.CallID, Kind: "ESCROW_CALL", State: string(call.State), Asset: call.Intent.Asset,
				AmountAtomic: call.Intent.AmountAtomic, FirstObservedAt: call.RegisteredAt,
				Reason: "canonical escrow history requires operator incident review", OperatorActionNeeded: true,
			})
		}
	}

	for _, transaction := range e.ledger {
		if transaction.OrganizationID != organizationID {
			continue
		}
		asset := e.assetForLedger(transaction)
		if asset == "" {
			view.Unclassified++
			continue
		}
		summary := assetSummary(asset)
		for _, posting := range transaction.Postings {
			amount := mustInteger(posting.AmountAtomic)
			switch posting.Account {
			case "escrow_locked":
				summary.escrowLocked.Add(summary.escrowLocked, amount)
			case "agent_service_expense":
				summary.recognizedExpense.Add(summary.recognizedExpense, amount)
				if sameUTCDay(transaction.RecordedAt, now) {
					summary.spentToday.Add(summary.spentToday, amount)
				}
				if transaction.RecordedAt.UTC().Year() == now.Year() && transaction.RecordedAt.UTC().Month() == now.Month() {
					summary.spentMonth.Add(summary.spentMonth, amount)
				}
			}
		}
	}

	assetIDs := make([]string, 0, len(assets))
	for asset := range assets {
		assetIDs = append(assetIDs, asset)
	}
	sort.Strings(assetIDs)
	for _, asset := range assetIDs {
		summary := assets[asset]
		view.Assets = append(view.Assets, AssetLedgerSummary{
			Asset: asset, EscrowLockedAtomic: summary.escrowLocked.String(),
			RecognizedExpenseAtomic: summary.recognizedExpense.String(),
			SpentTodayAtomic:        summary.spentToday.String(), SpentMonthAtomic: summary.spentMonth.String(),
			UnresolvedAtomic: summary.unresolved.String(),
		})
	}
	sort.Slice(view.Exceptions, func(i, j int) bool {
		if view.Exceptions[i].FirstObservedAt.Equal(view.Exceptions[j].FirstObservedAt) {
			return view.Exceptions[i].ID < view.Exceptions[j].ID
		}
		return view.Exceptions[i].FirstObservedAt.Before(view.Exceptions[j].FirstObservedAt)
	})
	if view.Chain.LastTrusted != nil {
		view.Recovery.CheckpointBlock = view.Chain.LastTrusted.Cursor
		view.Recovery.ObservedThroughBlock = view.Chain.LastTrusted.BlockNumber
	}
	view.Recovery.ReadyForManualResume = view.Chain.ReadyForManualResume
	view.Recovery.Complete = view.Recovery.UnresolvedOutcomes == 0 && view.Recovery.PendingFinality == 0
	return view
}

func (e *Engine) assetForLedger(transaction LedgerTransaction) string {
	if execution, ok := e.executions[transaction.ReferenceID]; ok {
		return execution.Expected.Asset
	}
	if call, ok := e.escrowCalls[transaction.ReferenceID]; ok {
		return call.Intent.Asset
	}
	return ""
}

func mustInteger(value string) *big.Int {
	result, ok := new(big.Int).SetString(value, 10)
	if !ok {
		panic("validated ledger integer became invalid")
	}
	return result
}

func sameUTCDay(left, right time.Time) bool {
	left, right = left.UTC(), right.UTC()
	return left.Year() == right.Year() && left.YearDay() == right.YearDay()
}
