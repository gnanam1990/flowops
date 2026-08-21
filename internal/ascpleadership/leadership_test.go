package ascpleadership

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gnanam1990/flowops/internal/controlapi"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

func TestPostgresLeadershipDrainWaitsForFencedEffectAndAdvancesExactlyOnce(t *testing.T) {
	db := leadershipDatabase(t)
	now := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	store, err := NewPostgres(db, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	record, err := store.Bootstrap(t.Context(), "org-test", "owner-safe", leadershipHash("1"))
	if err != nil || record.Epoch != 1 || record.State != Active {
		t.Fatalf("bootstrap=%+v err=%v", record, err)
	}
	if replay, err := store.Bootstrap(t.Context(), "org-test", "owner-safe", leadershipHash("1")); err != nil || replay != record {
		t.Fatalf("idempotent bootstrap=%+v err=%v", replay, err)
	}
	if _, err := store.Bootstrap(t.Context(), "org-test", "owner-safe", leadershipHash("9")); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("substituted bootstrap=%v", err)
	}
	var bootstrapEvents int
	if err := db.QueryRowContext(t.Context(), `SELECT count(*) FROM ascp_leadership_events
		WHERE organization_id='org-test' AND new_epoch=1 AND new_state='ACTIVE'`).Scan(&bootstrapEvents); err != nil || bootstrapEvents != 1 {
		t.Fatalf("bootstrap events=%d err=%v", bootstrapEvents, err)
	}

	effectStarted := make(chan struct{})
	releaseEffect := make(chan struct{})
	fenceDone := make(chan error, 1)
	var effects atomic.Int32
	go func() {
		fenceDone <- store.Fence(context.Background(), "org-test", 1, func(context.Context) error {
			effects.Add(1)
			close(effectStarted)
			<-releaseEffect
			return nil
		})
	}()
	<-effectStarted
	drainDone := make(chan error, 1)
	drainStarted := make(chan struct{})
	go func() {
		close(drainStarted)
		_, drainErr := store.BeginDrain(context.Background(), "org-test", 1, "owner-safe", leadershipHash("2"))
		drainDone <- drainErr
	}()
	<-drainStarted
	select {
	case err := <-drainDone:
		t.Fatalf("drain crossed active effect: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(releaseEffect)
	if err := <-fenceDone; err != nil {
		t.Fatal(err)
	}
	if err := <-drainDone; err != nil {
		t.Fatal(err)
	}
	if effects.Load() != 1 {
		t.Fatalf("effects=%d", effects.Load())
	}
	if _, err := store.Current(t.Context(), "org-test"); !errors.Is(err, ErrNotActive) {
		t.Fatalf("draining current=%v", err)
	}
	called := false
	if err := store.Fence(t.Context(), "org-test", 1, func(context.Context) error { called = true; return nil }); !errors.Is(err, ErrEpochChanged) || called {
		t.Fatalf("draining fence=%v called=%t", err, called)
	}
	record, err = store.Advance(t.Context(), "org-test", 1, "owner-safe", leadershipHash("3"))
	if err != nil || record.Epoch != 2 || record.State != Active {
		t.Fatalf("advance=%+v err=%v", record, err)
	}
	if _, err := store.Advance(t.Context(), "org-test", 1, "owner-safe", leadershipHash("4")); !errors.Is(err, ErrEpochChanged) {
		t.Fatalf("stale advance=%v", err)
	}
	if err := store.Fence(t.Context(), "org-test", 1, func(context.Context) error { return nil }); !errors.Is(err, ErrEpochChanged) {
		t.Fatalf("stale fence=%v", err)
	}
	if err := store.Fence(t.Context(), "org-test", 2, func(context.Context) error { effects.Add(1); return nil }); err != nil || effects.Load() != 2 {
		t.Fatalf("new fence=%v effects=%d", err, effects.Load())
	}
}

func TestPostgresLeadershipDatabaseRejectsSkippedEpochDeleteAndEventMutation(t *testing.T) {
	db := leadershipDatabase(t)
	store, _ := NewPostgres(db)
	_, _ = store.Bootstrap(t.Context(), "org-test", "owner-safe", leadershipHash("1"))
	if _, err := db.ExecContext(t.Context(), `UPDATE ascp_leadership_epochs SET epoch=3,state='ACTIVE',updated_at=updated_at+interval '1 second' WHERE organization_id='org-test'`); err == nil {
		t.Fatal("database accepted skipped epoch")
	}
	if _, err := db.ExecContext(t.Context(), `UPDATE ascp_leadership_epochs SET state='DRAINING',
		evidence_digest=$1,updated_at=updated_at+interval '1 second' WHERE organization_id='org-test'`, leadershipHash("2")); err == nil {
		t.Fatal("database accepted a state transition without its audit event")
	}
	if _, err := db.ExecContext(t.Context(), `DELETE FROM ascp_leadership_epochs WHERE organization_id='org-test'`); err == nil {
		t.Fatal("database accepted leadership deletion")
	}
	if _, err := db.ExecContext(t.Context(), `UPDATE ascp_leadership_events SET actor='attacker' WHERE organization_id='org-test'`); err == nil {
		t.Fatal("database accepted event mutation")
	}
	if _, err := db.ExecContext(t.Context(), `INSERT INTO ascp_leadership_events
		(organization_id,previous_epoch,new_epoch,previous_state,new_state,evidence_digest,actor,created_at)
		VALUES ('org-test',1,1,'ACTIVE','DRAINING',$1,'owner-safe',now())`, leadershipHash("2")); err == nil {
		t.Fatal("database accepted event that does not match current leadership")
	}
	if _, err := db.ExecContext(t.Context(), `INSERT INTO ascp_leadership_epochs
		(organization_id,epoch,state,evidence_digest,actor,updated_at)
		VALUES ('org with space',1,'ACTIVE',$1,'owner-safe',now())`, leadershipHash("3")); err == nil {
		t.Fatal("database accepted malformed organization")
	}
	tx, err := db.BeginTx(t.Context(), &sql.TxOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var drainingAt time.Time
	if err := tx.QueryRowContext(t.Context(), `UPDATE ascp_leadership_epochs SET state='DRAINING',
		evidence_digest=$1,updated_at=updated_at+interval '1 second' WHERE organization_id='org-test'
		RETURNING updated_at`, leadershipHash("4")).Scan(&drainingAt); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(t.Context(), `INSERT INTO ascp_leadership_events
		(organization_id,previous_epoch,new_epoch,previous_state,new_state,evidence_digest,actor,created_at)
		VALUES ('org-test',1,1,'ACTIVE','DRAINING',$1,'owner-safe',$2)`, leadershipHash("4"), drainingAt); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(t.Context(), `UPDATE ascp_leadership_epochs SET epoch=2,state='ACTIVE',
		evidence_digest=$1,updated_at=updated_at+interval '1 second' WHERE organization_id='org-test'`, leadershipHash("5")); err == nil {
		t.Fatal("database accepted drain and advance in one transaction")
	}
	_ = tx.Rollback()
}

func TestPostgresLeadershipDatabaseTriggerFencesDirectControllerUpdate(t *testing.T) {
	db := leadershipDatabase(t)
	store, _ := NewPostgres(db)
	if _, err := store.Bootstrap(t.Context(), "org-test", "owner-safe", leadershipHash("1")); err != nil {
		t.Fatal(err)
	}
	effectStarted := make(chan struct{})
	releaseEffect := make(chan struct{})
	fenceDone := make(chan error, 1)
	go func() {
		fenceDone <- store.Fence(context.Background(), "org-test", 1, func(context.Context) error {
			close(effectStarted)
			<-releaseEffect
			return nil
		})
	}()
	<-effectStarted
	directTx, err := db.BeginTx(t.Context(), &sql.TxOptions{})
	if err != nil {
		t.Fatal(err)
	}
	directDone := make(chan error, 1)
	directStarted := make(chan struct{})
	go func() {
		close(directStarted)
		_, updateErr := directTx.ExecContext(context.Background(), `UPDATE ascp_leadership_epochs
			SET state='DRAINING',evidence_digest=$2,actor='owner-safe',updated_at=updated_at+interval '1 second'
			WHERE organization_id=$1`, "org-test", leadershipHash("2"))
		directDone <- updateErr
	}()
	<-directStarted
	select {
	case err := <-directDone:
		t.Fatalf("direct controller update bypassed fence: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(releaseEffect)
	if err := <-fenceDone; err != nil {
		t.Fatal(err)
	}
	if err := <-directDone; err != nil {
		t.Fatal(err)
	}
	if err := directTx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if epoch, err := store.Current(t.Context(), "org-test"); err != nil || epoch != 1 {
		t.Fatalf("rolled-back direct update changed leadership: epoch=%d err=%v", epoch, err)
	}
}

func TestPostgresLeadershipConcurrentDrainHasOneCASWinner(t *testing.T) {
	db := leadershipDatabase(t)
	store, _ := NewPostgres(db)
	if _, err := store.Bootstrap(t.Context(), "org-test", "owner-safe", leadershipHash("1")); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		i := i
		go func() {
			<-start
			_, err := store.BeginDrain(context.Background(), "org-test", 1, fmt.Sprintf("owner-%d", i), leadershipHash(fmt.Sprint(i+2)))
			results <- err
		}()
	}
	close(start)
	var succeeded, conflicted int
	for i := 0; i < 2; i++ {
		switch err := <-results; {
		case err == nil:
			succeeded++
		case errors.Is(err, ErrStateConflict):
			conflicted++
		default:
			t.Fatalf("unexpected drain result: %v", err)
		}
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("drain results: succeeded=%d conflicted=%d", succeeded, conflicted)
	}
	var events int
	if err := db.QueryRowContext(t.Context(), `SELECT count(*) FROM ascp_leadership_events WHERE organization_id='org-test'`).Scan(&events); err != nil || events != 2 {
		t.Fatalf("events=%d err=%v", events, err)
	}
}

func TestLeadershipRejectsMalformedInputs(t *testing.T) {
	if _, err := NewPostgres(nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("nil database=%v", err)
	}
	if validOrganization("org test") || validMutation("org-test", "owner-safe", "0x01") {
		t.Fatal("malformed leadership input accepted")
	}
	store, _ := NewPostgres(&sql.DB{})
	if err := store.Fence(t.Context(), "org-test", maxEpoch+1, func(context.Context) error { return nil }); !errors.Is(err, ErrInvalid) {
		t.Fatalf("out-of-range fence epoch=%v", err)
	}
	if _, err := store.Advance(t.Context(), "org-test", maxEpoch, "owner-safe", leadershipHash("1")); !errors.Is(err, ErrInvalid) {
		t.Fatalf("overflowing advance epoch=%v", err)
	}
}

func leadershipDatabase(t *testing.T) *sql.DB {
	t.Helper()
	databaseURL := os.Getenv("FLOWOPS_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("FLOWOPS_TEST_DATABASE_URL is not configured")
	}
	admin, err := sql.Open("pgx", databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("flowops_leadership_it_%d", time.Now().UnixNano())
	if _, err := admin.ExecContext(t.Context(), `CREATE SCHEMA `+schema); err != nil {
		t.Fatal(err)
	}
	config, err := pgx.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	config.RuntimeParams["search_path"] = schema
	db := stdlib.OpenDB(*config)
	db.SetMaxOpenConns(8)
	if err := controlapi.ApplyMigrations(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = db.Close()
		_, _ = admin.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS `+schema+` CASCADE`)
		_ = admin.Close()
	})
	return db
}

func leadershipHash(s string) string {
	for len(s) < 64 {
		s += s
	}
	return "0x" + s[:64]
}
