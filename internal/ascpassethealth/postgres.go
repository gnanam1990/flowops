package ascpassethealth

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

func (s *PostgresStore) Transition(ctx context.Context, config Config, decision Decision, now time.Time) (Record, error) {
	for attempt := 0; attempt < 3; attempt++ {
		record, err := s.transitionOnce(ctx, config, decision, now)
		if !retryableAssetHealthTransaction(err) {
			return record, err
		}
		if err := ctx.Err(); err != nil {
			return Record{}, err
		}
	}
	return Record{}, errors.New("asset-health transition serialization retries exhausted")
}

func (s *PostgresStore) transitionOnce(ctx context.Context, config Config, decision Decision, now time.Time) (Record, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return Record{}, fmt.Errorf("begin asset-health transition: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO ascp_asset_health
			(chain_id,asset,proxy_implementation,runtime_code_hash,quorum,state,epoch,updated_at)
		VALUES ($1,$2,$3,$4,$5,'NORMAL',0,$6)
		ON CONFLICT (chain_id,asset) DO NOTHING`, config.ChainID, config.Asset, config.ProxyImplementation, config.RuntimeCodeHash, config.Quorum, now); err != nil {
		return Record{}, fmt.Errorf("initialize asset health: %w", err)
	}
	record, err := readRecord(ctx, tx, config.ChainID, config.Asset, true)
	if err != nil {
		return Record{}, err
	}
	if record.ProxyImplementation != config.ProxyImplementation || record.RuntimeCodeHash != config.RuntimeCodeHash || record.Quorum != config.Quorum {
		return Record{}, ErrInvalidConfiguration
	}
	var priorDigest string
	err = tx.QueryRowContext(ctx, `SELECT evidence_digest FROM ascp_asset_health_observations WHERE evidence_digest=$1`, decision.EvidenceDigest).Scan(&priorDigest)
	if err == nil {
		if err := tx.Commit(); err != nil {
			return Record{}, err
		}
		return record, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Record{}, fmt.Errorf("read asset-health observation: %w", err)
	}
	target := decision.State
	if decision.State == Normal && record.State != Normal {
		target = Recovering
	}
	epoch := record.Epoch
	if target != record.State {
		epoch++
	}
	providers, _ := json.Marshal(decision.Providers)
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO ascp_asset_health_observations
			(evidence_digest,chain_id,asset,previous_state,observed_state,resulting_state,epoch,
			 providers,finalized_block,observed_at,recorded_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, decision.EvidenceDigest, config.ChainID, config.Asset,
		record.State, decision.State, target, epoch, providers, decision.FinalizedBlock, decision.ObservedAt, now); err != nil {
		return Record{}, fmt.Errorf("append asset-health observation: %w", err)
	}
	// Freeze the first clean finalized observation as the recovery anchor. New
	// clean polls remain audit evidence but must not move the canonical-backfill
	// cutoff faster than the settlement worker can satisfy it.
	if decision.State == Normal && record.State == Recovering {
		if err := tx.Commit(); err != nil {
			return Record{}, fmt.Errorf("commit stable asset recovery anchor: %w", err)
		}
		return record, nil
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE ascp_asset_health
		SET state=$3,epoch=$4,evidence_digest=$5,providers=$6,finalized_block=$7,observed_at=$8,updated_at=$9
		WHERE chain_id=$1 AND asset=$2`, config.ChainID, config.Asset, target, epoch, decision.EvidenceDigest, providers,
		decision.FinalizedBlock, decision.ObservedAt, now); err != nil {
		return Record{}, fmt.Errorf("update asset health: %w", err)
	}
	if target == TokenPaused || target == AssetTransferBlocked {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO ascp_asset_reclassifications
				(evidence_digest,operation_id,direction,from_account,to_account,amount_base_units,refund_due,recorded_at)
			SELECT $1,o.operation_id,'BLOCK','EscrowRestrictedUSDC','TokenBlockedRestrictedUSDC',
			       o.amount_base_units,o.settle_by<=$4,$4
			FROM ascp_payment_operations o
			WHERE o.chain_id=$2 AND o.asset=$3
			  AND o.state IN ('LOCKED_SAFE','LOCKED_FINALIZED','PENDING_CHAIN_RECOVERY')
			  AND EXISTS (SELECT 1 FROM ascp_ledger_transactions l WHERE l.operation_id=o.operation_id AND l.kind='LOCK_FINALIZED')
			  AND NOT EXISTS (
			    SELECT 1 FROM ascp_asset_reclassifications b
			    WHERE b.operation_id=o.operation_id AND b.direction='BLOCK'
			      AND NOT EXISTS (SELECT 1 FROM ascp_asset_reclassifications r WHERE r.original_block_evidence=b.evidence_digest AND r.operation_id=b.operation_id AND r.direction='RECOVER')
			  )`, decision.EvidenceDigest, config.ChainID, config.Asset, now); err != nil {
			return Record{}, fmt.Errorf("classify token-blocked funds: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return Record{}, fmt.Errorf("commit asset-health transition: %w", err)
	}
	record.State, record.Epoch, record.EvidenceDigest, record.Providers = target, epoch, decision.EvidenceDigest, append([]string(nil), decision.Providers...)
	record.FinalizedBlock, record.ObservedAt, record.UpdatedAt = decision.FinalizedBlock, decision.ObservedAt, now
	return record, nil
}

func (s *PostgresStore) CompleteRecovery(ctx context.Context, config Config, proof RecoveryProof, now time.Time) (Record, error) {
	for attempt := 0; attempt < 3; attempt++ {
		record, err := s.completeRecoveryOnce(ctx, config, proof, now)
		if !retryableAssetHealthTransaction(err) {
			return record, err
		}
		if err := ctx.Err(); err != nil {
			return Record{}, err
		}
	}
	return Record{}, errors.New("asset recovery serialization retries exhausted")
}

func (s *PostgresStore) completeRecoveryOnce(ctx context.Context, config Config, proof RecoveryProof, now time.Time) (Record, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return Record{}, err
	}
	defer func() { _ = tx.Rollback() }()
	record, err := readRecord(ctx, tx, config.ChainID, config.Asset, true)
	if err != nil || record.State != Recovering {
		if err != nil {
			return Record{}, err
		}
		return Record{}, ErrRecoveryIncomplete
	}
	if err := validateRecoveryProof(record, proof, now); err != nil {
		return Record{}, err
	}
	counts, err := readRecoveryCounts(ctx, tx, config.ChainID, config.Asset, record.ObservedAt)
	if err != nil {
		return Record{}, err
	}
	if counts != (recoveryCounts{}) || proof.EvidenceDigest != recoveryEvidenceDigest(proof, counts) {
		return Record{}, ErrRecoveryIncomplete
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO ascp_asset_recovery_proofs
			(evidence_digest,chain_id,asset,health_epoch,clean_evidence_digest,clean_finalized_block,
			 reconciled_at,pending_operations,stale_canonical_attempts,unclassified_locks,recorded_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,0,0,0,$8)`, proof.EvidenceDigest, proof.ChainID, proof.Asset,
		proof.HealthEpoch, proof.CleanEvidenceDigest, proof.CleanFinalizedBlock, proof.ReconciledAt, now); err != nil {
		return Record{}, fmt.Errorf("append asset recovery proof: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO ascp_asset_reclassifications
			(evidence_digest,operation_id,direction,original_block_evidence,from_account,to_account,amount_base_units,refund_due,recorded_at)
		SELECT $1,b.operation_id,'RECOVER',b.evidence_digest,'TokenBlockedRestrictedUSDC','EscrowRestrictedUSDC',
		       b.amount_base_units,b.refund_due,$4
		FROM ascp_asset_reclassifications b
		JOIN ascp_payment_operations o ON o.operation_id=b.operation_id
		WHERE b.direction='BLOCK' AND o.chain_id=$2 AND o.asset=$3
		  AND NOT EXISTS (SELECT 1 FROM ascp_asset_reclassifications r WHERE r.original_block_evidence=b.evidence_digest AND r.operation_id=b.operation_id AND r.direction='RECOVER')`,
		proof.EvidenceDigest, config.ChainID, config.Asset, now); err != nil {
		return Record{}, fmt.Errorf("reverse token-blocked classifications: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE ascp_asset_health SET state='NORMAL',epoch=epoch+1,evidence_digest=$3,updated_at=$4
		WHERE chain_id=$1 AND asset=$2`, config.ChainID, config.Asset, proof.EvidenceDigest, now); err != nil {
		return Record{}, fmt.Errorf("complete asset recovery: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Record{}, err
	}
	record.State, record.Epoch, record.EvidenceDigest, record.UpdatedAt = Normal, record.Epoch+1, proof.EvidenceDigest, now
	return record, nil
}

func retryableAssetHealthTransaction(err error) bool {
	var postgresError *pgconn.PgError
	return errors.As(err, &postgresError) && (postgresError.Code == "40001" || postgresError.Code == "40P01")
}

func (s *PostgresStore) Get(ctx context.Context, chainID uint64, asset string) (Record, error) {
	return readRecord(ctx, s.db, chainID, asset, false)
}

type recordQuery interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func readRecord(ctx context.Context, query recordQuery, chainID uint64, asset string, lock bool) (Record, error) {
	suffix := ""
	if lock {
		suffix = " FOR UPDATE"
	}
	var record Record
	var providers []byte
	var digest sql.NullString
	var block sql.NullInt64
	var observed sql.NullTime
	err := query.QueryRowContext(ctx, `
		SELECT chain_id,asset,proxy_implementation,runtime_code_hash,quorum,state,epoch,evidence_digest,providers,finalized_block,observed_at,updated_at
		FROM ascp_asset_health WHERE chain_id=$1 AND asset=$2`+suffix, chainID, asset).Scan(
		&record.ChainID, &record.Asset, &record.ProxyImplementation, &record.RuntimeCodeHash, &record.Quorum, &record.State, &record.Epoch,
		&digest, &providers, &block, &observed, &record.UpdatedAt)
	if err != nil {
		return Record{}, err
	}
	record.EvidenceDigest, record.FinalizedBlock, record.ObservedAt = digest.String, uint64(block.Int64), observed.Time.UTC()
	if len(providers) > 0 {
		if err := json.Unmarshal(providers, &record.Providers); err != nil {
			return Record{}, err
		}
	}
	return record, nil
}
