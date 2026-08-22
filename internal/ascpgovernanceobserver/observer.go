// Package ascpgovernanceobserver independently discovers and verifies
// finalized Base governance receipts for approved ASCP proposal workflows.
package ascpgovernanceobserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/gnanam1990/flowops/internal/ascpworkflow"
	"github.com/gnanam1990/flowops/internal/reconciliation"
	"github.com/gnanam1990/flowops/pkg/governanceworkflow"
)

const governanceWorkflowBoundSignature = "GovernanceWorkflowBound(bytes32,bytes32,bytes4)"

var (
	ErrInvalidConfiguration = errors.New("invalid governance observer configuration")
	ErrReceiptPending       = errors.New("governance workflow receipt is pending")
	ErrReceiptRejected      = errors.New("governance workflow receipt was deterministically rejected")
	ErrQuorumUnavailable    = errors.New("governance receipt quorum is unavailable")
	ErrObserverDisagreement = errors.New("governance receipt observers disagree")
	ErrUnsafeFinality       = errors.New("governance receipt finality is insufficient")
	ErrUnsupportedWorkflow  = errors.New("workflow kind has no governed receipt mapping")
)

type Config struct {
	Observers              *reconciliation.ObserverSet
	Quorum                 int
	FinalizedConfirmations uint64
	FromBlock              uint64
	CallEscrowContract     string
	SpendModuleContract    string
	DirectoryContract      string
}

// ValidateGovernanceAction is the proposal-time allowlist boundary. It uses
// the same immutable chain, contracts, kinds, and selectors as receipt
// discovery so an unobservable or arbitrary target never reaches approval.
func (o *Observer) ValidateGovernanceAction(bound governanceworkflow.BoundAction) error {
	if o == nil || o.observers == nil || bound.ChainID != o.observers.ChainID() {
		return ErrUnsupportedWorkflow
	}
	rules, err := o.rules(ascpworkflow.Kind(bound.WorkflowKind))
	if err != nil {
		return err
	}
	for _, rule := range rules {
		if rule.Contract == bound.ContractAddress && rule.FunctionSelector == bound.FunctionSelector {
			return nil
		}
	}
	return ErrUnsupportedWorkflow
}

type Observer struct {
	observers              *reconciliation.ObserverSet
	quorum                 int
	finalizedConfirmations uint64
	fromBlock              uint64
	callEscrow             string
	spendModule            string
	directory              string
}

func New(config Config) (*Observer, error) {
	if config.Observers == nil || config.Quorum < 2 || config.Quorum > 5 || config.FinalizedConfirmations == 0 ||
		config.FromBlock == 0 || !address(config.CallEscrowContract) || !address(config.SpendModuleContract) ||
		!address(config.DirectoryContract) || config.CallEscrowContract == config.SpendModuleContract ||
		config.CallEscrowContract == config.DirectoryContract || config.SpendModuleContract == config.DirectoryContract {
		return nil, ErrInvalidConfiguration
	}
	return &Observer{
		observers: config.Observers, quorum: config.Quorum, finalizedConfirmations: config.FinalizedConfirmations,
		fromBlock: config.FromBlock, callEscrow: config.CallEscrowContract,
		spendModule: config.SpendModuleContract, directory: config.DirectoryContract,
	}, nil
}

