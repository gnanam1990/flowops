package ascpsettlement

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/gnanam1990/flowops/internal/ascpreservation"
	"github.com/gnanam1990/flowops/internal/reconciliation"
	"github.com/jackc/pgx/v5/pgconn"
)

const maximumRecordDelay = time.Minute

type PostgresStore struct {
	db    *sql.DB
	clock func() time.Time
}

func (s *PostgresStore) Pending(ctx context.Context, limit int) ([]Attempt, error) {
	if limit < 1 || limit > 1000 {
		return nil, ErrInvalidAttempt
	}
	return s.listAttempts(ctx, `state IN ('SUBMITTED','CONFIRMED_SAFE')`, limit)
}

func (s *PostgresStore) FinalizedUnchecked(ctx context.Context, limit int) ([]Attempt, error) {
	if limit < 1 || limit > 1000 {
		return nil, ErrInvalidAttempt
	}
	return s.listAttempts(ctx, `state='FINALIZED' AND (
		canonical_checked_at IS NULL OR EXISTS (
			SELECT 1
			FROM ascp_payment_operations recovery_operation
			JOIN ascp_asset_health recovery_health
			  ON recovery_health.chain_id=recovery_operation.chain_id
			 AND recovery_health.asset=recovery_operation.asset
			WHERE recovery_operation.operation_id=ascp_payment_attempts.operation_id
			  AND recovery_health.state='RECOVERING'
			  AND recovery_health.observed_at IS NOT NULL
			  AND canonical_checked_at<recovery_health.observed_at
		)
	)`, limit)
}

func (s *PostgresStore) listAttempts(ctx context.Context, predicate string, limit int) ([]Attempt, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT operation_id, action, transaction_hash, delivery_hash, evidence_hash, state,
		       registered_at, resolved_at, block_number, block_hash, evidence_digest, canonical_checked_at
		FROM ascp_payment_attempts WHERE `+predicate+` ORDER BY registered_at, operation_id LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("list ASCP payment attempts: %w", err)
	}
	defer rows.Close()
	var attempts []Attempt
	for rows.Next() {
		attempt, err := scanAttempt(rows)
		if err != nil {
			return nil, err
		}
		attempts = append(attempts, attempt)
	}
	return attempts, rows.Err()
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

func (s *PostgresStore) RegisterAttempt(ctx context.Context, input AttemptInput) (Attempt, bool, error) {
	now := s.clock().UTC()
	if !validAttemptInput(input) {
		return Attempt{}, false, ErrInvalidAttempt
	}
	for attempt := 0; attempt < 3; attempt++ {
		output, replay, err := s.registerAttemptOnce(ctx, input, now)
		if !serializationFailure(err) {
			return output, replay, err
		}
		if err := ctx.Err(); err != nil {
			return Attempt{}, false, err
		}
	}
	return Attempt{}, false, errors.New("ASCP attempt registration serialization retries exhausted")
}

