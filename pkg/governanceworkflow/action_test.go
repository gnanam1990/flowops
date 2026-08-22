package governanceworkflow

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
)

func TestBindActionBuildsExactExecutableGovernanceCalls(t *testing.T) {
	callEscrow := "0x1111111111111111111111111111111111111111"
	spendModule := "0x2222222222222222222222222222222222222222"
	directory := "0x3333333333333333333333333333333333333333"
	verifier := "0x4444444444444444444444444444444444444444"
	currentAuthorizer := "0x5555555555555555555555555555555555555555"
	nextAuthorizer := "0x6666666666666666666666666666666666666666"
	target := "0x7777777777777777777777777777777777777777"
	currentCaps := Caps{PerTransaction: "100", PerDay: "200", AllowanceCeiling: "300"}
	nextCaps := Caps{PerTransaction: "101", PerDay: "201", AllowanceCeiling: "301"}
	proposal := DirectoryProposal{
		VersionID: 2, PreviousVersion: 1, PreviousRoot: hashValue(1), NewRoot: hashValue(2),
		BlobContentHash: hashValue(3), LocationsHash: hashValue(4), ChangeClass: 2,
		RequestedActivatesAt: 1_800_000_000,
	}
	tests := []struct {
		name      string
		action    Action
		kind      string
		signature string
		digest    func() (common.Hash, error)
	}{
		{"add verifier", Action{Type: ActionCallEscrowAddVerifier, ChainID: 84532, ContractAddress: callEscrow,
			CallEscrowAddVerifier: &CallEscrowAddVerifierAction{Key: verifier, NextEpoch: 7}},
			"VERIFIER_GOVERNANCE", "addVerifier(address,uint64,bytes32,bytes32)",
			func() (common.Hash, error) {
				return CallEscrowAddVerifier(84532, callEscrow, vectorWorkflow, verifier, 0, 0, 0, false, 7)
			}},
		{"revoke verifier", Action{Type: ActionCallEscrowRevokeVerifier, ChainID: 84532, ContractAddress: callEscrow,
			CallEscrowRevokeVerifier: &CallEscrowRevokeVerifierAction{Key: verifier, ActiveEpoch: 7}},
			"VERIFIER_GOVERNANCE", "revokeVerifier(address,bytes32,bytes32)",
			func() (common.Hash, error) {
				return CallEscrowRevokeVerifier(84532, callEscrow, vectorWorkflow, verifier, 7, 0, 0, false)
			}},
		{"escrow pause", Action{Type: ActionCallEscrowPause, ChainID: 84532, ContractAddress: callEscrow,
			CallEscrowPause: &CallEscrowPauseAction{}}, "BREAK_GLASS", "setEmergencyPause(bytes32,bytes32)",
			func() (common.Hash, error) { return CallEscrowPause(84532, callEscrow, vectorWorkflow) }},
		{"authorizer", Action{Type: ActionSpendAuthorizer, ChainID: 84532, ContractAddress: spendModule,
			SpendAuthorizer: &SpendAuthorizerAction{Current: currentAuthorizer, CurrentEpoch: 9, Next: nextAuthorizer}},
			"MODULE_GOVERNANCE", "setSpendAuthorizer(address,bytes32,bytes32)",
			func() (common.Hash, error) {
				return SpendAuthorizer(84532, spendModule, vectorWorkflow, currentAuthorizer, 9, nextAuthorizer)
			}},
		{"allowlist", Action{Type: ActionSpendAllowlist, ChainID: 84532, ContractAddress: spendModule,
			SpendAllowlist: &SpendAllowlistAction{Target: target, CurrentCodeHash: hashValue(5), NextCodeHash: hashValue(6)}},
			"MODULE_GOVERNANCE", "setEscrowAllowlist(address,bytes32,bytes32,bytes32)",
			func() (common.Hash, error) {
				return SpendAllowlist(84532, spendModule, vectorWorkflow, target, hashValue(5), hashValue(6))
			}},
		{"caps", Action{Type: ActionSpendCaps, ChainID: 84532, ContractAddress: spendModule,
			SpendCaps: &SpendCapsAction{Current: currentCaps, Next: nextCaps}},
			"SIGNER_CAPS", "scheduleCaps((uint256,uint256,uint256),bytes32,bytes32)",
			func() (common.Hash, error) {
				return SpendCaps(84532, spendModule, vectorWorkflow, currentCaps, nextCaps)
			}},
		{"module pause", Action{Type: ActionSpendPause, ChainID: 84532, ContractAddress: spendModule,
			SpendPause: &SpendPauseAction{Current: false, Next: true}}, "BREAK_GLASS", "setEmergencyPause(bool,bytes32,bytes32)",
			func() (common.Hash, error) { return SpendPause(84532, spendModule, vectorWorkflow, false, true) }},
		{"invalidate", Action{Type: ActionSpendInvalidateNonces, ChainID: 84532, ContractAddress: spendModule,
			SpendInvalidateNonces: &SpendInvalidateNoncesAction{Nonces: []string{hashValue(7), hashValue(8)}}},
			"MODULE_GOVERNANCE", "invalidateNonces(bytes32[],bytes32,bytes32)",
			func() (common.Hash, error) {
				return SpendInvalidateNonces(84532, spendModule, vectorWorkflow, []string{hashValue(7), hashValue(8)})
			}},
		{"directory approve", Action{Type: ActionDirectoryApprove, ChainID: 84532, ContractAddress: directory,
			DirectoryApprove: &DirectoryApproveAction{Proposal: proposal, ProposerNonce: "9"}},
			"PAYOUT_CHANGE", "approveVersion(uint64,bytes32)",
			func() (common.Hash, error) { return DirectoryPublish(84532, directory, vectorWorkflow, proposal) }},
		{"directory cancel", Action{Type: ActionDirectoryCancel, ChainID: 84532, ContractAddress: directory,
			DirectoryCancel: &DirectoryCancelAction{VersionID: 2, ProposalHash: hashValue(10)}},
			"DIRECTORY_CANCEL", "cancelVersion(uint64,bytes32,bytes32,bytes32)",
			func() (common.Hash, error) {
				return DirectoryCancel(84532, directory, vectorWorkflow, 2, hashValue(10))
			}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			bound, err := BindAction(vectorWorkflow, test.action)
			if err != nil {
				t.Fatal(err)
			}
			digest, err := test.digest()
			if err != nil {
				t.Fatal(err)
			}
			selectorBytes := selector(test.signature)
			wantSelector := "0x" + common.Bytes2Hex(selectorBytes[:])
			if bound.WorkflowKind != test.kind || bound.PayloadHash != digest.Hex() || bound.ChainID != test.action.ChainID ||
				bound.ContractAddress != test.action.ContractAddress || bound.FunctionSelector != wantSelector ||
				!strings.HasPrefix(bound.Calldata, wantSelector) || len(bound.Calldata) <= len(wantSelector) {
				t.Fatalf("bound=%+v digest=%s selector=%s", bound, digest.Hex(), wantSelector)
			}
			var decoded Action
			if err := json.Unmarshal(bound.CanonicalAction, &decoded); err != nil {
				t.Fatal(err)
			}
			rebound, err := BindAction(vectorWorkflow, decoded)
			if err != nil || rebound.PayloadHash != bound.PayloadHash || rebound.Calldata != bound.Calldata ||
				string(rebound.CanonicalAction) != string(bound.CanonicalAction) {
				t.Fatalf("canonical action did not round trip: rebound=%+v err=%v", rebound, err)
			}
		})
	}
}

