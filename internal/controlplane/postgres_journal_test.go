package controlplane

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestPostgresJournalAppendsHashChainedEventTransactionally(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectQuery(`SELECT sequence, at_unix, kind`).WillReturnRows(
		sqlmock.NewRows([]string{"sequence", "at_unix", "kind", "request_id", "previous_hash", "payload", "hash"}),
	)
	journal, err := OpenPostgresJournal(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	mock.ExpectBegin()
	mock.ExpectExec(`SELECT pg_advisory_xact_lock`).WithArgs(postgresJournalLock).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT sequence, hash FROM control_events`).WillReturnError(sql.ErrNoRows)
	mock.ExpectExec(`INSERT INTO control_events`).WithArgs(uint64(1), int64(1786525200), "test.event", "req_1", "", []byte(`{"value":"bound"}`), sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	event, err := journal.Append(context.Background(), time.Date(2026, 8, 12, 9, 0, 0, 0, time.UTC), "test.event", "req_1", map[string]string{"value": "bound"})
	if err != nil || event.Sequence != 1 || event.PreviousHash != "" || event.Hash == "" {
		t.Fatalf("event = %+v, %v", event, err)
	}
	if got := journal.Events(); len(got) != 1 || got[0].Hash != event.Hash {
		t.Fatalf("events = %+v", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresJournalFailsClosedWhenAnotherWriterAdvancedHead(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectQuery(`SELECT sequence, at_unix, kind`).WillReturnRows(
		sqlmock.NewRows([]string{"sequence", "at_unix", "kind", "request_id", "previous_hash", "payload", "hash"}),
	)
	journal, err := OpenPostgresJournal(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	mock.ExpectBegin()
	mock.ExpectExec(`SELECT pg_advisory_xact_lock`).WithArgs(postgresJournalLock).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT sequence, hash FROM control_events`).WillReturnRows(sqlmock.NewRows([]string{"sequence", "hash"}).AddRow(1, "external_hash"))
	mock.ExpectRollback()
	_, err = journal.Append(context.Background(), time.Now(), "test.event", "req_1", map[string]string{"value": "bound"})
	if !errors.Is(err, ErrJournalStale) {
		t.Fatalf("stale writer error = %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
