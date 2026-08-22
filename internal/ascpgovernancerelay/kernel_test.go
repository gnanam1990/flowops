package ascpgovernancerelay

import (
	"crypto/ecdsa"
	"encoding/hex"
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/gnanam1990/flowops/internal/ascpworkflow"
	"github.com/gnanam1990/flowops/pkg/governanceworkflow"
	"github.com/gnanam1990/flowops/pkg/safegovernance"
)

func TestPrepareAndRetryOnlyExactSafeTransaction(t *testing.T) {
	now := time.Unix(1_800_000_100, 0).UTC()
	command := relayCommand(t, now)
	keys, owners := relayOwners(t, 3)
	snapshot := relaySnapshot(command, owners, now)
	preparedUnsigned, err := safeDigestForCommand(command, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	signatures := relaySignatures(t, preparedUnsigned, keys[:2])
	prepared, execCalldata, err := Prepare(command, snapshot.SafeAddress, snapshot, signatures, 2, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyPrepared(command, prepared, execCalldata); err != nil {
		t.Fatal(err)
	}
	outer := relayHash(70)
	evidence := exactOutcome(prepared, outer, OutcomeDropped, false, now)
	decision, err := DecideRetry(prepared, outer, evidence, 2, now)
	if err != nil || decision.Decision != DecisionRetryExact {
		t.Fatalf("decision=%+v err=%v", decision, err)
	}

	mutations := map[string]func(*OutcomeEvidence){
		"safe nonce":       func(value *OutcomeEvidence) { value.CurrentSafeNonce++ },
		"precondition":     func(value *OutcomeEvidence) { value.VerifiedPayloadHash = relayHash(71) },
		"safe tx hash":     func(value *OutcomeEvidence) { value.SafeTxHash = relayHash(72) },
		"exec bytes":       func(value *OutcomeEvidence) { value.ExecCalldataHash = relayHash(73) },
		"canonical old tx": func(value *OutcomeEvidence) { value.PreviousCanonical = true },
		"observer grammar": func(value *OutcomeEvidence) { value.Observers = []string{"rpc-a", "rpc b"} },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			changed := evidence
			mutate(&changed)
			decision, err := DecideRetry(prepared, outer, changed, 2, now)
			switch name {
			case "safe nonce":
				if err != nil || decision.Reason != ascpworkflow.SafeNonceConflict {
					t.Fatalf("decision=%+v err=%v", decision, err)
				}
			case "precondition":
				if err != nil || decision.Reason != ascpworkflow.PreconditionChanged {
					t.Fatalf("decision=%+v err=%v", decision, err)
				}
			default:
				if !errors.Is(err, ErrInvalidOutcome) {
					t.Fatalf("decision=%+v err=%v", decision, err)
				}
			}
		})
	}
}

