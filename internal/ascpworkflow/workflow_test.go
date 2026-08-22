package ascpworkflow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gnanam1990/flowops/pkg/governanceworkflow"
)

func TestKindRoleMatrixDualControlAndReceiptGate(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	clock := func() time.Time { return now }
	observer := &completionObserverStub{}
	service, err := New(NewMemoryStore(), observer, clock, workflowRandom(16))
	if err != nil {
		t.Fatal(err)
	}
	roles := map[Kind]struct {
		proposer, approver Role
		id                 uint64
	}{
		PayoutChange: {SellerAdmin, OrgAdmin, 101}, SignerCaps: {SignerOperator, OrgAdmin, 102},
		VerifierGovernance: {SignerOperator, OrgAdmin, 103}, ProductionGate: {OrgAdmin, OrgAdmin, 104},
		BreakGlass: {OrgAdmin, IncidentResponder, 105}, RoleAdmin: {OrgAdmin, OrgAdmin, 106},
		ModuleGovernance: {SignerOperator, OrgAdmin, 107}, DirectoryCancel: {SellerAdmin, OrgAdmin, 108},
	}
	for kind, pair := range roles {
		t.Run(string(kind), func(t *testing.T) {
			proposer := actor("proposer", pair.proposer, now)
			request := testCreateRequest(kind, pair.id)
			workflow, err := service.Create(t.Context(), proposer, "create_"+string(kind), request)
			if err != nil || workflow.State != Proposed {
				t.Fatalf("create=%+v err=%v", workflow, err)
			}
			if _, err := service.Approve(t.Context(), proposer, workflow.WorkflowID, "self_"+string(kind)); !errors.Is(err, ErrSamePrincipal) {
				t.Fatalf("self approval error=%v", err)
			}
			wrong := actor("wrong", SignerOperator, now)
			if _, err := service.Approve(t.Context(), wrong, workflow.WorkflowID, "wrong_"+string(kind)); !errors.Is(err, ErrForbiddenRole) {
				t.Fatalf("wrong role error=%v", err)
			}
			approved, err := service.Approve(t.Context(), actor("approver", pair.approver, now), workflow.WorkflowID, "approve_"+string(kind))
			want := ApprovedPendingChain
			if !requiresChainReceipt(kind) {
				want = Finalized
			}
			if err != nil || approved.State != want || approved.ApprovedBy != "approver" {
				t.Fatalf("approved=%+v err=%v want=%s", approved, err, want)
			}
			if want == ApprovedPendingChain {
				completed, err := service.ObserveAndComplete(t.Context(), "org_a", workflow.WorkflowID)
				if err != nil || completed.State != Finalized || completed.CompletionDigest == "" || observer.calls != 1 ||
					completed.SubmissionTxHash == "" || completed.SubmittedAt == 0 || completed.ConfirmedAt == 0 || completed.FinalizedAt == 0 {
					t.Fatalf("completed=%+v err=%v observer=%d", completed, err, observer.calls)
				}
				replay, err := service.ObserveAndComplete(t.Context(), "org_a", workflow.WorkflowID)
				if err != nil || !replay.Replayed || replay.CompletionDigest != completed.CompletionDigest || observer.calls != 1 {
					t.Fatalf("completion replay=%+v err=%v", replay, err)
				}
				observer.calls = 0
			}
		})
	}
}

