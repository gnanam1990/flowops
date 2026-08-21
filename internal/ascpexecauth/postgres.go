package ascpexecauth

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/gnanam1990/flowops/internal/ascpapproval"
	"github.com/gnanam1990/flowops/internal/ascpreservation"
	"github.com/jackc/pgx/v5/pgconn"
)

const ReservationTTL = 15 * time.Minute

// TransactionRevalidator performs only authoritative local checks using the
// supplied transaction. A non-empty reason is a durable business invalidation;
// an error is an infrastructure failure and must roll the transaction back.
// Network checks must be materialized into SQL before this boundary is called.
type TransactionRevalidator interface {
	Revalidate(context.Context, *sql.Tx, Input, time.Time) (reason string, err error)
}

type TransactionRevalidatorFunc func(context.Context, *sql.Tx, Input, time.Time) (string, error)

func (f TransactionRevalidatorFunc) Revalidate(ctx context.Context, tx *sql.Tx, input Input, now time.Time) (string, error) {
	return f(ctx, tx, input, now)
}

// PostgresStore is the authoritative execution-authorization boundary. It
// locks the exact approval, reruns local checks, computes every budget
// dimension, creates the reservation, and records the authorization in one
// serializable transaction.
type PostgresStore struct {
	db          *sql.DB
	revalidator TransactionRevalidator
	clock       func() time.Time
}

func NewPostgresStore(db *sql.DB, revalidator TransactionRevalidator, clocks ...func() time.Time) (*PostgresStore, error) {
	if db == nil || revalidator == nil || len(clocks) > 1 || len(clocks) == 1 && clocks[0] == nil {
		return nil, errors.New("database and transaction revalidator are required")
	}
	clock := time.Now
	if len(clocks) == 1 {
		clock = clocks[0]
	}
	return &PostgresStore{db: db, revalidator: revalidator, clock: clock}, nil
}

func (s *PostgresStore) ValidateAndReserve(ctx context.Context, input Input) (Authorization, error) {
	now := s.clock().UTC()
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		output, err := s.validateAndReserveOnce(ctx, input, now)
		if !serializationFailure(err) {
			return output, err
		}
		lastErr = err
		if err := ctx.Err(); err != nil {
			return Authorization{}, err
		}
	}
	return Authorization{}, fmt.Errorf("execution authorization serialization retries exhausted: %w", lastErr)
}

func (s *PostgresStore) validateAndReserveOnce(ctx context.Context, input Input, now time.Time) (Authorization, error) {
	now = now.UTC()
	if !validInput(input) || !reservationExpiresAfter(input, now) ||
		input.Reservation.ExpiresAt.After(now.Add(ReservationTTL)) {
		return Authorization{}, ErrInvalidInput
	}
	if err := validateReservation(input.Reservation); err != nil {
		return Authorization{}, fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return Authorization{}, fmt.Errorf("begin execution authorization transaction: %w", err)
	}
	defer tx.Rollback()

	if current, found, err := loadExisting(ctx, tx, input); err != nil {
		return Authorization{}, err
	} else if found {
		return current, ErrAlreadyEvaluated
	}
	var organizationID string
	err = tx.QueryRowContext(ctx, `
		SELECT organization_id
		FROM ascp_intents
		WHERE operation_id=$1
		FOR UPDATE`, input.IntentID).Scan(&organizationID)
	if errors.Is(err, sql.ErrNoRows) {
		return Authorization{}, ErrInvalidInput
	}
	if err != nil {
		return Authorization{}, fmt.Errorf("lock execution intent: %w", err)
	}

	output := Authorization{Input: input, State: Invalidated}
	if input.ApprovalID != "" {
		var approvalState, snapshot string
		var approvalExpiresAt time.Time
		err = tx.QueryRowContext(ctx, `
			SELECT state, review_snapshot_hash, expires_at
			FROM ascp_approvals
			WHERE approval_id=$1 AND intent_id=$2 AND organization_id=$3
			FOR UPDATE`, input.ApprovalID, input.IntentID, organizationID).Scan(&approvalState, &snapshot, &approvalExpiresAt)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return Authorization{}, fmt.Errorf("read approval for execution authorization: %w", err)
		}
		if errors.Is(err, sql.ErrNoRows) || approvalState != "APPROVED" {
			return s.persistInvalid(ctx, tx, output, now, ErrApprovalNotApproved)
		}
		if snapshot != input.ApprovalSnapshotHash {
			return s.persistInvalid(ctx, tx, output, now, ErrApprovalSnapshot)
		}
		if !now.Before(approvalExpiresAt) {
			return s.persistInvalid(ctx, tx, output, now, ErrApprovalExpired)
		}
	} else {
		var reviewSnapshotHash string
		err = tx.QueryRowContext(ctx, `
			SELECT review_snapshot_hash
			FROM ascp_policy_decisions
			WHERE decision_id=$1 AND operation_id=$2 AND organization_id=$3 AND outcome='AUTO_APPROVE'
			FOR SHARE`, input.AutoDecisionRef, input.IntentID, organizationID).Scan(&reviewSnapshotHash)
		if errors.Is(err, sql.ErrNoRows) {
			return Authorization{}, ErrInvalidInput
		}
		if err != nil {
			return Authorization{}, fmt.Errorf("read automatic policy decision for execution authorization: %w", err)
		}
		wantSnapshot, err := ascpapproval.ReviewHash(input.Review)
		if err != nil || wantSnapshot != reviewSnapshotHash {
			return Authorization{}, ErrInvalidInput
		}
	}

	reason, err := s.revalidator.Revalidate(ctx, tx, input, now)
	if err != nil {
		return Authorization{}, fmt.Errorf("revalidate execution authorization: %w", err)
	}
	if strings.TrimSpace(reason) != "" {
		output.InvalidationReason = strings.TrimSpace(reason)
		return s.persistInvalid(ctx, tx, output, now, fmt.Errorf("%w: %s", ErrRevalidationFailed, output.InvalidationReason))
	}

	if err := reserveBudget(ctx, tx, organizationID, input.Reservation, now); err != nil {
		if errors.Is(err, ascpreservation.ErrBudgetExceeded) {
			output.InvalidationReason = err.Error()
			return s.persistInvalid(ctx, tx, output, now, err)
		}
		return Authorization{}, err
	}

	output.State = ValidatedAndReserved
	err = tx.QueryRowContext(ctx, `
		INSERT INTO ascp_execution_authorizations
			(authorization_id, approval_id, auto_decision_ref, intent_id, state, execution_snapshot_hash, reservation_id, created_at, evaluated_at)
		VALUES ($1,NULLIF($2,''),NULLIF($3,''),$4,'VALIDATED_AND_RESERVED',$5,$6,$7,$7)
		RETURNING authorization_id`, input.AuthorizationID, input.ApprovalID, input.AutoDecisionRef, input.IntentID,
		input.ExecutionSnapshotHash, reservationID(input), now).Scan(&output.AuthorizationID)
	if err != nil {
		return Authorization{}, fmt.Errorf("create execution authorization: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Authorization{}, fmt.Errorf("commit execution authorization: %w", err)
	}
	return output, nil
}

