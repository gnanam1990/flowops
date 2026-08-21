package ascpexecauth

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gnanam1990/flowops/internal/ascpapproval"
	"github.com/gnanam1990/flowops/internal/ascpreservation"
)

var authorizationNow = time.Unix(1800000000, 0).UTC()

const authorizationOrganizationID = "org_acme"

func TestPostgresValidateAndReserveCommitsAtomicBinding(t *testing.T) {
	db, mock := postgresMock(t)
	store := postgresStore(t, db, successfulRevalidator())
	input := postgresInput()

	expectBeginAndNoExisting(mock, input)
	expectApproved(mock, input, authorizationNow.Add(time.Hour))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT rd.dimension_id, r.amount_base_units, r.state, rd.refundable")).
		WithArgs(authorizationOrganizationID, sqlmock.AnyArg()).WillReturnRows(
		sqlmock.NewRows([]string{"dimension_id", "amount_base_units", "state", "refundable"}))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO ascp_budget_reservations")).
		WithArgs(input.Reservation.ReservationID, input.IntentID, input.Reservation.Amount, sqlmock.AnyArg(), authorizationNow, input.Reservation.ExpiresAt.UTC()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO ascp_budget_reservation_dimensions")).
		WithArgs(input.Reservation.ReservationID, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO ascp_execution_authorizations")).
		WithArgs(input.AuthorizationID, input.ApprovalID, "", input.IntentID, input.ExecutionSnapshotHash, input.Reservation.ReservationID, authorizationNow).
		WillReturnRows(sqlmock.NewRows([]string{"authorization_id"}).AddRow(input.AuthorizationID))
	mock.ExpectCommit()

	output, err := store.ValidateAndReserve(context.Background(), input)
	if err != nil || output.State != ValidatedAndReserved || output.Reservation.ReservationID != input.Reservation.ReservationID {
		t.Fatalf("output=%+v err=%v", output, err)
	}
	expectationsMet(t, mock)
}

func TestPostgresAutomaticDecisionCommitsWithoutInventingHumanApproval(t *testing.T) {
	db, mock := postgresMock(t)
	store := postgresStore(t, db, successfulRevalidator())
	input := postgresInput()
	input.ApprovalID, input.ApprovalSnapshotHash = "", ""
	input.AutoDecisionRef = testHash(88)
	reviewHash, err := ascpapproval.ReviewHash(input.Review)
	if err != nil {
		t.Fatal(err)
	}
	expectBeginAndNoExisting(mock, input)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT review_snapshot_hash")).
		WithArgs(input.AutoDecisionRef, input.IntentID, authorizationOrganizationID).
		WillReturnRows(sqlmock.NewRows([]string{"review_snapshot_hash"}).AddRow(reviewHash))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT rd.dimension_id, r.amount_base_units, r.state, rd.refundable")).
		WithArgs(authorizationOrganizationID, sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"dimension_id", "amount_base_units", "state", "refundable"}))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO ascp_budget_reservations")).
		WithArgs(input.Reservation.ReservationID, input.IntentID, input.Reservation.Amount, sqlmock.AnyArg(), authorizationNow, input.Reservation.ExpiresAt.UTC()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO ascp_budget_reservation_dimensions")).
		WithArgs(input.Reservation.ReservationID, sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO ascp_execution_authorizations")).
		WithArgs(input.AuthorizationID, "", input.AutoDecisionRef, input.IntentID, input.ExecutionSnapshotHash, input.Reservation.ReservationID, authorizationNow).
		WillReturnRows(sqlmock.NewRows([]string{"authorization_id"}).AddRow(input.AuthorizationID))
	mock.ExpectCommit()

	output, err := store.ValidateAndReserve(context.Background(), input)
	if err != nil || output.State != ValidatedAndReserved || output.ApprovalID != "" || output.AutoDecisionRef != input.AutoDecisionRef {
		t.Fatalf("output=%+v err=%v", output, err)
	}
	expectationsMet(t, mock)
}

func TestPostgresApprovalSnapshotMismatchDurablyInvalidatesWithoutReservation(t *testing.T) {
	db, mock := postgresMock(t)
	store := postgresStore(t, db, successfulRevalidator())
	input := postgresInput()
	expectBeginAndNoExisting(mock, input)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT state, review_snapshot_hash, expires_at")).
		WithArgs(input.ApprovalID, input.IntentID, authorizationOrganizationID).
		WillReturnRows(sqlmock.NewRows([]string{"state", "review_snapshot_hash", "expires_at"}).
			AddRow("APPROVED", testHash(99), authorizationNow.Add(time.Hour)))
	expectInvalidInsert(mock, input, ErrApprovalSnapshot.Error())
	mock.ExpectCommit()

	output, err := store.ValidateAndReserve(context.Background(), input)
	if !errors.Is(err, ErrApprovalSnapshot) || output.State != Invalidated {
		t.Fatalf("output=%+v err=%v", output, err)
	}
	expectationsMet(t, mock)
}

func TestPostgresExpiredApprovalDurablyInvalidates(t *testing.T) {
	db, mock := postgresMock(t)
	store := postgresStore(t, db, successfulRevalidator())
	input := postgresInput()
	expectBeginAndNoExisting(mock, input)
	expectApproved(mock, input, authorizationNow)
	expectInvalidInsert(mock, input, ErrApprovalExpired.Error())
	mock.ExpectCommit()

	output, err := store.ValidateAndReserve(context.Background(), input)
	if !errors.Is(err, ErrApprovalExpired) || output.State != Invalidated {
		t.Fatalf("output=%+v err=%v", output, err)
	}
	expectationsMet(t, mock)
}

func TestPostgresRevalidationFailureInvalidatesButInfrastructureFailureRollsBack(t *testing.T) {
	t.Run("business invalidation", func(t *testing.T) {
		db, mock := postgresMock(t)
		store := postgresStore(t, db, TransactionRevalidatorFunc(func(context.Context, *sql.Tx, Input, time.Time) (string, error) {
			return "directory version changed", nil
		}))
		input := postgresInput()
		expectBeginAndNoExisting(mock, input)
		expectApproved(mock, input, authorizationNow.Add(time.Hour))
		expectInvalidInsert(mock, input, "directory version changed")
		mock.ExpectCommit()

		output, err := store.ValidateAndReserve(context.Background(), input)
		if !errors.Is(err, ErrRevalidationFailed) || output.State != Invalidated {
			t.Fatalf("output=%+v err=%v", output, err)
		}
		expectationsMet(t, mock)
	})

	t.Run("transient failure", func(t *testing.T) {
		db, mock := postgresMock(t)
		transient := errors.New("temporary database outage")
		store := postgresStore(t, db, TransactionRevalidatorFunc(func(context.Context, *sql.Tx, Input, time.Time) (string, error) {
			return "", transient
		}))
		input := postgresInput()
		expectBeginAndNoExisting(mock, input)
		expectApproved(mock, input, authorizationNow.Add(time.Hour))
		mock.ExpectRollback()

		if _, err := store.ValidateAndReserve(context.Background(), input); !errors.Is(err, transient) {
			t.Fatalf("error=%v", err)
		}
		expectationsMet(t, mock)
	})
}

func TestPostgresBudgetCountsSuccessfulConsumedSpendEvenWhenRefundable(t *testing.T) {
	db, mock := postgresMock(t)
	store := postgresStore(t, db, successfulRevalidator())
	input := postgresInput()
	input.Reservation.Dimensions[0].Limit = "20"
	expectBeginAndNoExisting(mock, input)
	expectApproved(mock, input, authorizationNow.Add(time.Hour))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT rd.dimension_id, r.amount_base_units, r.state, rd.refundable")).
		WithArgs(authorizationOrganizationID, sqlmock.AnyArg()).WillReturnRows(
		sqlmock.NewRows([]string{"dimension_id", "amount_base_units", "state", "refundable"}).
			AddRow(input.Reservation.Dimensions[0].ID, "11", "CONSUMED_ON_RELEASE", true))
	expectInvalidInsert(mock, input, "budget reservation exceeds a dimension limit: dimension org:day:2030-01-15")
	mock.ExpectCommit()

	output, err := store.ValidateAndReserve(context.Background(), input)
	if !errors.Is(err, ascpreservation.ErrBudgetExceeded) || output.State != Invalidated {
		t.Fatalf("output=%+v err=%v", output, err)
	}
	expectationsMet(t, mock)
}

func TestPostgresRefundRestoresOnlyRefundableDimensions(t *testing.T) {
	db, mock := postgresMock(t)
	store := postgresStore(t, db, successfulRevalidator())
	input := postgresInput()
	input.Reservation.Dimensions[0].Limit = "20"
	expectBeginAndNoExisting(mock, input)
	expectApproved(mock, input, authorizationNow.Add(time.Hour))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT rd.dimension_id, r.amount_base_units, r.state, rd.refundable")).
		WithArgs(authorizationOrganizationID, sqlmock.AnyArg()).WillReturnRows(
		sqlmock.NewRows([]string{"dimension_id", "amount_base_units", "state", "refundable"}).
			AddRow(input.Reservation.Dimensions[0].ID, "100", "RESTORED_ON_REFUND", true).
			AddRow(input.Reservation.Dimensions[0].ID, "11", "RESTORED_ON_REFUND", false))
	expectInvalidInsert(mock, input, "budget reservation exceeds a dimension limit: dimension org:day:2030-01-15")
	mock.ExpectCommit()

	output, err := store.ValidateAndReserve(context.Background(), input)
	if !errors.Is(err, ascpreservation.ErrBudgetExceeded) || output.State != Invalidated {
		t.Fatalf("output=%+v err=%v", output, err)
	}
	expectationsMet(t, mock)
}

func TestPostgresReservationWriteFailureRollsBackWithoutAuthorization(t *testing.T) {
	db, mock := postgresMock(t)
	store := postgresStore(t, db, successfulRevalidator())
	input := postgresInput()
	expectBeginAndNoExisting(mock, input)
	expectApproved(mock, input, authorizationNow.Add(time.Hour))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT rd.dimension_id, r.amount_base_units, r.state, rd.refundable")).
		WithArgs(authorizationOrganizationID, sqlmock.AnyArg()).WillReturnRows(
		sqlmock.NewRows([]string{"dimension_id", "amount_base_units", "state", "refundable"}))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO ascp_budget_reservations")).WillReturnError(errors.New("serialization failure"))
	mock.ExpectRollback()

	if _, err := store.ValidateAndReserve(context.Background(), input); err == nil {
		t.Fatal("expected reservation write error")
	}
	expectationsMet(t, mock)
}

func TestPostgresExistingAuthorizationIsReturnedWithoutReevaluation(t *testing.T) {
	db, mock := postgresMock(t)
	store := postgresStore(t, db, successfulRevalidator())
	input := postgresInput()
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT authorization_id, approval_id, auto_decision_ref, state, execution_snapshot_hash, reservation_id, invalidation_reason")).
		WithArgs(input.IntentID).
		WillReturnRows(sqlmock.NewRows([]string{"authorization_id", "approval_id", "auto_decision_ref", "state", "execution_snapshot_hash", "reservation_id", "invalidation_reason"}).
			AddRow(input.AuthorizationID, input.ApprovalID, nil, "VALIDATED_AND_RESERVED", input.ExecutionSnapshotHash, input.Reservation.ReservationID, ""))
	mock.ExpectRollback()

	output, err := store.ValidateAndReserve(context.Background(), input)
	if !errors.Is(err, ErrAlreadyEvaluated) || output.State != ValidatedAndReserved {
		t.Fatalf("output=%+v err=%v", output, err)
	}
	expectationsMet(t, mock)
}

func TestPostgresTransientApprovalReadFailureRollsBackWithoutPermanentInvalidation(t *testing.T) {
	db, mock := postgresMock(t)
	store := postgresStore(t, db, successfulRevalidator())
	input := postgresInput()
	expectBeginAndNoExisting(mock, input)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT state, review_snapshot_hash, expires_at")).
		WithArgs(input.ApprovalID, input.IntentID, authorizationOrganizationID).
		WillReturnError(errors.New("temporary database outage"))
	mock.ExpectRollback()
	if _, err := store.ValidateAndReserve(context.Background(), input); err == nil {
		t.Fatal("expected transient read error")
	}
	expectationsMet(t, mock)
}

func TestPostgresRejectsReservationBeyondPreSignatureTTLBeforeTransaction(t *testing.T) {
	db, mock := postgresMock(t)
	store := postgresStore(t, db, successfulRevalidator())
	input := postgresInput()
	input.Reservation.ExpiresAt = authorizationNow.Add(ReservationTTL + time.Second)
	if _, err := store.ValidateAndReserve(context.Background(), input); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("error=%v", err)
	}
	expectationsMet(t, mock)
}

func postgresInput() Input { return testInput() }

func successfulRevalidator() TransactionRevalidator {
	return TransactionRevalidatorFunc(func(context.Context, *sql.Tx, Input, time.Time) (string, error) { return "", nil })
}

func postgresMock(t *testing.T) (*sql.DB, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, mock
}

func postgresStore(t *testing.T, db *sql.DB, revalidator TransactionRevalidator) *PostgresStore {
	t.Helper()
	store, err := NewPostgresStore(db, revalidator, func() time.Time { return authorizationNow })
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func expectBeginAndNoExisting(mock sqlmock.Sqlmock, input Input) {
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT authorization_id, approval_id, auto_decision_ref, state, execution_snapshot_hash, reservation_id, invalidation_reason")).
		WithArgs(input.IntentID).
		WillReturnRows(sqlmock.NewRows([]string{"authorization_id", "approval_id", "auto_decision_ref", "state", "execution_snapshot_hash", "reservation_id", "invalidation_reason"}))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT organization_id")).
		WithArgs(input.IntentID).
		WillReturnRows(sqlmock.NewRows([]string{"organization_id"}).AddRow(authorizationOrganizationID))
}

func expectApproved(mock sqlmock.Sqlmock, input Input, expiresAt time.Time) {
	mock.ExpectQuery(regexp.QuoteMeta("SELECT state, review_snapshot_hash, expires_at")).
		WithArgs(input.ApprovalID, input.IntentID, authorizationOrganizationID).
		WillReturnRows(sqlmock.NewRows([]string{"state", "review_snapshot_hash", "expires_at"}).
			AddRow("APPROVED", input.ApprovalSnapshotHash, expiresAt))
}

func expectInvalidInsert(mock sqlmock.Sqlmock, input Input, reason string) {
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO ascp_execution_authorizations")).
		WithArgs(input.AuthorizationID, input.ApprovalID, "", input.IntentID, input.ExecutionSnapshotHash, reason, authorizationNow).
		WillReturnResult(sqlmock.NewResult(1, 1))
}

func expectationsMet(t *testing.T, mock sqlmock.Sqlmock) {
	t.Helper()
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