func TestFinalizedReceiptRecoversSideStateAndEarlierSafeAttempt(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	observer := &completionObserverStub{}
	service, err := New(NewMemoryStore(), observer, func() time.Time { return now }, workflowRandom(32))
	if err != nil {
		t.Fatal(err)
	}
	createApproved := func(id uint64) Workflow {
		workflow, err := service.Create(t.Context(), actor("proposer", SignerOperator, now),
			fmt.Sprintf("create_%d", id), testCreateRequest(SignerCaps, id))
		if err != nil {
			t.Fatal(err)
		}
		workflow, err = service.Approve(t.Context(), actor("approver", OrgAdmin, now), workflow.WorkflowID,
			fmt.Sprintf("approve_%d", id))
		if err != nil {
			t.Fatal(err)
		}
		return workflow
	}

	workflow := createApproved(901)
	firstHash := testHash(911)
	if _, err := service.RecordSubmission(t.Context(), workflow.OrganizationID, workflow.WorkflowID, firstHash); err != nil {
		t.Fatal(err)
	}
	if _, err := service.RecordChainFailure(t.Context(), workflow.OrganizationID, workflow.WorkflowID,
		TimedOut, SubmissionTimeout); err != nil {
		t.Fatal(err)
	}
	observer.transactionHash = firstHash
	completed, err := service.ObserveAndComplete(t.Context(), workflow.OrganizationID, workflow.WorkflowID)
	if err != nil || completed.State != Finalized || completed.CompletionReceipt == nil {
		t.Fatalf("side-state completion=%+v err=%v", completed, err)
	}

	workflow = createApproved(902)
	oldHash, replacementHash := testHash(912), testHash(913)
	if _, err := service.RecordSubmission(t.Context(), workflow.OrganizationID, workflow.WorkflowID, oldHash); err != nil {
		t.Fatal(err)
	}
	if _, err := service.RecordChainFailure(t.Context(), workflow.OrganizationID, workflow.WorkflowID,
		TimedOut, SubmissionTimeout); err != nil {
		t.Fatal(err)
	}
	proof := SafeRetryProof{
		WorkflowID: workflow.WorkflowID, PreviousTransactionHash: oldHash, RetryTransactionHash: replacementHash,
		Outcome: "DROPPED", PreviousCanonical: false, SafeAddress: "0x1111111111111111111111111111111111111111",
		SafeNonce: 7, SafeTxHash: testHash(914), ExecCalldataHash: testHash(915),
		VerifiedPayloadHash: workflow.PayloadHash, Observers: []string{"rpc_a", "rpc_b"},
		EvidenceDigest: testHash(916), ObservedAt: now.Unix(),
	}
	if _, err := service.RecordProvenRetry(t.Context(), workflow.OrganizationID, workflow.WorkflowID, replacementHash, proof); err != nil {
		t.Fatal(err)
	}
	replay, err := service.RecordProvenRetry(t.Context(), workflow.OrganizationID, workflow.WorkflowID, replacementHash, proof)
	if err != nil || !replay.Replayed {
		t.Fatalf("exact retry replay=%+v err=%v", replay, err)
	}
	mutatedProof := proof
	mutatedProof.EvidenceDigest = testHash(917)
	if _, err := service.RecordProvenRetry(t.Context(), workflow.OrganizationID, workflow.WorkflowID, replacementHash, mutatedProof); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("mutated retry replay error=%v", err)
	}
	// The first authorized outer can still win after the replacement is
	// submitted. The independent finalized action event is authoritative.
	observer.transactionHash = oldHash
	completed, err = service.ObserveAndComplete(t.Context(), workflow.OrganizationID, workflow.WorkflowID)
	if err != nil || completed.State != Finalized || completed.SubmissionTxHash != oldHash ||
		completed.CompletionReceipt == nil {
		t.Fatalf("earlier-attempt completion=%+v err=%v", completed, err)
	}
}

func TestChainCreateRequiresAndPersistsPayloadBoundWorkflowID(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	service, err := New(NewMemoryStore(), nil, func() time.Time { return now }, workflowRandom(1), WithGovernanceActionGate(testActionGate{}))
	if err != nil {
		t.Fatal(err)
	}
	proposer := actor("signer", SignerOperator, now)
	ungated, err := New(NewMemoryStore(), nil, func() time.Time { return now }, workflowRandom(1))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ungated.Create(t.Context(), proposer, "ungated", testCreateRequest(SignerCaps, 800)); !errors.Is(err, ErrGovernanceUnavailable) {
		t.Fatalf("ungated chain action error=%v", err)
	}
	missingID := testCreateRequest(SignerCaps, 801)
	missingID.WorkflowID = ""
	if _, err := service.Create(t.Context(), proposer, "missing_id", missingID); !errors.Is(err, ErrInvalidWorkflow) {
		t.Fatalf("missing workflow ID error=%v", err)
	}
	missingAction := testCreateRequest(SignerCaps, 801)
	missingAction.Action = nil
	if _, err := service.Create(t.Context(), proposer, "missing_action", missingAction); !errors.Is(err, ErrInvalidWorkflow) {
		t.Fatalf("missing typed action error=%v", err)
	}
	callerHash := testCreateRequest(SignerCaps, 801)
	callerHash.PayloadHash = testHash(802)
	if _, err := service.Create(t.Context(), proposer, "caller_hash", callerHash); !errors.Is(err, ErrInvalidWorkflow) {
		t.Fatalf("caller-supplied payload hash error=%v", err)
	}
	mismatched := testCreateRequest(BreakGlass, 801)
	mismatched.Kind = SignerCaps
	if _, err := service.Create(t.Context(), proposer, "mismatched_action", mismatched); !errors.Is(err, ErrInvalidWorkflow) {
		t.Fatalf("mismatched kind/action error=%v", err)
	}
	request := testCreateRequest(SignerCaps, 801)
	bound, err := governanceworkflow.BindAction(request.WorkflowID, *request.Action)
	if err != nil {
		t.Fatal(err)
	}
	created, err := service.Create(t.Context(), proposer, "bound_id", request)
	if err != nil || created.WorkflowID != request.WorkflowID || created.PayloadHash != bound.PayloadHash ||
		created.ChainID != request.Action.ChainID || created.ContractAddress != request.Action.ContractAddress ||
		created.FunctionSelector != bound.FunctionSelector || created.Calldata != bound.Calldata ||
		string(created.GovernanceAction) != string(bound.CanonicalAction) {
		t.Fatalf("created=%+v err=%v", created, err)
	}
	if _, err := service.Create(t.Context(), actor("owner", OrgAdmin, now), "duplicate_id",
		testCreateRequest(BreakGlass, 801)); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("duplicate workflow ID error=%v", err)
	}
	if _, err := service.Create(t.Context(), actor("owner", OrgAdmin, now), "local_supplied_id",
		CreateRequest{Kind: ProductionGate, WorkflowID: testHash(804), PayloadHash: testHash(805)}); !errors.Is(err, ErrInvalidWorkflow) {
		t.Fatalf("client-selected local workflow ID error=%v", err)
	}
}

