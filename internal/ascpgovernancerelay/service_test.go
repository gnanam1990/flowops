package ascpgovernancerelay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gnanam1990/flowops/internal/ascpworkflow"
)

func TestServiceConsumesAuthorizesRelaysAndProvesDroppedRetry(t *testing.T) {
	now := time.Unix(1_800_000_100, 0).UTC()
	clock := func() time.Time { return now }
	command := relayCommand(t, now)
	keys, owners := relayOwners(t, 3)
	snapshot := relaySnapshot(command, owners, now)
	digest, _ := safeDigestForCommand(command, snapshot)
	signatures := relaySignatures(t, digest, keys[:2])
	fixture := newRelayFixture(t, clock, command, snapshot)

	consumed, replayed, err := fixture.service.ConsumeOnce(context.Background())
	if err != nil || replayed || consumed.State != StateAwaitingSignatures {
		t.Fatalf("consumed=%+v replay=%t err=%v", consumed, replayed, err)
	}
	request, err := fixture.service.SigningRequest(context.Background(), command.OrganizationID, command.WorkflowID)
	if err != nil || request.SafeTxHash != digest || request.WorkflowID != command.WorkflowID {
		t.Fatalf("signing request=%+v err=%v", request, err)
	}
	authorized, err := fixture.service.Authorize(context.Background(), command.OrganizationID, command.WorkflowID, "owner-sign-1", signatures)
	if err != nil || authorized.State != StateReady {
		t.Fatalf("authorized=%+v err=%v", authorized, err)
	}
	fixture.snapshots.err = errors.New("replay must not call snapshot source")
	replayedJob, err := fixture.service.Authorize(context.Background(), command.OrganizationID, command.WorkflowID, "owner-sign-1", signatures)
	if err != nil || replayedJob.State != StateReady {
		t.Fatalf("replayed=%+v err=%v", replayedJob, err)
	}
	if _, err := fixture.service.SigningRequest(context.Background(), command.OrganizationID, command.WorkflowID); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("authorized job exposed a new signing request: %v", err)
	}
	fixture.snapshots.err = nil

	submitted, err := fixture.service.RelayOnce(context.Background())
	if err != nil || submitted.State != StateSubmitted || submitted.AttemptCount != 1 || fixture.workflow.state != ascpworkflow.Submitted {
		t.Fatalf("submitted=%+v workflow=%s err=%v", submitted, fixture.workflow.state, err)
	}
	fixture.outcomes.next = exactOutcome(submitted.Prepared, submitted.Outer.TransactionHash, OutcomeDropped, false, now)
	retryable, err := fixture.service.ObserveOnce(context.Background())
	if err != nil || retryable.State != StateRetryable || fixture.workflow.state != ascpworkflow.TimedOut {
		t.Fatalf("retryable=%+v workflow=%s err=%v", retryable, fixture.workflow.state, err)
	}
	observerRequest, err := json.Marshal(fixture.outcomes.seen)
	if err != nil || strings.Contains(string(observerRequest), submitted.ArtifactHandle) ||
		strings.Contains(string(observerRequest), submitted.AuthorizationKey) ||
		strings.Contains(string(observerRequest), submitted.Prepared.SignaturesHash) {
		t.Fatalf("chain observer received privileged relay fields: %s err=%v", observerRequest, err)
	}
	retried, err := fixture.service.RelayOnce(context.Background())
	if err != nil || retried.State != StateSubmitted || retried.AttemptCount != 2 || fixture.workflow.state != ascpworkflow.Submitted {
		t.Fatalf("retried=%+v workflow=%s err=%v", retried, fixture.workflow.state, err)
	}
	if fixture.workflow.retryProof.SafeTxHash != retried.Prepared.SafeTxHash ||
		fixture.workflow.retryProof.PreviousTransactionHash == fixture.workflow.retryProof.RetryTransactionHash {
		t.Fatalf("retry proof=%+v", fixture.workflow.retryProof)
	}
}

