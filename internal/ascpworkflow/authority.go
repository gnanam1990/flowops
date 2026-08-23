package ascpworkflow

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/gnanam1990/flowops/pkg/governanceworkflow"
)

var ErrAuthorityProof = errors.New("workflow chain authority proof is invalid")

type ChainAction string

const (
	ActionCallEscrowAddVerifier     ChainAction = "CALL_ESCROW_ADD_VERIFIER"
	ActionCallEscrowRevokeVerifier  ChainAction = "CALL_ESCROW_REVOKE_VERIFIER"
	ActionCallEscrowPause           ChainAction = "CALL_ESCROW_PAUSE"
	ActionDirectoryPublish          ChainAction = "DIRECTORY_PUBLISH"
	ActionDirectoryCancel           ChainAction = "DIRECTORY_CANCEL"
	ActionDirectorySetPublisher     ChainAction = "DIRECTORY_SET_PUBLISHER"
	ActionDirectorySetPauser        ChainAction = "DIRECTORY_SET_PAUSER"
	ActionDirectoryPauseSeller      ChainAction = "DIRECTORY_PAUSE_SELLER"
	ActionDirectoryUnpauseSeller    ChainAction = "DIRECTORY_UNPAUSE_SELLER"
	ActionDirectoryRevokeQuoteKey   ChainAction = "DIRECTORY_REVOKE_QUOTE_KEY"
	ActionDirectoryUnrevokeQuoteKey ChainAction = "DIRECTORY_UNREVOKE_QUOTE_KEY"
	ActionAgentRegister             ChainAction = "AGENT_REGISTER"
	ActionAgentUpdatePolicy         ChainAction = "AGENT_UPDATE_POLICY"
	ActionAgentSetStatus            ChainAction = "AGENT_SET_STATUS"
	ActionAgentSetRegistryAdmin     ChainAction = "AGENT_SET_REGISTRY_ADMIN"
	ActionSpendSetAuthorizer        ChainAction = "SPEND_SET_AUTHORIZER"
	ActionSpendSetAllowlist         ChainAction = "SPEND_SET_ALLOWLIST"
	ActionSpendScheduleCaps         ChainAction = "SPEND_SCHEDULE_CAPS"
	ActionSpendPause                ChainAction = "SPEND_PAUSE"
	ActionSpendInvalidateNonces     ChainAction = "SPEND_INVALIDATE_NONCES"
	ActionSafeEnableModule          ChainAction = "SAFE_ENABLE_MODULE"
	ActionSafeDisableModule         ChainAction = "SAFE_DISABLE_MODULE"
	ActionSafeAddOwner              ChainAction = "SAFE_ADD_OWNER_WITH_THRESHOLD"
	ActionSafeRemoveOwner           ChainAction = "SAFE_REMOVE_OWNER"
	ActionSafeSwapOwner             ChainAction = "SAFE_SWAP_OWNER"
	ActionSafeChangeThreshold       ChainAction = "SAFE_CHANGE_THRESHOLD"
)

type RelayerMode string

const (
	RelayerExact RelayerMode = "EXACT"
	RelayerAny   RelayerMode = "ANY"
)

// AuthorityRule is deployment-owned configuration, never owner-request input.
// It names the exact contract build, on-chain principal, workflow quorum,
// relayer policy, selector, and same-receipt events for one chain mutation.
type AuthorityRule struct {
	Action                        ChainAction `json:"action"`
	Kind                          Kind        `json:"kind"`
	ChainID                       uint64      `json:"chainId"`
	ContractAddress               string      `json:"contractAddress"`
	ContractCodeHash              string      `json:"contractCodeHash"`
	OnChainPrincipal              string      `json:"onChainPrincipal"`
	ProposerRole                  Role        `json:"proposerRole"`
	ApproverRole                  Role        `json:"approverRole"`
	WorkflowQuorum                uint8       `json:"workflowQuorum"`
	RelayerMode                   RelayerMode `json:"relayerMode"`
	Relayer                       string      `json:"relayer,omitempty"`
	FunctionSelector              string      `json:"functionSelector"`
	ActionEventSignature          string      `json:"actionEventSignature"`
	SecondaryActionEventSignature string      `json:"secondaryActionEventSignature,omitempty"`
	WorkflowEventSignature        string      `json:"workflowEventSignature,omitempty"`
	MinimumTimelockSeconds        uint64      `json:"minimumTimelockSeconds"`
	EmergencyPath                 string      `json:"emergencyPath"`
}

