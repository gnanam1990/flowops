package ascpassethealth

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

const maximumRecoveryProofAge = time.Minute

type recoveryCounts struct {
	PendingOperations      int64
	StaleCanonicalAttempts int64
	UnclassifiedLocks      int64
}

type recoveryCountQuery interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// PostgresRecoveryVerifier derives recovery readiness from durable settlement,
// canonicality, and classified-ledger state. Callers cannot self-attest that a
// backfill is complete.
type PostgresRecoveryVerifier struct {
	db    *sql.DB
	clock func() time.Time
}

func NewPostgresRecoveryVerifier(db *sql.DB, clock func() time.Time) (*PostgresRecoveryVerifier, error) {
	if db == nil {
		return nil, errors.New("database is required")
	}
	if clock == nil {
		clock = time.Now
	}
	return &PostgresRecoveryVerifier{db: db, clock: clock}, nil
}

func (v *PostgresRecoveryVerifier) VerifyRecovery(ctx context.Context, record Record) (RecoveryProof, error) {
	if record.State != Recovering || record.Epoch == 0 || record.ObservedAt.IsZero() ||
		!canonicalAddress(record.Asset) || !canonicalHash(record.EvidenceDigest) || record.FinalizedBlock == 0 {
		return RecoveryProof{}, ErrRecoveryIncomplete
	}
	tx, err := v.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable, ReadOnly: true})
	if err != nil {
		return RecoveryProof{}, fmt.Errorf("begin recovery verification: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	counts, err := readRecoveryCounts(ctx, tx, record.ChainID, record.Asset, record.ObservedAt)
	if err != nil {
		return RecoveryProof{}, err
	}
	if counts != (recoveryCounts{}) {
		return RecoveryProof{}, ErrRecoveryIncomplete
	}
	reconciledAt := v.clock().UTC()
	proof := RecoveryProof{
		ChainID: record.ChainID, Asset: record.Asset, HealthEpoch: record.Epoch,
		CleanEvidenceDigest: record.EvidenceDigest, CleanFinalizedBlock: record.FinalizedBlock,
		ReconciledAt: reconciledAt,
	}
	proof.EvidenceDigest = recoveryEvidenceDigest(proof, counts)
	if err := tx.Commit(); err != nil {
		return RecoveryProof{}, fmt.Errorf("commit recovery verification: %w", err)
	}
	return proof, nil
}

func readRecoveryCounts(ctx context.Context, query recoveryCountQuery, chainID uint64, asset string, cleanObservedAt time.Time) (recoveryCounts, error) {
	var counts recoveryCounts
	if err := query.QueryRowContext(ctx, `
		SELECT count(*)
		FROM ascp_payment_operations o
		WHERE o.chain_id=$1 AND o.asset=$2 AND (
		  o.state IN ('AUTH_SIGNED','LOCK_SUBMITTED','LOCKED_SAFE','REORGED_BACK','PENDING_CHAIN_RECOVERY','QUARANTINED')
		  OR (o.state='LOCKED_FINALIZED' AND (
		    NOT EXISTS (
		      SELECT 1 FROM ascp_payment_attempts a
		      WHERE a.operation_id=o.operation_id AND a.action='LOCK' AND a.state='FINALIZED'
		    ) OR NOT EXISTS (
		      SELECT 1 FROM ascp_ledger_transactions l
		      WHERE l.operation_id=o.operation_id AND l.kind='LOCK_FINALIZED'
		    )
		  ))
		)`, chainID, asset).Scan(&counts.PendingOperations); err != nil {
		return recoveryCounts{}, fmt.Errorf("count unreconciled asset operations: %w", err)
	}
	if err := query.QueryRowContext(ctx, `
		SELECT count(*)
		FROM ascp_payment_attempts a
		JOIN ascp_payment_operations o ON o.operation_id=a.operation_id
		WHERE o.chain_id=$1 AND o.asset=$2 AND a.state='FINALIZED'
		  AND (a.canonical_checked_at IS NULL OR a.canonical_checked_at<$3)`,
		chainID, asset, cleanObservedAt).Scan(&counts.StaleCanonicalAttempts); err != nil {
		return recoveryCounts{}, fmt.Errorf("count stale canonical checks: %w", err)
	}
	if err := query.QueryRowContext(ctx, `
		SELECT count(*)
		FROM ascp_payment_operations o
		WHERE o.chain_id=$1 AND o.asset=$2 AND o.state='LOCKED_FINALIZED'
		  AND EXISTS (
		    SELECT 1 FROM ascp_ledger_transactions l
		    WHERE l.operation_id=o.operation_id AND l.kind='LOCK_FINALIZED'
		  )
		  AND NOT EXISTS (
		    SELECT 1 FROM ascp_asset_reclassifications b
		    WHERE b.operation_id=o.operation_id AND b.direction='BLOCK'
		      AND NOT EXISTS (
		        SELECT 1 FROM ascp_asset_reclassifications r
		        WHERE r.original_block_evidence=b.evidence_digest
		          AND r.operation_id=b.operation_id AND r.direction='RECOVER'
		      )
		  )`, chainID, asset).Scan(&counts.UnclassifiedLocks); err != nil {
		return recoveryCounts{}, fmt.Errorf("count unclassified locked funds: %w", err)
	}
	return counts, nil
}

func validateRecoveryProof(record Record, proof RecoveryProof, now time.Time) error {
	if record.State != Recovering || proof.ChainID != record.ChainID || proof.Asset != record.Asset ||
		proof.HealthEpoch != record.Epoch || proof.CleanEvidenceDigest != record.EvidenceDigest ||
		proof.CleanFinalizedBlock != record.FinalizedBlock || proof.ReconciledAt.IsZero() ||
		proof.ReconciledAt.Before(record.ObservedAt) || proof.ReconciledAt.After(now.Add(time.Minute)) ||
		now.Sub(proof.ReconciledAt) > maximumRecoveryProofAge ||
		proof.EvidenceDigest != recoveryEvidenceDigest(proof, recoveryCounts{}) {
		return ErrRecoveryIncomplete
	}
	return nil
}

func recoveryEvidenceDigest(proof RecoveryProof, counts recoveryCounts) string {
	payload := struct {
		Version                    string `json:"version"`
		ChainID                    uint64 `json:"chainId"`
		Asset                      string `json:"asset"`
		HealthEpoch                uint64 `json:"healthEpoch"`
		CleanEvidenceDigest        string `json:"cleanEvidenceDigest"`
		CleanFinalizedBlock        uint64 `json:"cleanFinalizedBlock"`
		ReconciledAt               string `json:"reconciledAt"`
		PendingOperations          int64  `json:"pendingOperations"`
		StaleCanonicalAttempts     int64  `json:"staleCanonicalAttempts"`
		UnclassifiedFinalizedLocks int64  `json:"unclassifiedFinalizedLocks"`
	}{
		Version: "ASCP_ASSET_RECOVERY_V1", ChainID: proof.ChainID, Asset: proof.Asset,
		HealthEpoch: proof.HealthEpoch, CleanEvidenceDigest: proof.CleanEvidenceDigest,
		CleanFinalizedBlock: proof.CleanFinalizedBlock, ReconciledAt: proof.ReconciledAt.UTC().Format(time.RFC3339Nano),
		PendingOperations: counts.PendingOperations, StaleCanonicalAttempts: counts.StaleCanonicalAttempts,
		UnclassifiedFinalizedLocks: counts.UnclassifiedLocks,
	}
	encoded, _ := json.Marshal(payload)
	digest := sha256.Sum256(encoded)
	return "0x" + hex.EncodeToString(digest[:])
}