func TestServiceRequiresReapprovalWhenSafeNonceChangesBeforeRetry(t *testing.T) {
	now := time.Unix(1_800_000_100, 0).UTC()
	command := relayCommand(t, now)
	keys, owners := relayOwners(t, 3)
	snapshot := relaySnapshot(command, owners, now)
	digest, _ := safeDigestForCommand(command, snapshot)
	fixture := newRelayFixture(t, func() time.Time { return now }, command, snapshot)
	_, _, _ = fixture.service.ConsumeOnce(context.Background())
	if _, err := fixture.service.Authorize(context.Background(), command.OrganizationID, command.WorkflowID, "owner-sign-1", relaySignatures(t, digest, keys[:2])); err != nil {
		t.Fatal(err)
	}
	submitted, err := fixture.service.RelayOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	fixture.outcomes.next = exactOutcome(submitted.Prepared, submitted.Outer.TransactionHash, OutcomeDropped, false, now)
	if _, err := fixture.service.ObserveOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	fixture.snapshots.snapshot.SafeNonce++
	reapproval, err := fixture.service.RelayOnce(context.Background())
	if err != nil || reapproval.State != StateReapprovalRequired || fixture.workflow.state != ascpworkflow.RequiresReapproval {
		t.Fatalf("job=%+v workflow=%s err=%v", reapproval, fixture.workflow.state, err)
	}
	if fixture.broadcaster.prepareCalls != 1 {
		t.Fatalf("unsafe replacement reached broadcaster: %d prepares", fixture.broadcaster.prepareCalls)
	}
}

func TestServiceRefreshesRetryEvidenceImmediatelyBeforeBroadcast(t *testing.T) {
	now := time.Unix(1_800_000_100, 0).UTC()
	clock := func() time.Time { return now }
	command := relayCommand(t, now)
	keys, owners := relayOwners(t, 3)
	snapshot := relaySnapshot(command, owners, now)
	digest, _ := safeDigestForCommand(command, snapshot)
	fixture := newRelayFixture(t, clock, command, snapshot)
	_, _, _ = fixture.service.ConsumeOnce(context.Background())
	if _, err := fixture.service.Authorize(context.Background(), command.OrganizationID, command.WorkflowID,
		"owner-sign-1", relaySignatures(t, digest, keys[:2])); err != nil {
		t.Fatal(err)
	}
	first, err := fixture.service.RelayOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	fixture.outcomes.next = exactOutcome(first.Prepared, first.Outer.TransactionHash, OutcomeDropped, false, now)
	if _, err := fixture.service.ObserveOnce(context.Background()); err != nil {
		t.Fatal(err)
	}

	// The queued retry outlives the workflow proof's one-minute freshness
	// window. RelayOnce must replace the old observation before broadcasting.
	now = now.Add(2 * time.Minute)
	fixture.snapshots.snapshot.ObservedAt = now
	fixture.outcomes.next = exactOutcome(first.Prepared, first.Outer.TransactionHash, OutcomeDropped, false, now)
	retried, err := fixture.service.RelayOnce(context.Background())
	if err != nil || retried.State != StateSubmitted || retried.AttemptCount != 2 {
		t.Fatalf("retried=%+v err=%v", retried, err)
	}
	if fixture.workflow.retryProof.ObservedAt != now.Unix() {
		t.Fatalf("retry proof was not refreshed: %+v", fixture.workflow.retryProof)
	}
}

func TestServiceDoesNotBroadcastWhenFreshRetryObservationIsPending(t *testing.T) {
	now := time.Unix(1_800_000_100, 0).UTC()
	command := relayCommand(t, now)
	keys, owners := relayOwners(t, 3)
	snapshot := relaySnapshot(command, owners, now)
	digest, _ := safeDigestForCommand(command, snapshot)
	fixture := newRelayFixture(t, func() time.Time { return now }, command, snapshot)
	_, _, _ = fixture.service.ConsumeOnce(context.Background())
	if _, err := fixture.service.Authorize(context.Background(), command.OrganizationID, command.WorkflowID,
		"owner-sign-1", relaySignatures(t, digest, keys[:2])); err != nil {
		t.Fatal(err)
	}
	first, err := fixture.service.RelayOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	fixture.outcomes.next = exactOutcome(first.Prepared, first.Outer.TransactionHash, OutcomeDropped, false, now)
	if _, err := fixture.service.ObserveOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	fixture.outcomes.next = exactOutcome(first.Prepared, first.Outer.TransactionHash, OutcomePending, true, now)

	waiting, err := fixture.service.RelayOnce(context.Background())
	if err != nil || waiting.State != StateRetryable || fixture.workflow.state != ascpworkflow.TimedOut {
		t.Fatalf("waiting=%+v workflow=%s err=%v", waiting, fixture.workflow.state, err)
	}
	if fixture.broadcaster.prepareCalls != 1 {
		t.Fatalf("pending prior attempt triggered replacement: prepares=%d", fixture.broadcaster.prepareCalls)
	}

	// A later reorg observation is still a valid exact non-canonical proof,
	// even though the workflow first recorded the transaction as timed out.
	fixture.outcomes.next = exactOutcome(first.Prepared, first.Outer.TransactionHash, OutcomeReorged, false, now)
	retried, err := fixture.service.RelayOnce(context.Background())
	if err != nil || retried.State != StateSubmitted || fixture.workflow.state != ascpworkflow.Submitted ||
		fixture.workflow.retryProof.Outcome != string(OutcomeReorged) {
		t.Fatalf("reorg retry=%+v workflow=%s proof=%+v err=%v", retried, fixture.workflow.state, fixture.workflow.retryProof, err)
	}
}