func TestSigningRequestDisplaysTheExactDigestOwnersWillAuthorize(t *testing.T) {
	now := time.Unix(1_800_000_100, 0).UTC()
	command := relayCommand(t, now)
	keys, owners := relayOwners(t, 3)
	snapshot := relaySnapshot(command, owners, now)

	request, err := BuildSigningRequest(command, snapshot.SafeAddress, snapshot, 2, now)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := request.Transaction.Hash()
	if err != nil || request.SafeTxHash != digest || request.WorkflowID != command.WorkflowID ||
		request.PayloadHash != command.PayloadHash || request.OwnerSetHash != ownerSetHash(owners, snapshot.Threshold) ||
		!reflect.DeepEqual(request.SnapshotObservers, snapshot.Observers) || request.SnapshotBlockHash != snapshot.BlockHash ||
		request.Calldata != command.Calldata || !reflect.DeepEqual(request.GovernanceAction, command.GovernanceAction) {
		t.Fatalf("request=%+v digest=%s err=%v", request, digest, err)
	}
	prepared, _, err := Prepare(command, snapshot.SafeAddress, snapshot, relaySignatures(t, request.SafeTxHash, keys[:2]), 2, now)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.SafeTxHash != request.SafeTxHash || !reflect.DeepEqual(prepared.Transaction, request.Transaction) ||
		prepared.OwnerSetHash != request.OwnerSetHash || prepared.SnapshotEvidenceDigest != request.SnapshotEvidenceDigest ||
		!reflect.DeepEqual(prepared.SnapshotObservers, request.SnapshotObservers) {
		t.Fatalf("displayed request changed during authorization: request=%+v prepared=%+v", request, prepared)
	}
	malicious := prepared
	malicious.Transaction.Data = append([]byte(nil), prepared.Transaction.Data...)
	malicious.Transaction.Data[len(malicious.Transaction.Data)-1] ^= 1
	malicious.SafeTxHash, err = malicious.Transaction.Hash()
	if err != nil {
		t.Fatal(err)
	}
	maliciousSignatures := relaySignatures(t, malicious.SafeTxHash, keys[:2])
	maliciousExec, err := malicious.Transaction.ExecCalldata(
		safegovernance.OwnerSnapshot{Owners: owners, Threshold: snapshot.Threshold}, maliciousSignatures)
	if err != nil {
		t.Fatal(err)
	}
	malicious.ExecCalldataHash, malicious.SignaturesHash = hashBytes(maliciousExec), hashBytes(maliciousSignatures)
	if err := VerifyPrepared(command, malicious, maliciousExec); !errors.Is(err, ErrInvalidCommand) {
		t.Fatalf("owner-signed substituted command data error=%v", err)
	}
}

func TestRelaySnapshotClassifiesChangedPreconditionsForReapproval(t *testing.T) {
	now := time.Unix(1_800_000_100, 0).UTC()
	command := relayCommand(t, now)
	keys, owners := relayOwners(t, 3)
	snapshot := relaySnapshot(command, owners, now)
	digest, _ := safeDigestForCommand(command, snapshot)
	prepared, _, err := Prepare(command, snapshot.SafeAddress, snapshot, relaySignatures(t, digest, keys[:2]), 2, now)
	if err != nil {
		t.Fatal(err)
	}

	for name, mutate := range map[string]func(*Snapshot){
		"payload": func(value *Snapshot) { value.VerifiedPayloadHash = relayHash(91) },
		"owner set": func(value *Snapshot) {
			candidate := "0x3333333333333333333333333333333333333333"
			for _, owner := range value.Owners {
				if owner == candidate {
					candidate = "0x4444444444444444444444444444444444444444"
				}
			}
			value.Owners[0] = candidate
			sort.Strings(value.Owners)
		},
	} {
		t.Run(name, func(t *testing.T) {
			changed := snapshot
			changed.Owners = append([]string(nil), snapshot.Owners...)
			mutate(&changed)
			decision, err := ValidateRelaySnapshot(command, prepared, changed, 2, now)
			if err != nil || decision.Decision != DecisionReapprove || decision.Reason != ascpworkflow.PreconditionChanged {
				t.Fatalf("decision=%+v err=%v", decision, err)
			}
		})
	}
}