func TestCompletionReceiptOwnershipIsAtomicAcrossWorkflows(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	observer := &completionObserverStub{transactionHash: testHash(900), logIndex: 7}
	service, err := New(NewMemoryStore(), observer, func() time.Time { return now }, workflowRandom(4))
	if err != nil {
		t.Fatal(err)
	}
	createApproved := func(proposer, owner, key string) Workflow {
		workflow, err := service.Create(t.Context(), actor(proposer, SignerOperator, now), "create_"+key,
			testCreateRequest(SignerCaps, uint64(len(key)+200)))
		if err != nil {
			t.Fatal(err)
		}
		workflow, err = service.Approve(t.Context(), actor(owner, OrgAdmin, now), workflow.WorkflowID, "approve_"+key)
		if err != nil {
			t.Fatal(err)
		}
		return workflow
	}
	first := createApproved("signer_a", "owner_a", "a")
	second := createApproved("signer_b", "owner_b", "bb")
	if _, err := service.ObserveAndComplete(t.Context(), "org_a", first.WorkflowID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ObserveAndComplete(t.Context(), "org_a", second.WorkflowID); !errors.Is(err, ErrReceiptOwned) {
		t.Fatalf("shared receipt ownership error=%v", err)
	}
	stored, err := service.Get(t.Context(), actor("owner_b", OrgAdmin, now), second.WorkflowID)
	if err != nil || stored.State != ApprovedPendingChain {
		t.Fatalf("conflicting receipt changed second workflow=%+v err=%v", stored, err)
	}
}

func TestGovernanceExecutionCommandContainsOnlyApprovedImmutableBytes(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	service, err := New(NewMemoryStore(), nil, func() time.Time { return now }, workflowRandom(1), WithGovernanceActionGate(testActionGate{}))
	if err != nil {
		t.Fatal(err)
	}
	request := testCreateRequest(SignerCaps, 812)
	workflow, err := service.Create(t.Context(), actor("signer", SignerOperator, now), "create_command", request)
	if err != nil {
		t.Fatal(err)
	}
	workflow, err = service.Approve(t.Context(), actor("owner", OrgAdmin, now), workflow.WorkflowID, "approve_command")
	if err != nil {
		t.Fatal(err)
	}
	approvalHash := testHash(813)
	command, err := buildExecutionCommand(workflow, approvalHash)
	if err != nil || command.Version != GovernanceExecutionVersion || command.WorkflowID != workflow.WorkflowID ||
		command.OrganizationID != workflow.OrganizationID || command.Kind != workflow.Kind ||
		command.PayloadHash != workflow.PayloadHash || command.ChainID != workflow.ChainID ||
		command.ContractAddress != workflow.ContractAddress || command.FunctionSelector != workflow.FunctionSelector ||
		command.Calldata != workflow.Calldata || command.Value != "0" || command.Operation != "CALL" ||
		string(command.GovernanceAction) != string(workflow.GovernanceAction) || command.ApprovedBy != workflow.ApprovedBy ||
		command.ApprovedAt != workflow.ApprovedAt || command.ExecuteAfter != workflow.ApprovedAt+1 || command.ApprovalActionHash != approvalHash {
		t.Fatalf("command=%+v err=%v", command, err)
	}
	invalid := workflow
	invalid.Calldata = "0x12345678"
	if _, err := buildExecutionCommand(invalid, approvalHash); !errors.Is(err, ErrInvalidWorkflow) {
		t.Fatalf("malformed command calldata error=%v", err)
	}
	invalid = workflow
	var action governanceworkflow.Action
	if err := json.Unmarshal(workflow.GovernanceAction, &action); err != nil {
		t.Fatal(err)
	}
	action.SpendCaps.Next.PerTransaction = "102"
	invalid.GovernanceAction, _ = json.Marshal(action)
	if _, err := buildExecutionCommand(invalid, approvalHash); !errors.Is(err, ErrInvalidWorkflow) {
		t.Fatalf("mismatched command action error=%v", err)
	}
	invalid = workflow
	invalid.State = Submitted
	if _, err := buildExecutionCommand(invalid, approvalHash); !errors.Is(err, ErrInvalidWorkflow) {
		t.Fatalf("non-pending execution command error=%v", err)
	}
}

func TestCompletionDigestIgnoresObservationTimeMetadata(t *testing.T) {
	workflow := Workflow{WorkflowID: testHash(70), PayloadHash: testHash(71)}
	first := testReceipt(workflow)
	second := first
	second.ConfirmedHead = first.ConfirmedHead + 50
	second.FinalizedHead = first.FinalizedHead + 40
	second.Observers = []string{"rpc_b", "rpc_c"}
	second.EvidenceDigest = testHash(999)
	if completionDigest(first) != completionDigest(second) {
		t.Fatal("same canonical receipt received a provider-dependent completion identity")
	}
	second.LogIndex++
	if completionDigest(first) == completionDigest(second) {
		t.Fatal("different binding log received the same completion identity")
	}
}

func TestWorkflowIdempotencyExpiryCancellationAndStepUp(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	service, _ := New(NewMemoryStore(), nil, func() time.Time { return now }, bytes.NewReader(bytes.Repeat([]byte{8}, 256)), WithGovernanceActionGate(testActionGate{}))
	proposer := actor("seller", SellerAdmin, now)
	request := testCreateRequest(PayoutChange, 302)
	workflow, err := service.Create(t.Context(), proposer, "same_key", request)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := service.Create(t.Context(), proposer, "same_key", request)
	if err != nil || !replay.Replayed || replay.WorkflowID != workflow.WorkflowID {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
	changed := testCreateRequest(PayoutChange, 302)
	changed.Action.DirectoryApprove.Proposal.NewRoot = testHash(333)
	if _, err := service.Create(t.Context(), proposer, "same_key", changed); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("changed create error=%v", err)
	}
	if _, err := service.Create(t.Context(), Actor{OrganizationID: "org_a", PrincipalID: "seller", Role: SellerAdmin, StepUpAt: now.Add(-6 * time.Minute), StepUpUntil: now.Add(time.Minute)}, "stale_step", request); !errors.Is(err, ErrStepUpRequired) {
		t.Fatalf("stale step-up error=%v", err)
	}
	cancelled, err := service.Cancel(t.Context(), proposer, workflow.WorkflowID, "cancel_1")
	if err != nil || cancelled.State != Cancelled {
		t.Fatalf("cancelled=%+v err=%v", cancelled, err)
	}
	terminal, err := service.Approve(t.Context(), actor("owner", OrgAdmin, now), workflow.WorkflowID, "after_cancel")
	if err != nil || terminal.State != Cancelled || !terminal.Replayed {
		t.Fatalf("terminal outcome=%+v err=%v", terminal, err)
	}

	expiringRequest := request
	expiringRequest.WorkflowID = testHash(303)
	expiring, err := service.Create(t.Context(), proposer, "expiring", expiringRequest)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(ProposalTTL)
	expired, err := service.Get(t.Context(), Actor{OrganizationID: "org_a", PrincipalID: "auditor", Role: OrgAdmin}, expiring.WorkflowID)
	if err != nil || expired.State != Expired || expired.ExpiredAt != now.Unix() {
		t.Fatalf("expired=%+v err=%v", expired, err)
	}
}

func TestCreateReplayDoesNotNeedNewRandomnessAndAcceptsSameSecondStepUp(t *testing.T) {
	now := time.Unix(1_800_000_000, 750_000_000).UTC()
	service, err := New(NewMemoryStore(), nil, func() time.Time { return now }, bytes.NewReader(bytes.Repeat([]byte{6}, 32)), WithGovernanceActionGate(testActionGate{}))
	if err != nil {
		t.Fatal(err)
	}
	proposer := Actor{OrganizationID: "org_a", PrincipalID: "seller", Role: SellerAdmin,
		StepUpAt: time.Unix(1_800_000_000, 500_000_000).UTC(), StepUpUntil: now.Add(time.Minute)}
	request := testCreateRequest(PayoutChange, 330)
	created, err := service.Create(t.Context(), proposer, "random_independent", request)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := service.Create(t.Context(), proposer, "random_independent", request)
	if err != nil || !replay.Replayed || replay.WorkflowID != created.WorkflowID {
		t.Fatalf("replay after random exhaustion=%+v err=%v", replay, err)
	}
	now = now.Add(10 * time.Minute)
	replay, err = service.Create(t.Context(), proposer, "random_independent", request)
	if err != nil || !replay.Replayed || replay.WorkflowID != created.WorkflowID {
		t.Fatalf("replay after step-up expiry=%+v err=%v", replay, err)
	}
}

func TestDecisionReplayAfterStepUpExpiryDoesNotMutateAgain(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	service, _ := New(NewMemoryStore(), nil, func() time.Time { return now }, workflowRandom(1), WithGovernanceActionGate(testActionGate{}))
	workflow, err := service.Create(t.Context(), actor("signer", SignerOperator, now), "decision_create", testCreateRequest(SignerCaps, 334))
	if err != nil {
		t.Fatal(err)
	}
	approver := actor("owner", OrgAdmin, now)
	approved, err := service.Approve(t.Context(), approver, workflow.WorkflowID, "decision_approve")
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(10 * time.Minute)
	replayed, err := service.Approve(t.Context(), approver, workflow.WorkflowID, "decision_approve")
	if err != nil || !replayed.Replayed || replayed.State != approved.State || replayed.ApprovedAt != approved.ApprovedAt {
		t.Fatalf("delayed approval replay=%+v err=%v", replayed, err)
	}
}

func TestChainCreateReplaySurvivesMissingTargetConfiguration(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	service, err := New(NewMemoryStore(), nil, func() time.Time { return now }, workflowRandom(1), WithGovernanceActionGate(testActionGate{}))
	if err != nil {
		t.Fatal(err)
	}
	proposer := actor("signer", SignerOperator, now)
	request := testCreateRequest(SignerCaps, 331)
	created, err := service.Create(t.Context(), proposer, "configuration_replay", request)
	if err != nil {
		t.Fatal(err)
	}
	service.actionGate = nil
	replayed, err := service.Create(t.Context(), proposer, "configuration_replay", request)
	if err != nil || !replayed.Replayed || replayed.WorkflowID != created.WorkflowID {
		t.Fatalf("replay=%+v err=%v", replayed, err)
	}
	changed := request
	changed.Action = &governanceworkflow.Action{Type: governanceworkflow.ActionSpendCaps, ChainID: 84532,
		ContractAddress: request.Action.ContractAddress,
		SpendCaps: &governanceworkflow.SpendCapsAction{Current: request.Action.SpendCaps.Current,
			Next: governanceworkflow.Caps{PerTransaction: "102", PerDay: "202", AllowanceCeiling: "302"}}}
	if _, err := service.Create(t.Context(), proposer, "configuration_replay", changed); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("changed replay error=%v", err)
	}
}