func (s *PostgresStore) registerAttemptOnce(ctx context.Context, input AttemptInput, now time.Time) (Attempt, bool, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return Attempt{}, false, fmt.Errorf("begin ASCP attempt registration: %w", err)
	}
	defer tx.Rollback()
	operation, err := loadOperation(ctx, tx, input.OperationID, true)
	if err != nil {
		return Attempt{}, false, err
	}
	recoveryRetryValidated := false
	if existing, err := loadAttempt(ctx, tx, input.OperationID, input.Action, true); err == nil {
		if existing.AttemptInput == input {
			if err := tx.Commit(); err != nil {
				return Attempt{}, false, err
			}
			return existing, true, nil
		}
		if err := validateAssetRecoveryRetry(ctx, tx, operation, existing, input, now); err != nil {
			return Attempt{}, false, err
		}
		recoveryRetryValidated = true
	} else if !errors.Is(err, sql.ErrNoRows) {
		return Attempt{}, false, err
	}
	retry := operation.State == PendingChainRecovery
	if retry && !recoveryRetryValidated {
		return Attempt{}, false, ErrStateConflict
	}
	switch input.Action {
	case reconciliation.ASCPReceiptLock:
		if !retry && (operation.State != AuthSigned || operation.LockedTransactionHash != "") {
			return Attempt{}, false, ErrStateConflict
		}
	case reconciliation.ASCPReceiptRelease, reconciliation.ASCPReceiptRefund:
		if !retry && (operation.State != LockedSafe && operation.State != LockedFinalized || operation.TerminalTransactionHash != "") {
			return Attempt{}, false, ErrStateConflict
		}
	default:
		return Attempt{}, false, ErrInvalidAttempt
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO ascp_payment_attempts
			(operation_id, action, transaction_hash, delivery_hash, evidence_hash, state, registered_at)
		VALUES ($1,$2,$3,NULLIF($4,''),NULLIF($5,''),'SUBMITTED',$6)`, input.OperationID,
		input.Action, input.TransactionHash, input.DeliveryHash, input.EvidenceHash, now); err != nil {
		return Attempt{}, false, classifyAttemptWrite(err)
	}
	if input.Action == reconciliation.ASCPReceiptLock {
		fromState := AuthSigned
		if retry {
			fromState = PendingChainRecovery
		}
		_, err = tx.ExecContext(ctx, `
			UPDATE ascp_payment_operations
			SET state='LOCK_SUBMITTED', locked_transaction_hash=$2, updated_at=$3
			WHERE operation_id=$1 AND state=$4`, input.OperationID, input.TransactionHash, now, fromState)
	} else {
		targetState := operation.State
		if retry {
			var lockFinalized bool
			if err := tx.QueryRowContext(ctx, `SELECT EXISTS (
				SELECT 1 FROM ascp_ledger_transactions
				WHERE operation_id=$1 AND kind='LOCK_FINALIZED'
				  AND NOT EXISTS (
					SELECT 1 FROM ascp_ledger_transactions reversal
					WHERE reversal.reversal_of=ascp_ledger_transactions.transaction_id
				  )
			)`, input.OperationID).Scan(&lockFinalized); err != nil {
				return Attempt{}, false, fmt.Errorf("derive ASCP retry lock finality: %w", err)
			}
			targetState = LockedSafe
			if lockFinalized {
				targetState = LockedFinalized
			}
		}
		_, err = tx.ExecContext(ctx, `
			UPDATE ascp_payment_operations
			SET state=$2, terminal_action=$3, terminal_transaction_hash=$4,
			    terminal_block_number=NULL, terminal_block_hash=NULL, updated_at=$5
			WHERE operation_id=$1 AND state IN ('LOCKED_SAFE','LOCKED_FINALIZED')`, input.OperationID,
			targetState, input.Action, input.TransactionHash, now)
		if retry {
			_, err = tx.ExecContext(ctx, `
				UPDATE ascp_payment_operations
				SET state=$2, terminal_action=$3, terminal_transaction_hash=$4,
				    terminal_block_number=NULL, terminal_block_hash=NULL, updated_at=$5
				WHERE operation_id=$1 AND state='PENDING_CHAIN_RECOVERY'`, input.OperationID,
				targetState, input.Action, input.TransactionHash, now)
		}
	}
	if err != nil {
		return Attempt{}, false, fmt.Errorf("bind ASCP payment attempt: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Attempt{}, false, fmt.Errorf("commit ASCP attempt registration: %w", err)
	}
	return Attempt{AttemptInput: input, State: AttemptSubmitted, RegisteredAt: now}, false, nil
}

func (s *PostgresStore) Expected(ctx context.Context, operationID string, action reconciliation.ASCPReceiptAction) (reconciliation.ASCPExpectedReceipt, error) {
	if !hash(operationID) {
		return reconciliation.ASCPExpectedReceipt{}, ErrInvalidAttempt
	}
	operation, err := loadOperation(ctx, s.db, operationID, false)
	if err != nil {
		return reconciliation.ASCPExpectedReceipt{}, err
	}
	attempt, err := loadAttempt(ctx, s.db, operationID, action, false)
	if errors.Is(err, sql.ErrNoRows) {
		return reconciliation.ASCPExpectedReceipt{}, ErrNotFound
	}
	if err != nil {
		return reconciliation.ASCPExpectedReceipt{}, err
	}
	return expectedReceipt(operation, attempt), nil
}

func (s *PostgresStore) Apply(ctx context.Context, result Result) (Operation, error) {
	now := s.clock().UTC()
	if err := validateResult(result); err != nil {
		return Operation{}, err
	}
	if result.ObservedAt.After(now.Add(maximumRecordDelay)) || now.Sub(result.ObservedAt) > maximumRecordDelay {
		return Operation{}, ErrInvalidResult
	}
	for attempt := 0; attempt < 3; attempt++ {
		operation, err := s.applyOnce(ctx, result, now)
		if !serializationFailure(err) {
			return operation, err
		}
		if err := ctx.Err(); err != nil {
			return Operation{}, err
		}
	}
	return Operation{}, errors.New("ASCP settlement serialization retries exhausted")
}

func (s *PostgresStore) ApplyReorg(ctx context.Context, result ReorgResult) (Operation, error) {
	now := s.clock().UTC()
	if err := validateReorgResult(result); err != nil || result.ObservedAt.After(now.Add(maximumRecordDelay)) || now.Sub(result.ObservedAt) > maximumRecordDelay {
		return Operation{}, ErrInvalidReorgResult
	}
	for attempt := 0; attempt < 3; attempt++ {
		operation, err := s.applyReorgOnce(ctx, result, now)
		if !serializationFailure(err) {
			return operation, err
		}
		if err := ctx.Err(); err != nil {
			return Operation{}, err
		}
	}
	return Operation{}, errors.New("ASCP reorg serialization retries exhausted")
}

func (s *PostgresStore) ConfirmCanonical(ctx context.Context, result ReorgResult) error {
	now := s.clock().UTC()
	if err := validateCanonicalResult(result); err != nil || result.Reorged ||
		result.ObservedAt.After(now.Add(maximumRecordDelay)) || now.Sub(result.ObservedAt) > maximumRecordDelay {
		return ErrInvalidReorgResult
	}
	updated, err := s.db.ExecContext(ctx, `
		UPDATE ascp_payment_attempts SET canonical_checked_at=$6
		WHERE operation_id=$1 AND action=$2 AND transaction_hash=$3 AND state='FINALIZED'
		  AND block_number=$4 AND block_hash=$5
		  AND (canonical_checked_at IS NULL OR canonical_checked_at<$6)`, result.OperationID, result.Action,
		result.TransactionHash, result.BlockNumber, result.OriginalBlockHash, now)
	if err != nil {
		return fmt.Errorf("record ASCP canonical finality check: %w", err)
	}
	count, err := updated.RowsAffected()
	if err != nil {
		return err
	}
	if count == 1 {
		return nil
	}
	var checked sql.NullTime
	if err := s.db.QueryRowContext(ctx, `
		SELECT canonical_checked_at FROM ascp_payment_attempts
		WHERE operation_id=$1 AND action=$2 AND transaction_hash=$3 AND state='FINALIZED'
		  AND block_number=$4 AND block_hash=$5`, result.OperationID, result.Action, result.TransactionHash,
		result.BlockNumber, result.OriginalBlockHash).Scan(&checked); err != nil || !checked.Valid || checked.Time.Before(now) {
		return ErrStateConflict
	}
	return nil
}

func (s *PostgresStore) applyReorgOnce(ctx context.Context, result ReorgResult, now time.Time) (Operation, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return Operation{}, fmt.Errorf("begin ASCP reorg transaction: %w", err)
	}
	defer tx.Rollback()
	operation, err := loadOperation(ctx, tx, result.OperationID, true)
	if err != nil {
		return Operation{}, err
	}
	attempt, err := loadAttemptByTransaction(ctx, tx, result.OperationID, result.Action, result.TransactionHash, true)
	if err != nil {
		return Operation{}, err
	}
	if existing, found, err := observationStageExists(ctx, tx, operation.OperationID, result.Action, "REORGED", result.OriginalBlockHash); err != nil {
		return Operation{}, err
	} else if found {
		if existing != result.TransactionHash {
			return Operation{}, ErrStateConflict
		}
		if err := tx.Commit(); err != nil {
			return Operation{}, err
		}
		return operation, nil
	}
	if attempt.TransactionHash != result.TransactionHash || attempt.State != AttemptFinalized ||
		attempt.BlockNumber != result.BlockNumber || attempt.BlockHash != result.OriginalBlockHash {
		return Operation{}, ErrStateConflict
	}
	var reservationState string
	if err := tx.QueryRowContext(ctx, `SELECT state FROM ascp_budget_reservations WHERE reservation_id=$1 FOR UPDATE`, operation.ReservationID).Scan(&reservationState); err != nil {
		return Operation{}, fmt.Errorf("lock reorg reservation: %w", err)
	}
	if existing, found, err := observationExists(ctx, tx, result.EvidenceDigest); err != nil {
		return Operation{}, err
	} else if found {
		if existing != operation.OperationID {
			return Operation{}, ErrStateConflict
		}
		if err := tx.Commit(); err != nil {
			return Operation{}, err
		}
		return operation, nil
	}
	providers, _ := json.Marshal(result.Providers)
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO ascp_chain_observations
			(evidence_digest, operation_id, action, finality, transaction_hash, block_number,
			 block_hash, canonical_block_hash, confirmed_head, providers, observed_at)
		VALUES ($1,$2,$3,'REORGED',$4,$5,$6,$7,$8,$9,$10)`, result.EvidenceDigest,
		operation.OperationID, result.Action, result.TransactionHash, result.BlockNumber,
		result.OriginalBlockHash, result.CanonicalBlockHash, result.ObservedHead, providers, result.ObservedAt); err != nil {
		return Operation{}, fmt.Errorf("append ASCP reorg observation: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE ascp_payment_attempts SET state='REORGED', resolved_at=$4, evidence_digest=$5
		WHERE operation_id=$1 AND action=$2 AND transaction_hash=$3`, operation.OperationID, result.Action,
		result.TransactionHash, now, result.EvidenceDigest); err != nil {
		return Operation{}, fmt.Errorf("mark ASCP attempt reorged: %w", err)
	}
	if result.Action == reconciliation.ASCPReceiptLock {
		if reservationState != string(ascpreservation.CommittedFinalized) && reservationState != string(ascpreservation.Consumed) && reservationState != string(ascpreservation.Restored) {
			return Operation{}, ErrStateConflict
		}
		if err := reverseLedger(ctx, tx, operation, result.EvidenceDigest, "", now); err != nil {
			return Operation{}, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE ascp_budget_reservations SET state='REORGED_BACK' WHERE reservation_id=$1`, operation.ReservationID); err != nil {
			return Operation{}, err
		}
		operation.State = ReorgedBack
	} else {
		expectedState, expectedReservation, kind := ReleasedFinalized, string(ascpreservation.Consumed), "RELEASE_FINALIZED"
		if result.Action == reconciliation.ASCPReceiptRefund {
			expectedState, expectedReservation, kind = RefundedFinalized, string(ascpreservation.Restored), "REFUND_FINALIZED"
		}
		if operation.State != expectedState || reservationState != expectedReservation {
			return Operation{}, ErrStateConflict
		}
		if err := reverseLedger(ctx, tx, operation, result.EvidenceDigest, kind, now); err != nil {
			return Operation{}, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE ascp_budget_reservations SET state='COMMITTED_FINALIZED' WHERE reservation_id=$1`, operation.ReservationID); err != nil {
			return Operation{}, err
		}
		operation.State = PendingChainRecovery
	}
	operation.UpdatedAt = now
	if _, err := tx.ExecContext(ctx, `UPDATE ascp_payment_operations SET state=$2, updated_at=$3 WHERE operation_id=$1`, operation.OperationID, operation.State, now); err != nil {
		return Operation{}, err
	}
	if err := tx.Commit(); err != nil {
		return Operation{}, fmt.Errorf("commit ASCP reorg recovery: %w", err)
	}
	return operation, nil
}

func (s *PostgresStore) applyOnce(ctx context.Context, result Result, now time.Time) (Operation, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return Operation{}, fmt.Errorf("begin ASCP settlement transaction: %w", err)
	}
	defer tx.Rollback()
	operation, err := loadOperation(ctx, tx, result.Expected.OperationID, true)
	if err != nil {
		return Operation{}, err
	}
	attempt, err := loadAttemptByTransaction(ctx, tx, result.Expected.OperationID, result.Expected.Action, result.Expected.TransactionHash, true)
	if err != nil {
		return Operation{}, err
	}
	if expectedReceipt(operation, attempt) != result.Expected {
		return Operation{}, ErrStateConflict
	}
	if existing, found, err := observationStageExists(ctx, tx, operation.OperationID, result.Expected.Action, string(result.Finality), result.BlockHash); err != nil {
		return Operation{}, err
	} else if found {
		if existing != result.Expected.TransactionHash {
			return Operation{}, ErrStateConflict
		}
		if err := tx.Commit(); err != nil {
			return Operation{}, err
		}
		return operation, nil
	}
	var reservationState string
	err = tx.QueryRowContext(ctx, `
		SELECT state FROM ascp_budget_reservations
		WHERE reservation_id=$1 AND operation_id=$2 FOR UPDATE`, operation.ReservationID, operation.OperationID).
		Scan(&reservationState)
	if err != nil {
		return Operation{}, fmt.Errorf("lock ASCP settlement reservation: %w", err)
	}
	if existing, found, err := observationExists(ctx, tx, result.EvidenceDigest); err != nil {
		return Operation{}, err
	} else if found {
		if existing != result.Expected.OperationID {
			return Operation{}, ErrStateConflict
		}
		if err := tx.Commit(); err != nil {
			return Operation{}, err
		}
		return operation, nil
	}
	providers, _ := json.Marshal(result.Providers)
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO ascp_chain_observations
			(evidence_digest, operation_id, action, finality, transaction_hash, block_number,
			 block_hash, confirmed_head, providers, observed_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, result.EvidenceDigest,
		operation.OperationID, result.Expected.Action, result.Finality, result.Expected.TransactionHash,
		result.BlockNumber, result.BlockHash, result.ConfirmedHead, providers, result.ObservedAt); err != nil {
		return Operation{}, fmt.Errorf("append ASCP chain observation: %w", err)
	}
	if !result.Success {
		if attempt.State != AttemptSubmitted && attempt.State != AttemptSafe {
			return Operation{}, ErrStateConflict
		}
		if result.Expected.Action == reconciliation.ASCPReceiptLock {
			if operation.State != LockSubmitted && operation.State != LockedSafe {
				return Operation{}, ErrStateConflict
			}
		} else if operation.State != LockedSafe && operation.State != LockedFinalized {
			return Operation{}, ErrStateConflict
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE ascp_payment_attempts SET state='REVERTED', resolved_at=$4, block_number=$5,
				block_hash=$6, evidence_digest=$7
			WHERE operation_id=$1 AND action=$2 AND transaction_hash=$3`, operation.OperationID,
			result.Expected.Action, result.Expected.TransactionHash, now, result.BlockNumber,
			result.BlockHash, result.EvidenceDigest); err != nil {
			return Operation{}, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE ascp_payment_operations SET state='PENDING_CHAIN_RECOVERY', updated_at=$2 WHERE operation_id=$1`, operation.OperationID, now); err != nil {
			return Operation{}, err
		}
		operation.State, operation.UpdatedAt = PendingChainRecovery, now
		if err := tx.Commit(); err != nil {
			return Operation{}, err
		}
		return operation, nil
	}
	if err := applySuccessfulResult(ctx, tx, &operation, attempt, ascpreservation.State(reservationState), result, now); err != nil {
		return Operation{}, err
	}
	if err := tx.Commit(); err != nil {
		return Operation{}, fmt.Errorf("commit ASCP settlement result: %w", err)
	}
	return operation, nil
}

func applySuccessfulResult(ctx context.Context, tx *sql.Tx, operation *Operation, attempt Attempt, reservationState ascpreservation.State, result Result, now time.Time) error {
	action := result.Expected.Action
	if action == reconciliation.ASCPReceiptLock {
		if result.Finality == Safe && attempt.State != AttemptSubmitted ||
			result.Finality == Finalized && attempt.State != AttemptSubmitted && attempt.State != AttemptSafe {
			return ErrStateConflict
		}
		if operation.State != LockSubmitted && operation.State != LockedSafe ||
			reservationState != ascpreservation.AuthorizationLive && reservationState != ascpreservation.CommittedSafe {
			return ErrStateConflict
		}
		targetAttempt, targetOperation, targetReservation := AttemptSafe, LockedSafe, ascpreservation.CommittedSafe
		if result.Finality == Finalized {
			targetAttempt, targetOperation, targetReservation = AttemptFinalized, LockedFinalized, ascpreservation.CommittedFinalized
		}
		if _, err := tx.ExecContext(ctx, `UPDATE ascp_budget_reservations SET state=$2 WHERE reservation_id=$1`, operation.ReservationID, targetReservation); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE ascp_bearer_registry SET outcome='CONSUMED' WHERE digest=$1 AND outcome IN ('LIVE','CONSUMED')`, operation.BearerDigest); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE ascp_payment_attempts SET state=$4, resolved_at=$5, block_number=$6, block_hash=$7, evidence_digest=$8
			WHERE operation_id=$1 AND action=$2 AND transaction_hash=$3`, operation.OperationID, action,
			attempt.TransactionHash, targetAttempt, now, result.BlockNumber, result.BlockHash, result.EvidenceDigest); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE ascp_payment_operations SET state=$2, locked_block_number=$3, locked_block_hash=$4, updated_at=$5
			WHERE operation_id=$1`, operation.OperationID, targetOperation, result.BlockNumber, result.BlockHash, now); err != nil {
			return err
		}
		operation.State, operation.LockedBlockNumber, operation.LockedBlockHash, operation.UpdatedAt = targetOperation, result.BlockNumber, result.BlockHash, now
		if result.Finality == Finalized {
			return postLedger(ctx, tx, *operation, "LOCK_FINALIZED", result.EvidenceDigest, "", now)
		}
		return nil
	}
	if operation.State != LockedFinalized || reservationState != ascpreservation.CommittedFinalized || result.Finality != Finalized {
		if result.Finality == Safe && (operation.State == LockedSafe || operation.State == LockedFinalized) {
			if attempt.State != AttemptSubmitted {
				return ErrStateConflict
			}
			_, err := tx.ExecContext(ctx, `
				UPDATE ascp_payment_attempts SET state='CONFIRMED_SAFE', resolved_at=$4, block_number=$5, block_hash=$6, evidence_digest=$7
				WHERE operation_id=$1 AND action=$2 AND transaction_hash=$3`, operation.OperationID, action,
				attempt.TransactionHash, now, result.BlockNumber, result.BlockHash, result.EvidenceDigest)
			return err
		}
		return ErrStateConflict
	}
	if attempt.State != AttemptSubmitted && attempt.State != AttemptSafe {
		return ErrStateConflict
	}
	targetOperation, targetReservation, ledgerKind := ReleasedFinalized, ascpreservation.Consumed, "RELEASE_FINALIZED"
	if action == reconciliation.ASCPReceiptRefund {
		targetOperation, targetReservation, ledgerKind = RefundedFinalized, ascpreservation.Restored, "REFUND_FINALIZED"
	} else if action != reconciliation.ASCPReceiptRelease {
		return ErrStateConflict
	}
	if _, err := tx.ExecContext(ctx, `UPDATE ascp_budget_reservations SET state=$2 WHERE reservation_id=$1`, operation.ReservationID, targetReservation); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE ascp_payment_attempts SET state='FINALIZED', resolved_at=$4, block_number=$5, block_hash=$6, evidence_digest=$7
		WHERE operation_id=$1 AND action=$2 AND transaction_hash=$3`, operation.OperationID, action,
		attempt.TransactionHash, now, result.BlockNumber, result.BlockHash, result.EvidenceDigest); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE ascp_payment_operations SET state=$2, terminal_block_number=$3, terminal_block_hash=$4, updated_at=$5
		WHERE operation_id=$1`, operation.OperationID, targetOperation, result.BlockNumber, result.BlockHash, now); err != nil {
		return err
	}
	operation.State, operation.TerminalBlockNumber, operation.TerminalBlockHash, operation.UpdatedAt = targetOperation, result.BlockNumber, result.BlockHash, now
	return postLedger(ctx, tx, *operation, ledgerKind, result.EvidenceDigest, "", now)
}

