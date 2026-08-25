package controlapi

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
	"github.com/gnanam1990/flowops/pkg/governanceworkflow"
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
	service, err := ascpworkflow.New(store, nil, func() time.Time { return now }, nil,
		ascpworkflow.WithGovernanceActionGate(testGovernanceActionGate{}))
	if err != nil {
		t.Fatal(err)
	}
	actor := func(id string, role ascpworkflow.Role) ascpworkflow.Actor {
		return ascpworkflow.Actor{OrganizationID: "org_workflow_it", PrincipalID: id, Role: role,
			StepUpAt: now.Add(-time.Minute), StepUpUntil: now.Add(time.Minute)}
	}
	workflow, err := service.Create(ctx, actor("seller_it", ascpworkflow.SellerAdmin), "create_once", payoutWorkflowCreateRequest(101))
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
	wantOutbox := 2
	if terminal == ascpworkflow.ApprovedPendingChain {
		wantOutbox = 3
	}
	if workflowRows != 1 || events != 2 || outbox != wantOutbox {
		t.Fatalf("workflow=%d events=%d outbox=%d", workflowRows, events, outbox)
	}
	auditOwner := actor("audit_owner", ascpworkflow.OrgAdmin)
	if replay, err := service.Approve(ctx, auditOwner, workflow.WorkflowID, "terminal_noop"); err != nil || !replay.Replayed {
		t.Fatalf("terminal no-op replay=%+v err=%v", replay, err)
	}
	secondWorkflow, err := service.Create(ctx, actor("seller_second", ascpworkflow.SellerAdmin), "create_second", payoutWorkflowCreateRequest(102))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Approve(ctx, auditOwner, secondWorkflow.WorkflowID, "terminal_noop"); !errors.Is(err, ascpworkflow.ErrIdempotencyConflict) {
		t.Fatalf("terminal no-op key reuse error=%v", err)
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
	chainWorkflow, err := verifiedService.Create(ctx, actor("signer_it", ascpworkflow.SignerOperator), "create_chain", signerCapsWorkflowCreateRequest(103))
	if err != nil {
		t.Fatal(err)
	}
	chainWorkflow, err = verifiedService.Approve(ctx, actor("chain_owner", ascpworkflow.OrgAdmin), chainWorkflow.WorkflowID, "approve_chain")
	if err != nil || chainWorkflow.State != ascpworkflow.ApprovedPendingChain {
		t.Fatalf("pending chain=%+v err=%v", chainWorkflow, err)
	}
	chainTransactionHash := fmt.Sprintf("0x%064x", 4)
	if submitted, err := verifiedService.RecordSubmission(ctx, "org_workflow_it", chainWorkflow.WorkflowID, chainTransactionHash); err == nil {
		t.Fatalf("blind submission bypassed relay/receipt ownership: %+v", submitted)
	}
	completed, err := verifiedService.ObserveAndComplete(ctx, "org_workflow_it", chainWorkflow.WorkflowID)
	if err != nil || completed.State != ascpworkflow.Finalized || completed.CompletionDigest == "" {
		t.Fatalf("completed=%+v err=%v", completed, err)
	}
	replay, err := verifiedService.ObserveAndComplete(ctx, "org_workflow_it", chainWorkflow.WorkflowID)
	if err != nil || !replay.Replayed || replay.CompletionDigest != completed.CompletionDigest {
		t.Fatalf("completion replay=%+v err=%v", replay, err)
	}
	var proposedEvents, pendingEvents, submittedEvents, confirmedEvents, finalizedEvents, executionCommands int
	if err := db.QueryRowContext(ctx, `SELECT
		count(*) FILTER (WHERE event_kind='PROPOSED'),
		count(*) FILTER (WHERE event_kind='APPROVED_PENDING_CHAIN'),
		count(*) FILTER (WHERE event_kind='SUBMITTED'),
		count(*) FILTER (WHERE event_kind='CONFIRMED'),
		count(*) FILTER (WHERE event_kind='FINALIZED')
		FROM ascp_workflow_events WHERE workflow_id=$1`, chainWorkflow.WorkflowID).
		Scan(&proposedEvents, &pendingEvents, &submittedEvents, &confirmedEvents, &finalizedEvents); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM ascp_workflow_outbox
		WHERE workflow_id=$1 AND topic='ascp.governance.execute'`, chainWorkflow.WorkflowID).Scan(&executionCommands); err != nil {
		t.Fatal(err)
	}
	if proposedEvents != 1 || pendingEvents != 1 || submittedEvents != 1 || confirmedEvents != 1 || finalizedEvents != 1 || executionCommands != 1 {
		t.Fatalf("events proposed=%d pending=%d submitted=%d confirmed=%d finalized=%d execute=%d",
			proposedEvents, pendingEvents, submittedEvents, confirmedEvents, finalizedEvents, executionCommands)
	}
	var commandBytes []byte
	if err := db.QueryRowContext(ctx, `SELECT payload_json FROM ascp_workflow_outbox
		WHERE workflow_id=$1 AND topic='ascp.governance.execute'`, chainWorkflow.WorkflowID).Scan(&commandBytes); err != nil {
		t.Fatal(err)
	}
	var command ascpworkflow.GovernanceExecutionCommand
	if err := json.Unmarshal(commandBytes, &command); err != nil {
		t.Fatal(err)
	}
	if command.Version != ascpworkflow.GovernanceExecutionVersion || command.WorkflowID != chainWorkflow.WorkflowID ||
		command.OrganizationID != chainWorkflow.OrganizationID || command.PayloadHash != chainWorkflow.PayloadHash ||
		command.ChainID != chainWorkflow.ChainID || command.ContractAddress != chainWorkflow.ContractAddress ||
		command.FunctionSelector != chainWorkflow.FunctionSelector || command.Calldata != chainWorkflow.Calldata ||
		command.Operation != "CALL" || command.Value != "0" || len(command.GovernanceAction) == 0 ||
		command.ApprovedBy != chainWorkflow.ApprovedBy || command.ApprovedAt != chainWorkflow.ApprovedAt ||
		command.ExecuteAfter != chainWorkflow.ApprovedAt+1 || command.ApprovalActionHash == "" {
		t.Fatalf("execution command=%+v", command)
	}
	var receiptOwners int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM ascp_workflow_receipt_ownership WHERE workflow_id=$1`, chainWorkflow.WorkflowID).Scan(&receiptOwners); err != nil || receiptOwners != 1 {
		t.Fatalf("receipt owners=%d err=%v", receiptOwners, err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM ascp_workflow_receipt_ownership WHERE workflow_id=$1`, chainWorkflow.WorkflowID); err == nil {
		t.Fatal("database accepted workflow receipt ownership deletion")
	}
	conflict, err := verifiedService.Create(ctx, actor("signer_conflict", ascpworkflow.SignerOperator), "create_chain_conflict", signerCapsWorkflowCreateRequest(130))
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

func TestASCPGovernanceLifecycleMigrationFailsLegacyActionsClosed(t *testing.T) {
	db := ascpRawIntegrationDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := db.ExecContext(ctx, `CREATE TABLE flowops_schema_migrations (
		name text PRIMARY KEY, checksum text NOT NULL, applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		t.Fatal(err)
	}
	manifest, err := MigrationManifest()
	if err != nil {
		t.Fatal(err)
	}
	for _, migration := range manifest {
		if strings.HasPrefix(migration.Name, "0029_") {
			break
		}
		script, err := migrationFiles.ReadFile("migrations/" + migration.Name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, string(script)); err != nil {
			t.Fatalf("apply legacy migration %s: %v", migration.Name, err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO flowops_schema_migrations (name,checksum) VALUES ($1,$2)`, migration.Name, migration.Checksum); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC().Truncate(time.Second)
	if _, err := db.ExecContext(ctx, `INSERT INTO organizations (id,name) VALUES ('org_workflow_upgrade','Workflow Upgrade')`); err != nil {
		t.Fatal(err)
	}
	legacyProposed := fmt.Sprintf("0x%064x", 901)
	legacyApproved := fmt.Sprintf("0x%064x", 902)
	legacyLocalApproved := fmt.Sprintf("0x%064x", 907)
	if _, err := db.ExecContext(ctx, `INSERT INTO ascp_proposal_workflows
		(workflow_id,organization_id,kind,payload_hash,proposed_by,proposer_role,proposer_step_up_at,
		 proposer_step_up_until,state,proposed_at,expires_at)
		VALUES ($1,'org_workflow_upgrade','SIGNER_CAPS',$2,'legacy_signer','SIGNER_OPERATOR',$3,$4,'PROPOSED',$3,$5)`,
		legacyProposed, fmt.Sprintf("0x%064x", 903), now.Add(-time.Minute), now.Add(4*time.Minute), now.Add(24*time.Hour-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO ascp_proposal_workflows
		(workflow_id,organization_id,kind,payload_hash,proposed_by,proposer_role,proposer_step_up_at,
		 proposer_step_up_until,state,proposed_at,expires_at,approved_by,approver_role,
		 approver_step_up_at,approver_step_up_until,approved_at)
		VALUES ($1,'org_workflow_upgrade','PRODUCTION_GATE',$2,'legacy_owner_a','ORG_ADMIN',$3,$4,
		        'APPROVED',$3,$5,'legacy_owner_b','ORG_ADMIN',$3,$4,$6)`,
		legacyLocalApproved, fmt.Sprintf("0x%064x", 908), now.Add(-time.Minute), now.Add(4*time.Minute),
		now.Add(24*time.Hour-time.Minute), now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO ascp_workflow_actions
		(organization_id,actor_id,action,idempotency_key,input_hash,workflow_id,result_state,created_at)
		VALUES ('org_workflow_upgrade','legacy_owner_b','COMPLETE','legacy_complete',$1,$2,'APPROVED',$3)`,
		fmt.Sprintf("0x%064x", 909), legacyLocalApproved, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO ascp_workflow_events
		(event_id,workflow_id,organization_id,actor_id,event_kind,event_json,created_at)
		VALUES ($1,$2,'org_workflow_upgrade','legacy_owner_b','APPROVED',$3::jsonb,$4)`,
		fmt.Sprintf("0x%064x", 910), legacyLocalApproved, `{}`, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO ascp_proposal_workflows
		(workflow_id,organization_id,kind,payload_hash,proposed_by,proposer_role,proposer_step_up_at,
		 proposer_step_up_until,state,proposed_at,expires_at,approved_by,approver_role,
		 approver_step_up_at,approver_step_up_until,approved_at)
		VALUES ($1,'org_workflow_upgrade','SIGNER_CAPS',$2,'legacy_signer','SIGNER_OPERATOR',$3,$4,
		        'APPROVED_PENDING_CHAIN',$3,$5,'legacy_owner','ORG_ADMIN',$3,$4,$6)`,
		legacyApproved, fmt.Sprintf("0x%064x", 903), now.Add(-time.Minute), now.Add(4*time.Minute),
		now.Add(24*time.Hour-time.Minute), now); err != nil {
		t.Fatal(err)
	}
	if err := ApplyMigrations(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err := ApplyMigrations(ctx, db); err != nil {
		t.Fatalf("governance lifecycle migration replay=%v", err)
	}
	var proposedState, approvedState, approvedReason string
	if err := db.QueryRowContext(ctx, `SELECT state FROM ascp_proposal_workflows WHERE workflow_id=$1`, legacyProposed).Scan(&proposedState); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT state,terminal_reason FROM ascp_proposal_workflows WHERE workflow_id=$1`, legacyApproved).Scan(&approvedState, &approvedReason); err != nil {
		t.Fatal(err)
	}
	if proposedState != "EXPIRED" || approvedState != "REQUIRES_REAPPROVAL" || approvedReason != "PRECONDITION_CHANGED" {
		t.Fatalf("legacy proposed=%s approved=%s/%s", proposedState, approvedState, approvedReason)
	}
	var localState, legacyAction, legacyResult, legacyEvent string
	if err := db.QueryRowContext(ctx, `SELECT workflow.state,action.action,action.result_state,event.event_kind
		FROM ascp_proposal_workflows workflow
		JOIN ascp_workflow_actions action USING (workflow_id)
		JOIN ascp_workflow_events event USING (workflow_id)
		WHERE workflow.workflow_id=$1`, legacyLocalApproved).Scan(&localState, &legacyAction, &legacyResult, &legacyEvent); err != nil {
		t.Fatal(err)
	}
	if localState != "FINALIZED" || legacyAction != "COMPLETE" || legacyResult != "APPROVED" || legacyEvent != "APPROVED" {
		t.Fatalf("legacy immutable history state=%s action=%s result=%s event=%s", localState, legacyAction, legacyResult, legacyEvent)
	}
	for _, workflowID := range []string{legacyProposed, legacyApproved} {
		var actions, events, outbox int
		if err := db.QueryRowContext(ctx, `SELECT
			(SELECT count(*) FROM ascp_workflow_actions WHERE workflow_id=$1 AND actor_id='SYSTEM_MIGRATION'),
			(SELECT count(*) FROM ascp_workflow_events WHERE workflow_id=$1 AND actor_id='SYSTEM_MIGRATION'),
			(SELECT count(*) FROM ascp_workflow_outbox WHERE workflow_id=$1)`, workflowID).Scan(&actions, &events, &outbox); err != nil {
			t.Fatal(err)
		}
		if actions != 1 || events != 1 || outbox != 1 {
			t.Fatalf("legacy workflow %s actions=%d events=%d outbox=%d", workflowID, actions, events, outbox)
		}
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO ascp_proposal_workflows
		(workflow_id,organization_id,kind,payload_hash,proposed_by,proposer_role,proposer_step_up_at,
		 proposer_step_up_until,state,proposed_at,expires_at)
		VALUES ($1,'org_workflow_upgrade','SIGNER_CAPS',$2,'new_signer','SIGNER_OPERATOR',$3,$4,'PROPOSED',$3,$5)`,
		fmt.Sprintf("0x%064x", 904), fmt.Sprintf("0x%064x", 905), now, now.Add(4*time.Minute), now.Add(24*time.Hour)); err == nil {
		t.Fatal("migration accepted a new chain proposal without server-derived executable action")
	}
	store, err := ascpworkflow.NewPostgresStore(db)
	if err != nil {
		t.Fatal(err)
	}
	service, err := ascpworkflow.New(store, nil, func() time.Time { return now }, nil,
		ascpworkflow.WithGovernanceActionGate(testGovernanceActionGate{}))
	if err != nil {
		t.Fatal(err)
	}
	created, err := service.Create(ctx, ascpworkflow.Actor{OrganizationID: "org_workflow_upgrade", PrincipalID: "new_signer",
		Role: ascpworkflow.SignerOperator, StepUpAt: now.Add(-time.Minute), StepUpUntil: now.Add(4 * time.Minute)},
		"typed_after_upgrade", signerCapsWorkflowCreateRequest(906))
	if err != nil || created.Calldata == "" || len(created.GovernanceAction) == 0 {
		t.Fatalf("typed post-upgrade workflow=%+v err=%v", created, err)
	}
	created, err = service.Approve(ctx, ascpworkflow.Actor{OrganizationID: "org_workflow_upgrade", PrincipalID: "new_owner",
		Role: ascpworkflow.OrgAdmin, StepUpAt: now.Add(-time.Minute), StepUpUntil: now.Add(4 * time.Minute)},
		created.WorkflowID, "typed_after_upgrade_approve")
	if err != nil {
		t.Fatal(err)
	}
	created, err = service.RecordChainFailure(ctx, "org_workflow_upgrade", created.WorkflowID,
		ascpworkflow.TimedOut, ascpworkflow.SubmissionTimeout)
	if err != nil || created.State != ascpworkflow.TimedOut {
		t.Fatalf("timed-out post-upgrade workflow=%+v err=%v", created, err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE ascp_proposal_workflows SET state='SUBMITTED',
		submission_transaction_hash=$2,submitted_at=$3,terminal_reason=NULL,terminal_at=NULL WHERE workflow_id=$1`,
		created.WorkflowID, fmt.Sprintf("0x%064x", 913), now); err == nil {
		t.Fatal("migration allowed an unproved timed-out workflow resubmission")
	}
	if _, err := db.ExecContext(ctx, `UPDATE ascp_proposal_workflows SET calldata=$2 WHERE workflow_id=$1`,
		created.WorkflowID, created.FunctionSelector+strings.Repeat("0", 64)); err == nil {
		t.Fatal("migration allowed immutable governance calldata mutation")
	}
	localProposed := fmt.Sprintf("0x%064x", 911)
	if _, err := db.ExecContext(ctx, `INSERT INTO ascp_proposal_workflows
		(workflow_id,organization_id,kind,payload_hash,proposed_by,proposer_role,proposer_step_up_at,
		 proposer_step_up_until,state,proposed_at,expires_at)
		VALUES ($1,'org_workflow_upgrade','PRODUCTION_GATE',$2,'new_owner','ORG_ADMIN',$3,$4,'PROPOSED',$3,$5)`,
		localProposed, fmt.Sprintf("0x%064x", 912), now, now.Add(4*time.Minute), now.Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE ascp_proposal_workflows SET state='APPROVED_PENDING_CHAIN',
		approved_by='other_owner',approver_role='ORG_ADMIN',approver_step_up_at=$2,
		approver_step_up_until=$3,approved_at=$2 WHERE workflow_id=$1`, localProposed, now, now.Add(4*time.Minute)); err == nil {
		t.Fatal("migration allowed a local workflow to enter a chain lifecycle state")
	}
}

type workflowCompletionObserver struct{}

func (workflowCompletionObserver) ValidateGovernanceAction(governanceworkflow.BoundAction) error {
	return nil
}

func (workflowCompletionObserver) ObserveWorkflowCompletion(_ context.Context, workflow ascpworkflow.Workflow) (ascpworkflow.CompletionReceipt, error) {
	return ascpworkflow.CompletionReceipt{
		WorkflowID: workflow.WorkflowID, PayloadHash: workflow.PayloadHash, ChainAction: workflow.ChainAction, ChainID: workflow.ChainID,
		TransactionHash: fmt.Sprintf("0x%064x", 4), BlockNumber: 99, BlockHash: fmt.Sprintf("0x%064x", 5),
		BlockTimestamp: uint64(workflow.ApprovedAt + 1), ConfirmedHead: 130, FinalizedHead: 120, LogIndex: 2,
		ContractAddress: workflow.ContractAddress,
		EventSignature:  ascpworkflow.GovernanceWorkflowBoundTopic, FunctionSelector: workflow.FunctionSelector,
		ActionEventSignature: fmt.Sprintf("0x%064x", 7), ActionLogIndexes: []uint64{1},
		Observers: []string{"rpc_a", "rpc_b"}, EvidenceDigest: fmt.Sprintf("0x%064x", 8), Finality: "FINALIZED",
	}, nil
}