func TestDirectoryApproveDerivesExactProposalHash(t *testing.T) {
	proposal := DirectoryProposal{
		VersionID: 2, PreviousVersion: 1, PreviousRoot: hashValue(1), NewRoot: hashValue(2),
		BlobContentHash: hashValue(3), LocationsHash: hashValue(4), ChangeClass: 2,
		RequestedActivatesAt: 1_800_000_000,
	}
	action := Action{Type: ActionDirectoryApprove, ChainID: 84532, ContractAddress: vectorContract,
		DirectoryApprove: &DirectoryApproveAction{Proposal: proposal, ProposerNonce: "9"}}
	bound, err := BindAction(vectorWorkflow, action)
	if err != nil {
		t.Fatal(err)
	}
	proposalHash, err := DirectoryProposalHash(84532, vectorContract, vectorWorkflow, proposal, "9")
	if err != nil {
		t.Fatal(err)
	}
	if proposalHash.Hex() != "0xdd13a77dae74f9dff8ad40b7b6088947f9293c0922f4f25d92067e289b8af489" {
		t.Fatalf("proposal hash=%s", proposalHash.Hex())
	}
	want, err := packCall("approveVersion(uint64,bytes32)", []abi.Type{uint64Type, bytes32Type}, proposal.VersionID, proposalHash)
	if err != nil {
		t.Fatal(err)
	}
	if bound.Calldata != "0x"+hex.EncodeToString(want) {
		t.Fatalf("calldata=%s want=%x", bound.Calldata, want)
	}
	mutated := action
	mutated.DirectoryApprove = &DirectoryApproveAction{Proposal: proposal, ProposerNonce: "10"}
	changed, err := BindAction(vectorWorkflow, mutated)
	if err != nil || changed.PayloadHash != bound.PayloadHash || changed.Calldata == bound.Calldata {
		t.Fatalf("proposer nonce was not bound only to the selected proposal: changed=%+v err=%v", changed, err)
	}
}