func TestRetryOutcomeLifecycleIsClosed(t *testing.T) {
	now := time.Unix(1_800_000_100, 0).UTC()
	command := relayCommand(t, now)
	keys, owners := relayOwners(t, 3)
	snapshot := relaySnapshot(command, owners, now)
	digest, _ := safeDigestForCommand(command, snapshot)
	prepared, _, err := Prepare(command, snapshot.SafeAddress, snapshot, relaySignatures(t, digest, keys[:2]), 2, now)
	if err != nil {
		t.Fatal(err)
	}
	outer := relayHash(80)
	cases := []struct {
		outcome   Outcome
		canonical bool
		decision  Decision
		reason    ascpworkflow.TerminalReason
	}{
		{OutcomePending, true, DecisionWait, ""},
		{OutcomeDropped, false, DecisionRetryExact, ""},
		{OutcomeReorged, false, DecisionRetryExact, ""},
		{OutcomeMinedRevert, true, DecisionReapprove, ascpworkflow.MinedRevert},
		{OutcomeFinalized, true, DecisionFinalized, ""},
	}
	for _, scenario := range cases {
		t.Run(string(scenario.outcome), func(t *testing.T) {
			evidence := exactOutcome(prepared, outer, scenario.outcome, scenario.canonical, now)
			if scenario.outcome == OutcomeFinalized {
				evidence.CurrentSafeNonce++
			}
			decision, err := DecideRetry(prepared, outer, evidence, 2, now)
			if err != nil || decision.Decision != scenario.decision || decision.Reason != scenario.reason {
				t.Fatalf("decision=%+v err=%v", decision, err)
			}
		})
	}
}

func TestPrepareRejectsStaleSingleProviderAndChangedExecutionCommand(t *testing.T) {
	now := time.Unix(1_800_000_100, 0).UTC()
	command := relayCommand(t, now)
	keys, owners := relayOwners(t, 3)
	base := relaySnapshot(command, owners, now)
	digest, _ := safeDigestForCommand(command, base)
	signatures := relaySignatures(t, digest, keys[:2])
	for name, mutate := range map[string]func(*Snapshot){
		"stale":                       func(value *Snapshot) { value.ObservedAt = now.Add(-MaxEvidenceAge - time.Second) },
		"single provider":             func(value *Snapshot) { value.Observers = value.Observers[:1] },
		"chain time before approval":  func(value *Snapshot) { value.BlockTimestamp = uint64(command.ExecuteAfter - 1) },
		"payload changed":             func(value *Snapshot) { value.VerifiedPayloadHash = relayHash(90) },
		"owner duplicate":             func(value *Snapshot) { value.Owners[1] = value.Owners[0] },
		"single owner threshold":      func(value *Snapshot) { value.Owners = value.Owners[:1]; value.Threshold = 1 },
		"two of two owners":           func(value *Snapshot) { value.Owners = value.Owners[:2] },
		"invalid observer identifier": func(value *Snapshot) { value.Observers[1] = "rpc b" },
	} {
		t.Run(name, func(t *testing.T) {
			changed := base
			changed.Owners = append([]string(nil), base.Owners...)
			changed.Observers = append([]string(nil), base.Observers...)
			mutate(&changed)
			if _, _, err := Prepare(command, changed.SafeAddress, changed, signatures, 2, now); err == nil {
				t.Fatal("unsafe snapshot was accepted")
			}
		})
	}
	if _, _, err := Prepare(command, base.SafeAddress, base, signatures, 3, now); !errors.Is(err, ErrInvalidSnapshot) {
		t.Fatalf("snapshot below configured quorum error=%v", err)
	}
	changedCommand := command
	changedCommand.ExecuteAfter++
	if _, _, err := Prepare(changedCommand, base.SafeAddress, base, signatures, 2, now); !errors.Is(err, ErrInvalidCommand) {
		t.Fatalf("changed command error=%v", err)
	}
}

func relayCommand(t *testing.T, now time.Time) ascpworkflow.GovernanceExecutionCommand {
	t.Helper()
	workflowID := relayHash(1)
	action := governanceworkflow.Action{
		Type: governanceworkflow.ActionSpendPause, ChainID: 84532,
		ContractAddress: "0x2222222222222222222222222222222222222222",
		SpendPause:      &governanceworkflow.SpendPauseAction{Current: false, Next: true},
	}
	bound, err := governanceworkflow.BindAction(workflowID, action)
	if err != nil {
		t.Fatal(err)
	}
	return ascpworkflow.GovernanceExecutionCommand{
		Version: ascpworkflow.GovernanceExecutionVersion, WorkflowID: workflowID, OrganizationID: "org_a",
		Kind: ascpworkflow.BreakGlass, PayloadHash: bound.PayloadHash, ChainID: bound.ChainID,
		ContractAddress: bound.ContractAddress, FunctionSelector: bound.FunctionSelector, Calldata: bound.Calldata,
		Value: "0", Operation: "CALL", GovernanceAction: bound.CanonicalAction, ApprovedBy: "owner_b",
		ApprovedAt: now.Unix() - 10, ExecuteAfter: now.Unix() - 9, ApprovalActionHash: relayHash(2),
	}
}