func postLedger(ctx context.Context, tx *sql.Tx, operation Operation, kind, evidenceDigest, reversalOf string, now time.Time) error {
	transactionID := ledgerID(operation.OperationID, kind, evidenceDigest, reversalOf)
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO ascp_ledger_transactions
			(transaction_id, organization_id, operation_id, kind, asset, evidence_digest, reversal_of, recorded_at)
		VALUES ($1,$2,$3,$4,$5,$6,NULLIF($7,''),$8)`, transactionID, operation.OrganizationID,
		operation.OperationID, kind, operation.Asset, evidenceDigest, reversalOf, now); err != nil {
		return fmt.Errorf("append ASCP ledger transaction: %w", err)
	}
	debit, credit := "EscrowRestrictedUSDC", "WalletAvailableUSDC"
	if kind == "RELEASE_FINALIZED" {
		debit, credit = "SellerExpense", "EscrowRestrictedUSDC"
	} else if kind == "REFUND_FINALIZED" {
		debit, credit = "WalletAvailableUSDC", "EscrowRestrictedUSDC"
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO ascp_ledger_postings (transaction_id, line_number, account, amount_base_units)
		VALUES ($1,1,$2,$3),($1,2,$4,$5)`, transactionID, debit, operation.AmountBaseUnits,
		credit, "-"+operation.AmountBaseUnits); err != nil {
		return fmt.Errorf("append balanced ASCP ledger postings: %w", err)
	}
	return nil
}

