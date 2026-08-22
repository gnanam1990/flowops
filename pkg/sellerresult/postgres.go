package sellerresult

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

type PostgresStore struct{ db *sql.DB }

func NewPostgresStore(db *sql.DB) (*PostgresStore, error) {
	if db == nil {
		return nil, errors.New("database is required")
	}
	return &PostgresStore{db: db}, nil
}

func (s *PostgresStore) Begin(ctx context.Context, request Request, now time.Time) (Record, bool, error) {
	if err := validateRequest(request); err != nil {
		return Record{}, false, err
	}
	retainUntil, _ := retentionDeadline(request.SettleBy)
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return Record{}, false, fmt.Errorf("begin seller replay transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var createdAt, storedRetainUntil time.Time
	err = tx.QueryRowContext(ctx, `
		INSERT INTO ascp_seller_results
			(seller_id, call_id, request_hash, resource_operation_key, state, settle_by, retain_until, created_at)
		VALUES ($1,$2,$3,$4,'STARTED_UNKNOWN',$5,$6,$7)
		ON CONFLICT (seller_id, call_id) DO NOTHING
		RETURNING created_at, retain_until`, request.SellerID, request.CallID, request.RequestHash, request.ResourceOperationKey,
		request.SettleBy.UTC(), retainUntil, now.UTC()).Scan(&createdAt, &storedRetainUntil)
	if err == nil {
		if err := tx.Commit(); err != nil {
			return Record{}, false, fmt.Errorf("commit seller replay claim: %w", err)
		}
		return Record{Request: request, State: StateStartedUnknown, RetainUntil: storedRetainUntil.UTC(), CreatedAt: createdAt.UTC()}, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Record{}, false, classifyClaimError(err)
	}
	record, err := readRecord(ctx, tx, request.SellerID, request.CallID)
	if err != nil {
		return Record{}, false, err
	}
	if !sameRequest(record.Request, request) {
		return Record{}, false, ErrConflict
	}
	if record.State != StateCompleted {
		return Record{}, false, ErrRecoveryRequired
	}
	if err := tx.Commit(); err != nil {
		return Record{}, false, fmt.Errorf("commit seller replay read: %w", err)
	}
	return record, false, nil
}

func classifyClaimError(err error) error {
	var pgError *pgconn.PgError
	if errors.As(err, &pgError) && pgError.Code == "23505" && pgError.ConstraintName == "ascp_seller_results_resource_operation_key_unique" {
		return ErrConflict
	}
	return fmt.Errorf("claim seller replay: %w", err)
}

func (s *PostgresStore) Complete(ctx context.Context, request Request, response Response, now time.Time) (Record, error) {
	if err := validateRequest(request); err != nil {
		return Record{}, err
	}
	normalized, err := normalizeResponse(response)
	if err != nil {
		return Record{}, err
	}
	headers, err := json.Marshal(normalized.Header)
	if err != nil {
		return Record{}, fmt.Errorf("encode seller response headers: %w", err)
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return Record{}, fmt.Errorf("begin seller completion transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var completedAt time.Time
	err = tx.QueryRowContext(ctx, `
		UPDATE ascp_seller_results
		SET state='COMPLETED', response_status=$5, response_headers=$6, response_body=$7,
		    content_digest=$8, side_effect_status='COMPLETED', completed_at=$9
		WHERE seller_id=$1 AND call_id=$2 AND request_hash=$3 AND resource_operation_key=$4
		  AND state='STARTED_UNKNOWN'
		RETURNING completed_at`, request.SellerID, request.CallID, request.RequestHash, request.ResourceOperationKey,
		normalized.StatusCode, headers, normalized.Body, normalized.ContentDigest, now.UTC()).Scan(&completedAt)
	if errors.Is(err, sql.ErrNoRows) {
		existing, readErr := readRecord(ctx, tx, request.SellerID, request.CallID)
		if readErr != nil {
			return Record{}, readErr
		}
		if !sameRequest(existing.Request, request) {
			return Record{}, ErrConflict
		}
		if existing.State != StateCompleted || !sameResponse(existing.Response, normalized) {
			return Record{}, ErrResultConflict
		}
		if err := tx.Commit(); err != nil {
			return Record{}, fmt.Errorf("commit seller completion replay: %w", err)
		}
		return existing, nil
	}
	if err != nil {
		return Record{}, fmt.Errorf("complete seller replay: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Record{}, fmt.Errorf("commit seller completion: %w", err)
	}
	retainUntil, _ := retentionDeadline(request.SettleBy)
	return Record{Request: request, State: StateCompleted, Response: normalized, RetainUntil: retainUntil, CompletedAt: completedAt.UTC()}, nil
}

type queryRower interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func readRecord(ctx context.Context, query queryRower, sellerID, callID string) (Record, error) {
	var record Record
	var state string
	var status sql.NullInt64
	var headers []byte
	var body []byte
	var digest, sideEffect sql.NullString
	var completedAt sql.NullTime
	err := query.QueryRowContext(ctx, `
		SELECT seller_id, call_id, request_hash, resource_operation_key, state,
		       response_status, response_headers, response_body, content_digest, side_effect_status,
		       settle_by, retain_until, created_at, completed_at
		FROM ascp_seller_results WHERE seller_id=$1 AND call_id=$2`, sellerID, callID).Scan(
		&record.Request.SellerID, &record.Request.CallID, &record.Request.RequestHash, &record.Request.ResourceOperationKey, &state,
		&status, &headers, &body, &digest, &sideEffect, &record.Request.SettleBy, &record.RetainUntil, &record.CreatedAt, &completedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Record{}, ErrConflict
	}
	if err != nil {
		return Record{}, fmt.Errorf("read seller replay: %w", err)
	}
	record.State = State(state)
	if record.State == StateCompleted {
		if err := json.Unmarshal(headers, &record.Response.Header); err != nil {
			return Record{}, fmt.Errorf("decode seller response headers: %w", err)
		}
		record.Response.StatusCode = int(status.Int64)
		record.Response.Body = append([]byte(nil), body...)
		record.Response.ContentDigest = digest.String
		record.Response.SideEffectStatus = sideEffect.String
		record.CompletedAt = completedAt.Time.UTC()
	}
	return record, nil
}