func (o *Observer) ObserveWorkflowCompletion(ctx context.Context, workflow ascpworkflow.Workflow) (ascpworkflow.CompletionReceipt, error) {
	if !observableWorkflow(workflow) || workflow.ApprovedAt <= 0 {
		return ascpworkflow.CompletionReceipt{}, ErrReceiptRejected
	}
	rules, err := o.rulesForWorkflow(workflow)
	if err != nil {
		return ascpworkflow.CompletionReceipt{}, err
	}
	expected := reconciliation.GovernanceExpectedReceipt{
		WorkflowID: workflow.WorkflowID, PayloadHash: workflow.PayloadHash, ApprovedAt: uint64(workflow.ApprovedAt),
		FromBlock: o.fromBlock, Rules: rules,
	}
	result := o.observers.GovernanceReceiptQuorum(ctx, expected)
	// A quorum that validates one receipt cannot overrule a separate quorum
	// that deterministically rejects it. That split has no safe canonical
	// outcome even though only one side can return structured evidence.
	if len(result.Evidence) >= o.quorum && len(result.InvalidProviders) >= o.quorum {
		return ascpworkflow.CompletionReceipt{}, ErrObserverDisagreement
	}
	if len(result.Evidence) < o.quorum {
		if len(result.InvalidProviders) >= o.quorum {
			return ascpworkflow.CompletionReceipt{}, fmt.Errorf("%w: %d provider(s)", ErrReceiptRejected, len(result.InvalidProviders))
		}
		if len(result.Failures) > 0 && len(result.PendingProviders) == len(result.Failures) {
			return ascpworkflow.CompletionReceipt{}, ErrReceiptPending
		}
		return ascpworkflow.CompletionReceipt{}, fmt.Errorf("%w: got %d, need %d", ErrQuorumUnavailable, len(result.Evidence), o.quorum)
	}
	agreeing, ok := agreeingQuorum(result.Evidence, o.quorum)
	if !ok {
		return ascpworkflow.CompletionReceipt{}, ErrObserverDisagreement
	}
	canonical := agreeing[0]
	providers := make([]string, 0, len(agreeing))
	seen := make(map[string]struct{}, len(agreeing))
	confirmedHead := canonical.ConfirmedHead
	finalizedHead := canonical.FinalizedHead
	for _, evidence := range agreeing {
		if _, duplicate := seen[evidence.Provider]; duplicate {
			return ascpworkflow.CompletionReceipt{}, ErrObserverDisagreement
		}
		seen[evidence.Provider] = struct{}{}
		providers = append(providers, evidence.Provider)
		if evidence.ConfirmedHead < confirmedHead {
			confirmedHead = evidence.ConfirmedHead
		}
		if evidence.FinalizedHead < finalizedHead {
			finalizedHead = evidence.FinalizedHead
		}
	}
	if canonical.BlockNumber == 0 || confirmedHead < canonical.BlockNumber ||
		finalizedHead < canonical.BlockNumber || confirmedHead-canonical.BlockNumber+1 < o.finalizedConfirmations {
		return ascpworkflow.CompletionReceipt{}, ErrUnsafeFinality
	}
	receipt := ascpworkflow.CompletionReceipt{
		WorkflowID: canonical.WorkflowID, PayloadHash: canonical.PayloadHash, ChainID: canonical.ChainID,
		TransactionHash: canonical.TransactionHash, BlockNumber: canonical.BlockNumber, BlockHash: canonical.BlockHash,
		BlockTimestamp: canonical.BlockTimestamp, ConfirmedHead: confirmedHead, FinalizedHead: finalizedHead,
		LogIndex: canonical.BindingLogIndex, ContractAddress: canonical.ContractAddress,
		EventSignature: ascpworkflow.GovernanceWorkflowBoundTopic, FunctionSelector: canonical.FunctionSelector,
		ActionEventSignature: canonical.ActionEventSignature, ActionLogIndexes: slices.Clone(canonical.ActionLogIndexes),
		Observers: slices.Clone(providers), Finality: "FINALIZED",
	}
	receipt.EvidenceDigest = evidenceDigest(receipt)
	return receipt, nil
}

func observableWorkflow(workflow ascpworkflow.Workflow) bool {
	switch workflow.State {
	case ascpworkflow.ApprovedPendingChain, ascpworkflow.Submitted, ascpworkflow.Confirmed,
		ascpworkflow.Reverted, ascpworkflow.Reorged, ascpworkflow.TimedOut, ascpworkflow.RequiresReapproval:
		if workflow.State != ascpworkflow.ApprovedPendingChain && workflow.State != ascpworkflow.Submitted &&
			workflow.State != ascpworkflow.Confirmed && workflow.SubmissionTxHash == "" {
			return false
		}
		return true
	default:
		return false
	}
}

