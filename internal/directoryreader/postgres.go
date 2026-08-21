package directoryreader

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

var ErrObservationConflict = errors.New("finalized directory observation conflicts with durable head")

var ErrObservationExpired = errors.New("finalized directory observation is outside the record window")

const maximumRecordDelay = time.Minute

// PostgresStore materializes quorum-backed finalized observations for local,
// transaction-bound execution revalidation. It never accepts raw provider
// answers; callers can record only a Result produced by Reader.
type PostgresStore struct {
	db    *sql.DB
	clock func() time.Time
}

func NewPostgresStore(db *sql.DB, clocks ...func() time.Time) (*PostgresStore, error) {
	if db == nil || len(clocks) > 1 || len(clocks) == 1 && clocks[0] == nil {
		return nil, errors.New("database is required")
	}
	clock := time.Now
	if len(clocks) == 1 {
		clock = clocks[0]
	}
	return &PostgresStore{db: db, clock: clock}, nil
}

func (s *PostgresStore) Record(ctx context.Context, result Result) error {
	storedAt := s.clock().UTC()
	if err := validateResult(result); err != nil {
		return err
	}
	if result.ObservedAt.After(storedAt.Add(maximumRecordDelay)) || storedAt.Sub(result.ObservedAt) > maximumRecordDelay {
		return ErrObservationExpired
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return fmt.Errorf("begin directory observation transaction: %w", err)
	}
	defer tx.Rollback()
	providers, err := json.Marshal(result.Providers)
	if err != nil {
		return fmt.Errorf("encode directory providers: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO ascp_directory_snapshots
			(observation_digest, chain_id, directory_contract, directory_version, directory_root,
			 finalized_block_number, finalized_block_hash, providers, observed_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		ON CONFLICT (observation_digest) DO NOTHING`, result.ObservationDigest, result.ChainID,
		result.DirectoryContract, result.DirectoryVersion, result.DirectoryRoot, result.FinalizedBlockNumber,
		result.FinalizedBlockHash, providers, result.ObservedAt); err != nil {
		return classifyObservationWrite(err)
	}
	evidence := result.Evidence
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO ascp_directory_quote_evidence
			(observation_digest, seller_id, resource_id, quote_signing_key, key_epoch, payout_address,
			 ack_authority, amount_base_units, verification_spec_hash, declared_work_time,
			 verification_budget_seconds, active, quote_key_revoked)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
		ON CONFLICT (observation_digest, seller_id, resource_id) DO NOTHING`, result.ObservationDigest,
		evidence.SellerID, evidence.ResourceID, evidence.QuoteSigningKey, evidence.KeyEpoch,
		evidence.PayoutAddress, evidence.AckAuthority, evidence.AmountBaseUnits, evidence.VerificationSpecHash,
		evidence.DeclaredWorkTime, evidence.VerificationBudgetSeconds, evidence.Active, evidence.QuoteKeyRevoked); err != nil {
		return fmt.Errorf("record directory quote evidence: %w", err)
	}

	var currentDigest string
	var currentBlock uint64
	err = tx.QueryRowContext(ctx, `
		SELECT observation_digest, finalized_block_number
		FROM ascp_directory_heads
		WHERE chain_id=$1 AND directory_contract=$2
		FOR UPDATE`, result.ChainID, result.DirectoryContract).Scan(&currentDigest, &currentBlock)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("lock directory head: %w", err)
	}
	if err == nil {
		if currentBlock > result.FinalizedBlockNumber {
			if err := tx.Commit(); err != nil {
				return fmt.Errorf("commit historical directory observation: %w", err)
			}
			return nil
		}
		if currentBlock == result.FinalizedBlockNumber && currentDigest != result.ObservationDigest {
			return ErrObservationConflict
		}
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO ascp_directory_heads
			(chain_id, directory_contract, observation_digest, directory_version, finalized_block_number, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6)
		ON CONFLICT (chain_id, directory_contract) DO UPDATE SET
			observation_digest=EXCLUDED.observation_digest,
			directory_version=EXCLUDED.directory_version,
			finalized_block_number=EXCLUDED.finalized_block_number,
			updated_at=EXCLUDED.updated_at`, result.ChainID, result.DirectoryContract, result.ObservationDigest,
		result.DirectoryVersion, result.FinalizedBlockNumber, storedAt); err != nil {
		return fmt.Errorf("advance directory head: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit directory observation: %w", err)
	}
	return nil
}

func validateResult(result Result) error {
	evidence := result.Evidence
	if !result.verified || result.seal != resultSeal(result) ||
		(result.ChainID != 8453 && result.ChainID != 84532) || !address(result.DirectoryContract) ||
		result.DirectoryVersion == 0 || result.FinalizedBlockNumber == 0 || result.ObservedAt.IsZero() || !hash(result.DirectoryRoot) ||
		!hash(result.FinalizedBlockHash) || !hash(result.ObservationDigest) || len(result.Providers) < 2 ||
		!evidence.Verified || evidence.Version != result.DirectoryVersion || !hash(evidence.SellerID) ||
		!hash(evidence.ResourceID) || !address(evidence.QuoteSigningKey) || evidence.KeyEpoch == 0 ||
		!address(evidence.PayoutAddress) || !address(evidence.AckAuthority) || evidence.AmountBaseUnits == "" ||
		!hash(evidence.VerificationSpecHash) || evidence.DeclaredWorkTime == 0 || evidence.VerificationBudgetSeconds == 0 {
		return ErrInvalidObservation
	}
	seen := make(map[string]struct{}, len(result.Providers))
	for _, provider := range result.Providers {
		if !providerName(provider) {
			return ErrInvalidObservation
		}
		if _, duplicate := seen[provider]; duplicate {
			return ErrInvalidObservation
		}
		seen[provider] = struct{}{}
	}
	return nil
}

func classifyObservationWrite(err error) error {
	// A uniqueness failure on chain/contract/block means another digest already
	// claimed the same finalized height. Do not overwrite or silently advance.
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) && postgresError.Code == "23505" {
		return fmt.Errorf("%w: %v", ErrObservationConflict, err)
	}
	return fmt.Errorf("record directory snapshot: %w", err)
}