func serializationFailure(err error) bool {
	var postgresError *pgconn.PgError
	return errors.As(err, &postgresError) && postgresError.Code == "40001"
}

func loadExisting(ctx context.Context, tx *sql.Tx, input Input) (Authorization, bool, error) {
	var authorizationID, executionSnapshotHash string
	var approvalID, autoDecisionRef, storedReservation sql.NullString
	var state, invalidationReason string
	err := tx.QueryRowContext(ctx, `
		SELECT authorization_id, approval_id, auto_decision_ref, state, execution_snapshot_hash, reservation_id, invalidation_reason
		FROM ascp_execution_authorizations
		WHERE intent_id=$1
		FOR UPDATE`, input.IntentID).Scan(&authorizationID, &approvalID, &autoDecisionRef, &state,
		&executionSnapshotHash, &storedReservation, &invalidationReason)
	if errors.Is(err, sql.ErrNoRows) {
		return Authorization{}, false, nil
	}
	if err != nil {
		return Authorization{}, false, fmt.Errorf("read existing execution authorization: %w", err)
	}
	current := Authorization{Input: input, State: State(state), InvalidationReason: invalidationReason}
	current.AuthorizationID = authorizationID
	current.ApprovalID = approvalID.String
	current.AutoDecisionRef = autoDecisionRef.String
	current.ExecutionSnapshotHash = executionSnapshotHash
	if storedReservation.Valid {
		current.Reservation.ReservationID = storedReservation.String
	}
	return current, true, nil
}

type storedReservation struct {
	dimensionID string
	amount      *big.Int
	state       ascpreservation.State
	refundable  bool
}