func TestApprovalRevalidatesStoredExecutableActionAndCurrentTargetGate(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	gate := &rejectableActionGate{}
	store := NewMemoryStore()
	service, err := New(store, nil, func() time.Time { return now }, workflowRandom(2), WithGovernanceActionGate(gate))
	if err != nil {
		t.Fatal(err)
	}
	first, err := service.Create(t.Context(), actor("signer", SignerOperator, now), "approval_gate", testCreateRequest(SignerCaps, 332))
	if err != nil {
		t.Fatal(err)
	}
	gate.err = errors.New("target retired")
	if _, err := service.Approve(t.Context(), actor("owner", OrgAdmin, now), first.WorkflowID, "approval_gate"); !errors.Is(err, ErrInvalidWorkflow) {
		t.Fatalf("retired target approval error=%v", err)
	}
	gate.err = nil
	second, err := service.Create(t.Context(), actor("signer", SignerOperator, now), "tampered_action", testCreateRequest(SignerCaps, 333))
	if err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	tampered := store.workflows[second.WorkflowID]
	tampered.Calldata = tampered.FunctionSelector + strings.Repeat("0", 64)
	store.workflows[second.WorkflowID] = tampered
	store.mu.Unlock()
	if _, err := service.Approve(t.Context(), actor("owner", OrgAdmin, now), second.WorkflowID, "tampered_action"); !errors.Is(err, ErrInvalidWorkflow) {
		t.Fatalf("tampered executable approval error=%v", err)
	}
}