func reverseLedger(ctx context.Context, tx *sql.Tx, operation Operation, evidenceDigest, onlyKind string, now time.Time) error {
	query := `
		SELECT transaction_id, kind
		FROM ascp_ledger_transactions original
		WHERE operation_id=$1 AND kind <> 'REVERSAL'
		  AND NOT EXISTS (SELECT 1 FROM ascp_ledger_transactions reversal WHERE reversal.reversal_of=original.transaction_id)`
	args := []any{operation.OperationID}
	if onlyKind != "" {
		query += ` AND kind=$2`
		args = append(args, onlyKind)
	}
	query += ` ORDER BY recorded_at, transaction_id FOR UPDATE`
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("read reversible ASCP ledger entries: %w", err)
	}
	type original struct{ id, kind string }
	var originals []original
	for rows.Next() {
		var item original
		if err := rows.Scan(&item.id, &item.kind); err != nil {
			rows.Close()
			return err
		}
		originals = append(originals, item)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if len(originals) == 0 {
		return ErrStateConflict
	}
	for _, item := range originals {
		transactionID := ledgerID(operation.OperationID, "REVERSAL", evidenceDigest, item.id)
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO ascp_ledger_transactions
				(transaction_id, organization_id, operation_id, kind, asset, evidence_digest, reversal_of, recorded_at)
			VALUES ($1,$2,$3,'REVERSAL',$4,$5,$6,$7)`, transactionID, operation.OrganizationID,
			operation.OperationID, operation.Asset, evidenceDigest, item.id, now); err != nil {
			return fmt.Errorf("append ASCP ledger reversal: %w", err)
		}
		postings, err := tx.QueryContext(ctx, `SELECT line_number, account, amount_base_units FROM ascp_ledger_postings WHERE transaction_id=$1 ORDER BY line_number`, item.id)
		if err != nil {
			return err
		}
		type posting struct {
			line    int
			account string
			amount  string
		}
		var originalPostings []posting
		for postings.Next() {
			var entry posting
			if err := postings.Scan(&entry.line, &entry.account, &entry.amount); err != nil {
				postings.Close()
				return err
			}
			originalPostings = append(originalPostings, entry)
		}
		if err := postings.Close(); err != nil {
			return err
		}
		if len(originalPostings) < 2 {
			return ErrStateConflict
		}
		for _, entry := range originalPostings {
			value, ok := new(big.Int).SetString(entry.amount, 10)
			if !ok || value.Sign() == 0 {
				return ErrStateConflict
			}
			value.Neg(value)
			if _, err := tx.ExecContext(ctx, `INSERT INTO ascp_ledger_postings (transaction_id,line_number,account,amount_base_units) VALUES ($1,$2,$3,$4)`, transactionID, entry.line, entry.account, value.String()); err != nil {
				return err
			}
		}
	}
	return nil
}

func expectedReceipt(operation Operation, attempt Attempt) reconciliation.ASCPExpectedReceipt {
	return reconciliation.ASCPExpectedReceipt{
		Action: attempt.Action, TransactionHash: attempt.TransactionHash, ChainID: operation.ChainID,
		Contract: operation.EscrowContract, Asset: operation.Asset, CallID: operation.CallID,
		OperationID: operation.OperationID, CommitmentHash: operation.CommitmentHash,
		Buyer: operation.Buyer, PayTo: operation.PayTo, AmountAtomic: operation.AmountBaseUnits,
		SettleBy: uint64(operation.SettleBy.UTC().Unix()), DeliveryHash: attempt.DeliveryHash, EvidenceHash: attempt.EvidenceHash,
	}
}

type queryRower interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func loadOperation(ctx context.Context, query queryRower, operationID string, lock bool) (Operation, error) {
	suffix := ""
	if lock {
		suffix = " FOR UPDATE"
	}
	var operation Operation
	var lockedTx, lockedBlockHash, terminalAction, terminalTx, terminalBlockHash sql.NullString
	var lockedBlock, terminalBlock sql.NullInt64
	err := query.QueryRowContext(ctx, `
		SELECT operation_id, organization_id, agent_id, authorization_id, reservation_id, bearer_digest,
		       commitment_hash, call_id, chain_id, escrow_contract, asset, buyer, pay_to, amount_base_units,
		       settle_by, state, locked_transaction_hash, locked_block_number, locked_block_hash,
		       terminal_action, terminal_transaction_hash, terminal_block_number, terminal_block_hash,
		       created_at, updated_at
		FROM ascp_payment_operations WHERE operation_id=$1`+suffix, operationID).Scan(
		&operation.OperationID, &operation.OrganizationID, &operation.AgentID, &operation.AuthorizationID,
		&operation.ReservationID, &operation.BearerDigest, &operation.CommitmentHash, &operation.CallID,
		&operation.ChainID, &operation.EscrowContract, &operation.Asset, &operation.Buyer, &operation.PayTo,
		&operation.AmountBaseUnits, &operation.SettleBy, &operation.State, &lockedTx, &lockedBlock,
		&lockedBlockHash, &terminalAction, &terminalTx, &terminalBlock, &terminalBlockHash,
		&operation.CreatedAt, &operation.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Operation{}, ErrNotFound
	}
	if err != nil {
		return Operation{}, fmt.Errorf("read ASCP payment operation: %w", err)
	}
	operation.LockedTransactionHash, operation.LockedBlockHash = lockedTx.String, lockedBlockHash.String
	operation.TerminalAction, operation.TerminalTransactionHash, operation.TerminalBlockHash = terminalAction.String, terminalTx.String, terminalBlockHash.String
	if lockedBlock.Valid {
		operation.LockedBlockNumber = uint64(lockedBlock.Int64)
	}
	if terminalBlock.Valid {
		operation.TerminalBlockNumber = uint64(terminalBlock.Int64)
	}
	return operation, nil
}

func loadAttempt(ctx context.Context, query queryRower, operationID string, action reconciliation.ASCPReceiptAction, lock bool) (Attempt, error) {
	suffix := ""
	if lock {
		suffix = " FOR UPDATE"
	}
	var attempt Attempt
	var delivery, evidence, blockHash, evidenceDigest sql.NullString
	var resolved, canonicalChecked sql.NullTime
	var block sql.NullInt64
	err := query.QueryRowContext(ctx, `
		SELECT operation_id, action, transaction_hash, delivery_hash, evidence_hash, state,
		       registered_at, resolved_at, block_number, block_hash, evidence_digest, canonical_checked_at
		FROM ascp_payment_attempts WHERE operation_id=$1 AND action=$2
		ORDER BY (state IN ('SUBMITTED','CONFIRMED_SAFE','FINALIZED')) DESC,
		         registered_at DESC, transaction_hash DESC LIMIT 1`+suffix, operationID, action).Scan(
		&attempt.OperationID, &attempt.Action, &attempt.TransactionHash, &delivery, &evidence,
		&attempt.State, &attempt.RegisteredAt, &resolved, &block, &blockHash, &evidenceDigest, &canonicalChecked)
	if err != nil {
		return Attempt{}, err
	}
	attempt.DeliveryHash, attempt.EvidenceHash, attempt.BlockHash, attempt.EvidenceDigest = delivery.String, evidence.String, blockHash.String, evidenceDigest.String
	if resolved.Valid {
		attempt.ResolvedAt = resolved.Time.UTC()
	}
	if block.Valid {
		attempt.BlockNumber = uint64(block.Int64)
	}
	if canonicalChecked.Valid {
		attempt.CanonicalCheckedAt = canonicalChecked.Time.UTC()
	}
	return attempt, nil
}

func loadAttemptByTransaction(ctx context.Context, query queryRower, operationID string, action reconciliation.ASCPReceiptAction, transactionHash string, lock bool) (Attempt, error) {
	suffix := ""
	if lock {
		suffix = " FOR UPDATE"
	}
	var attempt Attempt
	var delivery, evidence, blockHash, evidenceDigest sql.NullString
	var resolved, canonicalChecked sql.NullTime
	var block sql.NullInt64
	err := query.QueryRowContext(ctx, `
		SELECT operation_id, action, transaction_hash, delivery_hash, evidence_hash, state,
		       registered_at, resolved_at, block_number, block_hash, evidence_digest, canonical_checked_at
		FROM ascp_payment_attempts
		WHERE operation_id=$1 AND action=$2 AND transaction_hash=$3`+suffix,
		operationID, action, transactionHash).Scan(
		&attempt.OperationID, &attempt.Action, &attempt.TransactionHash, &delivery, &evidence,
		&attempt.State, &attempt.RegisteredAt, &resolved, &block, &blockHash, &evidenceDigest, &canonicalChecked)
	if err != nil {
		return Attempt{}, err
	}
	attempt.DeliveryHash, attempt.EvidenceHash = delivery.String, evidence.String
	attempt.BlockHash, attempt.EvidenceDigest = blockHash.String, evidenceDigest.String
	if resolved.Valid {
		attempt.ResolvedAt = resolved.Time.UTC()
	}
	if block.Valid {
		attempt.BlockNumber = uint64(block.Int64)
	}
	if canonicalChecked.Valid {
		attempt.CanonicalCheckedAt = canonicalChecked.Time.UTC()
	}
	return attempt, nil
}

func validateAssetRecoveryRetry(ctx context.Context, tx *sql.Tx, operation Operation, previous Attempt, input AttemptInput, now time.Time) error {
	if operation.State != PendingChainRecovery || previous.State != AttemptReverted || previous.Action != input.Action ||
		previous.DeliveryHash != input.DeliveryHash || previous.EvidenceHash != input.EvidenceHash {
		return ErrStateConflict
	}
	if input.Action != reconciliation.ASCPReceiptLock && operation.TerminalAction != string(input.Action) {
		return ErrStateConflict
	}
	var fresh bool
	err := tx.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM ascp_asset_health h
			JOIN ascp_asset_health_observations o
			  ON o.chain_id=h.chain_id AND o.asset=h.asset
			WHERE h.chain_id=$1 AND h.asset=$2 AND h.state='RECOVERING'
			  AND o.observed_state='NORMAL' AND o.resulting_state='RECOVERING'
			  AND o.epoch=h.epoch
			  AND o.observed_at BETWEEN $3 AND $4
		)`, operation.ChainID, operation.Asset, now.Add(-maximumRecordDelay), now.Add(maximumRecordDelay)).Scan(&fresh)
	if err != nil {
		return fmt.Errorf("verify fresh asset recovery evidence: %w", err)
	}
	if !fresh {
		return ErrStateConflict
	}
	return nil
}

