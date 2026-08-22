package ascpcapacity

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestPostgresGateMapsAuthoritativeStatuses(t *testing.T) {
	tests := []struct {
		status string
		want   error
	}{
		{status: "ACQUIRED"},
		{status: "EXHAUSTED", want: ErrExhausted},
		{status: "LIMIT_MISMATCH", want: ErrLimitMismatch},
		{status: "RESERVATION_MISMATCH", want: ErrConflict},
		{status: "CONFLICT", want: ErrConflict},
	}
	for _, item := range tests {
		t.Run(item.status, func(t *testing.T) {
			db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = db.Close() }()
			mock.ExpectBegin()
			tx, err := db.BeginTx(context.Background(), &sql.TxOptions{Isolation: sql.LevelSerializable})
			if err != nil {
				t.Fatal(err)
			}
			now := time.Unix(1800000000, 0).UTC()
			mock.ExpectQuery(regexp.QuoteMeta("SELECT ascp_acquire_capacity($1,$2,$3,$4)")).
				WithArgs("operation-1", "reservation-1", 250, now).
				WillReturnRows(sqlmock.NewRows([]string{"status"}).AddRow(item.status))
			gate, err := NewPostgresGate(250)
			if err != nil {
				t.Fatal(err)
			}
			err = gate.Acquire(context.Background(), tx, "operation-1", "reservation-1", now)
			if !errors.Is(err, item.want) || item.want == nil && err != nil {
				t.Fatalf("error=%v want=%v", err, item.want)
			}
			mock.ExpectRollback()
			_ = tx.Rollback()
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestPostgresGateRejectsInvalidBoundaryInputs(t *testing.T) {
	if _, err := NewPostgresGate(0); err == nil {
		t.Fatal("zero capacity was accepted")
	}
	gate, err := NewPostgresGate(1)
	if err != nil {
		t.Fatal(err)
	}
	if err := gate.Acquire(context.Background(), nil, "operation-1", "reservation-1", time.Now()); !errors.Is(err, ErrConflict) {
		t.Fatalf("nil transaction error=%v", err)
	}
}