func (o *Observer) rulesForWorkflow(workflow ascpworkflow.Workflow) ([]reconciliation.GovernanceRule, error) {
	if (workflow.ChainID != 8453 && workflow.ChainID != 84532) || !address(workflow.ContractAddress) ||
		!executableCalldata(workflow.Calldata, workflow.FunctionSelector) ||
		len(workflow.GovernanceAction) == 0 || !json.Valid(workflow.GovernanceAction) {
		return nil, ErrReceiptRejected
	}
	bound, err := governanceworkflow.RebindAction(workflow.WorkflowID, workflow.GovernanceAction)
	if err != nil || bound.WorkflowKind != string(workflow.Kind) || bound.PayloadHash != workflow.PayloadHash ||
		bound.ChainID != workflow.ChainID || bound.ContractAddress != workflow.ContractAddress ||
		bound.FunctionSelector != workflow.FunctionSelector || bound.Calldata != workflow.Calldata {
		return nil, ErrReceiptRejected
	}
	var action governanceworkflow.Action
	if err := json.Unmarshal(bound.CanonicalAction, &action); err != nil {
		return nil, ErrReceiptRejected
	}
	candidates, err := o.rules(workflow.Kind)
	if err != nil {
		return nil, err
	}
	matched := make([]reconciliation.GovernanceRule, 0, 1)
	for _, candidate := range candidates {
		if candidate.Contract == workflow.ContractAddress && candidate.FunctionSelector == workflow.FunctionSelector {
			if action.Type == governanceworkflow.ActionSpendInvalidateNonces {
				candidate.ExpectedActionEvents = len(action.SpendInvalidateNonces.Nonces)
			}
			matched = append(matched, candidate)
		}
	}
	if len(matched) != 1 {
		return nil, ErrReceiptRejected
	}
	return matched, nil
}

func executableCalldata(value, selector string) bool {
	if len(selector) != 10 || len(value) <= 10 || len(value) > 131082 || len(value)%2 != 0 ||
		value != strings.ToLower(value) || !strings.HasPrefix(value, selector) {
		return false
	}
	decoded, err := hex.DecodeString(value[2:])
	return err == nil && len(decoded) > 4
}

// agreeingQuorum accepts one and only one canonical evidence group meeting the
// configured quorum. A dissenting provider cannot veto a valid quorum, while
// two independently qualifying groups remain ambiguous and fail closed.
func agreeingQuorum(evidence []reconciliation.GovernanceReceiptEvidence, quorum int) ([]reconciliation.GovernanceReceiptEvidence, bool) {
	groups := make([][]reconciliation.GovernanceReceiptEvidence, 0, len(evidence))
	for _, item := range evidence {
		matched := false
		for index := range groups {
			if sameEvidence(groups[index][0], item) {
				groups[index] = append(groups[index], item)
				matched = true
				break
			}
		}
		if !matched {
			groups = append(groups, []reconciliation.GovernanceReceiptEvidence{item})
		}
	}
	var selected []reconciliation.GovernanceReceiptEvidence
	for _, group := range groups {
		if len(group) < quorum {
			continue
		}
		if selected != nil {
			return nil, false
		}
		selected = group
	}
	return selected, len(selected) >= quorum
}