type rowScanner interface {
	Scan(...any) error
}

func scanAttempt(row rowScanner) (Attempt, error) {
	var attempt Attempt
	var delivery, evidence, blockHash, evidenceDigest sql.NullString
	var resolved, canonicalChecked sql.NullTime
	var block sql.NullInt64
	if err := row.Scan(&attempt.OperationID, &attempt.Action, &attempt.TransactionHash, &delivery, &evidence,
		&attempt.State, &attempt.RegisteredAt, &resolved, &block, &blockHash, &evidenceDigest, &canonicalChecked); err != nil {
		return Attempt{}, err
	}
	attempt.DeliveryHash, attempt.EvidenceHash = delivery.String, evidence.String
	attempt.BlockHash, attempt.EvidenceDigest = blockHash.String, evidenceDigest.String
	if resolved.Valid {
		attempt.ResolvedAt = resolved.Time.UTC()
	}
	if block.Valid {
		attempt.BlockNumber = uint64(block.Int64)
	}
	if canonicalChecked.Valid {
		attempt.CanonicalCheckedAt = canonicalChecked.Time.UTC()
	}
	return attempt, nil
}

func observationExists(ctx context.Context, tx *sql.Tx, digest string) (string, bool, error) {
	var operationID string
	err := tx.QueryRowContext(ctx, `SELECT operation_id FROM ascp_chain_observations WHERE evidence_digest=$1`, digest).Scan(&operationID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return operationID, err == nil, err
}

func observationStageExists(ctx context.Context, tx *sql.Tx, operationID string, action reconciliation.ASCPReceiptAction, finality, blockHash string) (string, bool, error) {
	var transactionHash string
	err := tx.QueryRowContext(ctx, `SELECT transaction_hash FROM ascp_chain_observations
		WHERE operation_id=$1 AND action=$2 AND finality=$3 AND block_hash=$4`, operationID, action, finality, blockHash).Scan(&transactionHash)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return transactionHash, err == nil, err
}

func validAttemptInput(input AttemptInput) bool {
	if !hash(input.OperationID) || !hash(input.TransactionHash) {
		return false
	}
	if input.Action == reconciliation.ASCPReceiptRelease {
		return hash(input.DeliveryHash) && hash(input.EvidenceHash)
	}
	return (input.Action == reconciliation.ASCPReceiptLock || input.Action == reconciliation.ASCPReceiptRefund) && input.DeliveryHash == "" && input.EvidenceHash == ""
}

func ledgerID(parts ...string) string {
	digest := sha256.Sum256([]byte("ASCP_LEDGER_TRANSACTION_V1\n" + strings.Join(parts, "\n")))
	return "0x" + hex.EncodeToString(digest[:])
}

func serializationFailure(err error) bool {
	var postgresError *pgconn.PgError
	return errors.As(err, &postgresError) && postgresError.Code == "40001"
}

func classifyAttemptWrite(err error) error {
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) && postgresError.Code == "23505" {
		return ErrStateConflict
	}
	return fmt.Errorf("register ASCP payment attempt: %w", err)
}
