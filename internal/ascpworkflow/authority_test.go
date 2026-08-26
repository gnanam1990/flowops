package ascpworkflow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"reflect"
	"testing"

	"github.com/gnanam1990/flowops/pkg/governanceworkflow"
)

func TestAuthorityVerifierBindsDualControlPrincipalRelayerAndReceiptQuorum(t *testing.T) {
	rule := authorityRuleFixture()
	verifier, err := NewAuthorityVerifier([]AuthorityRule{rule}, 2)
	if err != nil {
		t.Fatal(err)
	}
	workflow := Workflow{
		WorkflowID: testHash(40), OrganizationID: "org_a", Kind: SignerCaps, ChainAction: ActionSpendScheduleCaps,
		PayloadHash: testHash(41), ProposedBy: "signer_a", ProposerRole: SignerOperator,
		ApprovedBy: "owner_b", ApproverRole: OrgAdmin, State: ApprovedPendingChain,
	}
	receipt := authorityReceiptFixture(workflow, rule)
	if err := verifier.VerifyWorkflowCompletion(context.Background(), workflow, receipt); err != nil {
		t.Fatal(err)
	}

	mutations := map[string]func(*Workflow, *CompletionReceipt){
		"single principal": func(workflow *Workflow, _ *CompletionReceipt) { workflow.ApprovedBy = workflow.ProposedBy },
		"wrong principal": func(_ *Workflow, receipt *CompletionReceipt) {
			receipt.AuthorityProof[0].OnChainPrincipal = "0x9999999999999999999999999999999999999999"
		},
		"wrong relayer": func(_ *Workflow, receipt *CompletionReceipt) {
			receipt.AuthorityProof[0].Relayer = "0x9999999999999999999999999999999999999999"
		},
		"provider disagreement": func(_ *Workflow, receipt *CompletionReceipt) { receipt.AuthorityProof[1].BlockHash = testHash(99) },
		"provider reuse": func(_ *Workflow, receipt *CompletionReceipt) {
			receipt.AuthorityProof[1].Provider = receipt.AuthorityProof[0].Provider
		},
		"short timelock": func(_ *Workflow, receipt *CompletionReceipt) {
			receipt.AuthorityProof[0].ObservedTimelockSeconds = 3599
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			changedWorkflow := workflow
			changedReceipt := receipt
			changedReceipt.AuthorityProof = append([]AuthorityObservation(nil), receipt.AuthorityProof...)
			mutate(&changedWorkflow, &changedReceipt)
			if err := verifier.VerifyWorkflowCompletion(context.Background(), changedWorkflow, changedReceipt); !errors.Is(err, ErrAuthorityProof) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestAuthorityVerifierRejectsUnmappedCreateAction(t *testing.T) {
	verifier, err := NewAuthorityVerifier([]AuthorityRule{authorityRuleFixture()}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifier.ValidateWorkflow(SignerCaps, ActionSpendScheduleCaps, testHash(1)); err != nil {
		t.Fatal(err)
	}
	if err := verifier.ValidateWorkflow(ModuleGovernance, ActionSpendPause, testHash(1)); !errors.Is(err, ErrAuthorityProof) {
		t.Fatalf("error=%v", err)
	}
}

func TestAuthorityRuleRejectsConfiguredABISubstitution(t *testing.T) {
	rule := authorityRuleFixture()
	rule.FunctionSelector = "0x12345678"
	if _, err := NewAuthorityVerifier([]AuthorityRule{rule}, 2); !errors.Is(err, ErrAuthorityProof) {
		t.Fatalf("selector substitution error=%v", err)
	}
	rule = authorityRuleFixture()
	rule.ActionEventSignature = testHash(99)
	if _, err := NewAuthorityVerifier([]AuthorityRule{rule}, 2); !errors.Is(err, ErrAuthorityProof) {
		t.Fatalf("event substitution error=%v", err)
	}
}

func TestDisabledSafeOwnerSwapCannotBeInstalledAsOwnerAPIRule(t *testing.T) {
	functionSelector, actionEvent, secondaryEvent, workflowEvent, _ := expectedAuthoritySurface(ActionSafeSwapOwner)
	rule := AuthorityRule{
		Action: ActionSafeSwapOwner, Kind: BreakGlass, ChainID: 84532,
		ContractAddress: "0x1111111111111111111111111111111111111111", ContractCodeHash: testHash(50),
		OnChainPrincipal: "0x2222222222222222222222222222222222222222",
		ProposerRole:     OrgAdmin, ApproverRole: IncidentResponder, WorkflowQuorum: 2,
		RelayerMode: RelayerAny, FunctionSelector: functionSelector, ActionEventSignature: actionEvent,
		SecondaryActionEventSignature: secondaryEvent, WorkflowEventSignature: workflowEvent,
		MinimumTimelockSeconds: 3600, EmergencyPath: "Safe owner recovery procedure",
	}
	if secondaryEvent == "" || workflowEvent != "" {
		t.Fatalf("Safe swap surface lost its two-event contract: secondary=%s workflow=%s", secondaryEvent, workflowEvent)
	}
	if _, err := NewAuthorityVerifier([]AuthorityRule{rule}, 2); !errors.Is(err, ErrAuthorityProof) {
		t.Fatalf("disabled Safe action was installable: %v", err)
	}
}

func TestOwnerChainActionInventoryIsCompleteAndFailClosed(t *testing.T) {
	inventory := OwnerChainActionInventory()
	if len(inventory) != 26 {
		t.Fatalf("inventory entries=%d want=26", len(inventory))
	}
	enabledTypes := map[governanceworkflow.ActionType]ChainAction{
		governanceworkflow.ActionCallEscrowAddVerifier:    ActionCallEscrowAddVerifier,
		governanceworkflow.ActionCallEscrowRevokeVerifier: ActionCallEscrowRevokeVerifier,
		governanceworkflow.ActionCallEscrowPause:          ActionCallEscrowPause,
		governanceworkflow.ActionDirectoryApprove:         ActionDirectoryPublish,
		governanceworkflow.ActionDirectoryCancel:          ActionDirectoryCancel,
		governanceworkflow.ActionSpendAuthorizer:          ActionSpendSetAuthorizer,
		governanceworkflow.ActionSpendAllowlist:           ActionSpendSetAllowlist,
		governanceworkflow.ActionSpendCaps:                ActionSpendScheduleCaps,
		governanceworkflow.ActionSpendPause:               ActionSpendPause,
		governanceworkflow.ActionSpendInvalidateNonces:    ActionSpendInvalidateNonces,
	}
	seenActions := make(map[ChainAction]struct{}, len(inventory))
	seenTypes := make(map[governanceworkflow.ActionType]struct{}, len(enabledTypes))
	for _, entry := range inventory {
		if _, duplicate := seenActions[entry.Action]; duplicate || !validChainAction(entry.Action) {
			t.Fatalf("duplicate or invalid inventory action %q", entry.Action)
		}
		seenActions[entry.Action] = struct{}{}
		rule := authorityRuleForAction(entry.Action)
		if entry.OwnerAPIEnabled {
			wantAction, ok := enabledTypes[entry.ActionType]
			if !ok || wantAction != entry.Action || entry.DisabledReason != "" || entry.OwnerAPIPath == "" ||
				entry.Approval == "" || entry.Execution == "" || entry.Receipt == "" || entry.Audit == "" {
				t.Fatalf("invalid enabled inventory entry %+v", entry)
			}
			if _, duplicate := seenTypes[entry.ActionType]; duplicate {
				t.Fatalf("duplicate enabled action type %q", entry.ActionType)
			}
			seenTypes[entry.ActionType] = struct{}{}
			verifier, err := NewAuthorityVerifier([]AuthorityRule{rule}, 2)
			if err != nil {
				t.Fatalf("enabled action %s rejected: %v", entry.Action, err)
			}
			workflow := Workflow{
				WorkflowID: testHash(70), OrganizationID: "org_a", Kind: rule.Kind, ChainAction: entry.Action,
				PayloadHash: testHash(71), ProposedBy: "principal_a", ProposerRole: rule.ProposerRole,
				ApprovedBy: "principal_b", ApproverRole: rule.ApproverRole, State: ApprovedPendingChain,
			}
			receipt := authorityReceiptFixture(workflow, rule)
			if err := verifier.VerifyWorkflowCompletion(context.Background(), workflow, receipt); err != nil {
				t.Fatalf("enabled action %s completion rejected: %v", entry.Action, err)
			}
			workflow.ApprovedBy = workflow.ProposedBy
			if err := verifier.VerifyWorkflowCompletion(context.Background(), workflow, receipt); !errors.Is(err, ErrAuthorityProof) {
				t.Fatalf("enabled action %s allowed one principal: %v", entry.Action, err)
			}
			continue
		}
		if entry.ActionType != "" || entry.DisabledReason == "" || entry.OwnerAPIPath != "" || entry.Approval != "" ||
			entry.Execution != "" || entry.Receipt != "" || entry.Audit != "" {
			t.Fatalf("disabled entry lacks a closed reason: %+v", entry)
		}
		if _, err := NewAuthorityVerifier([]AuthorityRule{rule}, 2); !errors.Is(err, ErrAuthorityProof) {
			t.Fatalf("disabled action %s was installable: %v", entry.Action, err)
		}
	}
	if len(seenTypes) != len(enabledTypes) {
		t.Fatalf("enabled action types=%d want=%d", len(seenTypes), len(enabledTypes))
	}
}

func TestOwnerChainActionEvidenceManifestMatchesExecutableInventory(t *testing.T) {
	raw, err := os.ReadFile("../../docs/evidence/AC66_OWNER_CHAIN_API_INVENTORY_2026-08-26.json")
	if err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var manifest struct {
		SchemaVersion       int                              `json:"schemaVersion"`
		AcceptanceCriterion string                           `json:"acceptanceCriterion"`
		Entries             []OwnerChainActionInventoryEntry `json:"entries"`
	}
	if err := decoder.Decode(&manifest); err != nil {
		t.Fatal(err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		t.Fatalf("trailing manifest value: %v", err)
	}
	if manifest.SchemaVersion != 1 || manifest.AcceptanceCriterion != "AC-66" ||
		!reflect.DeepEqual(manifest.Entries, OwnerChainActionInventory()) {
		t.Fatalf("evidence manifest drifted from executable inventory")
	}
}

func authorityRuleForAction(action ChainAction) AuthorityRule {
	kind, proposer, approver, _ := expectedAuthorityWorkflow(action)
	functionSelector, actionEvent, secondaryEvent, workflowEvent, _ := expectedAuthoritySurface(action)
	return AuthorityRule{
		Action: action, Kind: kind, ChainID: 84532,
		ContractAddress: "0x1111111111111111111111111111111111111111", ContractCodeHash: testHash(72),
		OnChainPrincipal: "0x2222222222222222222222222222222222222222",
		ProposerRole:     proposer, ApproverRole: approver, WorkflowQuorum: 2,
		RelayerMode: RelayerExact, Relayer: "0x3333333333333333333333333333333333333333",
		FunctionSelector: functionSelector, ActionEventSignature: actionEvent,
		SecondaryActionEventSignature: secondaryEvent, WorkflowEventSignature: workflowEvent,
		MinimumTimelockSeconds: 3600, EmergencyPath: "deployment-owned recovery runbook",
	}
}

func authorityRuleFixture() AuthorityRule {
	functionSelector, actionEvent, secondaryEvent, workflowEvent, _ :=
		expectedAuthoritySurface(ActionSpendScheduleCaps)
	return AuthorityRule{
		Action: ActionSpendScheduleCaps, Kind: SignerCaps, ChainID: 84532,
		ContractAddress: "0x1111111111111111111111111111111111111111", ContractCodeHash: testHash(30),
		OnChainPrincipal: "0x2222222222222222222222222222222222222222",
		ProposerRole:     SignerOperator, ApproverRole: OrgAdmin, WorkflowQuorum: 2,
		RelayerMode: RelayerExact, Relayer: "0x3333333333333333333333333333333333333333",
		FunctionSelector: functionSelector, ActionEventSignature: actionEvent,
		SecondaryActionEventSignature: secondaryEvent, WorkflowEventSignature: workflowEvent,
		MinimumTimelockSeconds: 3600, EmergencyPath: "operational Safe emergency pause",
	}
}

func authorityReceiptFixture(workflow Workflow, rule AuthorityRule) CompletionReceipt {
	actionIndexes := []uint64{1}
	workflowLogIndex := uint64(2)
	if rule.SecondaryActionEventSignature != "" {
		actionIndexes = append(actionIndexes, 2)
		workflowLogIndex = 3
	}
	observation := AuthorityObservation{
		Provider: "rpc_alpha", ChainID: rule.ChainID, TransactionHash: testHash(42), BlockNumber: 100, BlockHash: testHash(43),
		ContractAddress: rule.ContractAddress, ContractCodeHash: rule.ContractCodeHash,
		OnChainPrincipal: rule.OnChainPrincipal, Relayer: rule.Relayer, FunctionSelector: rule.FunctionSelector,
		ActionEventSignature:          rule.ActionEventSignature,
		SecondaryActionEventSignature: rule.SecondaryActionEventSignature,
		WorkflowEventSignature:        rule.WorkflowEventSignature,
		WorkflowID:                    workflow.WorkflowID, PayloadHash: workflow.PayloadHash, ActionLogIndex: 1,
		WorkflowLogIndex:        workflowLogIndex,
		ObservedTimelockSeconds: rule.MinimumTimelockSeconds, Finality: "FINALIZED",
	}
	second := observation
	second.Provider = "rpc_beta"
	return CompletionReceipt{
		WorkflowID: workflow.WorkflowID, PayloadHash: workflow.PayloadHash, ChainAction: workflow.ChainAction,
		ChainID: rule.ChainID, TransactionHash: observation.TransactionHash, BlockNumber: observation.BlockNumber,
		BlockHash: observation.BlockHash, BlockTimestamp: 101, ConfirmedHead: 100, FinalizedHead: 100,
		LogIndex: workflowLogIndex, ContractAddress: rule.ContractAddress,
		EventSignature: GovernanceWorkflowBoundTopic, FunctionSelector: rule.FunctionSelector,
		ActionEventSignature: rule.ActionEventSignature, ActionLogIndexes: actionIndexes,
		Observers: []string{"rpc_alpha", "rpc_beta"}, EvidenceDigest: testHash(60), Finality: "FINALIZED",
		AuthorityProof: []AuthorityObservation{observation, second},
	}
}