func TestTwentyConcurrentWorkflowDecisionsHaveOneTerminalTransition(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	service, _ := New(NewMemoryStore(), nil, func() time.Time { return now }, bytes.NewReader(bytes.Repeat([]byte{9}, 64)), WithGovernanceActionGate(testActionGate{}))
	workflow, err := service.Create(t.Context(), actor("seller", SellerAdmin, now), "create", testCreateRequest(PayoutChange, 304))
	if err != nil {
		t.Fatal(err)
	}
	const contenders = 20
	results := make(chan Workflow, contenders)
	errorsSeen := make(chan error, contenders)
	var wait sync.WaitGroup
	for index := 0; index < contenders; index++ {
		index := index
		wait.Add(1)
		go func() {
			defer wait.Done()
			var result Workflow
			var err error
			if index%2 == 0 {
				result, err = service.Approve(context.Background(), actor(fmt.Sprintf("owner_%d", index), OrgAdmin, now), workflow.WorkflowID, fmt.Sprintf("approve_%d", index))
			} else {
				result, err = service.Cancel(context.Background(), actor(fmt.Sprintf("owner_%d", index), OrgAdmin, now), workflow.WorkflowID, fmt.Sprintf("cancel_%d", index))
			}
			results <- result
			errorsSeen <- err
		}()
	}
	wait.Wait()
	close(results)
	close(errorsSeen)
	for err := range errorsSeen {
		if err != nil {
			t.Fatal(err)
		}
	}
	terminal := State("")
	for result := range results {
		if terminal == "" {
			terminal = result.State
		}
		if result.State != terminal {
			t.Fatalf("split terminal states %s and %s", terminal, result.State)
		}
	}
	if terminal != Cancelled && terminal != ApprovedPendingChain {
		t.Fatalf("terminal=%s", terminal)
	}
}

func TestCompletionFailsClosedWhenIndependentObserverRejectsReceipt(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	observer := &completionObserverStub{err: errors.New("receipt is not canonical")}
	service, _ := New(NewMemoryStore(), observer, func() time.Time { return now }, workflowRandom(2))
	workflow, err := service.Create(t.Context(), actor("signer", SignerOperator, now), "create_rejected", testCreateRequest(SignerCaps, 320))
	if err != nil {
		t.Fatal(err)
	}
	workflow, err = service.Approve(t.Context(), actor("owner", OrgAdmin, now), workflow.WorkflowID, "approve_rejected")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ObserveAndComplete(t.Context(), "org_a", workflow.WorkflowID); !errors.Is(err, ErrInvalidReceipt) {
		t.Fatalf("rejected completion error=%v", err)
	}
	stored, err := service.Get(t.Context(), actor("owner", OrgAdmin, now), workflow.WorkflowID)
	if err != nil || stored.State != ApprovedPendingChain || stored.CompletionDigest != "" {
		t.Fatalf("rejected receipt changed state=%+v err=%v", stored, err)
	}
}

