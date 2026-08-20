package ascpintake

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// PostgresStore is the production-oriented Store adapter. Its insert binds the
// operation row and quote nonce in one serializable database transaction.
type PostgresStore struct{ db *sql.DB }

func NewPostgresStore(db *sql.DB) (*PostgresStore, error) {
	if db == nil {
		return nil, errors.New("database is required")
	}
	return &PostgresStore{db: db}, nil
}

func (s *PostgresStore) Create(ctx context.Context, input StoreInput) (Operation, bool, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return Operation{}, false, fmt.Errorf("begin ASCP intake transaction: %w", err)
	}
	defer tx.Rollback()
	operation := input.Operation
	var createdAt time.Time
	err = tx.QueryRowContext(ctx, `
		INSERT INTO ascp_intents
			(operation_id, organization_id, actor_id, endpoint, idempotency_key, canonical_input_hash,
			 quote_hash, purchase_spec_hash, quote_nonce, directory_version, directory_contract, seller_signer, quote_json, purchase_spec_json, request_body, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,to_timestamp($16))
		ON CONFLICT (organization_id, actor_id, endpoint, idempotency_key) DO NOTHING
		RETURNING created_at`,
		operation.OperationID, operation.OrganizationID, operation.ActorID, Endpoint, input.IdempotencyKey, input.CanonicalInputHash,
		operation.QuoteHash, operation.PurchaseSpecHash, operation.QuoteNonce, int64(operation.DirectoryVersion), operation.DirectoryContract, operation.SellerSigner, input.QuoteJSON, input.PurchaseSpecJSON, input.RequestBody, operation.CreatedAt,
	).Scan(&createdAt)
	if err == nil {
		if err := tx.Commit(); err != nil {
			return Operation{}, false, fmt.Errorf("commit ASCP intake transaction: %w", err)
		}
		operation.CreatedAt = createdAt.UTC().Unix()
		return operation, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Operation{}, false, classifyInsertError(err)
	}
	var existing Operation
	var storedHash string
	err = tx.QueryRowContext(ctx, `
		SELECT operation_id, organization_id, actor_id, quote_hash, purchase_spec_hash, quote_nonce,
		       directory_version, directory_contract, seller_signer, created_at, canonical_input_hash
		FROM ascp_intents
		WHERE organization_id = $1 AND actor_id = $2 AND endpoint = $3 AND idempotency_key = $4`,
		operation.OrganizationID, operation.ActorID, Endpoint, input.IdempotencyKey,
	).Scan(&existing.OperationID, &existing.OrganizationID, &existing.ActorID, &existing.QuoteHash, &existing.PurchaseSpecHash, &existing.QuoteNonce,
		&existing.DirectoryVersion, &existing.DirectoryContract, &existing.SellerSigner, &createdAt, &storedHash)
	if err != nil {
		return Operation{}, false, fmt.Errorf("read ASCP idempotency result: %w", err)
	}
	if storedHash != input.CanonicalInputHash {
		return Operation{}, false, ErrIdempotencyConflict
	}
	if err := tx.Commit(); err != nil {
		return Operation{}, false, fmt.Errorf("commit ASCP idempotency replay: %w", err)
	}
	existing.CreatedAt = createdAt.UTC().Unix()
	return existing, true, nil
}

func classifyInsertError(err error) error {
	var pgError *pgconn.PgError
	if errors.As(err, &pgError) && pgError.Code == "23505" && pgError.ConstraintName == "ascp_intents_quote_nonce_unique" {
		return ErrQuoteNonceConsumed
	}
	return fmt.Errorf("insert ASCP intake: %w", err)
}