func TestServiceRequiresReapprovalWhenOrganizationSafeBindingChanges(t *testing.T) {
	now := time.Unix(1_800_000_100, 0).UTC()
	command := relayCommand(t, now)
	keys, owners := relayOwners(t, 3)
	snapshot := relaySnapshot(command, owners, now)
	digest, _ := safeDigestForCommand(command, snapshot)
	fixture := newRelayFixture(t, func() time.Time { return now }, command, snapshot)
	_, _, _ = fixture.service.ConsumeOnce(context.Background())
	if _, err := fixture.service.Authorize(context.Background(), command.OrganizationID, command.WorkflowID,
		"owner-sign-1", relaySignatures(t, digest, keys[:2])); err != nil {
		t.Fatal(err)
	}
	fixture.directory.safe = "0x3333333333333333333333333333333333333333"
	reapproval, err := fixture.service.RelayOnce(context.Background())
	if err != nil || reapproval.State != StateReapprovalRequired || fixture.workflow.state != ascpworkflow.RequiresReapproval {
		t.Fatalf("job=%+v workflow=%s err=%v", reapproval, fixture.workflow.state, err)
	}
	if fixture.broadcaster.prepareCalls != 0 {
		t.Fatalf("changed organization Safe reached broadcaster: %d prepares", fixture.broadcaster.prepareCalls)
	}
}

func TestServiceMinedRevertRequiresOneStepReapprovalWithoutNonceConsumption(t *testing.T) {
	now := time.Unix(1_800_000_100, 0).UTC()
	command := relayCommand(t, now)
	keys, owners := relayOwners(t, 3)
	snapshot := relaySnapshot(command, owners, now)
	digest, _ := safeDigestForCommand(command, snapshot)
	fixture := newRelayFixture(t, func() time.Time { return now }, command, snapshot)
	_, _, _ = fixture.service.ConsumeOnce(context.Background())
	if _, err := fixture.service.Authorize(context.Background(), command.OrganizationID, command.WorkflowID,
		"owner-sign-1", relaySignatures(t, digest, keys[:2])); err != nil {
		t.Fatal(err)
	}
	submitted, err := fixture.service.RelayOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// An outer revert can occur before Safe consumes its nonce. It must still
	// terminalize into reapproval rather than wait or blind-retry.
	fixture.outcomes.next = exactOutcome(submitted.Prepared, submitted.Outer.TransactionHash, OutcomeMinedRevert, true, now)
	reapproval, err := fixture.service.ObserveOnce(context.Background())
	if err != nil || reapproval.State != StateReapprovalRequired || fixture.workflow.state != ascpworkflow.RequiresReapproval {
		t.Fatalf("job=%+v workflow=%s err=%v", reapproval, fixture.workflow.state, err)
	}
}