// AuthorityObservation is the normalized output of one independent finalized
// receipt observer. Provider credentials and raw RPC material remain outside
// the Owner API; only exact verified facts cross this boundary.
type AuthorityObservation struct {
	Provider                      string `json:"provider"`
	ChainID                       uint64 `json:"chainId"`
	TransactionHash               string `json:"transactionHash"`
	BlockNumber                   uint64 `json:"blockNumber"`
	BlockHash                     string `json:"blockHash"`
	ContractAddress               string `json:"contractAddress"`
	ContractCodeHash              string `json:"contractCodeHash"`
	OnChainPrincipal              string `json:"onChainPrincipal"`
	Relayer                       string `json:"relayer"`
	FunctionSelector              string `json:"functionSelector"`
	ActionEventSignature          string `json:"actionEventSignature"`
	SecondaryActionEventSignature string `json:"secondaryActionEventSignature,omitempty"`
	WorkflowEventSignature        string `json:"workflowEventSignature,omitempty"`
	WorkflowID                    string `json:"workflowId"`
	PayloadHash                   string `json:"payloadHash"`
	ActionLogIndex                uint64 `json:"actionLogIndex"`
	SecondaryActionLogIndex       uint64 `json:"secondaryActionLogIndex,omitempty"`
	WorkflowLogIndex              uint64 `json:"workflowLogIndex"`
	ObservedTimelockSeconds       uint64 `json:"observedTimelockSeconds"`
	Finality                      string `json:"finality"`
}

type AuthorityVerifier struct {
	rules          map[ChainAction]AuthorityRule
	providerQuorum int
}

