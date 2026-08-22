package ascpgovernancerelay

import (
	"bytes"
	"context"
	cryptorand "crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"time"

	"github.com/gnanam1990/flowops/internal/ascpworkflow"
)

type PostgresStore struct {
	db    *sql.DB
	clock func() time.Time
}

func NewPostgresStore(db *sql.DB, clock func() time.Time) (*PostgresStore, error) {
	if db == nil {
		return nil, ErrInvalidCommand
	}
	if clock == nil {
		clock = time.Now
	}
	return &PostgresStore{db: db, clock: clock}, nil
}

func (s *PostgresStore) ConsumeCommand(ctx context.Context) (Job, bool, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return Job{}, false, err
	}
	defer tx.Rollback()
	var outboxID, workflowID, organizationID string
	var payload []byte
	err = tx.QueryRowContext(ctx, `SELECT outbox.outbox_id,outbox.workflow_id,outbox.organization_id,outbox.payload_json::text
		FROM ascp_workflow_outbox outbox
		JOIN ascp_proposal_workflows workflow
		  ON workflow.workflow_id=outbox.workflow_id AND workflow.organization_id=outbox.organization_id
		WHERE outbox.topic='ascp.governance.execute'
		  AND workflow.state='APPROVED_PENDING_CHAIN'
		  AND NOT EXISTS (SELECT 1 FROM ascp_governance_relay_jobs relay
		                  WHERE relay.outbox_id=outbox.outbox_id OR
		                        (relay.workflow_id=outbox.workflow_id AND relay.organization_id=outbox.organization_id))
		ORDER BY outbox.created_at,outbox.outbox_id LIMIT 1`).
		Scan(&outboxID, &workflowID, &organizationID, &payload)
	if errors.Is(err, sql.ErrNoRows) {
		return Job{}, false, ErrNoWork
	}
	if err != nil {
		return Job{}, false, fmt.Errorf("claim governance command: %w", err)
	}
	command, err := decodeCommand(payload)
	if err != nil || command.WorkflowID != workflowID || command.OrganizationID != organizationID {
		return Job{}, false, errors.Join(ErrInvalidCommand, err)
	}
	now := s.clock().UTC()
	result, err := tx.ExecContext(ctx, `INSERT INTO ascp_governance_relay_jobs
		(outbox_id,workflow_id,organization_id,command_json,state,created_at,updated_at)
		VALUES ($1,$2,$3,$4::jsonb,'AWAITING_SIGNATURES',$5,$5) ON CONFLICT (outbox_id) DO NOTHING`,
		outboxID, workflowID, organizationID, payload, now)
	if err != nil {
		return Job{}, false, fmt.Errorf("consume governance command: %w", err)
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return Job{}, false, err
	}
	job, err := scanRelayJob(tx.QueryRowContext(ctx, relayJobSelect+` WHERE outbox_id=$1`, outboxID))
	if err != nil {
		return Job{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return Job{}, false, err
	}
	return job, inserted == 0, nil
}

func (s *PostgresStore) Get(ctx context.Context, organizationID, workflowID string) (Job, error) {
	return scanRelayJob(s.db.QueryRowContext(ctx, relayJobSelect+` WHERE organization_id=$1 AND workflow_id=$2`, organizationID, workflowID))
}

func (s *PostgresStore) ReplayAuthorization(ctx context.Context, organizationID, workflowID, key, inputHash string) (Job, bool, error) {
	var storedHash, storedWorkflow string
	err := s.db.QueryRowContext(ctx, `SELECT input_hash,workflow_id FROM ascp_governance_relay_authorizations
		WHERE organization_id=$1 AND idempotency_key=$2`, organizationID, key).Scan(&storedHash, &storedWorkflow)
	if errors.Is(err, sql.ErrNoRows) {
		return Job{}, false, nil
	}
	if err != nil {
		return Job{}, false, err
	}
	if storedHash != inputHash || storedWorkflow != workflowID {
		return Job{}, false, ErrIdempotencyConflict
	}
	job, err := s.Get(ctx, organizationID, workflowID)
	return job, err == nil, err
}

