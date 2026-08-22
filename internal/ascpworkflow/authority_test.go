package ascpworkflow

import (
	"context"
	"errors"
	"testing"
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

func TestSafeOwnerSwapRequiresRemovedAndAddedOwnerEvents(t *testing.T) {
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
	verifier, err := NewAuthorityVerifier([]AuthorityRule{rule}, 2)
	if err != nil {
		t.Fatal(err)
	}
	workflow := Workflow{
		WorkflowID: testHash(51), OrganizationID: "org_a", Kind: BreakGlass, ChainAction: ActionSafeSwapOwner,
		PayloadHash: testHash(52), ProposedBy: "owner_a", ProposerRole: OrgAdmin,
		ApprovedBy: "responder_b", ApproverRole: IncidentResponder, State: ApprovedPendingChain,
	}
	receipt := authorityReceiptFixture(workflow, rule)
	for index := range receipt.AuthorityProof {
		receipt.AuthorityProof[index].Relayer = "0x3333333333333333333333333333333333333333"
		receipt.AuthorityProof[index].SecondaryActionLogIndex = 2
	}
	if err := verifier.VerifyWorkflowCompletion(context.Background(), workflow, receipt); err != nil {
		t.Fatal(err)
	}
	receipt.AuthorityProof[0].SecondaryActionEventSignature = ""
	if err := verifier.VerifyWorkflowCompletion(context.Background(), workflow, receipt); !errors.Is(err, ErrAuthorityProof) {
		t.Fatalf("missing AddedOwner error=%v", err)
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