func ParseAuthorityRules(raw string) ([]AuthorityRule, error) {
	if strings.TrimSpace(raw) == "" || len(raw) > 256*1024 {
		return nil, ErrAuthorityProof
	}
	decoder := json.NewDecoder(bytes.NewBufferString(raw))
	decoder.DisallowUnknownFields()
	var rules []AuthorityRule
	if err := decoder.Decode(&rules); err != nil {
		return nil, fmt.Errorf("%w: decode authority rules: %v", ErrAuthorityProof, err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, ErrAuthorityProof
	}
	return rules, nil
}

func NewAuthorityVerifier(rules []AuthorityRule, providerQuorum int) (*AuthorityVerifier, error) {
	if providerQuorum < 2 || providerQuorum > 5 || len(rules) == 0 {
		return nil, ErrAuthorityProof
	}
	indexed := make(map[ChainAction]AuthorityRule, len(rules))
	for _, rule := range rules {
		if !validAuthorityRule(rule) {
			return nil, fmt.Errorf("%w: invalid rule for %s", ErrAuthorityProof, rule.Action)
		}
		if _, duplicate := indexed[rule.Action]; duplicate {
			return nil, fmt.Errorf("%w: duplicate rule for %s", ErrAuthorityProof, rule.Action)
		}
		indexed[rule.Action] = rule
	}
	return &AuthorityVerifier{rules: indexed, providerQuorum: providerQuorum}, nil
}

func (v *AuthorityVerifier) ValidateWorkflow(kind Kind, action ChainAction, payloadHash string) error {
	rule, ok := v.rules[action]
	if !ok || rule.Kind != kind || !hash(payloadHash) {
		return ErrAuthorityProof
	}
	return nil
}

func (v *AuthorityVerifier) ValidateGovernanceAction(bound governanceworkflow.BoundAction) error {
	action, err := chainActionForBound(bound)
	if err != nil {
		return ErrAuthorityProof
	}
	rule, ok := v.rules[action]
	if !ok || rule.Kind != Kind(bound.WorkflowKind) || rule.ChainID != bound.ChainID ||
		rule.ContractAddress != bound.ContractAddress || rule.FunctionSelector != bound.FunctionSelector ||
		!hash(bound.PayloadHash) {
		return ErrAuthorityProof
	}
	return nil
}

func chainActionForBound(bound governanceworkflow.BoundAction) (ChainAction, error) {
	var action struct {
		Type governanceworkflow.ActionType `json:"type"`
	}
	if err := json.Unmarshal(bound.CanonicalAction, &action); err != nil {
		return "", ErrAuthorityProof
	}
	switch action.Type {
	case governanceworkflow.ActionCallEscrowAddVerifier:
		return ActionCallEscrowAddVerifier, nil
	case governanceworkflow.ActionCallEscrowRevokeVerifier:
		return ActionCallEscrowRevokeVerifier, nil
	case governanceworkflow.ActionCallEscrowPause:
		return ActionCallEscrowPause, nil
	case governanceworkflow.ActionSpendAuthorizer:
		return ActionSpendSetAuthorizer, nil
	case governanceworkflow.ActionSpendAllowlist:
		return ActionSpendSetAllowlist, nil
	case governanceworkflow.ActionSpendCaps:
		return ActionSpendScheduleCaps, nil
	case governanceworkflow.ActionSpendPause:
		return ActionSpendPause, nil
	case governanceworkflow.ActionSpendInvalidateNonces:
		return ActionSpendInvalidateNonces, nil
	case governanceworkflow.ActionDirectoryApprove:
		return ActionDirectoryPublish, nil
	case governanceworkflow.ActionDirectoryCancel:
		return ActionDirectoryCancel, nil
	default:
		return "", ErrAuthorityProof
	}
}

func (v *AuthorityVerifier) VerifyWorkflowCompletion(_ context.Context, workflow Workflow, receipt CompletionReceipt) error {
	rule, ok := v.rules[workflow.ChainAction]
	if !validReceipt(receipt) || !ok || receipt.ChainAction != workflow.ChainAction || rule.Kind != workflow.Kind ||
		workflow.PayloadHash != receipt.PayloadHash || workflow.WorkflowID != receipt.WorkflowID ||
		!completionCandidateState(workflow.State) || workflow.ProposerRole != rule.ProposerRole ||
		workflow.ApproverRole != rule.ApproverRole || workflow.ProposedBy == workflow.ApprovedBy ||
		rule.WorkflowQuorum != 2 || len(receipt.AuthorityProof) < v.providerQuorum || len(receipt.AuthorityProof) > 5 {
		return ErrAuthorityProof
	}
	seen := make(map[string]struct{}, len(receipt.AuthorityProof))
	var canonical AuthorityObservation
	for index, observation := range receipt.AuthorityProof {
		if !identifier(observation.Provider) {
			return ErrAuthorityProof
		}
		if _, duplicate := seen[observation.Provider]; duplicate {
			return ErrAuthorityProof
		}
		seen[observation.Provider] = struct{}{}
		if !observationMatchesRule(observation, rule, workflow, receipt) {
			return ErrAuthorityProof
		}
		if index == 0 {
			canonical = observation
			continue
		}
		if !sameAuthorityOutcome(canonical, observation) {
			return ErrAuthorityProof
		}
	}
	if len(seen) < v.providerQuorum {
		return ErrAuthorityProof
	}
	return nil
}

func validAuthorityRule(rule AuthorityRule) bool {
	wantKind, wantProposer, wantApprover, mapped := expectedAuthorityWorkflow(rule.Action)
	wantSelector, wantActionEvent, wantSecondaryEvent, wantWorkflowEvent, surfaceMapped :=
		expectedAuthoritySurface(rule.Action)
	if !validChainAction(rule.Action) || !validKind(rule.Kind) || (rule.ChainID != 8453 && rule.ChainID != 84532) ||
		!canonicalAddress(rule.ContractAddress) || !hash(rule.ContractCodeHash) || !canonicalAddress(rule.OnChainPrincipal) ||
		!mapped || rule.Kind != wantKind || rule.ProposerRole != wantProposer || rule.ApproverRole != wantApprover ||
		!canPropose(rule.Kind, rule.ProposerRole) || !canApprove(rule.Kind, rule.ApproverRole) || rule.WorkflowQuorum != 2 ||
		!surfaceMapped || rule.FunctionSelector != wantSelector || rule.ActionEventSignature != wantActionEvent ||
		rule.SecondaryActionEventSignature != wantSecondaryEvent || rule.WorkflowEventSignature != wantWorkflowEvent ||
		strings.TrimSpace(rule.EmergencyPath) == "" || len(rule.EmergencyPath) > 256 {
		return false
	}
	if rule.RelayerMode == RelayerExact {
		return canonicalAddress(rule.Relayer)
	}
	return rule.RelayerMode == RelayerAny && rule.Relayer == ""
}

func observationMatchesRule(observation AuthorityObservation, rule AuthorityRule, workflow Workflow, receipt CompletionReceipt) bool {
	if observation.ChainID != rule.ChainID || observation.ChainID != receipt.ChainID ||
		observation.TransactionHash != receipt.TransactionHash || observation.BlockNumber != receipt.BlockNumber ||
		observation.BlockHash != receipt.BlockHash || observation.ContractAddress != rule.ContractAddress ||
		observation.ContractAddress != receipt.ContractAddress || observation.ContractCodeHash != rule.ContractCodeHash ||
		observation.OnChainPrincipal != rule.OnChainPrincipal || observation.FunctionSelector != rule.FunctionSelector ||
		observation.ActionEventSignature != rule.ActionEventSignature || observation.ActionEventSignature != receipt.ActionEventSignature ||
		observation.SecondaryActionEventSignature != rule.SecondaryActionEventSignature ||
		observation.WorkflowEventSignature != rule.WorkflowEventSignature ||
		observation.WorkflowID != workflow.WorkflowID ||
		observation.PayloadHash != workflow.PayloadHash || observation.Finality != "FINALIZED" || receipt.Finality != "FINALIZED" {
		return false
	}
	if rule.WorkflowEventSignature != "" && observation.WorkflowEventSignature != receipt.EventSignature {
		return false
	}
	if !containsLogIndex(receipt.ActionLogIndexes, observation.ActionLogIndex) ||
		observation.WorkflowLogIndex != receipt.LogIndex || observation.ObservedTimelockSeconds < rule.MinimumTimelockSeconds {
		return false
	}
	if rule.WorkflowEventSignature != "" && observation.ActionLogIndex == observation.WorkflowLogIndex {
		return false
	}
	if rule.SecondaryActionEventSignature != "" &&
		(observation.SecondaryActionLogIndex == observation.ActionLogIndex ||
			(rule.WorkflowEventSignature != "" && observation.SecondaryActionLogIndex == observation.WorkflowLogIndex)) {
		return false
	}
	if !canonicalAddress(observation.Relayer) {
		return false
	}
	return rule.RelayerMode == RelayerAny || observation.Relayer == rule.Relayer
}

func containsLogIndex(indexes []uint64, target uint64) bool {
	for _, index := range indexes {
		if index == target {
			return true
		}
	}
	return false
}

func sameAuthorityOutcome(left, right AuthorityObservation) bool {
	left.Provider, right.Provider = "", ""
	return left == right
}

func validChainAction(action ChainAction) bool {
	switch action {
	case ActionCallEscrowAddVerifier, ActionCallEscrowRevokeVerifier, ActionCallEscrowPause,
		ActionDirectoryPublish, ActionDirectoryCancel, ActionDirectorySetPublisher, ActionDirectorySetPauser,
		ActionDirectoryPauseSeller, ActionDirectoryUnpauseSeller,
		ActionDirectoryRevokeQuoteKey, ActionDirectoryUnrevokeQuoteKey,
		ActionAgentRegister, ActionAgentUpdatePolicy, ActionAgentSetStatus, ActionAgentSetRegistryAdmin,
		ActionSpendSetAuthorizer, ActionSpendSetAllowlist, ActionSpendScheduleCaps, ActionSpendPause, ActionSpendInvalidateNonces,
		ActionSafeEnableModule, ActionSafeDisableModule, ActionSafeAddOwner, ActionSafeRemoveOwner,
		ActionSafeSwapOwner, ActionSafeChangeThreshold:
		return true
	default:
		return false
	}
}

func expectedAuthorityWorkflow(action ChainAction) (Kind, Role, Role, bool) {
	switch action {
	case ActionCallEscrowAddVerifier, ActionCallEscrowRevokeVerifier:
		return VerifierGovernance, SignerOperator, OrgAdmin, true
	case ActionCallEscrowPause, ActionSpendPause:
		return BreakGlass, OrgAdmin, IncidentResponder, true
	case ActionDirectoryPublish:
		return PayoutChange, SellerAdmin, OrgAdmin, true
	case ActionDirectoryCancel, ActionDirectoryPauseSeller, ActionDirectoryUnpauseSeller, ActionDirectoryRevokeQuoteKey, ActionDirectoryUnrevokeQuoteKey:
		return DirectoryCancel, SellerAdmin, OrgAdmin, true
	case ActionAgentRegister, ActionAgentUpdatePolicy, ActionAgentSetStatus:
		return RoleAdmin, OrgAdmin, OrgAdmin, true
	case ActionDirectorySetPublisher, ActionDirectorySetPauser, ActionAgentSetRegistryAdmin:
		return BreakGlass, OrgAdmin, IncidentResponder, true
	case ActionSpendScheduleCaps:
		return SignerCaps, SignerOperator, OrgAdmin, true
	case ActionSpendSetAuthorizer, ActionSpendSetAllowlist, ActionSpendInvalidateNonces,
		ActionSafeEnableModule, ActionSafeDisableModule:
		return ModuleGovernance, SignerOperator, OrgAdmin, true
	case ActionSafeAddOwner, ActionSafeRemoveOwner, ActionSafeSwapOwner, ActionSafeChangeThreshold:
		return BreakGlass, OrgAdmin, IncidentResponder, true
	default:
		return "", "", "", false
	}
}

func expectedAuthoritySurface(action ChainAction) (string, string, string, string, bool) {
	const authorization = "(bytes32,address,uint256,bytes32,bytes4,bytes32,bytes32,uint256,uint64,uint64,uint64,bytes32)"
	const workflowEvent = "GovernanceWorkflowBound(bytes32,bytes32,bytes4)"
	var function, event, secondary string
	wantsWorkflowEvent := false
	switch action {
	case ActionCallEscrowAddVerifier:
		function, event, wantsWorkflowEvent = "addVerifier(address,uint64,bytes32,bytes32)", "VerifierAdded(address,uint64,uint64)", true
	case ActionCallEscrowRevokeVerifier:
		function, event, wantsWorkflowEvent = "revokeVerifier(address,bytes32,bytes32)", "VerifierRevoked(address,uint64,uint64)", true
	case ActionCallEscrowPause:
		function, event, wantsWorkflowEvent = "setEmergencyPause(bytes32,bytes32)", "EmergencyPauseSet()", true
	case ActionDirectoryPublish:
		function, event, wantsWorkflowEvent = "approveVersion(uint64,bytes32)", "VersionApproved(bytes32,uint64,address,uint64,uint64)", true
	case ActionDirectoryCancel:
		function, event, wantsWorkflowEvent = "cancelVersion(uint64,bytes32,bytes32,bytes32)", "VersionCancelled(bytes32,uint64,address)", true
	case ActionDirectorySetPublisher:
		function, event, wantsWorkflowEvent = "setDirectoryPublisher(address,bytes32,bytes32)", "DirectoryPublisherSet(address,address,uint64)", true
	case ActionDirectorySetPauser:
		function, event, wantsWorkflowEvent = "setPauser(address,bytes32,bytes32)", "PauserSet(address,address,uint64)", true
	case ActionDirectoryPauseSeller, ActionDirectoryUnpauseSeller:
		function = "pauseSeller(bytes32,bool,bytes32,bytes32," + authorization + ",bytes)"
		event, wantsWorkflowEvent = "SellerPaused(bytes32,bool,address,address)", true
	case ActionDirectoryRevokeQuoteKey, ActionDirectoryUnrevokeQuoteKey:
		function = "setQuoteKeyRevoked(address,bool,bytes32,bytes32," + authorization + ",bytes)"
		event, wantsWorkflowEvent = "QuoteKeyRevoked(address,bool,address,address)", true
	case ActionAgentRegister:
		function, event = "register(string,bytes32,bytes32,"+authorization+",bytes)", "AgentRegistered(bytes32,bytes32,bytes32,string,bytes32,address,address)"
	case ActionAgentUpdatePolicy:
		function, event = "updatePolicyHash(bytes32,bytes32,bytes32,"+authorization+",bytes)", "AgentPolicyUpdated(bytes32,bytes32,bytes32,bytes32,bytes32,address,address)"
	case ActionAgentSetStatus:
		function, event = "setStatus(bytes32,uint8,bytes32,"+authorization+",bytes)", "AgentStatusSet(bytes32,uint8,uint8,bytes32,bytes32,address,address)"
	case ActionAgentSetRegistryAdmin:
		function, event, wantsWorkflowEvent = "setRegistryAdmin(address,bytes32,bytes32)", "RegistryAdminSet(address,address,uint64)", true
	case ActionSpendSetAuthorizer:
		function, event, wantsWorkflowEvent = "setSpendAuthorizer(address,bytes32,bytes32)", "SpendAuthorizerSet(address,uint64)", true
	case ActionSpendSetAllowlist:
		function, event, wantsWorkflowEvent = "setEscrowAllowlist(address,bytes32,bytes32,bytes32)", "EscrowAllowlistSet(address,bytes32)", true
	case ActionSpendScheduleCaps:
		function, event, wantsWorkflowEvent = "scheduleCaps((uint256,uint256,uint256),bytes32,bytes32)", "CapsScheduled(uint256,uint256,uint256,uint64)", true
	case ActionSpendPause:
		function, event, wantsWorkflowEvent = "setEmergencyPause(bool,bytes32,bytes32)", "EmergencyPauseSet(bool)", true
	case ActionSpendInvalidateNonces:
		function, event, wantsWorkflowEvent = "invalidateNonces(uint256[],bytes32,bytes32)", "NonceInvalidated(uint256)", true
	case ActionSafeEnableModule:
		function, event = "enableModule(address)", "EnabledModule(address)"
	case ActionSafeDisableModule:
		function, event = "disableModule(address,address)", "DisabledModule(address)"
	case ActionSafeAddOwner:
		function, event = "addOwnerWithThreshold(address,uint256)", "AddedOwner(address)"
	case ActionSafeRemoveOwner:
		function, event = "removeOwner(address,address,uint256)", "RemovedOwner(address)"
	case ActionSafeSwapOwner:
		function, event, secondary = "swapOwner(address,address,address)", "RemovedOwner(address)", "AddedOwner(address)"
	case ActionSafeChangeThreshold:
		function, event = "changeThreshold(uint256)", "ChangedThreshold(uint256)"
	default:
		return "", "", "", "", false
	}
	workflow := ""
	if wantsWorkflowEvent {
		workflow = topicHash(workflowEvent)
	}
	return functionSelector(function), topicHash(event), topicHashOrEmpty(secondary), workflow, true
}

func functionSelector(signature string) string {
	digest := crypto.Keccak256([]byte(signature))
	return "0x" + hex.EncodeToString(digest[:4])
}

func topicHash(signature string) string { return crypto.Keccak256Hash([]byte(signature)).Hex() }

func topicHashOrEmpty(signature string) string {
	if signature == "" {
		return ""
	}
	return topicHash(signature)
}

func validKind(kind Kind) bool {
	switch kind {
	case PayoutChange, SignerCaps, VerifierGovernance, ProductionGate, BreakGlass, RoleAdmin, ModuleGovernance, DirectoryCancel:
		return true
	default:
		return false
	}
}

var _ GovernanceActionGate = (*AuthorityVerifier)(nil)
