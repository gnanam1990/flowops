package ascpevents

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gnanam1990/flowops/internal/controlapi"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

func TestPostgresEventChainConcurrencyReplayCheckpointAndImmutability(t *testing.T) {
	db := eventDatabase(t)
	store, _ := NewPostgresStore(db)
	writer, _ := NewWriter("writer-key-a", []byte(strings.Repeat("a", 32)))
	now := time.Now().UTC().Truncate(time.Microsecond)
	const count = 20
	events := make(chan Event, count)
	errorsFound := make(chan error, count)
	var group sync.WaitGroup
	for index := 0; index < count; index++ {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			input := testInput(fmt.Sprintf("event_%08d", index), now.Add(time.Duration(index)*time.Microsecond), map[string]any{"index": uint64(index)})
			event, replayed, err := store.Append(context.Background(), input, writer)
			if err != nil || replayed {
				errorsFound <- fmt.Errorf("append %d replay=%v: %w", index, replayed, err)
				return
			}
			events <- event
		}(index)
	}
	group.Wait()
	close(events)
	close(errorsFound)
	for err := range errorsFound {
		t.Fatal(err)
	}
	sequences := make([]int, 0, count)
	for event := range events {
		sequences = append(sequences, int(event.Sequence))
	}
	sort.Ints(sequences)
	for index, sequence := range sequences {
		if sequence != index+1 {
			t.Fatalf("sequences=%v", sequences)
		}
	}
	head, err := store.Verify(context.Background(), map[string][]byte{writer.KeyID: writer.Key})
	if err != nil || head.Sequence != count {
		t.Fatalf("head=%+v err=%v", head, err)
	}
	if _, err := db.Exec(`INSERT INTO ascp_events
		(sequence,event_id,organization_id,occurred_at_unix_micro,event_type,actor,causation_id,correlation_id,
		 entity_refs,payload,supersedes_event_id,previous_hash,event_hash,writer_key_id,writer_mac)
		SELECT sequence+2,event_id||'_gap',organization_id,occurred_at_unix_micro,event_type,actor,causation_id,
		 correlation_id,entity_refs,payload,supersedes_event_id,$1,$2,writer_key_id,writer_mac
		FROM ascp_events WHERE sequence=$3`, zeroHash, strings.Repeat("e", 64), count); err == nil {
		t.Fatal("database accepted an event-chain gap")
	}

	replayInput := testInput("event_00000000", now, map[string]any{"index": uint64(0)})
	rotated, _ := NewWriter("writer-key-b", []byte(strings.Repeat("b", 32)))
	replayedEvent, replayed, err := store.Append(context.Background(), replayInput, rotated)
	if err != nil || !replayed || replayedEvent.WriterKeyID != writer.KeyID {
		t.Fatalf("rotated replay=%+v %v %v", replayedEvent, replayed, err)
	}
	replayInput.Payload = map[string]any{"index": uint64(999)}
	if _, _, err := store.Append(context.Background(), replayInput, writer); !errors.Is(err, ErrEventConflict) {
		t.Fatalf("substitution error=%v", err)
	}

	if _, err := db.Exec(`UPDATE ascp_events SET actor='attacker' WHERE sequence=1`); err == nil {
		t.Fatal("append-only event update succeeded")
	}
	if _, err := db.Exec(`DELETE FROM ascp_events WHERE sequence=1`); err == nil {
		t.Fatal("append-only event delete succeeded")
	}

	publicKey, privateKey, _ := ed25519.GenerateKey(nil)
	worm := &memoryWORM{objects: map[string][]byte{}}
	remote := &memoryRemote{}
	publisher, _ := NewPublisher(store, worm, remote, "checkpoint-key-a", privateKey, map[string][]byte{writer.KeyID: writer.Key}, func() time.Time { return now.Add(time.Minute) })
	checkpoint, replayed, err := publisher.Publish(context.Background(), strings.Repeat("c", 64))
	if err != nil || replayed || checkpoint.LastSequence != count {
		t.Fatalf("checkpoint=%+v replay=%v err=%v", checkpoint, replayed, err)
	}
	status, err := VerifyRecovery(context.Background(), store, worm, remote,
		map[string][]byte{writer.KeyID: writer.Key}, map[string]ed25519.PublicKey{"checkpoint-key-a": publicKey})
	if err != nil || !status.ExternallyCheckpointed {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO ascp_event_checkpoints
		(checkpoint_id,last_sequence,last_event_hash,journal_trial_balance_hash,created_at_unix_micro,signing_key_id,canonical_document,signature,worm_ref)
		VALUES ('checkpoint_invalid',1,$1,$2,$3,'checkpoint-key-a','{}',$4,'ascp/checkpoints/invalid.json')`,
		strings.Repeat("f", 64), strings.Repeat("c", 64), now.UnixMicro(), make([]byte, 64)); err == nil {
		t.Fatal("database accepted a checkpoint for the wrong event hash")
	}
	if _, err := db.Exec(`UPDATE ascp_event_checkpoints SET last_event_hash=$1 WHERE checkpoint_id=$2`, strings.Repeat("f", 64), checkpoint.CheckpointID); err == nil {
		t.Fatal("append-only checkpoint update succeeded")
	}
}

func eventDatabase(t *testing.T) *sql.DB {
	t.Helper()
	databaseURL := os.Getenv("FLOWOPS_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("FLOWOPS_TEST_DATABASE_URL is not configured")
	}
	admin, err := sql.Open("pgx", databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("flowops_events_it_%d", time.Now().UnixNano())
	if _, err := admin.Exec(`CREATE SCHEMA ` + schema); err != nil {
		t.Fatal(err)
	}
	config, err := pgx.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	config.RuntimeParams["search_path"] = schema
	db := stdlib.OpenDB(*config)
	db.SetMaxOpenConns(30)
	if err := controlapi.ApplyMigrations(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO organizations (id,name) VALUES ('org-test','Event test')`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = db.Close()
		_, _ = admin.Exec(`DROP SCHEMA IF EXISTS ` + schema + ` CASCADE`)
		_ = admin.Close()
	})
	return db
}
