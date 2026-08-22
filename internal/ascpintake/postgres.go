package ascpintake

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/gnanam1990/flowops/internal/ascpadaptation"
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
	defer func() { _ = tx.Rollback() }()
	operation := input.Operation
	var createdAt time.Time
	err = tx.QueryRowContext(ctx, `
		INSERT INTO ascp_financial_tombstones
			(organization_id, actor_id, endpoint, logical_operation, idempotency_key, canonical_request_hash,
			 operation_id, quote_hash, purchase_spec_hash, quote_nonce, directory_version, directory_contract,
			 seller_signer, adaptation_grant_id, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,NULLIF($14,''),to_timestamp($15))
		ON CONFLICT (organization_id, actor_id, endpoint, logical_operation, idempotency_key) DO NOTHING
		RETURNING created_at`,
		operation.OrganizationID, operation.ActorID, Endpoint, LogicalOperation, input.IdempotencyKey, input.CanonicalInputHash,
		operation.OperationID, operation.QuoteHash, operation.PurchaseSpecHash, operation.QuoteNonce, int64(operation.DirectoryVersion),
		operation.DirectoryContract, operation.SellerSigner, input.AdaptationGrantID, operation.CreatedAt,
	).Scan(&createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return s.replayTombstone(ctx, tx, input)
	}
	if err != nil {
		return Operation{}, false, classifyInsertError(err)
	}
	err = tx.QueryRowContext(ctx, `
		INSERT INTO ascp_intents
			(operation_id, organization_id, actor_id, endpoint, idempotency_key, canonical_input_hash,
			 quote_hash, purchase_spec_hash, quote_nonce, directory_version, directory_contract, seller_signer,
			 quote_json, purchase_spec_json, purchase_spec_bytes, request_body, adaptation_grant_id, adaptation_grant_digest, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,NULLIF($17,''),NULLIF($18,''),to_timestamp($19))
		RETURNING created_at`,
		operation.OperationID, operation.OrganizationID, operation.ActorID, Endpoint, input.IdempotencyKey, input.CanonicalInputHash,
		operation.QuoteHash, operation.PurchaseSpecHash, operation.QuoteNonce, int64(operation.DirectoryVersion), operation.DirectoryContract, operation.SellerSigner,
		input.QuoteJSON, input.PurchaseSpecJSON, input.PurchaseSpecJSON, input.RequestBody, input.AdaptationGrantID, input.AdaptationDigest, operation.CreatedAt,
	).Scan(&createdAt)
	if err == nil {
		if input.AdaptationGrantID != "" {
			result, consumeErr := tx.ExecContext(ctx, `
				UPDATE ascp_adaptation_grants
				SET state='CONSUMED', remaining_attempts=0, consumed_operation_id=$3, consumed_at=statement_timestamp()
				WHERE grant_id=$1 AND grant_digest=$2 AND organization_id=$4 AND agent_id=$5
				  AND state='ISSUED' AND remaining_attempts=1
				  AND issued_at <= statement_timestamp() + interval '60 seconds'
				  AND expires_at > statement_timestamp()`,
				input.AdaptationGrantID, input.AdaptationDigest, operation.OperationID, operation.OrganizationID, operation.ActorID)
			if consumeErr != nil {
				return Operation{}, false, fmt.Errorf("consume adaptation grant: %w", consumeErr)
			}
			rows, rowsErr := result.RowsAffected()
			if rowsErr != nil {
				return Operation{}, false, rowsErr
			}
			if rows != 1 {
				return Operation{}, false, ascpadaptation.ErrGrantConsumed
			}
		}
		if err := tx.Commit(); err != nil {
			return Operation{}, false, fmt.Errorf("commit ASCP intake transaction: %w", err)
		}
		operation.CreatedAt = createdAt.UTC().Unix()
		return operation, false, nil
	}
	return Operation{}, false, classifyInsertError(err)
}