func TestBindActionRejectsAmbiguousAndSubstitutedActions(t *testing.T) {
	base := Action{Type: ActionSpendCaps, ChainID: 84532, ContractAddress: vectorContract,
		SpendCaps: &SpendCapsAction{Current: Caps{"100", "200", "300"}, Next: Caps{"101", "201", "301"}}}
	bound, err := BindAction(vectorWorkflow, base)
	if err != nil {
		t.Fatal(err)
	}
	mutated := base
	mutated.SpendCaps = &SpendCapsAction{Current: Caps{"99", "200", "300"}, Next: base.SpendCaps.Next}
	changed, err := BindAction(vectorWorkflow, mutated)
	if err != nil || changed.PayloadHash == bound.PayloadHash || changed.Calldata == bound.Calldata {
		t.Fatalf("current-state substitution was not independently bound: changed=%+v err=%v", changed, err)
	}
	mutated = base
	mutated.SpendCaps = &SpendCapsAction{Current: base.SpendCaps.Current, Next: Caps{"102", "202", "302"}}
	changed, err = BindAction(vectorWorkflow, mutated)
	if err != nil || changed.PayloadHash == bound.PayloadHash || changed.Calldata == bound.Calldata {
		t.Fatalf("execution-value substitution was not bound: changed=%+v err=%v", changed, err)
	}
	for name, action := range map[string]Action{
		"missing selected action": {Type: ActionSpendCaps, ChainID: 84532, ContractAddress: vectorContract},
		"two actions": {Type: ActionSpendCaps, ChainID: 84532, ContractAddress: vectorContract,
			SpendCaps:  &SpendCapsAction{Current: Caps{"100", "200", "300"}, Next: Caps{"101", "201", "301"}},
			SpendPause: &SpendPauseAction{Current: false, Next: true}},
		"mismatched discriminator": {Type: ActionSpendPause, ChainID: 84532, ContractAddress: vectorContract,
			SpendCaps: &SpendCapsAction{Current: Caps{"100", "200", "300"}, Next: Caps{"101", "201", "301"}}},
		"unsupported chain": {Type: ActionSpendPause, ChainID: 1, ContractAddress: vectorContract,
			SpendPause: &SpendPauseAction{Current: false, Next: true}},
	} {
		if _, err := BindAction(vectorWorkflow, action); !errors.Is(err, ErrInvalidPayload) {
			t.Errorf("%s error=%v", name, err)
		}
	}
}

func TestRebindActionAcceptsJSONBOrderingButRejectsSchemaAmbiguity(t *testing.T) {
	raw := json.RawMessage(`{
		"spendPause":{"next":true,"current":false},
		"contractAddress":"0x1111111111111111111111111111111111111111",
		"chainId":84532,
		"type":"SPEND_PAUSE"
	}`)
	bound, err := RebindAction(vectorWorkflow, raw)
	if err != nil || bound.WorkflowKind != "BREAK_GLASS" {
		t.Fatalf("JSONB-style action bound=%+v err=%v", bound, err)
	}
	for name, value := range map[string]json.RawMessage{
		"unknown field":         json.RawMessage(`{"type":"SPEND_PAUSE","chainId":84532,"contractAddress":"0x1111111111111111111111111111111111111111","spendPause":{"current":false,"next":true},"unexpected":true}`),
		"explicit null variant": json.RawMessage(`{"type":"SPEND_PAUSE","chainId":84532,"contractAddress":"0x1111111111111111111111111111111111111111","spendPause":{"current":false,"next":true},"spendCaps":null}`),
		"trailing value":        json.RawMessage(`{"type":"SPEND_PAUSE","chainId":84532,"contractAddress":"0x1111111111111111111111111111111111111111","spendPause":{"current":false,"next":true}} {}`),
	} {
		if _, err := RebindAction(vectorWorkflow, value); !errors.Is(err, ErrInvalidPayload) {
			t.Errorf("%s error=%v", name, err)
		}
	}
}
