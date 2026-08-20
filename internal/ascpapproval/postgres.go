package ascpapproval

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// PostgresStore uses conditional updates as the durable compare-and-swap
// boundary. A database retry may replay a terminal record but never overwrite
// it.
type PostgresStore struct{ db *sql.DB }

func NewPostgresStore(db *sql.DB) (*PostgresStore, error) {
	if db == nil {
		return nil, errors.New("database is required")
	}
	return &PostgresStore{db: db}, nil
}

func (s *PostgresStore) Create(ctx context.Context, input Approval) (Approval, bool, error) {
	var output Approval
	err := scanApproval(s.db.QueryRowContext(ctx, `
		INSERT INTO ascp_approvals (approval_id, organization_id, intent_id, state, review_snapshot_hash, requested_at, expires_at)
		VALUES ($1,$2,$3,$4,$5,to_timestamp($6),to_timestamp($7))
		ON CONFLICT (organization_id, intent_id) DO NOTHING
		RETURNING approval_id, organization_id, intent_id, state, review_snapshot_hash, requested_at, expires_at, decided_at, decided_by, cancel_reason`,
		input.ApprovalID, input.OrganizationID, input.IntentID, input.State, input.ReviewSnapshotHash, input.RequestedAt, input.ExpiresAt), &output)
	if err == nil {
		return output, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Approval{}, false, fmt.Errorf("insert ASCP approval: %w", err)
	}
	err = scanApproval(s.db.QueryRowContext(ctx, `SELECT approval_id, organization_id, intent_id, state, review_snapshot_hash, requested_at, expires_at, decided_at, decided_by, cancel_reason FROM ascp_approvals WHERE organization_id=$1 AND intent_id=$2`, input.OrganizationID, input.IntentID), &output)
	if err != nil {
		return Approval{}, false, fmt.Errorf("read existing ASCP approval: %w", err)
	}
	return output, true, nil
}

func (s *PostgresStore) Decide(ctx context.Context, id, snapshot string, target State, actor string, now time.Time) (Approval, error) {
	var output Approval
	err := scanApproval(s.db.QueryRowContext(ctx, `
		UPDATE ascp_approvals SET state=$3, decided_at=$4, decided_by=$5
		WHERE approval_id=$1 AND review_snapshot_hash=$2 AND state='REQUESTED' AND expires_at > $4
		RETURNING approval_id, organization_id, intent_id, state, review_snapshot_hash, requested_at, expires_at, decided_at, decided_by, cancel_reason`,
		id, snapshot, target, now.UTC()), &output)
	if err == nil {
		return output, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Approval{}, fmt.Errorf("decide ASCP approval: %w", err)
	}
	return s.notDecidable(ctx, id, snapshot, now)
}

func (s *PostgresStore) Cancel(ctx context.Context, id, reason string, now time.Time) (Approval, error) {
	var output Approval
	err := scanApproval(s.db.QueryRowContext(ctx, `
		UPDATE ascp_approvals SET state='CANCELLED', decided_at=$2, cancel_reason=$3
		WHERE approval_id=$1 AND state='REQUESTED' AND expires_at > $2
		RETURNING approval_id, organization_id, intent_id, state, review_snapshot_hash, requested_at, expires_at, decided_at, decided_by, cancel_reason`, id, now.UTC(), reason), &output)
	if err == nil {
		return output, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Approval{}, fmt.Errorf("cancel ASCP approval: %w", err)
	}
	return s.notDecidable(ctx, id, "", now)
}

func (s *PostgresStore) notDecidable(ctx context.Context, id, snapshot string, now time.Time) (Approval, error) {
	var output Approval
	err := scanApproval(s.db.QueryRowContext(ctx, `SELECT approval_id, organization_id, intent_id, state, review_snapshot_hash, requested_at, expires_at, decided_at, decided_by, cancel_reason FROM ascp_approvals WHERE approval_id=$1`, id), &output)
	if errors.Is(err, sql.ErrNoRows) {
		return Approval{}, ErrNotFound
	}
	if err != nil {
		return Approval{}, fmt.Errorf("read ASCP approval outcome: %w", err)
	}
	if snapshot != "" && output.ReviewSnapshotHash != snapshot {
		return Approval{}, ErrSnapshotMismatch
	}
	if output.State == Requested && !now.Before(time.Unix(output.ExpiresAt, 0)) {
		result, err := s.db.ExecContext(ctx, `UPDATE ascp_approvals SET state='EXPIRED' WHERE approval_id=$1 AND state='REQUESTED' AND expires_at <= $2`, id, now.UTC())
		if err != nil {
			return Approval{}, fmt.Errorf("expire ASCP approval: %w", err)
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return Approval{}, fmt.Errorf("read ASCP approval expiry outcome: %w", err)
		}
		if changed == 1 {
			output.State = Expired
		}
	}
	return output, ErrNotRequested
}

type rowScanner interface{ Scan(...any) error }

func scanApproval(row rowScanner, output *Approval) error {
	var requestedAt, expiresAt time.Time
	var decidedAt sql.NullTime
	var decidedBy, cancelReason sql.NullString
	if err := row.Scan(&output.ApprovalID, &output.OrganizationID, &output.IntentID, &output.State, &output.ReviewSnapshotHash, &requestedAt, &expiresAt, &decidedAt, &decidedBy, &cancelReason); err != nil {
		return err
	}
	output.RequestedAt, output.ExpiresAt = requestedAt.UTC().Unix(), expiresAt.UTC().Unix()
	if decidedAt.Valid {
		output.DecidedAt = decidedAt.Time.UTC().Unix()
	}
	if decidedBy.Valid {
		output.DecidedBy = decidedBy.String
	}
	if cancelReason.Valid {
		output.CancelReason = cancelReason.String
	}
	return nil
}