func (o *Observer) rules(kind ascpworkflow.Kind) ([]reconciliation.GovernanceRule, error) {
	rule := func(contract, function, event string, multiple bool) reconciliation.GovernanceRule {
		return reconciliation.GovernanceRule{
			Contract: contract, FunctionSelector: functionSelector(function),
			ActionEventSignature: eventTopic(event), MultipleActionEvents: multiple, ExpectedActionEvents: 1,
		}
	}
	switch kind {
	case ascpworkflow.PayoutChange:
		return []reconciliation.GovernanceRule{rule(o.directory, "approveVersion(uint64,bytes32)", "VersionApproved(bytes32,uint64,address,uint64,uint64)", false)}, nil
	case ascpworkflow.SignerCaps:
		return []reconciliation.GovernanceRule{rule(o.spendModule, "scheduleCaps((uint256,uint256,uint256),bytes32,bytes32)", "CapsScheduled(uint256,uint256,uint256,uint64)", false)}, nil
	case ascpworkflow.VerifierGovernance:
		return []reconciliation.GovernanceRule{
			rule(o.callEscrow, "addVerifier(address,uint64,bytes32,bytes32)", "VerifierAdded(address,uint64,uint64)", false),
			rule(o.callEscrow, "revokeVerifier(address,bytes32,bytes32)", "VerifierRevoked(address,uint64,uint64)", false),
		}, nil
	case ascpworkflow.BreakGlass:
		return []reconciliation.GovernanceRule{
			rule(o.callEscrow, "setEmergencyPause(bytes32,bytes32)", "EmergencyPauseSet()", false),
			rule(o.spendModule, "setEmergencyPause(bool,bytes32,bytes32)", "EmergencyPauseSet(bool)", false),
		}, nil
	case ascpworkflow.ModuleGovernance:
		return []reconciliation.GovernanceRule{
			rule(o.spendModule, "setSpendAuthorizer(address,bytes32,bytes32)", "SpendAuthorizerSet(address,uint64)", false),
			rule(o.spendModule, "setEscrowAllowlist(address,bytes32,bytes32,bytes32)", "EscrowAllowlistSet(address,bytes32)", false),
			rule(o.spendModule, "invalidateNonces(bytes32[],bytes32,bytes32)", "NonceInvalidated(bytes32)", true),
		}, nil
	case ascpworkflow.DirectoryCancel:
		return []reconciliation.GovernanceRule{rule(o.directory, "cancelVersion(uint64,bytes32,bytes32,bytes32)", "VersionCancelled(bytes32,uint64,address)", false)}, nil
	default:
		return nil, ErrUnsupportedWorkflow
	}
}

func sameEvidence(left, right reconciliation.GovernanceReceiptEvidence) bool {
	return left.ChainID == right.ChainID && left.WorkflowID == right.WorkflowID && left.PayloadHash == right.PayloadHash &&
		left.TransactionHash == right.TransactionHash && left.BlockNumber == right.BlockNumber && left.BlockHash == right.BlockHash &&
		left.BlockTimestamp == right.BlockTimestamp &&
		left.BindingLogIndex == right.BindingLogIndex && left.ContractAddress == right.ContractAddress &&
		left.FunctionSelector == right.FunctionSelector && left.ActionEventSignature == right.ActionEventSignature &&
		slices.Equal(left.ActionLogIndexes, right.ActionLogIndexes)
}

func evidenceDigest(receipt ascpworkflow.CompletionReceipt) string {
	copy := receipt
	copy.EvidenceDigest = ""
	encoded, _ := json.Marshal(copy)
	digest := sha256.Sum256(append([]byte("ASCP_GOVERNANCE_RECEIPT_V1\n"), encoded...))
	return "0x" + hex.EncodeToString(digest[:])
}

func functionSelector(signature string) string {
	return "0x" + hex.EncodeToString(crypto.Keccak256([]byte(signature))[:4])
}
func eventTopic(signature string) string { return crypto.Keccak256Hash([]byte(signature)).Hex() }

func address(value string) bool {
	if len(value) != 42 || !strings.HasPrefix(value, "0x") || value != strings.ToLower(value) {
		return false
	}
	decoded, err := hex.DecodeString(value[2:])
	if err != nil || len(decoded) != 20 {
		return false
	}
	for _, item := range decoded {
		if item != 0 {
			return true
		}
	}
	return false
}
