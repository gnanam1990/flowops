package controlapi

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/gnanam1990/flowops/internal/ascpworkflow"
)

func TestASCPWorkflowRealPostgresConcurrentDecisionAndImmutableAudit(t *testing.T) {
	db := ascpIntegrationDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := db.ExecContext(ctx, `INSERT INTO organizations (id,name) VALUES ('org_workflow_it','Workflow Integration'),('org_workflow_other','Other Workflow')`); err != nil {
		t.Fatal(err)
	}
	store, err := ascpworkflow.NewPostgresStore(db)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	service, err := ascpworkflow.New(store, nil, func() time.Time { return now }, nil)
	if err != nil {
		t.Fatal(err)
	}
	actor := func(id string, role ascpworkflow.Role) ascpworkflow.Actor {
		return ascpworkflow.Actor{OrganizationID: "org_workflow_it", PrincipalID: id, Role: role,
			StepUpAt: now.Add(-time.Minute), StepUpUntil: now.Add(time.Minute)}
	}
	workflow, err := service.Create(ctx, actor("seller_it", ascpworkflow.SellerAdmin), "create_once", ascpworkflow.CreateRequest{
		Kind: ascpworkflow.PayoutChange, PayloadHash: fmt.Sprintf("0x%064x", 1),
	})
	if err != nil {
		t.Fatal(err)
	}
	type result struct {
		workflow ascpworkflow.Workflow
		err      error
	}
	start := make(chan struct{})
	results := make(chan result, 20)
	var group sync.WaitGroup
	for index := 0; index < 20; index++ {
		index := index
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			current := actor(fmt.Sprintf("owner_%d", index), ascpworkflow.OrgAdmin)
			var item ascpworkflow.Workflow
			var err error
			if index%2 == 0 {
				item, err = service.Approve(ctx, current, workflow.WorkflowID, fmt.Sprintf("approve_%d", index))
			} else {
				item, err = service.Cancel(ctx, current, workflow.WorkflowID, fmt.Sprintf("cancel_%d", index))
			}
			results <- result{item, err}
		}()
	}
	close(start)
	group.Wait()
	close(results)
	terminal := ascpworkflow.State("")
	for current := range results {
		if current.err != nil {
			t.Fatal(current.err)
		}
		if terminal == "" {
			terminal = current.workflow.State
		}
		if current.workflow.State != terminal {
			t.Fatalf("split terminal outcomes: %s and %s", terminal, current.workflow.State)
		}
	}
	var workflowRows, events, outbox int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM ascp_proposal_workflows WHERE workflow_id=$1`, workflow.WorkflowID).Scan(&workflowRows); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM ascp_workflow_events WHERE workflow_id=$1`, workflow.WorkflowID).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM ascp_workflow_outbox WHERE workflow_id=$1`, workflow.WorkflowID).Scan(&outbox); err != nil {
		t.Fatal(err)
	}
	if workflowRows != 1 || events != 2 || outbox != 2 {
		t.Fatalf("workflow=%d events=%d outbox=%d", workflowRows, events, outbox)
	}
	if _, err := db.ExecContext(ctx, `UPDATE ascp_proposal_workflows SET payload_hash=$2 WHERE workflow_id=$1`, workflow.WorkflowID, fmt.Sprintf("0x%064x", 2)); err == nil {
		t.Fatal("database accepted mutable workflow payload")
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM ascp_workflow_events WHERE workflow_id=$1`, workflow.WorkflowID); err == nil {
		t.Fatal("database accepted workflow event deletion")
	}
	if _, err := db.ExecContext(ctx, `TRUNCATE ascp_workflow_outbox`); err == nil {
		t.Fatal("database accepted workflow outbox truncation")
	}
	other := actor("other_owner", ascpworkflow.OrgAdmin)
	other.OrganizationID = "org_workflow_other"
	if _, err := service.Get(ctx, other, workflow.WorkflowID); !errors.Is(err, ascpworkflow.ErrNotFound) {
		t.Fatalf("cross-tenant read error=%v", err)
	}

	verifiedService, err := ascpworkflow.New(store, workflowCompletionObserver{}, func() time.Time { return now }, nil)
	if err != nil {
		t.Fatal(err)
	}
	chainWorkflow, err := verifiedService.Create(ctx, actor("signer_it", ascpworkflow.SignerOperator), "create_chain", ascpworkflow.CreateRequest{
		Kind: ascpworkflow.SignerCaps, PayloadHash: fmt.Sprintf("0x%064x", 3),
	})
	if err != nil {
		t.Fatal(err)
	}
	chainWorkflow, err = verifiedService.Approve(ctx, actor("chain_owner", ascpworkflow.OrgAdmin), chainWorkflow.WorkflowID, "approve_chain")
	if err != nil || chainWorkflow.State != ascpworkflow.ApprovedPendingChain {
		t.Fatalf("pending chain=%+v err=%v", chainWorkflow, err)
	}
	completed, err := verifiedService.ObserveAndComplete(ctx, "org_workflow_it", chainWorkflow.WorkflowID)
	if err != nil || completed.State != ascpworkflow.Approved || completed.CompletionDigest == "" {
		t.Fatalf("completed=%+v err=%v", completed, err)
	}
	replay, err := verifiedService.ObserveAndComplete(ctx, "org_workflow_it", chainWorkflow.WorkflowID)
	if err != nil || !replay.Replayed || replay.CompletionDigest != completed.CompletionDigest {
		t.Fatalf("completion replay=%+v err=%v", replay, err)
	}
	var receiptOwners int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM ascp_workflow_receipt_ownership WHERE workflow_id=$1`, chainWorkflow.WorkflowID).Scan(&receiptOwners); err != nil || receiptOwners != 1 {
		t.Fatalf("receipt owners=%d err=%v", receiptOwners, err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM ascp_workflow_receipt_ownership WHERE workflow_id=$1`, chainWorkflow.WorkflowID); err == nil {
		t.Fatal("database accepted workflow receipt ownership deletion")
	}
	conflict, err := verifiedService.Create(ctx, actor("signer_conflict", ascpworkflow.SignerOperator), "create_chain_conflict", ascpworkflow.CreateRequest{
		Kind: ascpworkflow.SignerCaps, PayloadHash: fmt.Sprintf("0x%064x", 30),
	})
	if err != nil {
		t.Fatal(err)
	}
	conflict, err = verifiedService.Approve(ctx, actor("chain_owner_conflict", ascpworkflow.OrgAdmin), conflict.WorkflowID, "approve_chain_conflict")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := verifiedService.ObserveAndComplete(ctx, "org_workflow_it", conflict.WorkflowID); !errors.Is(err, ascpworkflow.ErrReceiptOwned) {
		t.Fatalf("shared receipt ownership error=%v", err)
	}
	storedConflict, err := verifiedService.Get(ctx, actor("chain_owner_conflict", ascpworkflow.OrgAdmin), conflict.WorkflowID)
	if err != nil || storedConflict.State != ascpworkflow.ApprovedPendingChain {
		t.Fatalf("conflicting receipt changed workflow=%+v err=%v", storedConflict, err)
	}
}

type workflowCompletionObserver struct{}

func (workflowCompletionObserver) ObserveWorkflowCompletion(_ context.Context, workflow ascpworkflow.Workflow) (ascpworkflow.CompletionReceipt, error) {
	return ascpworkflow.CompletionReceipt{
		WorkflowID: workflow.WorkflowID, PayloadHash: workflow.PayloadHash, ChainID: 84532,
		TransactionHash: fmt.Sprintf("0x%064x", 4), BlockNumber: 99, BlockHash: fmt.Sprintf("0x%064x", 5),
		BlockTimestamp: uint64(workflow.ApprovedAt + 1), ConfirmedHead: 130, FinalizedHead: 120, LogIndex: 2,
		ContractAddress: "0x1111111111111111111111111111111111111111",
		EventSignature:  ascpworkflow.GovernanceWorkflowBoundTopic, FunctionSelector: "0x12345678",
		ActionEventSignature: fmt.Sprintf("0x%064x", 7), ActionLogIndexes: []uint64{1},
		Observers: []string{"rpc_a", "rpc_b"}, EvidenceDigest: fmt.Sprintf("0x%064x", 8), Finality: "FINALIZED",
	}, nil
}
