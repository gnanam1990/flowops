package ascpworkflow

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

type PostgresStore struct{ db *sql.DB }

func NewPostgresStore(db *sql.DB) (*PostgresStore, error) {
	if db == nil {
		return nil, errors.New("database is required")
	}
	return &PostgresStore{db: db}, nil
}

func (s *PostgresStore) ReplayCreate(ctx context.Context, actor Actor, key, inputHash string) (Workflow, bool, error) {
	tx, err := s.begin(ctx)
	if err != nil {
		return Workflow{}, false, err
	}
	defer tx.Rollback()
	workflow, replayed, err := lookupAction(ctx, tx, actor.OrganizationID, actor.PrincipalID, "CREATE", key, inputHash)
	if err != nil || !replayed {
		return workflow, replayed, err
	}
	if err := tx.Commit(); err != nil {
		return Workflow{}, false, err
	}
	return workflow, true, nil
}

func (s *PostgresStore) Create(ctx context.Context, workflow Workflow, key, inputHash string) (Workflow, bool, error) {
	tx, err := s.begin(ctx)
	if err != nil {
		return Workflow{}, false, err
	}
	defer tx.Rollback()
	if err := lockAction(ctx, tx, workflow.OrganizationID, workflow.ProposedBy, "CREATE", key); err != nil {
		return Workflow{}, false, err
	}
	if existing, replayed, err := lookupAction(ctx, tx, workflow.OrganizationID, workflow.ProposedBy, "CREATE", key, inputHash); err != nil || replayed {
		if err == nil {
			err = tx.Commit()
		}
		return existing, replayed, err
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO ascp_proposal_workflows
		(workflow_id, organization_id, kind, payload_hash, chain_id, contract_address, function_selector, calldata,
		 governance_action, proposed_by, proposer_role, proposer_step_up_at, proposer_step_up_until, state, proposed_at, expires_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9::jsonb,$10,$11,to_timestamp($12),to_timestamp($13),$14,to_timestamp($15),to_timestamp($16))
		ON CONFLICT (workflow_id) DO NOTHING`,
		workflow.WorkflowID, workflow.OrganizationID, workflow.Kind, workflow.PayloadHash, nullableUint64(workflow.ChainID),
		nullableString(workflow.ContractAddress), nullableString(workflow.FunctionSelector), nullableString(workflow.Calldata),
		nullableJSON(workflow.GovernanceAction), workflow.ProposedBy, workflow.ProposerRole, workflow.ProposerStepUpAt,
		workflow.ProposerStepUpUntil, workflow.State, workflow.ProposedAt, workflow.ExpiresAt)
	if err != nil {
		return Workflow{}, false, fmt.Errorf("insert proposal workflow: %w", err)
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return Workflow{}, false, err
	}
	if inserted != 1 {
		return Workflow{}, false, ErrStateConflict
	}
	if err := recordTransition(ctx, tx, workflow, "CREATE", key, inputHash, workflow.ProposedBy, workflow.ProposedAt); err != nil {
		return Workflow{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return Workflow{}, false, fmt.Errorf("commit proposal workflow: %w", err)
	}
	return workflow, false, nil
}

func (s *PostgresStore) Get(ctx context.Context, organizationID, workflowID string) (Workflow, error) {
	return scanWorkflow(s.db.QueryRowContext(ctx, workflowSelect+` WHERE organization_id=$1 AND workflow_id=$2`, organizationID, workflowID))
}

func (s *PostgresStore) Pending(ctx context.Context, limit int, afterWorkflowID string) ([]Workflow, error) {
	if limit < 1 || limit > 1000 || (afterWorkflowID != "" && !hash(afterWorkflowID)) {
		return nil, ErrInvalidWorkflow
	}
	rows, err := s.db.QueryContext(ctx, workflowSelect+`
		WHERE state IN ('APPROVED_PENDING_CHAIN','SUBMITTED','CONFIRMED')
		  AND ($2='' OR (approved_at,workflow_id) > (
		      (SELECT approved_at FROM ascp_proposal_workflows WHERE workflow_id=$2),$2))
		ORDER BY approved_at, workflow_id LIMIT $1`, limit, afterWorkflowID)
	if err != nil {
		return nil, fmt.Errorf("list pending proposal workflows: %w", err)
	}
	defer rows.Close()
	workflows := make([]Workflow, 0, limit)
	for rows.Next() {
		workflow, err := scanWorkflow(rows)
		if err != nil {
			return nil, err
		}
		workflows = append(workflows, workflow)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate pending proposal workflows: %w", err)
	}
	return workflows, nil
}

func (s *PostgresStore) Approve(ctx context.Context, actor Actor, workflowID, key, inputHash string, now time.Time) (Workflow, bool, error) {
	return s.decide(ctx, actor, workflowID, "APPROVE", key, inputHash, now)
}

func (s *PostgresStore) Cancel(ctx context.Context, actor Actor, workflowID, key, inputHash string, now time.Time) (Workflow, bool, error) {
	return s.decide(ctx, actor, workflowID, "CANCEL", key, inputHash, now)
}

func (s *PostgresStore) decide(ctx context.Context, actor Actor, workflowID, action, key, inputHash string, now time.Time) (Workflow, bool, error) {
	tx, err := s.begin(ctx)
	if err != nil {
		return Workflow{}, false, err
	}
	defer tx.Rollback()
	if err := lockAction(ctx, tx, actor.OrganizationID, actor.PrincipalID, action, key); err != nil {
		return Workflow{}, false, err
	}
	if existing, replayed, err := lookupAction(ctx, tx, actor.OrganizationID, actor.PrincipalID, action, key, inputHash); err != nil || replayed {
		if err == nil {
			err = tx.Commit()
		}
		return existing, replayed, err
	}
	workflow, err := scanWorkflow(tx.QueryRowContext(ctx, workflowSelect+` WHERE organization_id=$1 AND workflow_id=$2 FOR UPDATE`, actor.OrganizationID, workflowID))
	if err != nil {
		return Workflow{}, false, err
	}
	if workflow.State != Proposed {
		if err := recordAction(ctx, tx, workflow, action, key, inputHash, actor.PrincipalID, now.Unix()); err != nil {
			return Workflow{}, false, err
		}
		return workflow, true, tx.Commit()
	}
	if now.Unix() >= workflow.ExpiresAt {
		workflow.State, workflow.ExpiredAt = Expired, now.Unix()
		if _, err := tx.ExecContext(ctx, `UPDATE ascp_proposal_workflows SET state='EXPIRED', expired_at=$3 WHERE organization_id=$1 AND workflow_id=$2`, actor.OrganizationID, workflowID, now); err != nil {
			return Workflow{}, false, err
		}
		if err := recordTransition(ctx, tx, workflow, "EXPIRE", "expire:"+workflowID, eventHash("EXPIRE", workflowID), actor.PrincipalID, now.Unix()); err != nil {
			return Workflow{}, false, err
		}
		if err := recordAction(ctx, tx, workflow, action, key, inputHash, actor.PrincipalID, now.Unix()); err != nil {
			return Workflow{}, false, err
		}
		return workflow, true, tx.Commit()
	}
	if action == "APPROVE" && actor.PrincipalID == workflow.ProposedBy {
		return Workflow{}, false, ErrSamePrincipal
	}
	if action == "APPROVE" {
		if !canApprove(workflow.Kind, actor.Role) {
			return Workflow{}, false, ErrForbiddenRole
		}
		workflow.ApprovedBy, workflow.ApproverRole = actor.PrincipalID, actor.Role
		workflow.ApproverStepUpAt, workflow.ApproverStepUpUntil = actor.StepUpAt.UTC().Unix(), actor.StepUpUntil.UTC().Unix()
		workflow.ApprovedAt = now.Unix()
		workflow.State = ApprovedPendingChain
		var finalizedAt any
		if !requiresChainReceipt(workflow.Kind) {
			workflow.State, workflow.FinalizedAt, finalizedAt = Finalized, now.Unix(), now
		}
		_, err = tx.ExecContext(ctx, `UPDATE ascp_proposal_workflows SET state=$3, approved_by=$4, approver_role=$5,
			approver_step_up_at=$6, approver_step_up_until=$7, approved_at=$8, completed_at=$9
			WHERE organization_id=$1 AND workflow_id=$2`, actor.OrganizationID, workflowID, workflow.State,
			actor.PrincipalID, actor.Role, actor.StepUpAt, actor.StepUpUntil, now, finalizedAt)
	} else {
		if !canCancel(workflow, actor) {
			return Workflow{}, false, ErrForbiddenRole
		}
		workflow.State, workflow.CancelledBy, workflow.CancelledAt = Cancelled, actor.PrincipalID, now.Unix()
		_, err = tx.ExecContext(ctx, `UPDATE ascp_proposal_workflows SET state='CANCELLED', cancelled_by=$3, cancelled_at=$4
			WHERE organization_id=$1 AND workflow_id=$2`, actor.OrganizationID, workflowID, actor.PrincipalID, now)
	}
	if err != nil {
		return Workflow{}, false, fmt.Errorf("transition proposal workflow: %w", err)
	}
	if err := recordTransition(ctx, tx, workflow, action, key, inputHash, actor.PrincipalID, now.Unix()); err != nil {
		return Workflow{}, false, err
	}
	if action == "APPROVE" && workflow.State == ApprovedPendingChain {
		if err := recordExecutionCommand(ctx, tx, workflow, inputHash, now.Unix()); err != nil {
			return Workflow{}, false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return Workflow{}, false, fmt.Errorf("commit proposal workflow transition: %w", err)
	}
	return workflow, false, nil
}

func (s *PostgresStore) Expire(ctx context.Context, organizationID, workflowID string, now time.Time) (Workflow, bool, error) {
	tx, err := s.begin(ctx)
	if err != nil {
		return Workflow{}, false, err
	}
	defer tx.Rollback()
	workflow, err := scanWorkflow(tx.QueryRowContext(ctx, workflowSelect+` WHERE organization_id=$1 AND workflow_id=$2 FOR UPDATE`, organizationID, workflowID))
	if err != nil {
		return Workflow{}, false, err
	}
	if workflow.State != Proposed || now.Unix() < workflow.ExpiresAt {
		return workflow, false, tx.Commit()
	}
	workflow.State, workflow.ExpiredAt = Expired, now.Unix()
	if _, err := tx.ExecContext(ctx, `UPDATE ascp_proposal_workflows SET state='EXPIRED', expired_at=$3 WHERE organization_id=$1 AND workflow_id=$2`, organizationID, workflowID, now); err != nil {
		return Workflow{}, false, err
	}
	if err := recordTransition(ctx, tx, workflow, "EXPIRE", "expire:"+workflowID, eventHash("EXPIRE", workflowID), "SYSTEM", now.Unix()); err != nil {
		return Workflow{}, false, err
	}
	return workflow, true, tx.Commit()
}

func (s *PostgresStore) Submit(ctx context.Context, organizationID, workflowID, transactionHash string, now time.Time) (Workflow, bool, error) {
	tx, err := s.begin(ctx)
	if err != nil {
		return Workflow{}, false, err
	}
	defer tx.Rollback()
	workflow, err := scanWorkflow(tx.QueryRowContext(ctx, workflowSelect+` WHERE organization_id=$1 AND workflow_id=$2 FOR UPDATE`, organizationID, workflowID))
	if err != nil {
		return Workflow{}, false, err
	}
	if workflow.State == Submitted && workflow.SubmissionTxHash == transactionHash {
		return workflow, true, tx.Commit()
	}
	// Reorg and timeout retries require proof that the Safe transaction bytes
	// and nonce remain exactly approved. This boundary only receives an outer
	// transaction hash, so accepting either side state here would be a blind
	// financial retry.
	if workflow.State != ApprovedPendingChain {
		return Workflow{}, false, ErrStateConflict
	}
	if now.Unix() < workflow.ApprovedAt {
		return Workflow{}, false, ErrStateConflict
	}
	transitionKey := eventHash("SUBMIT", workflowID, transactionHash)
	workflow.State, workflow.SubmissionTxHash, workflow.SubmittedAt = Submitted, transactionHash, now.Unix()
	workflow.ConfirmedAt, workflow.TerminalReason, workflow.TerminalAt = 0, "", 0
	if _, err := tx.ExecContext(ctx, `UPDATE ascp_proposal_workflows SET state='SUBMITTED', submission_transaction_hash=$3,
		submitted_at=$4, confirmed_at=NULL, terminal_reason=NULL, terminal_at=NULL WHERE organization_id=$1 AND workflow_id=$2`,
		organizationID, workflowID, transactionHash, now); err != nil {
		return Workflow{}, false, fmt.Errorf("record governance submission: %w", err)
	}
	if err := recordTransition(ctx, tx, workflow, "SUBMIT", transitionKey, transitionKey, "CHAIN_RELAYER", now.Unix()); err != nil {
		return Workflow{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return Workflow{}, false, err
	}
	return workflow, false, nil
}

func (s *PostgresStore) Confirm(ctx context.Context, organizationID, workflowID, transactionHash string, now time.Time) (Workflow, bool, error) {
	tx, err := s.begin(ctx)
	if err != nil {
		return Workflow{}, false, err
	}
	defer tx.Rollback()
	workflow, err := scanWorkflow(tx.QueryRowContext(ctx, workflowSelect+` WHERE organization_id=$1 AND workflow_id=$2 FOR UPDATE`, organizationID, workflowID))
	if err != nil {
		return Workflow{}, false, err
	}
	if workflow.State == Confirmed && workflow.SubmissionTxHash == transactionHash {
		return workflow, true, tx.Commit()
	}
	if workflow.State != Submitted || workflow.SubmissionTxHash != transactionHash {
		return Workflow{}, false, ErrStateConflict
	}
	if now.Unix() < workflow.SubmittedAt {
		return Workflow{}, false, ErrStateConflict
	}
	transitionKey := eventHash("CONFIRM", workflowID, transactionHash, fmt.Sprint(workflow.SubmittedAt))
	workflow.State, workflow.ConfirmedAt = Confirmed, now.Unix()
	if _, err := tx.ExecContext(ctx, `UPDATE ascp_proposal_workflows SET state='CONFIRMED', confirmed_at=$3
		WHERE organization_id=$1 AND workflow_id=$2`, organizationID, workflowID, now); err != nil {
		return Workflow{}, false, fmt.Errorf("record governance confirmation: %w", err)
	}
	if err := recordTransition(ctx, tx, workflow, "CONFIRM", transitionKey, transitionKey, "CHAIN_OBSERVER", now.Unix()); err != nil {
		return Workflow{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return Workflow{}, false, err
	}
	return workflow, false, nil
}

func (s *PostgresStore) Complete(ctx context.Context, organizationID, workflowID string, receipt CompletionReceipt, digest string, encoded []byte, now time.Time) (Workflow, bool, error) {
	tx, err := s.begin(ctx)
	if err != nil {
		return Workflow{}, false, err
	}
	defer tx.Rollback()
	workflow, err := scanWorkflow(tx.QueryRowContext(ctx, workflowSelect+` WHERE organization_id=$1 AND workflow_id=$2 FOR UPDATE`, organizationID, workflowID))
	if err != nil {
		return Workflow{}, false, err
	}
	if workflow.State == Finalized && workflow.CompletionDigest == digest {
		return workflow, true, tx.Commit()
	}
	if !activeChainState(workflow.State) || (workflow.SubmissionTxHash != "" && workflow.SubmissionTxHash != receipt.TransactionHash) {
		return Workflow{}, false, ErrStateConflict
	}
	if now.Unix() < workflow.ApprovedAt || now.Unix() < workflow.SubmittedAt || now.Unix() < workflow.ConfirmedAt {
		return Workflow{}, false, ErrStateConflict
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO ascp_workflow_receipt_ownership
		(chain_id, transaction_hash, log_index, workflow_id, organization_id, completion_digest, claimed_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7) ON CONFLICT DO NOTHING`, receipt.ChainID, receipt.TransactionHash,
		receipt.LogIndex, workflowID, organizationID, digest, now)
	if err != nil {
		return Workflow{}, false, fmt.Errorf("claim workflow completion receipt: %w", err)
	}
	claimed, err := result.RowsAffected()
	if err != nil {
		return Workflow{}, false, err
	}
	if claimed == 0 {
		var ownerWorkflow, ownerOrganization, ownerDigest string
		err := tx.QueryRowContext(ctx, `SELECT workflow_id, organization_id, completion_digest
			FROM ascp_workflow_receipt_ownership WHERE chain_id=$1 AND transaction_hash=$2 AND log_index=$3 FOR UPDATE`,
			receipt.ChainID, receipt.TransactionHash, receipt.LogIndex).Scan(&ownerWorkflow, &ownerOrganization, &ownerDigest)
		if errors.Is(err, sql.ErrNoRows) {
			// The INSERT can also conflict on UNIQUE(workflow_id). Resolve that
			// axis explicitly rather than returning an opaque sql.ErrNoRows.
			err = tx.QueryRowContext(ctx, `SELECT workflow_id, organization_id, completion_digest
				FROM ascp_workflow_receipt_ownership WHERE workflow_id=$1 FOR UPDATE`, workflowID).
				Scan(&ownerWorkflow, &ownerOrganization, &ownerDigest)
			if err == nil {
				return Workflow{}, false, ErrStateConflict
			}
		}
		if err != nil {
			return Workflow{}, false, fmt.Errorf("read workflow receipt owner: %w", err)
		}
		if ownerWorkflow != workflowID || ownerOrganization != organizationID {
			return Workflow{}, false, ErrReceiptOwned
		}
		if ownerDigest != digest {
			return Workflow{}, false, ErrStateConflict
		}
	}
	if workflow.State == ApprovedPendingChain {
		workflow.State, workflow.SubmissionTxHash, workflow.SubmittedAt = Submitted, receipt.TransactionHash, now.Unix()
		if _, err := tx.ExecContext(ctx, `UPDATE ascp_proposal_workflows SET state='SUBMITTED', submission_transaction_hash=$3,
			submitted_at=$4 WHERE organization_id=$1 AND workflow_id=$2`, organizationID, workflowID, receipt.TransactionHash, now); err != nil {
			return Workflow{}, false, fmt.Errorf("reconstruct governance submission: %w", err)
		}
		if err := recordTransition(ctx, tx, workflow, "SUBMIT_RECOVERED", digest, digest, "CHAIN_OBSERVER", now.Unix()); err != nil {
			return Workflow{}, false, err
		}
	}
	if workflow.State == Submitted {
		workflow.State, workflow.ConfirmedAt = Confirmed, now.Unix()
		if _, err := tx.ExecContext(ctx, `UPDATE ascp_proposal_workflows SET state='CONFIRMED', confirmed_at=$3
			WHERE organization_id=$1 AND workflow_id=$2`, organizationID, workflowID, now); err != nil {
			return Workflow{}, false, fmt.Errorf("confirm governance submission: %w", err)
		}
		if err := recordTransition(ctx, tx, workflow, "CONFIRM", digest, digest, "CHAIN_OBSERVER", now.Unix()); err != nil {
			return Workflow{}, false, err
		}
	}
	workflow.State, workflow.CompletionReceipt, workflow.CompletionDigest, workflow.FinalizedAt = Finalized, append([]byte(nil), encoded...), digest, now.Unix()
	if _, err := tx.ExecContext(ctx, `UPDATE ascp_proposal_workflows SET state='FINALIZED', completion_receipt=$3::jsonb,
		completion_digest=$4, completed_at=$5 WHERE organization_id=$1 AND workflow_id=$2`, organizationID, workflowID, encoded, digest, now); err != nil {
		return Workflow{}, false, fmt.Errorf("complete proposal workflow: %w", err)
	}
	if err := recordTransition(ctx, tx, workflow, "FINALIZE", digest, digest, "CHAIN_OBSERVER", now.Unix()); err != nil {
		return Workflow{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return Workflow{}, false, err
	}
	return workflow, false, nil
}

func (s *PostgresStore) FailChain(ctx context.Context, organizationID, workflowID string, state State, reason TerminalReason, now time.Time) (Workflow, bool, error) {
	tx, err := s.begin(ctx)
	if err != nil {
		return Workflow{}, false, err
	}
	defer tx.Rollback()
	workflow, err := scanWorkflow(tx.QueryRowContext(ctx, workflowSelect+` WHERE organization_id=$1 AND workflow_id=$2 FOR UPDATE`, organizationID, workflowID))
	if err != nil {
		return Workflow{}, false, err
	}
	if workflow.State == state && workflow.TerminalReason == reason {
		return workflow, true, tx.Commit()
	}
	if !validFailureTransition(workflow.State, state) {
		return Workflow{}, false, ErrStateConflict
	}
	if now.Unix() < workflow.ApprovedAt || now.Unix() < workflow.SubmittedAt || now.Unix() < workflow.ConfirmedAt {
		return Workflow{}, false, ErrStateConflict
	}
	action := "FAIL_" + string(state)
	transitionKey := eventHash(action, workflowID, string(reason), workflow.SubmissionTxHash,
		fmt.Sprint(workflow.SubmittedAt), fmt.Sprint(workflow.ConfirmedAt))
	workflow.State, workflow.TerminalReason, workflow.TerminalAt = state, reason, now.Unix()
	if _, err := tx.ExecContext(ctx, `UPDATE ascp_proposal_workflows SET state=$3, terminal_reason=$4,
		terminal_at=$5 WHERE organization_id=$1 AND workflow_id=$2`, organizationID, workflowID, state, reason, now); err != nil {
		return Workflow{}, false, fmt.Errorf("terminalize governance workflow: %w", err)
	}
	if err := recordTransition(ctx, tx, workflow, action, transitionKey, transitionKey, "CHAIN_OBSERVER", now.Unix()); err != nil {
		return Workflow{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return Workflow{}, false, err
	}
	return workflow, false, nil
}

func (s *PostgresStore) begin(ctx context.Context) (*sql.Tx, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return nil, fmt.Errorf("begin proposal workflow transaction: %w", err)
	}
	return tx, nil
}

func lockAction(ctx context.Context, tx *sql.Tx, organizationID, actorID, action, key string) error {
	lockKey := fmt.Sprintf("%d:%s%d:%s%d:%s%d:%s", len(organizationID), organizationID, len(actorID), actorID, len(action), action, len(key), key)
	_, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, lockKey)
	return err
}