func relaySnapshot(command ascpworkflow.GovernanceExecutionCommand, owners []string, now time.Time) Snapshot {
	return Snapshot{
		ChainID: command.ChainID, SafeAddress: "0x1111111111111111111111111111111111111111", SafeNonce: 4,
		Owners: owners, Threshold: 2, VerifiedPayloadHash: command.PayloadHash, BlockNumber: 100,
		BlockHash: relayHash(3), BlockTimestamp: uint64(now.Unix()), ConfirmedHead: 110,
		Observers: []string{"rpc-a", "rpc-b"}, EvidenceDigest: relayHash(4), ObservedAt: now,
	}
}

func exactOutcome(prepared Prepared, outer string, outcome Outcome, canonical bool, now time.Time) OutcomeEvidence {
	return OutcomeEvidence{
		WorkflowID: prepared.WorkflowID, OuterTransactionHash: outer, Outcome: outcome, PreviousCanonical: canonical,
		ChainID: prepared.Transaction.ChainID, SafeAddress: prepared.Transaction.Safe, CurrentSafeNonce: prepared.Transaction.Nonce,
		SafeTxHash: prepared.SafeTxHash, ExecCalldataHash: prepared.ExecCalldataHash, VerifiedPayloadHash: prepared.PayloadHash,
		BlockNumber: 120, BlockHash: relayHash(5), ConfirmedHead: 130, Observers: []string{"rpc-a", "rpc-b"},
		EvidenceDigest: relayHash(6), ObservedAt: now,
	}
}

func safeDigestForCommand(command ascpworkflow.GovernanceExecutionCommand, snapshot Snapshot) (string, error) {
	data, err := hex.DecodeString(strings.TrimPrefix(command.Calldata, "0x"))
	if err != nil {
		return "", err
	}
	tx, err := safegovernance.NewTransaction(command.ChainID, snapshot.SafeAddress, command.ContractAddress, data, snapshot.SafeNonce)
	if err != nil {
		return "", err
	}
	return tx.Hash()
}

func relayOwners(t *testing.T, count int) ([]*ecdsa.PrivateKey, []string) {
	t.Helper()
	keys := make([]*ecdsa.PrivateKey, count)
	owners := make([]string, count)
	for index := range keys {
		key, err := crypto.GenerateKey()
		if err != nil {
			t.Fatal(err)
		}
		keys[index] = key
		owners[index] = strings.ToLower(crypto.PubkeyToAddress(key.PublicKey).Hex())
	}
	sort.Strings(owners)
	return keys, owners
}

func relaySignatures(t *testing.T, digest string, keys []*ecdsa.PrivateKey) []byte {
	t.Helper()
	type pair struct {
		owner     string
		signature []byte
	}
	pairs := make([]pair, len(keys))
	digestBytes, _ := hex.DecodeString(strings.TrimPrefix(digest, "0x"))
	for index, key := range keys {
		signature, err := crypto.Sign(digestBytes, key)
		if err != nil {
			t.Fatal(err)
		}
		signature[64] += 27
		pairs[index] = pair{strings.ToLower(crypto.PubkeyToAddress(key.PublicKey).Hex()), signature}
	}
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].owner < pairs[j].owner })
	var output []byte
	for _, pair := range pairs {
		output = append(output, pair.signature...)
	}
	return output
}

func relayHash(value byte) string {
	return "0x" + strings.Repeat(hex.EncodeToString([]byte{value}), 32)
}