func (s *PostgresStore) Authorize(ctx context.Context, organizationID, workflowID, key, inputHash string, prepared Prepared, handle string, now time.Time) (Job, bool, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return Job{}, false, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, organizationID+"\x00"+key); err != nil {
		return Job{}, false, err
	}
	var storedHash, storedWorkflow string
	err = tx.QueryRowContext(ctx, `SELECT input_hash,workflow_id FROM ascp_governance_relay_authorizations
		WHERE organization_id=$1 AND idempotency_key=$2`, organizationID, key).Scan(&storedHash, &storedWorkflow)
	if err == nil {
		if storedHash != inputHash || storedWorkflow != workflowID {
			return Job{}, false, ErrIdempotencyConflict
		}
		job, loadErr := scanRelayJob(tx.QueryRowContext(ctx, relayJobSelect+` WHERE organization_id=$1 AND workflow_id=$2`, organizationID, workflowID))
		if loadErr == nil {
			loadErr = tx.Commit()
		}
		return job, true, loadErr
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Job{}, false, err
	}
	job, err := scanRelayJob(tx.QueryRowContext(ctx, relayJobSelect+` WHERE organization_id=$1 AND workflow_id=$2 FOR UPDATE`, organizationID, workflowID))
	if err != nil {
		return Job{}, false, err
	}
	if job.State != StateAwaitingSignatures || prepared.WorkflowID != workflowID || prepared.OrganizationID != organizationID ||
		!identifierPattern.MatchString(handle) || !canonicalHash(inputHash) {
		return Job{}, false, ErrStateConflict
	}
	preparedJSON, err := json.Marshal(prepared)
	if err != nil {
		return Job{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO ascp_governance_relay_authorizations
		(organization_id,idempotency_key,input_hash,workflow_id,created_at) VALUES ($1,$2,$3,$4,$5)`,
		organizationID, key, inputHash, workflowID, now); err != nil {
		return Job{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE ascp_governance_relay_jobs SET state='READY',prepared_json=$3::jsonb,
		artifact_handle=$4,authorization_key=$5,authorization_hash=$6,updated_at=$7
		WHERE organization_id=$1 AND workflow_id=$2 AND state='AWAITING_SIGNATURES'`,
		organizationID, workflowID, preparedJSON, handle, key, inputHash, now); err != nil {
		return Job{}, false, err
	}
	job, err = scanRelayJob(tx.QueryRowContext(ctx, relayJobSelect+` WHERE organization_id=$1 AND workflow_id=$2`, organizationID, workflowID))
	if err != nil {
		return Job{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return Job{}, false, err
	}
	return job, false, nil
}

func (s *PostgresStore) ClaimRelay(ctx context.Context, worker string, duration time.Duration) (Lease, error) {
	return s.claim(ctx, worker, duration, true)
}

func (s *PostgresStore) ClaimObservation(ctx context.Context, worker string, duration time.Duration) (Lease, error) {
	return s.claim(ctx, worker, duration, false)
}

func (s *PostgresStore) claim(ctx context.Context, worker string, duration time.Duration, relay bool) (Lease, error) {
	if !identifierPattern.MatchString(worker) || duration < time.Second || duration > time.Minute {
		return Lease{}, ErrInvalidCommand
	}
	now := s.clock().UTC()
	token, err := postgresToken()
	if err != nil {
		return Lease{}, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return Lease{}, err
	}
	defer tx.Rollback()
	statePredicate := `state IN ('READY','RETRYABLE_EXACT','BROADCASTING')`
	if !relay {
		statePredicate = `state IN ('SUBMITTED','PENDING')`
	}
	var workflowID string
	err = tx.QueryRowContext(ctx, `SELECT workflow_id FROM ascp_governance_relay_jobs WHERE `+statePredicate+`
		AND (lease_expires_at IS NULL OR lease_expires_at <= $1)
		ORDER BY updated_at,workflow_id FOR UPDATE SKIP LOCKED LIMIT 1`, now).Scan(&workflowID)
	if errors.Is(err, sql.ErrNoRows) {
		return Lease{}, ErrNoWork
	}
	if err != nil {
		return Lease{}, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE ascp_governance_relay_jobs SET lease_owner=$2,lease_token=$3,
		lease_expires_at=$4,updated_at=$5 WHERE workflow_id=$1`, workflowID, worker, token, now.Add(duration), now)
	if err != nil || rowsAffected(result) != 1 {
		return Lease{}, errors.Join(ErrLeaseLost, err)
	}
	job, err := scanRelayJob(tx.QueryRowContext(ctx, relayJobSelect+` WHERE workflow_id=$1`, workflowID))
	if err != nil {
		return Lease{}, err
	}
	if err := tx.Commit(); err != nil {
		return Lease{}, err
	}
	return Lease{Job: job, Token: token}, nil
}

func (s *PostgresStore) RecordOuterPrepared(ctx context.Context, lease Lease, outer OuterArtifact, now time.Time) (Job, error) {
	if !validOuter(outer, lease.Job.Prepared, now) {
		return Job{}, ErrInvalidOutcome
	}
	encoded, err := json.Marshal(outer)
	if err != nil {
		return Job{}, err
	}
	return s.transition(ctx, lease, now, []State{StateReady, StateRetryable}, StateBroadcasting,
		`outer_json=$4::jsonb`, encoded)
}

func (s *PostgresStore) RecordSubmitted(ctx context.Context, lease Lease, transactionHash string, now time.Time) (Job, error) {
	if transactionHash != lease.Job.Outer.TransactionHash {
		return Job{}, ErrStateConflict
	}
	return s.transition(ctx, lease, now, []State{StateBroadcasting}, StateSubmitted,
		`attempt_count=attempt_count+1`, nil)
}

func (s *PostgresStore) ApplyDecision(ctx context.Context, lease Lease, evidence OutcomeEvidence, decision DecisionResult, now time.Time) (Job, error) {
	target := State("")
	sources := []State{StateSubmitted, StatePending}
	switch decision.Decision {
	case DecisionWait:
		target = StatePending
	case DecisionRetryExact:
		target, sources = StateRetryable, []State{StateSubmitted, StatePending, StateRetryable}
	case DecisionReapprove:
		target, sources = StateReapprovalRequired, []State{StateReady, StateRetryable, StateBroadcasting, StateSubmitted, StatePending}
	case DecisionFinalized:
		target, sources = StateFinalizedObserved, []State{StateSubmitted, StatePending, StateRetryable}
	default:
		return Job{}, ErrStateConflict
	}
	assignment := `last_outcome_json=last_outcome_json`
	var encoded any
	if evidence.WorkflowID != "" {
		value, err := json.Marshal(evidence)
		if err != nil {
			return Job{}, err
		}
		assignment, encoded = `last_outcome_json=$4::jsonb`, value
	}
	return s.transition(ctx, lease, now, sources, target, assignment, encoded)
}

func (s *PostgresStore) transition(ctx context.Context, lease Lease, now time.Time, sources []State, target State, assignment string, value any) (Job, error) {
	if lease.Token == "" || lease.Job.Command.WorkflowID == "" {
		return Job{}, ErrLeaseLost
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return Job{}, err
	}
	defer tx.Rollback()
	job, err := scanRelayJob(tx.QueryRowContext(ctx, relayJobSelect+` WHERE workflow_id=$1 FOR UPDATE`, lease.Job.Command.WorkflowID))
	if err != nil {
		return Job{}, err
	}
	if job.LeaseToken != lease.Token || job.LeaseExpiresAt.Before(now) || !containsState(sources, job.State) {
		return Job{}, ErrLeaseLost
	}
	arguments := []any{job.Command.WorkflowID, target, now}
	leasePlaceholder := `$4`
	if value != nil {
		arguments = append(arguments, value)
		leasePlaceholder = `$5`
	}
	arguments = append(arguments, lease.Token)
	query := `UPDATE ascp_governance_relay_jobs SET state=$2,updated_at=$3,` + assignment +
		` WHERE workflow_id=$1 AND lease_token=` + leasePlaceholder
	result, err := tx.ExecContext(ctx, query, arguments...)
	if err != nil || rowsAffected(result) != 1 {
		return Job{}, errors.Join(ErrLeaseLost, err)
	}
	job, err = scanRelayJob(tx.QueryRowContext(ctx, relayJobSelect+` WHERE workflow_id=$1`, job.Command.WorkflowID))
	if err != nil {
		return Job{}, err
	}
	if err := tx.Commit(); err != nil {
		return Job{}, err
	}
	return job, nil
}

func (s *PostgresStore) ReleaseLease(ctx context.Context, lease Lease) error {
	result, err := s.db.ExecContext(ctx, `UPDATE ascp_governance_relay_jobs SET lease_owner=NULL,lease_token=NULL,
		lease_expires_at=NULL,updated_at=$3 WHERE workflow_id=$1 AND lease_token=$2`,
		lease.Job.Command.WorkflowID, lease.Token, s.clock().UTC())
	if err != nil {
		return err
	}
	if rowsAffected(result) != 1 {
		return ErrLeaseLost
	}
	return nil
}

const relayJobSelect = `SELECT outbox_id,workflow_id,organization_id,command_json::text,state,
	prepared_json::text,COALESCE(artifact_handle,''),COALESCE(authorization_key,''),COALESCE(authorization_hash,''),
	outer_json::text,last_outcome_json::text,attempt_count,COALESCE(lease_owner,''),COALESCE(lease_token,''),
	lease_expires_at,created_at,updated_at FROM ascp_governance_relay_jobs`

type relayScanner interface{ Scan(...any) error }

func scanRelayJob(row relayScanner) (Job, error) {
	var job Job
	var commandJSON, preparedJSON, outerJSON, outcomeJSON []byte
	var leaseExpiry sql.NullTime
	err := row.Scan(&job.OutboxID, &job.Command.WorkflowID, &job.Command.OrganizationID, &commandJSON, &job.State,
		&preparedJSON, &job.ArtifactHandle, &job.AuthorizationKey, &job.AuthorizationHash, &outerJSON, &outcomeJSON,
		&job.AttemptCount, &job.LeaseOwner, &job.LeaseToken, &leaseExpiry, &job.CreatedAt, &job.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Job{}, ascpworkflow.ErrNotFound
	}
	if err != nil {
		return Job{}, fmt.Errorf("read governance relay job: %w", err)
	}
	command, err := decodeCommand(commandJSON)
	if err != nil || command.WorkflowID != job.Command.WorkflowID || command.OrganizationID != job.Command.OrganizationID {
		return Job{}, errors.Join(ErrInvalidCommand, err)
	}
	job.Command = command
	if len(preparedJSON) > 0 {
		if err := strictJSON(preparedJSON, &job.Prepared); err != nil {
			return Job{}, err
		}
	}
	if len(outerJSON) > 0 {
		if err := strictJSON(outerJSON, &job.Outer); err != nil {
			return Job{}, err
		}
	}
	if len(outcomeJSON) > 0 {
		if err := strictJSON(outcomeJSON, &job.LastOutcome); err != nil {
			return Job{}, err
		}
	}
	if leaseExpiry.Valid {
		job.LeaseExpiresAt = leaseExpiry.Time.UTC()
	}
	return job, nil
}

func decodeCommand(value []byte) (ascpworkflow.GovernanceExecutionCommand, error) {
	var command ascpworkflow.GovernanceExecutionCommand
	if err := strictJSON(value, &command); err != nil {
		return command, err
	}
	if err := ascpworkflow.ValidateExecutionCommand(command); err != nil {
		return command, err
	}
	return command, nil
}

func strictJSON(value []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(value))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return ErrInvalidCommand
	}
	canonical, err := json.Marshal(target)
	if err != nil || !sameJSONDocument(value, canonical) {
		return errors.Join(ErrInvalidCommand, err)
	}
	return nil
}

func sameJSONDocument(left, right []byte) bool {
	decode := func(value []byte) (any, error) {
		decoder := json.NewDecoder(bytes.NewReader(value))
		decoder.UseNumber()
		var decoded any
		if err := decoder.Decode(&decoded); err != nil {
			return nil, err
		}
		if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
			return nil, ErrInvalidCommand
		}
		return decoded, nil
	}
	leftValue, leftErr := decode(left)
	rightValue, rightErr := decode(right)
	return leftErr == nil && rightErr == nil && reflect.DeepEqual(leftValue, rightValue)
}

func postgresToken() (string, error) {
	value := make([]byte, 16)
	if _, err := cryptorand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

func rowsAffected(result sql.Result) int64 {
	if result == nil {
		return 0
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0
	}
	return count
}

func containsState(values []State, target State) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

var _ Store = (*PostgresStore)(nil)