func TestRejectedReceiptRequiresReapprovalAndLeavesPendingQueue(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	store := NewMemoryStore()
	service, _ := New(store, &completionObserverStub{}, func() time.Time { return now }, workflowRandom(1))
	workflow, err := service.Create(t.Context(), actor("signer", SignerOperator, now), "create_terminal",
		testCreateRequest(SignerCaps, 820))
	if err != nil {
		t.Fatal(err)
	}
	workflow, err = service.Approve(t.Context(), actor("owner", OrgAdmin, now), workflow.WorkflowID, "approve_terminal")
	if err != nil {
		t.Fatal(err)
	}
	terminal, err := service.RequireReapproval(t.Context(), "org_a", workflow.WorkflowID, ReceiptRejected)
	if err != nil || terminal.State != RequiresReapproval || terminal.TerminalReason != ReceiptRejected || terminal.TerminalAt != now.Unix() {
		t.Fatalf("terminal=%+v err=%v", terminal, err)
	}
	replay, err := service.RequireReapproval(t.Context(), "org_a", workflow.WorkflowID, ReceiptRejected)
	if err != nil || !replay.Replayed {
		t.Fatalf("terminal replay=%+v err=%v", replay, err)
	}
	if pending, err := store.Pending(t.Context(), 10, ""); err != nil || len(pending) != 0 {
		t.Fatalf("pending=%+v err=%v", pending, err)
	}
	if _, err := service.ObserveAndComplete(t.Context(), "org_a", workflow.WorkflowID); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("terminal workflow completion error=%v", err)
	}
}

func TestRelayerLifecycleBindsSubmissionConfirmationFinalityAndSideStates(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	txHash := testHash(840)
	observer := &completionObserverStub{transactionHash: txHash}
	service, _ := New(NewMemoryStore(), observer, func() time.Time { return now }, workflowRandom(2))
	createApproved := func(id uint64, key string) Workflow {
		workflow, err := service.Create(t.Context(), actor("signer_"+key, SignerOperator, now), "create_"+key,
			testCreateRequest(SignerCaps, id))
		if err != nil {
			t.Fatal(err)
		}
		workflow, err = service.Approve(t.Context(), actor("owner_"+key, OrgAdmin, now), workflow.WorkflowID, "approve_"+key)
		if err != nil {
			t.Fatal(err)
		}
		return workflow
	}
	workflow := createApproved(841, "normal")
	if _, err := service.RecordConfirmation(t.Context(), "org_a", workflow.WorkflowID, txHash); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("confirmation before submission error=%v", err)
	}
	now = now.Add(time.Second)
	submitted, err := service.RecordSubmission(t.Context(), "org_a", workflow.WorkflowID, txHash)
	if err != nil || submitted.State != Submitted || submitted.SubmissionTxHash != txHash {
		t.Fatalf("submitted=%+v err=%v", submitted, err)
	}
	replay, err := service.RecordSubmission(t.Context(), "org_a", workflow.WorkflowID, txHash)
	if err != nil || !replay.Replayed {
		t.Fatalf("submission replay=%+v err=%v", replay, err)
	}
	if _, err := service.RecordConfirmation(t.Context(), "org_a", workflow.WorkflowID, testHash(842)); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("wrong transaction confirmation error=%v", err)
	}
	now = now.Add(time.Second)
	confirmed, err := service.RecordConfirmation(t.Context(), "org_a", workflow.WorkflowID, txHash)
	if err != nil || confirmed.State != Confirmed || confirmed.ConfirmedAt == 0 {
		t.Fatalf("confirmed=%+v err=%v", confirmed, err)
	}
	submittedAt, confirmedAt := confirmed.SubmittedAt, confirmed.ConfirmedAt
	now = now.Add(time.Second)
	finalized, err := service.ObserveAndComplete(t.Context(), "org_a", workflow.WorkflowID)
	if err != nil || finalized.State != Finalized || finalized.SubmissionTxHash != txHash ||
		finalized.SubmittedAt != submittedAt || finalized.ConfirmedAt != confirmedAt {
		t.Fatalf("finalized=%+v err=%v", finalized, err)
	}

	reorgedWorkflow := createApproved(850, "reorg")
	if _, err := service.RecordSubmission(t.Context(), "org_a", reorgedWorkflow.WorkflowID, testHash(851)); err != nil {
		t.Fatal(err)
	}
	if _, err := service.RecordConfirmation(t.Context(), "org_a", reorgedWorkflow.WorkflowID, testHash(851)); err != nil {
		t.Fatal(err)
	}
	reorged, err := service.RecordChainFailure(t.Context(), "org_a", reorgedWorkflow.WorkflowID, Reorged, ReorgDetected)
	if err != nil || reorged.State != Reorged || reorged.TerminalReason != ReorgDetected {
		t.Fatalf("reorged=%+v err=%v", reorged, err)
	}
	if _, err := service.RecordSubmission(t.Context(), "org_a", reorgedWorkflow.WorkflowID, testHash(852)); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("unproved reorg resubmission error=%v", err)
	}
	reapproval, err := service.RecordChainFailure(t.Context(), "org_a", reorgedWorkflow.WorkflowID, RequiresReapproval, PreconditionChanged)
	if err != nil || reapproval.State != RequiresReapproval || reapproval.TerminalReason != PreconditionChanged {
		t.Fatalf("reapproval=%+v err=%v", reapproval, err)
	}
	timedOutWorkflow := createApproved(853, "timeout")
	timedOut, err := service.RecordChainFailure(t.Context(), "org_a", timedOutWorkflow.WorkflowID, TimedOut, SubmissionTimeout)
	if err != nil || timedOut.State != TimedOut || timedOut.TerminalReason != SubmissionTimeout {
		t.Fatalf("timed out=%+v err=%v", timedOut, err)
	}
	if _, err := service.RecordSubmission(t.Context(), "org_a", timedOutWorkflow.WorkflowID, testHash(854)); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("unproved timeout resubmission error=%v", err)
	}
	if _, err := service.RecordChainFailure(t.Context(), "org_a", reorgedWorkflow.WorkflowID, Reverted, ReorgDetected); !errors.Is(err, ErrInvalidWorkflow) {
		t.Fatalf("invalid state/reason pair error=%v", err)
	}
}