func lookupAction(ctx context.Context, tx *sql.Tx, organizationID, actorID, action, key, inputHash string) (Workflow, bool, error) {
	var storedHash, workflowID string
	err := tx.QueryRowContext(ctx, `SELECT input_hash, workflow_id FROM ascp_workflow_actions
		WHERE organization_id=$1 AND actor_id=$2 AND action=$3 AND idempotency_key=$4`, organizationID, actorID, action, key).Scan(&storedHash, &workflowID)
	if errors.Is(err, sql.ErrNoRows) {
		return Workflow{}, false, nil
	}
	if err != nil {
		return Workflow{}, false, err
	}
	if storedHash != inputHash {
		return Workflow{}, false, ErrIdempotencyConflict
	}
	workflow, err := scanWorkflow(tx.QueryRowContext(ctx, workflowSelect+` WHERE organization_id=$1 AND workflow_id=$2`, organizationID, workflowID))
	return workflow, err == nil, err
}

func recordTransition(ctx context.Context, tx *sql.Tx, workflow Workflow, action, key, inputHash, actorID string, at int64) error {
	if action != "EXPIRE" || key != "" {
		if err := recordAction(ctx, tx, workflow, action, key, inputHash, actorID, at); err != nil {
			return err
		}
	}
	payload, err := json.Marshal(workflow)
	if err != nil {
		return err
	}
	eventID := eventHash(action, workflow.WorkflowID, inputHash)
	outboxID := eventHash("OUTBOX", eventID)
	if _, err := tx.ExecContext(ctx, `INSERT INTO ascp_workflow_events
		(event_id, workflow_id, organization_id, actor_id, event_kind, event_json, created_at)
		VALUES ($1,$2,$3,$4,$5,$6::jsonb,to_timestamp($7))`, eventID, workflow.WorkflowID, workflow.OrganizationID, actorID, workflow.State, payload, at); err != nil {
		return fmt.Errorf("record workflow event: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO ascp_workflow_outbox
		(outbox_id, workflow_id, organization_id, topic, payload_json, created_at)
		VALUES ($1,$2,$3,'ascp.workflow.changed',$4::jsonb,to_timestamp($5))`, outboxID, workflow.WorkflowID, workflow.OrganizationID, payload, at); err != nil {
		return fmt.Errorf("record workflow outbox: %w", err)
	}
	return nil
}

func recordAction(ctx context.Context, tx *sql.Tx, workflow Workflow, action, key, inputHash, actorID string, at int64) error {
	if _, err := tx.ExecContext(ctx, `INSERT INTO ascp_workflow_actions
		(organization_id, actor_id, action, idempotency_key, input_hash, workflow_id, result_state, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,to_timestamp($8))`, workflow.OrganizationID, actorID, action, key, inputHash,
		workflow.WorkflowID, workflow.State, at); err != nil {
		return fmt.Errorf("record workflow action: %w", err)
	}
	return nil
}

func recordExecutionCommand(ctx context.Context, tx *sql.Tx, workflow Workflow, approvalHash string, at int64) error {
	command, err := buildExecutionCommand(workflow, approvalHash)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(command)
	if err != nil {
		return err
	}
	outboxID := eventHash("GOVERNANCE_EXECUTE", workflow.WorkflowID, approvalHash)
	if _, err := tx.ExecContext(ctx, `INSERT INTO ascp_workflow_outbox
		(outbox_id, workflow_id, organization_id, topic, payload_json, created_at)
		VALUES ($1,$2,$3,'ascp.governance.execute',$4::jsonb,to_timestamp($5))`, outboxID,
		workflow.WorkflowID, workflow.OrganizationID, payload, at); err != nil {
		return fmt.Errorf("record governance execution command: %w", err)
	}
	return nil
}

func buildExecutionCommand(workflow Workflow, approvalHash string) (GovernanceExecutionCommand, error) {
	if workflow.State != ApprovedPendingChain || !hash(workflow.WorkflowID) || !hash(workflow.PayloadHash) ||
		!identifier(workflow.OrganizationID) || !identifier(workflow.ApprovedBy) || workflow.ApprovedAt <= 0 ||
		(workflow.ChainID != 8453 && workflow.ChainID != 84532) || !canonicalAddress(workflow.ContractAddress) ||
		!selector(workflow.FunctionSelector) || !governanceCalldata(workflow.Calldata, workflow.FunctionSelector) ||
		len(workflow.GovernanceAction) == 0 || !json.Valid(workflow.GovernanceAction) || !hash(approvalHash) {
		return GovernanceExecutionCommand{}, ErrInvalidWorkflow
	}
	if _, err := boundActionForWorkflow(workflow); err != nil {
		return GovernanceExecutionCommand{}, err
	}
	command := GovernanceExecutionCommand{
		Version: GovernanceExecutionVersion, WorkflowID: workflow.WorkflowID, OrganizationID: workflow.OrganizationID,
		Kind: workflow.Kind, PayloadHash: workflow.PayloadHash, ChainID: workflow.ChainID,
		ContractAddress: workflow.ContractAddress, FunctionSelector: workflow.FunctionSelector, Calldata: workflow.Calldata,
		Value: "0", Operation: "CALL", GovernanceAction: append(json.RawMessage(nil), workflow.GovernanceAction...),
		ApprovedBy: workflow.ApprovedBy, ApprovedAt: workflow.ApprovedAt, ExecuteAfter: workflow.ApprovedAt + 1,
		ApprovalActionHash: approvalHash,
	}
	return command, nil
}

const workflowSelect = `SELECT workflow_id, organization_id, kind, payload_hash, COALESCE(chain_id,0),
	COALESCE(contract_address,''), COALESCE(function_selector,''), COALESCE(calldata,''), governance_action,
	proposed_by, proposer_role, state,
	COALESCE(approved_by,''), COALESCE(approver_role,''), COALESCE(cancelled_by,''),
	extract(epoch FROM proposer_step_up_at)::bigint, extract(epoch FROM proposer_step_up_until)::bigint,
	COALESCE(extract(epoch FROM approver_step_up_at)::bigint,0), COALESCE(extract(epoch FROM approver_step_up_until)::bigint,0),
	extract(epoch FROM proposed_at)::bigint, COALESCE(extract(epoch FROM approved_at)::bigint,0),
	COALESCE(extract(epoch FROM cancelled_at)::bigint,0), COALESCE(extract(epoch FROM expired_at)::bigint,0),
	extract(epoch FROM expires_at)::bigint, completion_receipt, COALESCE(completion_digest,''),
	COALESCE(extract(epoch FROM completed_at)::bigint,0), COALESCE(submission_transaction_hash,''),
	COALESCE(extract(epoch FROM submitted_at)::bigint,0), COALESCE(extract(epoch FROM confirmed_at)::bigint,0),
	COALESCE(terminal_reason,''), COALESCE(extract(epoch FROM terminal_at)::bigint,0) FROM ascp_proposal_workflows`

type scanner interface{ Scan(...any) error }

func scanWorkflow(row scanner) (Workflow, error) {
	var workflow Workflow
	var receipt, action []byte
	err := row.Scan(&workflow.WorkflowID, &workflow.OrganizationID, &workflow.Kind, &workflow.PayloadHash,
		&workflow.ChainID, &workflow.ContractAddress, &workflow.FunctionSelector, &workflow.Calldata, &action,
		&workflow.ProposedBy, &workflow.ProposerRole, &workflow.State, &workflow.ApprovedBy, &workflow.ApproverRole,
		&workflow.CancelledBy, &workflow.ProposerStepUpAt, &workflow.ProposerStepUpUntil, &workflow.ApproverStepUpAt, &workflow.ApproverStepUpUntil, &workflow.ProposedAt,
		&workflow.ApprovedAt, &workflow.CancelledAt, &workflow.ExpiredAt, &workflow.ExpiresAt, &receipt,
		&workflow.CompletionDigest, &workflow.FinalizedAt, &workflow.SubmissionTxHash, &workflow.SubmittedAt,
		&workflow.ConfirmedAt, &workflow.TerminalReason, &workflow.TerminalAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Workflow{}, ErrNotFound
	}
	if err != nil {
		return Workflow{}, fmt.Errorf("read proposal workflow: %w", err)
	}
	workflow.CompletionReceipt = append([]byte(nil), receipt...)
	workflow.GovernanceAction = append([]byte(nil), action...)
	return workflow, nil
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullableUint64(value uint64) any {
	if value == 0 {
		return nil
	}
	return value
}

func nullableJSON(value json.RawMessage) any {
	if len(value) == 0 {
		return nil
	}
	return []byte(value)
}

func eventHash(values ...string) string {
	return "0x" + fmt.Sprintf("%x", sha256Bytes([]byte(fmt.Sprint(values))))
}