func (s *PostgresStore) replayTombstone(ctx context.Context, tx *sql.Tx, input StoreInput) (Operation, bool, error) {
	var existing Operation
	var storedHash string
	var createdAt time.Time
	err := tx.QueryRowContext(ctx, `
		SELECT operation_id, organization_id, actor_id, quote_hash, purchase_spec_hash, quote_nonce,
		       directory_version, directory_contract, seller_signer, COALESCE(adaptation_grant_id,''), created_at, canonical_request_hash
		FROM ascp_financial_tombstones
		WHERE organization_id = $1 AND actor_id = $2 AND endpoint = $3 AND logical_operation = $4 AND idempotency_key = $5`,
		input.Operation.OrganizationID, input.Operation.ActorID, Endpoint, LogicalOperation, input.IdempotencyKey,
	).Scan(&existing.OperationID, &existing.OrganizationID, &existing.ActorID, &existing.QuoteHash, &existing.PurchaseSpecHash, &existing.QuoteNonce,
		&existing.DirectoryVersion, &existing.DirectoryContract, &existing.SellerSigner, &existing.AdaptationGrantID, &createdAt, &storedHash)
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

func (s *PostgresStore) Lookup(ctx context.Context, organizationID, actorID, idempotencyKey string) (Operation, string, bool, error) {
	var operation Operation
	var canonicalInputHash string
	var createdAt time.Time
	err := s.db.QueryRowContext(ctx, `
		SELECT operation_id, organization_id, actor_id, quote_hash, purchase_spec_hash, quote_nonce,
		       directory_version, directory_contract, seller_signer, COALESCE(adaptation_grant_id,''), created_at, canonical_request_hash
		FROM ascp_financial_tombstones
		WHERE organization_id=$1 AND actor_id=$2 AND endpoint=$3 AND logical_operation=$4 AND idempotency_key=$5`,
		organizationID, actorID, Endpoint, LogicalOperation, idempotencyKey).Scan(
		&operation.OperationID, &operation.OrganizationID, &operation.ActorID, &operation.QuoteHash,
		&operation.PurchaseSpecHash, &operation.QuoteNonce, &operation.DirectoryVersion,
		&operation.DirectoryContract, &operation.SellerSigner, &operation.AdaptationGrantID, &createdAt, &canonicalInputHash,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Operation{}, "", false, nil
	}
	if err != nil {
		return Operation{}, "", false, fmt.Errorf("lookup ASCP idempotency result: %w", err)
	}
	operation.CreatedAt = createdAt.UTC().Unix()
	return operation, canonicalInputHash, true, nil
}

func (s *PostgresStore) Get(ctx context.Context, organizationID, actorID, operationID string) (Operation, error) {
	var operation Operation
	var createdAt time.Time
	err := s.db.QueryRowContext(ctx, `
		SELECT operation_id, organization_id, actor_id, quote_hash, purchase_spec_hash, quote_nonce,
		       directory_version, directory_contract, seller_signer, COALESCE(adaptation_grant_id,''), created_at
		FROM ascp_financial_tombstones
		WHERE operation_id=$1 AND organization_id=$2 AND actor_id=$3`, operationID, organizationID, actorID).Scan(
		&operation.OperationID, &operation.OrganizationID, &operation.ActorID, &operation.QuoteHash,
		&operation.PurchaseSpecHash, &operation.QuoteNonce, &operation.DirectoryVersion,
		&operation.DirectoryContract, &operation.SellerSigner, &operation.AdaptationGrantID, &createdAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Operation{}, ErrNotFound
	}
	if err != nil {
		return Operation{}, fmt.Errorf("read ASCP operation: %w", err)
	}
	operation.CreatedAt = createdAt.UTC().Unix()
	return operation, nil
}

func classifyInsertError(err error) error {
	var pgError *pgconn.PgError
	if errors.As(err, &pgError) && pgError.Code == "23505" && (pgError.ConstraintName == "ascp_intents_quote_nonce_unique" || pgError.ConstraintName == "ascp_financial_tombstones_quote_nonce_unique") {
		return ErrQuoteNonceConsumed
	}
	return fmt.Errorf("insert ASCP intake: %w", err)
}
