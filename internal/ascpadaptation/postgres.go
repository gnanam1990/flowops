package ascpadaptation

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

type PostgresStore struct{ db *sql.DB }

func NewPostgresStore(db *sql.DB) (*PostgresStore, error) {
	if db == nil {
		return nil, errors.New("adaptation grant database is required")
	}
	return &PostgresStore{db: db}, nil
}

func (s *PostgresStore) Issue(ctx context.Context, record Record) (Record, bool, error) {
	if err := ValidateRecord(record); err != nil {
		return Record{}, false, err
	}
	payload, err := json.Marshal(record.Artifact.Grant)
	if err != nil {
		return Record{}, false, err
	}
	grant := record.Artifact.Grant
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO ascp_adaptation_grants
			(grant_id, original_intent_id, organization_id, agent_id, task_id, reason_class,
			 allowed_category, max_amount_atomic, allowed_seller_set, remaining_attempts,
			 issued_at, expires_at, grant_digest, canonical_request_hash, payload_json, signature, state)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,to_timestamp($11),to_timestamp($12),$13,$14,$15,$16,'ISSUED')
		ON CONFLICT (original_intent_id) DO NOTHING`,
		grant.GrantID, grant.OriginalIntentID, grant.OrganizationID, grant.AgentID, grant.TaskID, record.ReasonClass,
		grant.AllowedCategory, grant.MaxAmountAtomic, grant.AllowedSellerSet, grant.RemainingAttempts,
		grant.IssuedAt, grant.ExpiresAt, record.Digest, record.CanonicalRequestHash, payload, record.Artifact.Signature)
	if err != nil {
		return Record{}, false, fmt.Errorf("issue adaptation grant: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return Record{}, false, err
	}
	if rows == 1 {
		return record, false, nil
	}
	existing, err := s.GetByOriginalIntent(ctx, grant.OrganizationID, grant.AgentID, grant.OriginalIntentID)
	if err != nil {
		return Record{}, false, err
	}
	if existing.CanonicalRequestHash != record.CanonicalRequestHash || existing.ReasonClass != record.ReasonClass {
		return Record{}, false, ErrIssueConflict
	}
	return existing, true, nil
}

func (s *PostgresStore) GetGrant(ctx context.Context, organizationID, agentID, grantID string) (Record, error) {
	return scanRecord(s.db.QueryRowContext(ctx, `
		SELECT reason_class, grant_digest, canonical_request_hash, payload_json, signature, consumed_operation_id
		FROM ascp_adaptation_grants
		WHERE grant_id=$1 AND organization_id=$2 AND agent_id=$3`, grantID, organizationID, agentID))
}

func (s *PostgresStore) GetByOriginalIntent(ctx context.Context, organizationID, agentID, originalIntentID string) (Record, error) {
	return scanRecord(s.db.QueryRowContext(ctx, `
		SELECT reason_class, grant_digest, canonical_request_hash, payload_json, signature, consumed_operation_id
		FROM ascp_adaptation_grants
		WHERE original_intent_id=$1 AND organization_id=$2 AND agent_id=$3`, originalIntentID, organizationID, agentID))
}

type rowScanner interface{ Scan(...any) error }

func scanRecord(row rowScanner) (Record, error) {
	var record Record
	var payload []byte
	var signature string
	var consumed sql.NullString
	if err := row.Scan(&record.ReasonClass, &record.Digest, &record.CanonicalRequestHash, &payload, &signature, &consumed); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Record{}, ErrGrantNotFound
		}
		return Record{}, fmt.Errorf("read adaptation grant: %w", err)
	}
	if err := json.Unmarshal(payload, &record.Artifact.Grant); err != nil {
		return Record{}, fmt.Errorf("decode adaptation grant: %w", err)
	}
	record.Artifact.Signature = signature
	record.ConsumedOperationID = consumed.String
	if err := ValidateRecord(record); err != nil {
		return Record{}, err
	}
	return record, nil
}
