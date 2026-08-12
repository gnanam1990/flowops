package controlplane

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"
)

const postgresJournalLock = int64(703247667731)

// ErrJournalStale means another control-plane process appended after this
// process loaded its replay state. The safe response is to stop issuing
// authorizations and restart from the canonical database stream.
var ErrJournalStale = errors.New("control-plane journal changed in another process")

// PostgresJournal stores the same hash-chained events as Journal while using
// PostgreSQL as the durable source of truth. It intentionally fails closed on
// concurrent writers instead of attempting to merge independently evaluated
// policy reservations.
type PostgresJournal struct {
	mu       sync.Mutex
	db       *sql.DB
	events   []Event
	lastHash string
	fault    error
}

func OpenPostgresJournal(ctx context.Context, db *sql.DB) (*PostgresJournal, error) {
	if db == nil {
		return nil, errors.New("database is required")
	}
	rows, err := db.QueryContext(ctx, `
		SELECT sequence, at_unix, kind, request_id, previous_hash, payload, hash
		FROM control_events
		ORDER BY sequence ASC`)
	if err != nil {
		return nil, fmt.Errorf("query control events: %w", err)
	}
	defer rows.Close()

	j := &PostgresJournal{db: db}
	for rows.Next() {
		event := Event{Version: journalVersion}
		if err := rows.Scan(&event.Sequence, &event.At, &event.Kind, &event.RequestID, &event.PreviousHash, &event.Payload, &event.Hash); err != nil {
			return nil, fmt.Errorf("scan control event: %w", err)
		}
		if err := validateEvent(event, uint64(len(j.events)+1), j.lastHash); err != nil {
			return nil, fmt.Errorf("control event %d: %w", event.Sequence, err)
		}
		j.events = append(j.events, cloneEvent(event))
		j.lastHash = event.Hash
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate control events: %w", err)
	}
	return j, nil
}

func (j *PostgresJournal) Append(ctx context.Context, at time.Time, kind, requestID string, payload any) (Event, error) {
	if err := ctx.Err(); err != nil {
		return Event{}, err
	}
	if kind == "" || requestID == "" {
		return Event{}, errors.New("event kind and request ID are required")
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return Event{}, fmt.Errorf("encode event payload: %w", err)
	}

	j.mu.Lock()
	defer j.mu.Unlock()
	if j.fault != nil {
		return Event{}, fmt.Errorf("journal is faulted: %w", j.fault)
	}
	tx, err := j.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return Event{}, fmt.Errorf("begin journal transaction: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, postgresJournalLock); err != nil {
		return Event{}, fmt.Errorf("lock control event stream: %w", err)
	}

	var databaseSequence uint64
	var databaseHash string
	err = tx.QueryRowContext(ctx, `SELECT sequence, hash FROM control_events ORDER BY sequence DESC LIMIT 1`).Scan(&databaseSequence, &databaseHash)
	if errors.Is(err, sql.ErrNoRows) {
		databaseSequence, databaseHash, err = 0, "", nil
	}
	if err != nil {
		return Event{}, fmt.Errorf("read control event head: %w", err)
	}
	if databaseSequence != uint64(len(j.events)) || databaseHash != j.lastHash {
		j.fault = ErrJournalStale
		return Event{}, ErrJournalStale
	}

	event := Event{
		Version: journalVersion, Sequence: databaseSequence + 1, At: at.UTC().Unix(),
		Kind: kind, RequestID: requestID, PreviousHash: databaseHash, Payload: raw,
	}
	event.Hash, err = hashEvent(eventHashInput{
		Version: event.Version, Sequence: event.Sequence, At: event.At, Kind: event.Kind,
		RequestID: event.RequestID, PreviousHash: event.PreviousHash, Payload: event.Payload,
	})
	if err != nil {
		return Event{}, err
	}
	if err := validateEvent(event, event.Sequence, databaseHash); err != nil {
		return Event{}, fmt.Errorf("validate event before insert: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO control_events
			(sequence, at_unix, kind, request_id, previous_hash, payload, hash)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		event.Sequence, event.At, event.Kind, event.RequestID, event.PreviousHash, event.Payload, event.Hash,
	); err != nil {
		return Event{}, fmt.Errorf("insert control event: %w", err)
	}
	if err := tx.Commit(); err != nil {
		j.fault = err
		return Event{}, fmt.Errorf("commit control event: %w", err)
	}
	j.events = append(j.events, cloneEvent(event))
	j.lastHash = event.Hash
	return cloneEvent(event), nil
}

func (j *PostgresJournal) Events() []Event {
	j.mu.Lock()
	defer j.mu.Unlock()
	result := make([]Event, len(j.events))
	for i, event := range j.events {
		result[i] = cloneEvent(event)
	}
	return result
}
