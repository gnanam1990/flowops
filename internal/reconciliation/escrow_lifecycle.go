package reconciliation

import (
	"context"
	"errors"
	"fmt"
)

type EscrowLifecycleManifest struct {
	SchemaVersion    int                     `json:"schemaVersion"`
	Network          string                  `json:"network"`
	MinConfirmations uint64                  `json:"minConfirmations"`
	Transitions      []EscrowExpectedReceipt `json:"transitions"`
}

type EscrowTransitionResult struct {
	Action           EscrowAction            `json:"action"`
	TransactionHash  string                  `json:"transactionHash"`
	CanonicalReceipt EscrowReceiptEvidence   `json:"canonicalReceipt"`
	ProviderEvidence []EscrowReceiptEvidence `json:"providerEvidence"`
	Failures         map[string]string       `json:"failures,omitempty"`
}

type EscrowLifecycleResult struct {
	Ready       bool                     `json:"ready"`
	FinalAction EscrowAction             `json:"finalAction,omitempty"`
	Transitions []EscrowTransitionResult `json:"transitions"`
}

func (m EscrowLifecycleManifest) Validate() error {
	if m.SchemaVersion != 1 || m.MinConfirmations == 0 {
		return errors.New("escrow lifecycle schema version or confirmation floor is invalid")
	}
	if len(m.Transitions) < 2 || len(m.Transitions) > 4 {
		return errors.New("escrow lifecycle must contain two to four transitions")
	}
	chainID := m.Transitions[0].ChainID
	if chainID == 84532 && m.Network != "base-sepolia" || chainID == 8453 && m.Network != "base-mainnet" {
		return errors.New("escrow lifecycle network does not match its Base chain ID")
	}
	seenTransactions := make(map[string]struct{}, len(m.Transitions))
	for index, transition := range m.Transitions {
		if err := transition.Validate(); err != nil {
			return fmt.Errorf("transition %d: %w", index, err)
		}
		if _, exists := seenTransactions[transition.TransactionHash]; exists {
			return errors.New("escrow lifecycle transaction hashes must be unique")
		}
		seenTransactions[transition.TransactionHash] = struct{}{}
		if index > 0 && !sameEscrowCall(m.Transitions[0], transition) {
			return errors.New("escrow lifecycle transitions do not describe one immutable call")
		}
	}
	actions := make([]EscrowAction, len(m.Transitions))
	for index := range m.Transitions {
		actions[index] = m.Transitions[index].Action
	}
	valid := false
	switch len(actions) {
	case 2:
		valid = actions[0] == EscrowFund && actions[1] == EscrowRefund && m.Transitions[1].RefundedFromState == 1
	case 3:
		valid = actions[0] == EscrowFund && actions[1] == EscrowAcknowledge && actions[2] == EscrowRefund && m.Transitions[2].RefundedFromState == 2
	case 4:
		valid = actions[0] == EscrowFund && actions[1] == EscrowAcknowledge && actions[2] == EscrowDeliver && actions[3] == EscrowRelease
	}
	if !valid {
		return errors.New("escrow lifecycle transition order is invalid")
	}
	return nil
}

func VerifyEscrowLifecycle(ctx context.Context, observers *ObserverSet, manifest EscrowLifecycleManifest) (EscrowLifecycleResult, error) {
	if observers == nil {
		return EscrowLifecycleResult{}, errors.New("escrow lifecycle requires an observer set")
	}
	if err := manifest.Validate(); err != nil {
		return EscrowLifecycleResult{}, err
	}
	result := EscrowLifecycleResult{}
	var priorBlock uint64
	for _, transition := range manifest.Transitions {
		quorum := observers.EscrowReceiptQuorum(ctx, transition)
		item := EscrowTransitionResult{Action: transition.Action, TransactionHash: transition.TransactionHash, ProviderEvidence: quorum.Evidence, Failures: quorum.Failures}
		if len(quorum.Evidence) < 2 {
			result.Transitions = append(result.Transitions, item)
			return result, errors.New("escrow transition lacks two-provider receipt evidence")
		}
		canonical := quorum.Evidence[0]
		if !canonical.Success {
			result.Transitions = append(result.Transitions, item)
			return result, errors.New("escrow transition reverted")
		}
		for _, evidence := range quorum.Evidence[1:] {
			if evidence.TransactionHash != canonical.TransactionHash || evidence.BlockNumber != canonical.BlockNumber || evidence.BlockHash != canonical.BlockHash || evidence.Action != canonical.Action || evidence.CallID != canonical.CallID || !evidence.Success {
				result.Transitions = append(result.Transitions, item)
				return result, errors.New("escrow receipt providers disagree on canonical transition evidence")
			}
		}
		if canonical.BlockNumber < priorBlock {
			result.Transitions = append(result.Transitions, item)
			return result, errors.New("escrow lifecycle block order regressed")
		}
		for _, evidence := range quorum.Evidence {
			if evidence.ConfirmedHead < evidence.BlockNumber || evidence.ConfirmedHead-evidence.BlockNumber < manifest.MinConfirmations {
				result.Transitions = append(result.Transitions, item)
				return result, errors.New("escrow transition has insufficient independent confirmations")
			}
		}
		item.CanonicalReceipt = canonical
		result.Transitions = append(result.Transitions, item)
		priorBlock = canonical.BlockNumber
	}
	result.Ready = true
	result.FinalAction = manifest.Transitions[len(manifest.Transitions)-1].Action
	return result, nil
}

func sameEscrowCall(left, right EscrowExpectedReceipt) bool {
	return left.ChainID == right.ChainID && left.Contract == right.Contract && left.Asset == right.Asset && left.CallID == right.CallID &&
		left.Buyer == right.Buyer && left.Provider == right.Provider && left.AmountAtomic == right.AmountAtomic &&
		left.TaskDigest == right.TaskDigest && left.RequestDigest == right.RequestDigest && left.AcknowledgeBy == right.AcknowledgeBy &&
		left.DeliverBy == right.DeliverBy && left.ReleaseWindow == right.ReleaseWindow
}