func TestServiceReconcilesBroadcastCrashBeforeRecordingSubmission(t *testing.T) {
	now := time.Unix(1_800_000_100, 0).UTC()
	command := relayCommand(t, now)
	keys, owners := relayOwners(t, 3)
	snapshot := relaySnapshot(command, owners, now)
	digest, _ := safeDigestForCommand(command, snapshot)
	fixture := newRelayFixture(t, func() time.Time { return now }, command, snapshot)
	_, _, _ = fixture.service.ConsumeOnce(context.Background())
	authorized, err := fixture.service.Authorize(context.Background(), command.OrganizationID, command.WorkflowID,
		"owner-sign-1", relaySignatures(t, digest, keys[:2]))
	if err != nil {
		t.Fatal(err)
	}
	lease, err := fixture.store.ClaimRelay(context.Background(), "crashed-worker", 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	outer := OuterArtifact{Handle: "prepared-before-crash", TransactionHash: relayHash(61),
		SafeTxHash: authorized.Prepared.SafeTxHash, ExecCalldataHash: authorized.Prepared.ExecCalldataHash, PreparedAt: now}
	prepared, err := fixture.store.RecordOuterPrepared(context.Background(), lease, outer, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.ReleaseLease(context.Background(), lease); err != nil {
		t.Fatal(err)
	}
	fixture.outcomes.next = exactOutcome(prepared.Prepared, outer.TransactionHash, OutcomePending, true, now)
	recovered, err := fixture.service.RelayOnce(context.Background())
	if err != nil || recovered.State != StateSubmitted || recovered.AttemptCount != 1 || fixture.workflow.state != ascpworkflow.Submitted {
		t.Fatalf("recovered=%+v workflow=%s err=%v", recovered, fixture.workflow.state, err)
	}
	if fixture.broadcaster.prepareCalls != 0 {
		t.Fatalf("known pending outer transaction was prepared again: %d", fixture.broadcaster.prepareCalls)
	}
}

func TestServiceReconcilesRetryBroadcastCrashWithExactProof(t *testing.T) {
	now := time.Unix(1_800_000_100, 0).UTC()
	command := relayCommand(t, now)
	keys, owners := relayOwners(t, 3)
	snapshot := relaySnapshot(command, owners, now)
	digest, _ := safeDigestForCommand(command, snapshot)
	fixture := newRelayFixture(t, func() time.Time { return now }, command, snapshot)
	_, _, _ = fixture.service.ConsumeOnce(context.Background())
	if _, err := fixture.service.Authorize(context.Background(), command.OrganizationID, command.WorkflowID,
		"owner-sign-1", relaySignatures(t, digest, keys[:2])); err != nil {
		t.Fatal(err)
	}
	first, err := fixture.service.RelayOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	fixture.outcomes.next = exactOutcome(first.Prepared, first.Outer.TransactionHash, OutcomeDropped, false, now)
	if _, err := fixture.service.ObserveOnce(context.Background()); err != nil {
		t.Fatal(err)
	}

	// The replacement outer transaction crossed the boundary, but the process
	// crashed before RecordProvenRetry and RecordSubmitted committed.
	lease, err := fixture.store.ClaimRelay(context.Background(), "crashed-retry-worker", 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	outer := OuterArtifact{Handle: "retry-prepared-before-crash", TransactionHash: relayHash(63),
		SafeTxHash: first.Prepared.SafeTxHash, ExecCalldataHash: first.Prepared.ExecCalldataHash, PreparedAt: now}
	broadcasting, err := fixture.store.RecordOuterPrepared(context.Background(), lease, outer, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.ReleaseLease(context.Background(), lease); err != nil {
		t.Fatal(err)
	}
	fixture.outcomes.next = exactOutcome(broadcasting.Prepared, outer.TransactionHash, OutcomePending, true, now)

	recovered, err := fixture.service.RelayOnce(context.Background())
	if err != nil || recovered.State != StateSubmitted || recovered.AttemptCount != 2 || fixture.workflow.state != ascpworkflow.Submitted {
		t.Fatalf("recovered=%+v workflow=%s proof=%+v err=%v", recovered, fixture.workflow.state, fixture.workflow.retryProof, err)
	}
	if fixture.workflow.retryProof.RetryTransactionHash != outer.TransactionHash ||
		fixture.workflow.retryProof.PreviousTransactionHash != first.Outer.TransactionHash {
		t.Fatalf("retry recovery proof=%+v", fixture.workflow.retryProof)
	}
	if fixture.broadcaster.prepareCalls != 1 {
		t.Fatalf("known pending retry was prepared again: %d", fixture.broadcaster.prepareCalls)
	}
}

func TestServiceDoesNotResendProvenDroppedOuterAfterBindingDrift(t *testing.T) {
	now := time.Unix(1_800_000_100, 0).UTC()
	command := relayCommand(t, now)
	keys, owners := relayOwners(t, 3)
	snapshot := relaySnapshot(command, owners, now)
	digest, _ := safeDigestForCommand(command, snapshot)
	fixture := newRelayFixture(t, func() time.Time { return now }, command, snapshot)
	_, _, _ = fixture.service.ConsumeOnce(context.Background())
	authorized, err := fixture.service.Authorize(context.Background(), command.OrganizationID, command.WorkflowID,
		"owner-sign-1", relaySignatures(t, digest, keys[:2]))
	if err != nil {
		t.Fatal(err)
	}
	lease, err := fixture.store.ClaimRelay(context.Background(), "crashed-worker", 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	outer := OuterArtifact{Handle: "dropped-before-record", TransactionHash: relayHash(62),
		SafeTxHash: authorized.Prepared.SafeTxHash, ExecCalldataHash: authorized.Prepared.ExecCalldataHash, PreparedAt: now}
	prepared, err := fixture.store.RecordOuterPrepared(context.Background(), lease, outer, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.ReleaseLease(context.Background(), lease); err != nil {
		t.Fatal(err)
	}
	fixture.outcomes.next = exactOutcome(prepared.Prepared, outer.TransactionHash, OutcomeDropped, false, now)
	fixture.directory.safe = "0x3333333333333333333333333333333333333333"
	reapproval, err := fixture.service.RelayOnce(context.Background())
	if err != nil || reapproval.State != StateReapprovalRequired || fixture.workflow.state != ascpworkflow.RequiresReapproval {
		t.Fatalf("reapproval=%+v workflow=%s err=%v", reapproval, fixture.workflow.state, err)
	}
	if fixture.broadcaster.prepareCalls != 0 {
		t.Fatalf("stale dropped outer was prepared again: %d", fixture.broadcaster.prepareCalls)
	}
}

func TestServiceRequiresReapprovalAfterMaximumRelayAttempts(t *testing.T) {
	now := time.Unix(1_800_000_100, 0).UTC()
	command := relayCommand(t, now)
	keys, owners := relayOwners(t, 3)
	snapshot := relaySnapshot(command, owners, now)
	digest, _ := safeDigestForCommand(command, snapshot)
	fixture := newRelayFixture(t, func() time.Time { return now }, command, snapshot)
	_, _, _ = fixture.service.ConsumeOnce(context.Background())
	if _, err := fixture.service.Authorize(context.Background(), command.OrganizationID, command.WorkflowID,
		"owner-sign-1", relaySignatures(t, digest, keys[:2])); err != nil {
		t.Fatal(err)
	}
	submitted, err := fixture.service.RelayOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for submitted.AttemptCount < MaxRelayAttempts {
		fixture.outcomes.next = exactOutcome(submitted.Prepared, submitted.Outer.TransactionHash, OutcomeDropped, false, now)
		retryable, err := fixture.service.ObserveOnce(context.Background())
		if err != nil || retryable.State != StateRetryable || fixture.workflow.state != ascpworkflow.TimedOut {
			t.Fatalf("attempt=%d retryable=%+v workflow=%s err=%v", submitted.AttemptCount, retryable, fixture.workflow.state, err)
		}
		submitted, err = fixture.service.RelayOnce(context.Background())
		if err != nil || submitted.State != StateSubmitted {
			t.Fatalf("attempt=%d submitted=%+v err=%v", submitted.AttemptCount, submitted, err)
		}
	}
	fixture.outcomes.next = exactOutcome(submitted.Prepared, submitted.Outer.TransactionHash, OutcomeDropped, false, now)
	if retryable, err := fixture.service.ObserveOnce(context.Background()); err != nil || retryable.State != StateRetryable {
		t.Fatalf("exhausted retryable=%+v err=%v", retryable, err)
	}
	fixture.outcomes.next = exactOutcome(submitted.Prepared, submitted.Outer.TransactionHash, OutcomePending, true, now)
	waiting, err := fixture.service.RelayOnce(context.Background())
	if err != nil || waiting.State != StateRetryable || fixture.workflow.state != ascpworkflow.TimedOut {
		t.Fatalf("exhausted pending job=%+v workflow=%s err=%v", waiting, fixture.workflow.state, err)
	}
	if fixture.broadcaster.prepareCalls != MaxRelayAttempts {
		t.Fatalf("pending attempt eleven reached broadcaster: %d prepares", fixture.broadcaster.prepareCalls)
	}
	fixture.outcomes.next = exactOutcome(submitted.Prepared, submitted.Outer.TransactionHash, OutcomeDropped, false, now)

	reapproval, err := fixture.service.RelayOnce(context.Background())
	if err != nil || reapproval.State != StateReapprovalRequired || fixture.workflow.state != ascpworkflow.RequiresReapproval {
		t.Fatalf("job=%+v workflow=%s err=%v", reapproval, fixture.workflow.state, err)
	}
	if fixture.broadcaster.prepareCalls != MaxRelayAttempts {
		t.Fatalf("attempt eleven reached broadcaster: %d prepares", fixture.broadcaster.prepareCalls)
	}
}

func TestServiceRejectsReleasedArtifactMutationAndConcurrentClaim(t *testing.T) {
	now := time.Unix(1_800_000_100, 0).UTC()
	command := relayCommand(t, now)
	keys, owners := relayOwners(t, 3)
	snapshot := relaySnapshot(command, owners, now)
	digest, _ := safeDigestForCommand(command, snapshot)
	fixture := newRelayFixture(t, func() time.Time { return now }, command, snapshot)
	_, _, _ = fixture.service.ConsumeOnce(context.Background())
	authorized, err := fixture.service.Authorize(context.Background(), command.OrganizationID, command.WorkflowID, "owner-sign-1", relaySignatures(t, digest, keys[:2]))
	if err != nil {
		t.Fatal(err)
	}
	fixture.vault.values[authorized.ArtifactHandle][10] ^= 1
	if _, err := fixture.service.RelayOnce(context.Background()); err == nil {
		t.Fatal("mutated owner-signed artifact was relayed")
	}
	if fixture.broadcaster.prepareCalls != 0 {
		t.Fatal("broadcaster was called after artifact mutation")
	}

	// The failed worker releases its lease. A raw concurrent store claim still
	// proves that only one worker can own the next relay boundary at a time.
	first, err := fixture.store.ClaimRelay(context.Background(), "worker-a", 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.ClaimRelay(context.Background(), "worker-b", 10*time.Second); !errors.Is(err, ErrNoWork) {
		t.Fatalf("second claim err=%v", err)
	}
	if err := fixture.store.ReleaseLease(context.Background(), first); err != nil {
		t.Fatal(err)
	}
}

type relayFixture struct {
	service     *Service
	store       *MemoryStore
	snapshots   *snapshotFixture
	vault       *vaultFixture
	broadcaster *broadcasterFixture
	outcomes    *outcomeFixture
	workflow    *workflowFixture
	directory   *directoryFixture
}

func newRelayFixture(t *testing.T, clock func() time.Time, command ascpworkflow.GovernanceExecutionCommand, snapshot Snapshot) *relayFixture {
	t.Helper()
	store := NewMemoryStore(clock)
	if err := store.EnqueueCommand(context.Background(), relayHash(40), command, clock()); err != nil {
		t.Fatal(err)
	}
	snapshots := &snapshotFixture{snapshot: snapshot}
	vault := &vaultFixture{values: map[string][]byte{}}
	hashes := make([]string, MaxRelayAttempts)
	for index := range hashes {
		hashes[index] = relayHash(byte(50 + index))
	}
	broadcaster := &broadcasterFixture{clock: clock, hashes: hashes}
	outcomes := &outcomeFixture{}
	workflow := &workflowFixture{state: ascpworkflow.ApprovedPendingChain, workflowID: command.WorkflowID, payloadHash: command.PayloadHash}
	directory := &directoryFixture{safe: snapshot.SafeAddress}
	service, err := NewService(store, directory, snapshots, vault, broadcaster, outcomes, workflow,
		Config{WorkerID: "governance-relay-1", Quorum: 2, LeaseDuration: 10 * time.Second, Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	return &relayFixture{service, store, snapshots, vault, broadcaster, outcomes, workflow, directory}
}

type directoryFixture struct{ safe string }

func (d *directoryFixture) SafeFor(context.Context, string, uint64) (string, error) {
	return d.safe, nil
}

type snapshotFixture struct {
	snapshot Snapshot
	err      error
}

func (s *snapshotFixture) Observe(context.Context, ascpworkflow.GovernanceExecutionCommand, string) (Snapshot, error) {
	result := s.snapshot
	result.Owners = append([]string(nil), result.Owners...)
	result.Observers = append([]string(nil), result.Observers...)
	return result, s.err
}

type vaultFixture struct {
	mu     sync.Mutex
	values map[string][]byte
	next   int
}

func (v *vaultFixture) Seal(_ context.Context, value, _ []byte) (string, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.next++
	handle := fmt.Sprintf("safe-artifact-%d", v.next)
	v.values[handle] = append([]byte(nil), value...)
	return handle, nil
}
func (v *vaultFixture) Open(_ context.Context, handle string, _ []byte) ([]byte, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	value, ok := v.values[handle]
	if !ok {
		return nil, errors.New("missing artifact")
	}
	return append([]byte(nil), value...), nil
}

type broadcasterFixture struct {
	clock        func() time.Time
	hashes       []string
	prepareCalls int
	artifacts    map[string]string
}

func (b *broadcasterFixture) Prepare(_ context.Context, binding RelayBinding, _ []byte) (OuterArtifact, error) {
	if b.prepareCalls >= len(b.hashes) {
		return OuterArtifact{}, errors.New("no prepared outer transaction")
	}
	b.prepareCalls++
	if b.artifacts == nil {
		b.artifacts = map[string]string{}
	}
	handle := fmt.Sprintf("outer-artifact-%d", b.prepareCalls)
	hash := b.hashes[b.prepareCalls-1]
	b.artifacts[handle] = hash
	return OuterArtifact{Handle: handle, TransactionHash: hash, SafeTxHash: binding.SafeTxHash,
		ExecCalldataHash: binding.ExecCalldataHash, PreparedAt: b.clock()}, nil
}
func (b *broadcasterFixture) Broadcast(_ context.Context, handle string) (string, error) {
	hash, ok := b.artifacts[handle]
	if !ok {
		return "", errors.New("unknown outer artifact")
	}
	return hash, nil
}

type outcomeFixture struct {
	next OutcomeEvidence
	err  error
	seen OutcomeBinding
}

func (o *outcomeFixture) Observe(_ context.Context, binding OutcomeBinding) (OutcomeEvidence, error) {
	o.seen = binding
	return o.next, o.err
}

type workflowFixture struct {
	state                           ascpworkflow.State
	workflowID, payloadHash, txHash string
	retryProof                      ascpworkflow.SafeRetryProof
}

func (w *workflowFixture) RecordSubmission(_ context.Context, _, workflowID, transactionHash string) (ascpworkflow.Workflow, error) {
	if workflowID != w.workflowID || w.state != ascpworkflow.ApprovedPendingChain && !(w.state == ascpworkflow.Submitted && w.txHash == transactionHash) {
		return ascpworkflow.Workflow{}, ascpworkflow.ErrStateConflict
	}
	w.state, w.txHash = ascpworkflow.Submitted, transactionHash
	return ascpworkflow.Workflow{State: w.state, SubmissionTxHash: transactionHash}, nil
}
func (w *workflowFixture) RecordProvenRetry(_ context.Context, _, workflowID, transactionHash string, proof ascpworkflow.SafeRetryProof) (ascpworkflow.Workflow, error) {
	if workflowID != w.workflowID || w.state != ascpworkflow.TimedOut && w.state != ascpworkflow.Reorged ||
		proof.PreviousTransactionHash != w.txHash || proof.RetryTransactionHash != transactionHash || proof.VerifiedPayloadHash != w.payloadHash {
		return ascpworkflow.Workflow{}, ascpworkflow.ErrStateConflict
	}
	w.state, w.txHash, w.retryProof = ascpworkflow.Submitted, transactionHash, proof
	return ascpworkflow.Workflow{State: w.state, SubmissionTxHash: transactionHash}, nil
}
func (w *workflowFixture) RecordChainFailure(_ context.Context, _, workflowID string, state ascpworkflow.State, _ ascpworkflow.TerminalReason) (ascpworkflow.Workflow, error) {
	if workflowID != w.workflowID || w.state != ascpworkflow.Submitted {
		return ascpworkflow.Workflow{}, ascpworkflow.ErrStateConflict
	}
	w.state = state
	return ascpworkflow.Workflow{State: state}, nil
}
func (w *workflowFixture) RequireReapproval(_ context.Context, _, workflowID string, _ ascpworkflow.TerminalReason) (ascpworkflow.Workflow, error) {
	if workflowID != w.workflowID {
		return ascpworkflow.Workflow{}, ascpworkflow.ErrStateConflict
	}
	w.state = ascpworkflow.RequiresReapproval
	return ascpworkflow.Workflow{State: w.state}, nil
}
