package ascpworkflow

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestKindRoleMatrixDualControlAndReceiptGate(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	clock := func() time.Time { return now }
	observer := &completionObserverStub{}
	service, err := New(NewMemoryStore(), observer, clock, workflowRandom(16))
	if err != nil {
		t.Fatal(err)
	}
	roles := map[Kind]struct{ proposer, approver Role }{
		PayoutChange: {SellerAdmin, OrgAdmin}, SignerCaps: {SignerOperator, OrgAdmin},
		VerifierGovernance: {SignerOperator, OrgAdmin}, ProductionGate: {OrgAdmin, OrgAdmin},
		BreakGlass: {OrgAdmin, IncidentResponder}, RoleAdmin: {OrgAdmin, OrgAdmin},
		ModuleGovernance: {SignerOperator, OrgAdmin}, DirectoryCancel: {SellerAdmin, OrgAdmin},
	}
	for kind, pair := range roles {
		t.Run(string(kind), func(t *testing.T) {
			proposer := actor("proposer", pair.proposer, now)
			workflow, err := service.Create(t.Context(), proposer, "create_"+string(kind), CreateRequest{Kind: kind, PayloadHash: testHash(1)})
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
				want = Approved
			}
			if err != nil || approved.State != want || approved.ApprovedBy != "approver" {
				t.Fatalf("approved=%+v err=%v want=%s", approved, err, want)
			}
			if want == ApprovedPendingChain {
				completed, err := service.ObserveAndComplete(t.Context(), "org_a", workflow.WorkflowID)
				if err != nil || completed.State != Approved || completed.CompletionDigest == "" || observer.calls != 1 {
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

func TestCompletionReceiptOwnershipIsAtomicAcrossWorkflows(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	observer := &completionObserverStub{transactionHash: testHash(900), logIndex: 7}
	service, err := New(NewMemoryStore(), observer, func() time.Time { return now }, workflowRandom(4))
	if err != nil {
		t.Fatal(err)
	}
	createApproved := func(proposer, owner, key string) Workflow {
		workflow, err := service.Create(t.Context(), actor(proposer, SignerOperator, now), "create_"+key,
			CreateRequest{Kind: SignerCaps, PayloadHash: testHash(uint64(len(key) + 100))})
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
	service, _ := New(NewMemoryStore(), nil, func() time.Time { return now }, bytes.NewReader(bytes.Repeat([]byte{8}, 256)))
	proposer := actor("seller", SellerAdmin, now)
	request := CreateRequest{Kind: PayoutChange, PayloadHash: testHash(2)}
	workflow, err := service.Create(t.Context(), proposer, "same_key", request)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := service.Create(t.Context(), proposer, "same_key", request)
	if err != nil || !replay.Replayed || replay.WorkflowID != workflow.WorkflowID {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
	changed := request
	changed.PayloadHash = testHash(3)
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

	expiring, err := service.Create(t.Context(), proposer, "expiring", request)
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
	service, err := New(NewMemoryStore(), nil, func() time.Time { return now }, bytes.NewReader(bytes.Repeat([]byte{6}, 32)))
	if err != nil {
		t.Fatal(err)
	}
	proposer := Actor{OrganizationID: "org_a", PrincipalID: "seller", Role: SellerAdmin,
		StepUpAt: time.Unix(1_800_000_000, 500_000_000).UTC(), StepUpUntil: now.Add(time.Minute)}
	request := CreateRequest{Kind: PayoutChange, PayloadHash: testHash(30)}
	created, err := service.Create(t.Context(), proposer, "random_independent", request)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := service.Create(t.Context(), proposer, "random_independent", request)
	if err != nil || !replay.Replayed || replay.WorkflowID != created.WorkflowID {
		t.Fatalf("replay after random exhaustion=%+v err=%v", replay, err)
	}
}

func TestTwentyConcurrentWorkflowDecisionsHaveOneTerminalTransition(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	service, _ := New(NewMemoryStore(), nil, func() time.Time { return now }, bytes.NewReader(bytes.Repeat([]byte{9}, 64)))
	workflow, err := service.Create(t.Context(), actor("seller", SellerAdmin, now), "create", CreateRequest{Kind: PayoutChange, PayloadHash: testHash(4)})
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
	workflow, err := service.Create(t.Context(), actor("signer", SignerOperator, now), "create_rejected", CreateRequest{Kind: SignerCaps, PayloadHash: testHash(20)})
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

func TestCompletionRejectsMalformedObserverOutputBeforePersistence(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	observer := &completionObserverStub{mutate: func(receipt *CompletionReceipt) {
		receipt.EventSignature = testHash(404)
	}}
	service, _ := New(NewMemoryStore(), observer, func() time.Time { return now }, workflowRandom(2))
	workflow, err := service.Create(t.Context(), actor("signer", SignerOperator, now), "create_malformed",
		CreateRequest{Kind: SignerCaps, PayloadHash: testHash(405)})
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
		CreateRequest{Kind: SignerCaps, PayloadHash: testHash(406)})
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
		WorkflowID: workflow.WorkflowID, PayloadHash: workflow.PayloadHash, ChainID: 84532,
		TransactionHash: workflow.WorkflowID, BlockNumber: 100, BlockHash: testHash(11), BlockTimestamp: 1_800_000_001,
		ConfirmedHead: 130, FinalizedHead: 120, LogIndex: 2,
		ContractAddress: "0x1111111111111111111111111111111111111111", EventSignature: GovernanceWorkflowBoundTopic,
		FunctionSelector: "0x12345678", ActionEventSignature: testHash(13), ActionLogIndexes: []uint64{1},
		Observers: []string{"rpc_a", "rpc_b"}, EvidenceDigest: testHash(14), Finality: "FINALIZED",
	}
}

func workflowRandom(count int) *bytes.Reader {
	values := make([]byte, 0, count*32)
	for index := 1; index <= count; index++ {
		values = append(values, bytes.Repeat([]byte{byte(index)}, 32)...)
	}
	return bytes.NewReader(values)
}
