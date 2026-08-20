package ascpexecauth

import (
	"context"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestPostgresValidateAndReserveCommitsOnlyApprovedReservedBinding(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, err := NewPostgresStore(db)
	if err != nil {
		t.Fatal(err)
	}
	input := postgresInput()
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT state FROM ascp_approvals")).WithArgs(input.ApprovalID, input.IntentID).WillReturnRows(sqlmock.NewRows([]string{"state"}).AddRow("APPROVED"))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT state FROM ascp_budget_reservations")).WithArgs(input.ReservationID, input.IntentID).WillReturnRows(sqlmock.NewRows([]string{"state"}).AddRow("RESERVED"))
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO ascp_execution_authorizations")).WillReturnRows(sqlmock.NewRows([]string{"authorization_id"}).AddRow(input.AuthorizationID))
	mock.ExpectCommit()
	output, err := store.ValidateAndReserve(context.Background(), input, time.Unix(1800000000, 0))
	if err != nil || output.State != ValidatedAndReserved {
		t.Fatalf("output=%+v err=%v", output, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresInvalidatesInsteadOfMutatingApprovalOrReservation(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, _ := NewPostgresStore(db)
	input := postgresInput()
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT state FROM ascp_approvals")).WithArgs(input.ApprovalID, input.IntentID).WillReturnRows(sqlmock.NewRows([]string{"state"}).AddRow("REJECTED"))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO ascp_execution_authorizations")).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	output, err := store.ValidateAndReserve(context.Background(), input, time.Unix(1800000000, 0))
	if !errors.Is(err, ErrApprovalNotApproved) || output.State != Invalidated {
		t.Fatalf("output=%+v err=%v", output, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresUnavailableReservationInvalidates(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, _ := NewPostgresStore(db)
	input := postgresInput()
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT state FROM ascp_approvals")).WillReturnRows(sqlmock.NewRows([]string{"state"}).AddRow("APPROVED"))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT state FROM ascp_budget_reservations")).WillReturnRows(sqlmock.NewRows([]string{"state"}).AddRow("AUTHORIZATION_LIVE"))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO ascp_execution_authorizations")).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	output, err := store.ValidateAndReserve(context.Background(), input, time.Unix(1800000000, 0))
	if err == nil || output.State != Invalidated {
		t.Fatalf("output=%+v err=%v", output, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
func postgresInput() Input {
	return Input{AuthorizationID: testHash(1), ApprovalID: testHash(2), IntentID: testHash(3), ExecutionSnapshotHash: testHash(4), ReservationID: testHash(5)}
}