func reserveBudget(ctx context.Context, tx *sql.Tx, organizationID string, request ascpreservation.Request, now time.Time) error {
	dimensionIDs := make([]string, 0, len(request.Dimensions))
	for _, dimension := range request.Dimensions {
		dimensionIDs = append(dimensionIDs, dimension.ID)
	}
	dimensionIDsJSON, err := json.Marshal(dimensionIDs)
	if err != nil {
		return fmt.Errorf("encode requested budget dimension IDs: %w", err)
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT rd.dimension_id, r.amount_base_units, r.state, rd.refundable
		FROM ascp_budget_reservations r
		JOIN ascp_intents i ON i.operation_id = r.operation_id
		JOIN ascp_budget_reservation_dimensions rd ON rd.reservation_id = r.reservation_id
		WHERE i.organization_id=$1
		  AND rd.dimension_id IN (SELECT jsonb_array_elements_text($2::jsonb))
		  AND r.state IN ('RESERVED','AUTHORIZATION_LIVE','COMMITTED_SAFE','COMMITTED_FINALIZED','CONSUMED_ON_RELEASE','RESTORED_ON_REFUND','REORGED_BACK')
		FOR UPDATE OF r, rd`, organizationID, dimensionIDsJSON)
	if err != nil {
		return fmt.Errorf("lock active budget reservations: %w", err)
	}
	defer rows.Close()
	existing := make([]storedReservation, 0)
	for rows.Next() {
		var dimensionID, amountText, state string
		var refundable bool
		if err := rows.Scan(&dimensionID, &amountText, &state, &refundable); err != nil {
			return fmt.Errorf("scan active budget reservation: %w", err)
		}
		amount, ok := positiveInteger(amountText)
		if !ok {
			return errors.New("stored budget reservation amount is invalid")
		}
		existing = append(existing, storedReservation{dimensionID: dimensionID, amount: amount, state: ascpreservation.State(state), refundable: refundable})
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate active budget reservations: %w", err)
	}

	requestedAmount, _ := positiveInteger(request.Amount)
	for _, dimension := range request.Dimensions {
		remaining, _ := positiveInteger(dimension.Limit)
		for _, current := range existing {
			if current.dimensionID != dimension.ID || current.state == ascpreservation.Restored && current.refundable {
				continue
			}
			remaining.Sub(remaining, current.amount)
		}
		if remaining.Cmp(requestedAmount) < 0 {
			return fmt.Errorf("%w: dimension %s", ascpreservation.ErrBudgetExceeded, dimension.ID)
		}
	}

	dimensionsJSON, err := json.Marshal(request.Dimensions)
	if err != nil {
		return fmt.Errorf("encode budget reservation dimensions: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO ascp_budget_reservations
			(reservation_id, operation_id, amount_base_units, state, dimensions, created_at, expires_at)
		VALUES ($1,$2,$3,'RESERVED',$4,$5,$6)`, request.ReservationID, request.OperationID,
		request.Amount, dimensionsJSON, now, request.ExpiresAt.UTC()); err != nil {
		return fmt.Errorf("create budget reservation: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO ascp_budget_reservation_dimensions
			(reservation_id, dimension_id, limit_base_units, refundable)
		SELECT $1, d->>'ID', d->>'Limit', (d->>'Refundable')::boolean
		FROM jsonb_array_elements($2::jsonb) AS d`, request.ReservationID, dimensionsJSON); err != nil {
		return fmt.Errorf("create budget reservation dimensions: %w", err)
	}
	return nil
}

func validateReservation(request ascpreservation.Request) error {
	amount, ok := positiveInteger(request.Amount)
	if !ok || len(request.Dimensions) == 0 {
		return errors.New("reservation amount and dimensions are required")
	}
	seen := make(map[string]struct{}, len(request.Dimensions))
	for _, dimension := range request.Dimensions {
		limit, valid := positiveInteger(dimension.Limit)
		if !valid || strings.TrimSpace(dimension.ID) != dimension.ID || dimension.ID == "" || len(dimension.ID) > 256 {
			return errors.New("reservation dimension is invalid")
		}
		if _, duplicate := seen[dimension.ID]; duplicate {
			return errors.New("reservation dimension is duplicated")
		}
		seen[dimension.ID] = struct{}{}
		if amount.Cmp(limit) > 0 {
			return fmt.Errorf("%w: dimension %s", ascpreservation.ErrBudgetExceeded, dimension.ID)
		}
	}
	return nil
}

func positiveInteger(value string) (*big.Int, bool) {
	if value == "" || value[0] == '0' {
		return nil, false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return nil, false
		}
	}
	integer, ok := new(big.Int).SetString(value, 10)
	return integer, ok && integer.Sign() > 0 && integer.BitLen() <= 256
}

func (s *PostgresStore) persistInvalid(ctx context.Context, tx *sql.Tx, output Authorization, now time.Time, cause error) (Authorization, error) {
	if output.InvalidationReason == "" {
		output.InvalidationReason = cause.Error()
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO ascp_execution_authorizations
			(authorization_id, approval_id, auto_decision_ref, intent_id, state, execution_snapshot_hash, invalidation_reason, created_at, evaluated_at)
		VALUES ($1,NULLIF($2,''),NULLIF($3,''),$4,'INVALIDATED',$5,$6,$7,$7)`, output.AuthorizationID, output.ApprovalID,
		output.AutoDecisionRef, output.IntentID, output.ExecutionSnapshotHash, output.InvalidationReason, now)
	if err != nil {
		return Authorization{}, fmt.Errorf("persist invalid execution authorization: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Authorization{}, fmt.Errorf("commit invalid execution authorization: %w", err)
	}
	return output, cause
}