func TestChainLifecycleRejectsBackwardTransitionTimes(t *testing.T) {
	base := time.Unix(1_800_000_000, 0).UTC()
	now := base
	service, _ := New(NewMemoryStore(), &completionObserverStub{}, func() time.Time { return now }, workflowRandom(1))
	workflow, err := service.Create(t.Context(), actor("signer", SignerOperator, now), "clock_create", testCreateRequest(SignerCaps, 860))
	if err != nil {
		t.Fatal(err)
	}
	workflow, err = service.Approve(t.Context(), actor("owner", OrgAdmin, now), workflow.WorkflowID, "clock_approve")
	if err != nil {
		t.Fatal(err)
	}
	now = base.Add(-time.Second)
	if _, err := service.RecordSubmission(t.Context(), "org_a", workflow.WorkflowID, testHash(861)); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("backward submission error=%v", err)
	}
	now = base.Add(time.Second)
	if _, err := service.RecordSubmission(t.Context(), "org_a", workflow.WorkflowID, testHash(861)); err != nil {
		t.Fatal(err)
	}
	now = base
	if _, err := service.RecordConfirmation(t.Context(), "org_a", workflow.WorkflowID, testHash(861)); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("backward confirmation error=%v", err)
	}
	now = base.Add(2 * time.Second)
	if _, err := service.RecordConfirmation(t.Context(), "org_a", workflow.WorkflowID, testHash(861)); err != nil {
		t.Fatal(err)
	}
	now = base.Add(time.Second)
	if _, err := service.RecordChainFailure(t.Context(), "org_a", workflow.WorkflowID, Reorged, ReorgDetected); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("backward failure error=%v", err)
	}
}

func TestCompletionRejectsMalformedObserverOutputBeforePersistence(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	observer := &completionObserverStub{mutate: func(receipt *CompletionReceipt) {
		receipt.EventSignature = testHash(404)
	}}
	service, _ := New(NewMemoryStore(), observer, func() time.Time { return now }, workflowRandom(2))
	workflow, err := service.Create(t.Context(), actor("signer", SignerOperator, now), "create_malformed",
		testCreateRequest(SignerCaps, 505))
	if err != nil {
		t.Fatal(err)
	}
	workflow, err = service.Approve(t.Context(), actor("owner", OrgAdmin, now), workflow.WorkflowID, "approve_malformed")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ObserveAndComplete(t.Context(), "org_a", workflow.WorkflowID); !errors.Is(err, ErrInvalidReceipt) {
		t.Fatalf("malformed observer output error=%v", err)
	}
	stored, err := service.Get(t.Context(), actor("owner", OrgAdmin, now), workflow.WorkflowID)
	if err != nil || stored.State != ApprovedPendingChain || stored.CompletionDigest != "" {
		t.Fatalf("malformed output changed workflow=%+v err=%v", stored, err)
	}
}

func TestCompletionRejectsObserverReceiptAtOrBeforeApproval(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	observer := &completionObserverStub{mutate: func(receipt *CompletionReceipt) {
		receipt.BlockTimestamp = uint64(now.Unix())
	}}
	service, _ := New(NewMemoryStore(), observer, func() time.Time { return now }, workflowRandom(2))
	workflow, err := service.Create(t.Context(), actor("signer", SignerOperator, now), "create_premature",
		testCreateRequest(SignerCaps, 506))
	if err != nil {
		t.Fatal(err)
	}
	workflow, err = service.Approve(t.Context(), actor("owner", OrgAdmin, now), workflow.WorkflowID, "approve_premature")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ObserveAndComplete(t.Context(), "org_a", workflow.WorkflowID); !errors.Is(err, ErrInvalidReceipt) {
		t.Fatalf("pre-approval observer output error=%v", err)
	}
}

type completionObserverStub struct {
	calls           int
	err             error
	transactionHash string
	logIndex        uint64
	mutate          func(*CompletionReceipt)
}

type testActionGate struct{}

func (testActionGate) ValidateGovernanceAction(governanceworkflow.BoundAction) error { return nil }

type rejectableActionGate struct{ err error }

func (g *rejectableActionGate) ValidateGovernanceAction(governanceworkflow.BoundAction) error {
	return g.err
}

func (s *completionObserverStub) ValidateGovernanceAction(bound governanceworkflow.BoundAction) error {
	return testActionGate{}.ValidateGovernanceAction(bound)
}

