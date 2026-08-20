package ascpexecauth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// PostgresStore is the authoritative execution-authorization boundary. The
// approval and reservation rows are locked in the same serializable transaction
// before the authorization becomes executable.
type PostgresStore struct{ db *sql.DB }

func NewPostgresStore(db *sql.DB) (*PostgresStore, error) {
	if db == nil {
		return nil, errors.New("database is required")
	}
	return &PostgresStore{db: db}, nil
}

func (s *PostgresStore) ValidateAndReserve(ctx context.Context, input Input, now time.Time) (Authorization, error) {
	if !validInput(input) || input.ApprovalID == "" {
		return Authorization{}, ErrInvalidInput
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return Authorization{}, fmt.Errorf("begin execution authorization transaction: %w", err)
	}
	defer tx.Rollback()
	output := Authorization{Input: input, State: Invalidated}
	var state string
	err = tx.QueryRowContext(ctx, `SELECT state FROM ascp_approvals WHERE approval_id=$1 AND intent_id=$2 FOR UPDATE`, input.ApprovalID, input.IntentID).Scan(&state)
	if err != nil || state != "APPROVED" {
		output.InvalidationReason = ErrApprovalNotApproved.Error()
		return s.persistInvalid(ctx, tx, output, now, ErrApprovalNotApproved)
	}
	var reservationState string
	err = tx.QueryRowContext(ctx, `SELECT state FROM ascp_budget_reservations WHERE reservation_id=$1 AND operation_id=$2 FOR UPDATE`, input.ReservationID, input.IntentID).Scan(&reservationState)
	if err != nil || reservationState != "RESERVED" {
		output.InvalidationReason = "reservation is unavailable"
		return s.persistInvalid(ctx, tx, output, now, errors.New(output.InvalidationReason))
	}
	output.State = ValidatedAndReserved
	err = tx.QueryRowContext(ctx, `
		INSERT INTO ascp_execution_authorizations (authorization_id, approval_id, intent_id, state, execution_snapshot_hash, reservation_id, created_at, evaluated_at)
		VALUES ($1,$2,$3,'VALIDATED_AND_RESERVED',$4,$5,$6,$6)
		RETURNING authorization_id`, input.AuthorizationID, input.ApprovalID, input.IntentID, input.ExecutionSnapshotHash, input.ReservationID, now.UTC()).Scan(&output.AuthorizationID)
	if err != nil {
		return Authorization{}, fmt.Errorf("create execution authorization: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Authorization{}, fmt.Errorf("commit execution authorization: %w", err)
	}
	return output, nil
}

func (s *PostgresStore) persistInvalid(ctx context.Context, tx *sql.Tx, output Authorization, now time.Time, cause error) (Authorization, error) {
	_, err := tx.ExecContext(ctx, `INSERT INTO ascp_execution_authorizations (authorization_id, approval_id, intent_id, state, execution_snapshot_hash, invalidation_reason, created_at, evaluated_at) VALUES ($1,$2,$3,'INVALIDATED',$4,$5,$6,$6) ON CONFLICT (intent_id) DO NOTHING`, output.AuthorizationID, output.ApprovalID, output.IntentID, output.ExecutionSnapshotHash, output.InvalidationReason, now.UTC())
	if err != nil {
		return Authorization{}, fmt.Errorf("persist invalid execution authorization: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Authorization{}, fmt.Errorf("commit invalid execution authorization: %w", err)
	}
	return output, cause
}
