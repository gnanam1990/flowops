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
	_, err = tx.ExecContext(ctx, `INSERT INTO ascp_proposal_workflows
		(workflow_id, organization_id, kind, payload_hash, proposed_by, proposer_role, proposer_step_up_at, proposer_step_up_until,
		 state, proposed_at, expires_at)
		VALUES ($1,$2,$3,$4,$5,$6,to_timestamp($7),to_timestamp($8),$9,to_timestamp($10),to_timestamp($11))`,
		workflow.WorkflowID, workflow.OrganizationID, workflow.Kind, workflow.PayloadHash, workflow.ProposedBy,
		workflow.ProposerRole, workflow.ProposerStepUpAt, workflow.ProposerStepUpUntil, workflow.State, workflow.ProposedAt, workflow.ExpiresAt)
	if err != nil {
		return Workflow{}, false, fmt.Errorf("insert proposal workflow: %w", err)
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
		if !requiresChainReceipt(workflow.Kind) {
			workflow.State = Approved
		}
		_, err = tx.ExecContext(ctx, `UPDATE ascp_proposal_workflows SET state=$3, approved_by=$4, approver_role=$5,
			approver_step_up_at=$6, approver_step_up_until=$7, approved_at=$8 WHERE organization_id=$1 AND workflow_id=$2`,
			actor.OrganizationID, workflowID, workflow.State, actor.PrincipalID, actor.Role, actor.StepUpAt, actor.StepUpUntil, now)
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
	if workflow.State == Approved && workflow.CompletionDigest == digest {
		return workflow, true, tx.Commit()
	}
	if workflow.State != ApprovedPendingChain {
		return Workflow{}, false, ErrStateConflict
	}
	workflow.State, workflow.CompletionReceipt, workflow.CompletionDigest, workflow.CompletedAt = Approved, append([]byte(nil), encoded...), digest, now.Unix()
	if _, err := tx.ExecContext(ctx, `UPDATE ascp_proposal_workflows SET state='APPROVED', completion_receipt=$3::jsonb,
		completion_digest=$4, completed_at=$5 WHERE organization_id=$1 AND workflow_id=$2`, organizationID, workflowID, encoded, digest, now); err != nil {
		return Workflow{}, false, fmt.Errorf("complete proposal workflow: %w", err)
	}
	if err := recordTransition(ctx, tx, workflow, "COMPLETE", digest, digest, "CHAIN_OBSERVER", now.Unix()); err != nil {
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
		if _, err := tx.ExecContext(ctx, `INSERT INTO ascp_workflow_actions
			(organization_id, actor_id, action, idempotency_key, input_hash, workflow_id, result_state, created_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,to_timestamp($8))`, workflow.OrganizationID, actorID, action, key, inputHash, workflow.WorkflowID, workflow.State, at); err != nil {
			return fmt.Errorf("record workflow action: %w", err)
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

const workflowSelect = `SELECT workflow_id, organization_id, kind, payload_hash, proposed_by, proposer_role, state,
	COALESCE(approved_by,''), COALESCE(approver_role,''), COALESCE(cancelled_by,''),
	extract(epoch FROM proposer_step_up_at)::bigint, extract(epoch FROM proposer_step_up_until)::bigint,
	COALESCE(extract(epoch FROM approver_step_up_at)::bigint,0), COALESCE(extract(epoch FROM approver_step_up_until)::bigint,0),
	extract(epoch FROM proposed_at)::bigint, COALESCE(extract(epoch FROM approved_at)::bigint,0),
	COALESCE(extract(epoch FROM cancelled_at)::bigint,0), COALESCE(extract(epoch FROM expired_at)::bigint,0),
	extract(epoch FROM expires_at)::bigint, completion_receipt, COALESCE(completion_digest,''),
	COALESCE(extract(epoch FROM completed_at)::bigint,0) FROM ascp_proposal_workflows`

type scanner interface{ Scan(...any) error }

func scanWorkflow(row scanner) (Workflow, error) {
	var workflow Workflow
	var receipt []byte
	err := row.Scan(&workflow.WorkflowID, &workflow.OrganizationID, &workflow.Kind, &workflow.PayloadHash,
		&workflow.ProposedBy, &workflow.ProposerRole, &workflow.State, &workflow.ApprovedBy, &workflow.ApproverRole,
		&workflow.CancelledBy, &workflow.ProposerStepUpAt, &workflow.ProposerStepUpUntil, &workflow.ApproverStepUpAt, &workflow.ApproverStepUpUntil, &workflow.ProposedAt,
		&workflow.ApprovedAt, &workflow.CancelledAt, &workflow.ExpiredAt, &workflow.ExpiresAt, &receipt,
		&workflow.CompletionDigest, &workflow.CompletedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Workflow{}, ErrNotFound
	}
	if err != nil {
		return Workflow{}, fmt.Errorf("read proposal workflow: %w", err)
	}
	workflow.CompletionReceipt = append([]byte(nil), receipt...)
	return workflow, nil
}

func eventHash(values ...string) string {
	return "0x" + fmt.Sprintf("%x", sha256Bytes([]byte(fmt.Sprint(values))))
}