func (s *completionObserverStub) ObserveWorkflowCompletion(_ context.Context, workflow Workflow) (CompletionReceipt, error) {
	s.calls++
	if s.err != nil {
		return CompletionReceipt{}, s.err
	}
	receipt := testReceipt(workflow)
	if s.transactionHash != "" {
		receipt.TransactionHash = s.transactionHash
	}
	if s.logIndex != 0 {
		receipt.LogIndex = s.logIndex
		receipt.ActionLogIndexes = []uint64{s.logIndex - 1}
	}
	if s.mutate != nil {
		s.mutate(&receipt)
	}
	return receipt, nil
}

func actor(id string, role Role, now time.Time) Actor {
	return Actor{OrganizationID: "org_a", PrincipalID: id, Role: role, StepUpAt: now.Add(-time.Minute), StepUpUntil: now.Add(4 * time.Minute)}
}

func testHash(value uint64) string { return fmt.Sprintf("0x%064x", value) }

func testReceipt(workflow Workflow) CompletionReceipt {
	return CompletionReceipt{
		WorkflowID: workflow.WorkflowID, PayloadHash: workflow.PayloadHash, ChainID: workflow.ChainID,
		TransactionHash: workflow.WorkflowID, BlockNumber: 100, BlockHash: testHash(11), BlockTimestamp: 1_800_000_001,
		ConfirmedHead: 130, FinalizedHead: 120, LogIndex: 2,
		ContractAddress: workflow.ContractAddress, EventSignature: GovernanceWorkflowBoundTopic,
		FunctionSelector: workflow.FunctionSelector, ActionEventSignature: testHash(13), ActionLogIndexes: []uint64{1},
		Observers: []string{"rpc_a", "rpc_b"}, EvidenceDigest: testHash(14), Finality: "FINALIZED",
	}
}

func testCreateRequest(kind Kind, id uint64) CreateRequest {
	workflowID := testHash(id)
	request := CreateRequest{Kind: kind, WorkflowID: workflowID}
	switch kind {
	case PayoutChange:
		request.Action = &governanceworkflow.Action{Type: governanceworkflow.ActionDirectoryApprove, ChainID: 84532,
			ContractAddress: "0x1111111111111111111111111111111111111111",
			DirectoryApprove: &governanceworkflow.DirectoryApproveAction{
				Proposal: governanceworkflow.DirectoryProposal{
					VersionID: id + 1, PreviousVersion: id, PreviousRoot: testHash(id + 10), NewRoot: testHash(id + 11),
					BlobContentHash: testHash(id + 12), LocationsHash: testHash(id + 13), ChangeClass: 2,
					RequestedActivatesAt: 1_800_100_000,
				},
				ProposerNonce: fmt.Sprint(id + 14),
			},
		}
	case SignerCaps:
		request.Action = &governanceworkflow.Action{Type: governanceworkflow.ActionSpendCaps, ChainID: 84532,
			ContractAddress: "0x2222222222222222222222222222222222222222",
			SpendCaps: &governanceworkflow.SpendCapsAction{
				Current: governanceworkflow.Caps{PerTransaction: "100", PerDay: "200", AllowanceCeiling: "300"},
				Next:    governanceworkflow.Caps{PerTransaction: "101", PerDay: "201", AllowanceCeiling: "301"},
			},
		}
	case VerifierGovernance:
		request.Action = &governanceworkflow.Action{Type: governanceworkflow.ActionCallEscrowAddVerifier, ChainID: 84532,
			ContractAddress: "0x3333333333333333333333333333333333333333",
			CallEscrowAddVerifier: &governanceworkflow.CallEscrowAddVerifierAction{
				Key: "0x4444444444444444444444444444444444444444", NextEpoch: id + 1,
			},
		}
	case BreakGlass:
		request.Action = &governanceworkflow.Action{Type: governanceworkflow.ActionCallEscrowPause, ChainID: 84532,
			ContractAddress: "0x3333333333333333333333333333333333333333",
			CallEscrowPause: &governanceworkflow.CallEscrowPauseAction{},
		}
	case ModuleGovernance:
		request.Action = &governanceworkflow.Action{Type: governanceworkflow.ActionSpendAuthorizer, ChainID: 84532,
			ContractAddress: "0x2222222222222222222222222222222222222222",
			SpendAuthorizer: &governanceworkflow.SpendAuthorizerAction{
				Current: "0x5555555555555555555555555555555555555555", CurrentEpoch: id + 1,
				Next: "0x6666666666666666666666666666666666666666",
			},
		}
	case DirectoryCancel:
		request.Action = &governanceworkflow.Action{Type: governanceworkflow.ActionDirectoryCancel, ChainID: 84532,
			ContractAddress: "0x1111111111111111111111111111111111111111",
			DirectoryCancel: &governanceworkflow.DirectoryCancelAction{VersionID: id + 1, ProposalHash: testHash(id + 10)},
		}
	case ProductionGate, RoleAdmin:
		request.WorkflowID = ""
		request.PayloadHash = testHash(id)
	}
	return request
}

func workflowRandom(count int) *bytes.Reader {
	values := make([]byte, 0, count*32)
	for index := 1; index <= count; index++ {
		values = append(values, bytes.Repeat([]byte{byte(index)}, 32)...)
	}
	return bytes.NewReader(values)
}
