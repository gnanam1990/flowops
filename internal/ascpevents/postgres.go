package ascpevents

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const eventStreamLock = int64(704832801153)

type PostgresStore struct{ db *sql.DB }

func NewPostgresStore(db *sql.DB) (*PostgresStore, error) {
	if db == nil {
		return nil, errors.New("database is required")
	}
	return &PostgresStore{db: db}, nil
}

func (s *PostgresStore) Append(ctx context.Context, input Input, writer Writer) (Event, bool, error) {
	for attempt := 0; attempt < 32; attempt++ {
		event, replayed, err := s.append(ctx, input, writer)
		if err == nil || !serializationError(err) {
			return event, replayed, err
		}
		delay := time.Duration(attempt+1) * time.Millisecond
		if delay > 20*time.Millisecond {
			delay = 20 * time.Millisecond
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return Event{}, false, ctx.Err()
		case <-timer.C:
		}
	}
	return Event{}, false, errors.New("event append serialization retries exhausted")
}

func (s *PostgresStore) append(ctx context.Context, input Input, writer Writer) (Event, bool, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return Event{}, false, fmt.Errorf("begin event append: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, eventStreamLock); err != nil {
		return Event{}, false, fmt.Errorf("lock event stream: %w", err)
	}
	stored, err := eventByID(ctx, tx, input.EventID)
	if err == nil {
		if !eventMatchesInput(stored, input) {
			return Event{}, false, ErrEventConflict
		}
		if err := tx.Commit(); err != nil {
			return Event{}, false, fmt.Errorf("commit event replay: %w", err)
		}
		return cloneEvent(stored), true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Event{}, false, fmt.Errorf("read event replay: %w", err)
	}

	sequence, previousHash, err := headTx(ctx, tx)
	if err != nil {
		return Event{}, false, err
	}
	event, err := buildEvent(input, sequence+1, previousHash, writer)
	if err != nil {
		return Event{}, false, err
	}
	refs, err := canonicalJSON(event.EntityRefs)
	if err != nil {
		return Event{}, false, err
	}
	mac, err := hex.DecodeString(event.WriterMAC)
	if err != nil || len(mac) != sha256.Size {
		return Event{}, false, ErrInvalidEvent
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO ascp_events
		(sequence,event_id,organization_id,occurred_at_unix_micro,event_type,actor,causation_id,correlation_id,
		 entity_refs,payload,supersedes_event_id,previous_hash,event_hash,writer_key_id,writer_mac)
		VALUES ($1,$2,$3,$4,$5,$6,NULLIF($7,''),$8,$9,$10,NULLIF($11,''),$12,$13,$14,$15)`,
		event.Sequence, event.EventID, event.OrganizationID, event.OccurredAtUnixMic, event.Type, event.Actor,
		event.CausationID, event.CorrelationID, refs, []byte(event.Payload), event.SupersedesEventID,
		event.PreviousHash, event.EventHash, event.WriterKeyID, mac)
	if err != nil {
		return Event{}, false, fmt.Errorf("insert event: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Event{}, false, fmt.Errorf("commit event append: %w", err)
	}
	return cloneEvent(event), false, nil
}

func (s *PostgresStore) Head(ctx context.Context) (Head, error) {
	var head Head
	err := s.db.QueryRowContext(ctx, `SELECT sequence,event_hash FROM ascp_events ORDER BY sequence DESC LIMIT 1`).Scan(&head.Sequence, &head.EventHash)
	if errors.Is(err, sql.ErrNoRows) {
		return Head{EventHash: zeroHash}, nil
	}
	if err != nil {
		return Head{}, fmt.Errorf("read event head: %w", err)
	}
	return head, nil
}

func (s *PostgresStore) EventAt(ctx context.Context, sequence uint64) (Event, error) {
	return scanEvent(s.db.QueryRowContext(ctx, eventSelect+` WHERE sequence=$1`, sequence))
}

func (s *PostgresStore) Verify(ctx context.Context, keys map[string][]byte) (Head, error) {
	rows, err := s.db.QueryContext(ctx, eventSelect+` ORDER BY sequence`)
	if err != nil {
		return Head{}, fmt.Errorf("read event chain: %w", err)
	}
	defer rows.Close()
	previous := zeroHash
	var sequence uint64
	for rows.Next() {
		event, err := scanEvent(rows)
		if err != nil {
			return Head{}, err
		}
		sequence++
		if err := VerifyEvent(event, sequence, previous, keys); err != nil {
			return Head{}, fmt.Errorf("event %d: %w", sequence, err)
		}
		previous = event.EventHash
	}
	if err := rows.Err(); err != nil {
		return Head{}, fmt.Errorf("iterate event chain: %w", err)
	}
	return Head{Sequence: sequence, EventHash: previous}, nil
}

func (s *PostgresStore) SaveCheckpoint(ctx context.Context, checkpoint Checkpoint) (Checkpoint, bool, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return Checkpoint{}, false, fmt.Errorf("begin checkpoint save: %w", err)
	}
	defer tx.Rollback()
	stored, err := checkpointByID(ctx, tx, checkpoint.CheckpointID)
	if err == nil {
		if !sameCheckpoint(stored, checkpoint) {
			return Checkpoint{}, false, ErrCheckpointConflict
		}
		if err := tx.Commit(); err != nil {
			return Checkpoint{}, false, err
		}
		return stored, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Checkpoint{}, false, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO ascp_event_checkpoints
		(checkpoint_id,last_sequence,last_event_hash,journal_trial_balance_hash,created_at_unix_micro,
		 signing_key_id,canonical_document,signature,worm_ref)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`, checkpoint.CheckpointID, checkpoint.LastSequence,
		checkpoint.LastEventHash, checkpoint.JournalTrialBalanceHash, checkpoint.CreatedAtUnixMic,
		checkpoint.SigningKeyID, []byte(checkpoint.CanonicalDocument), checkpoint.Signature, checkpoint.WORMRef)
	if err != nil {
		return Checkpoint{}, false, fmt.Errorf("insert checkpoint: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Checkpoint{}, false, fmt.Errorf("commit checkpoint: %w", err)
	}
	return cloneCheckpoint(checkpoint), false, nil
}

func (s *PostgresStore) LatestCheckpoint(ctx context.Context) (Checkpoint, error) {
	return scanCheckpoint(s.db.QueryRowContext(ctx, checkpointSelect+` ORDER BY last_sequence DESC LIMIT 1`))
}

const eventSelect = `SELECT sequence,event_id,organization_id,occurred_at_unix_micro,event_type,actor,
	COALESCE(causation_id,''),correlation_id,entity_refs,payload,COALESCE(supersedes_event_id,''),
	previous_hash,event_hash,writer_key_id,encode(writer_mac,'hex') FROM ascp_events`

type rowScanner interface{ Scan(...any) error }

func eventByID(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, eventID string) (Event, error) {
	return scanEvent(q.QueryRowContext(ctx, eventSelect+` WHERE event_id=$1`, eventID))
}

func scanEvent(row rowScanner) (Event, error) {
	var event Event
	var refs, payload []byte
	err := row.Scan(&event.Sequence, &event.EventID, &event.OrganizationID, &event.OccurredAtUnixMic, &event.Type,
		&event.Actor, &event.CausationID, &event.CorrelationID, &refs, &payload, &event.SupersedesEventID,
		&event.PreviousHash, &event.EventHash, &event.WriterKeyID, &event.WriterMAC)
	if err != nil {
		return Event{}, err
	}
	event.SchemaVersion = SchemaVersion
	if err := json.Unmarshal(refs, &event.EntityRefs); err != nil {
		return Event{}, fmt.Errorf("decode event refs: %w", err)
	}
	event.Payload = append(json.RawMessage(nil), payload...)
	return event, nil
}

func headTx(ctx context.Context, tx *sql.Tx) (uint64, string, error) {
	var sequence uint64
	var eventHash string
	err := tx.QueryRowContext(ctx, `SELECT sequence,event_hash FROM ascp_events ORDER BY sequence DESC LIMIT 1`).Scan(&sequence, &eventHash)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, zeroHash, nil
	}
	if err != nil {
		return 0, "", fmt.Errorf("read event head: %w", err)
	}
	return sequence, eventHash, nil
}

func serializationError(err error) bool {
	if err == nil {
		return false
	}
	var state interface{ SQLState() string }
	if errors.As(err, &state) && state.SQLState() == "40001" {
		return true
	}
	text := err.Error()
	return strings.Contains(text, "SQLSTATE 40001") || strings.Contains(text, "could not serialize")
}
