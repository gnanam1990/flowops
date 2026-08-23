package ascpleadership_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	. "github.com/gnanam1990/flowops/internal/ascpleadership"
	"github.com/gnanam1990/flowops/internal/controlapi"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

func TestPostgresLeadershipDrainWaitsForFencedEffectAndAdvancesExactlyOnce(t *testing.T) {
	db, schema := leadershipDatabase(t)
	now := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	store, err := NewPostgres(db, schema, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	record, err := store.Bootstrap(t.Context(), "org-test", "owner-safe", leadershipHash("1"))
	if err != nil || record.Epoch != 1 || record.State != Active {
		t.Fatalf("bootstrap=%+v err=%v", record, err)
	}
	if replay, err := store.Bootstrap(t.Context(), "org-test", "owner-safe", leadershipHash("1")); err != nil || !reflect.DeepEqual(replay, record) {
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
	if _, err := db.ExecContext(t.Context(), `UPDATE ascp_leadership_effects
		SET state='ABANDONED',resolved_at=clock_timestamp(),resolution_actor='operator',
			resolution_evidence_digest=$1
		WHERE organization_id='org-test' AND state='IN_FLIGHT'`, leadershipHash("8")); err == nil {
		t.Fatal("database accepted effect abandonment while leadership was active")
	}
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseEffect) }) }
	defer release()
	drainDone := make(chan error, 1)
	go func() {
		_, drainErr := store.BeginPromotion(context.Background(), "org-test", 1, "owner-safe", leadershipHash("2"), 2*time.Minute)
		drainDone <- drainErr
	}()
	if err := waitForLeadershipState(t.Context(), db, Draining); err != nil {
		t.Fatal(err)
	}
	assertInFlightEffects(t, db, "org-test", 1, 1)
	release()
	if err := <-fenceDone; err != nil {
		t.Fatal(err)
	}
	if err := <-drainDone; err != nil {
		t.Fatal(err)
	}
	assertInFlightEffects(t, db, "org-test", 1, 0)
	var completedEffectID string
	if err := db.QueryRowContext(t.Context(), `SELECT effect_id FROM ascp_leadership_effects
		WHERE organization_id='org-test' AND state='COMPLETED'`).Scan(&completedEffectID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), `DELETE FROM ascp_leadership_effects WHERE effect_id=$1`, completedEffectID); err == nil {
		t.Fatal("database deleted immutable leadership effect evidence")
	}
	if _, err := db.ExecContext(t.Context(), `UPDATE ascp_leadership_effects
		SET state='IN_FLIGHT',resolved_at=NULL WHERE effect_id=$1`, completedEffectID); err == nil {
		t.Fatal("database reopened a resolved leadership effect")
	}
	if _, err := db.ExecContext(t.Context(), `INSERT INTO ascp_leadership_effects
		(effect_id,organization_id,epoch,state,started_at)
		VALUES ($1,'org-test',1,'IN_FLIGHT',now())`, leadershipHash("f")); err == nil {
		t.Fatal("database admitted a new effect while draining")
	}
	if effects.Load() != 1 {
		t.Fatalf("effects=%d", effects.Load())
	}
	if _, err := store.Current(t.Context(), "org-test"); !errors.Is(err, ErrEpochChanged) {
		t.Fatalf("draining current=%v", err)
	}
	called := false
	if err := store.Fence(t.Context(), "org-test", 1, func(context.Context) error { called = true; return nil }); !errors.Is(err, ErrEpochChanged) || called {
		t.Fatalf("draining fence=%v called=%t", err, called)
	}
	if _, err := store.MarkPromotionReady(t.Context(), "org-test", 1, leadershipHash("a")); err != nil {
		t.Fatalf("ready promotion=%v", err)
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
	for _, sink := range []Sink{SinkSignerIssuance, SinkVerifierAttestation, SinkKeeperRelay, SinkSellerProxyEgress, SinkOutboxDispatch, SinkCheckpointWrite} {
		called = false
		if err := store.FenceSink(t.Context(), "org-test", 1, sink, func(context.Context) error { called = true; return nil }); !errors.Is(err, ErrEpochChanged) || called {
			t.Fatalf("stale sink %s err=%v called=%t", sink, err, called)
		}
	}
	if promotion, err := store.CompletePromotion(t.Context(), "org-test", 1, leadershipHash("b")); err != nil || promotion.State != PromotionComplete {
		t.Fatalf("complete promotion=%+v err=%v", promotion, err)
	}
	if err := store.Fence(t.Context(), "org-test", 2, func(context.Context) error { effects.Add(1); return nil }); err != nil || effects.Load() != 2 {
		t.Fatalf("new fence=%v effects=%d", err, effects.Load())
	}
}

func TestPostgresLeadershipDatabaseRejectsSkippedEpochDeleteAndEventMutation(t *testing.T) {
	db, schema := leadershipDatabase(t)
	store, _ := NewPostgres(db, schema)
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

	conn, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(t.Context(), fmt.Sprintf(`CREATE TEMP TABLE ascp_leadership_events
		(LIKE "%s".ascp_leadership_events INCLUDING ALL)`, schema)); err != nil {
		t.Fatal(err)
	}
	shadowTx, err := conn.BeginTx(t.Context(), &sql.TxOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var shadowedAt time.Time
	if err := shadowTx.QueryRowContext(t.Context(), fmt.Sprintf(`UPDATE "%s".ascp_leadership_epochs
		SET state='DRAINING',evidence_digest=$1,updated_at=updated_at+interval '1 second'
		WHERE organization_id='org-test' RETURNING updated_at`, schema), leadershipHash("6")).Scan(&shadowedAt); err != nil {
		t.Fatal(err)
	}
	if _, err := shadowTx.ExecContext(t.Context(), `INSERT INTO ascp_leadership_events
		(organization_id,previous_epoch,new_epoch,previous_state,new_state,evidence_digest,actor,created_at)
		VALUES ('org-test',1,1,'ACTIVE','DRAINING',$1,'owner-safe',$2)`, leadershipHash("6"), shadowedAt); err != nil {
		t.Fatal(err)
	}
	if err := shadowTx.Commit(); err == nil {
		t.Fatal("temporary audit table bypassed durable leadership evidence")
	}
}

func TestPostgresLeadershipAdapterIgnoresTemporaryTableShadow(t *testing.T) {
	db, schema := leadershipDatabase(t)
	db.SetMaxOpenConns(1)
	store, err := NewPostgres(db, schema)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Bootstrap(t.Context(), "org-test", "owner-safe", leadershipHash("1")); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), fmt.Sprintf(`CREATE TEMP TABLE ascp_leadership_epochs
		(LIKE "%s".ascp_leadership_epochs INCLUDING ALL)`, schema)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), `INSERT INTO ascp_leadership_epochs
		(organization_id,epoch,state,evidence_digest,actor,updated_at)
		VALUES ('org-test',999,'ACTIVE',$1,'attacker',now())`, leadershipHash("9")); err != nil {
		t.Fatal(err)
	}
	var shadowEpoch uint64
	if err := db.QueryRowContext(t.Context(), `SELECT epoch FROM ascp_leadership_epochs
		WHERE organization_id='org-test'`).Scan(&shadowEpoch); err != nil || shadowEpoch != 999 {
		t.Fatalf("temporary shadow setup failed: epoch=%d err=%v", shadowEpoch, err)
	}
	record, err := store.Get(t.Context(), "org-test")
	if err != nil || record.Epoch != 1 || record.Actor != "owner-safe" {
		t.Fatalf("qualified adapter read shadowed state: record=%+v err=%v", record, err)
	}
}

func TestPostgresLeadershipDatabaseRejectsDirectAdvanceWhileEffectIsInFlight(t *testing.T) {
	db, schema := leadershipDatabase(t)
	store, _ := NewPostgres(db, schema)
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
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseEffect) }) }
	defer release()
	drainTx, err := db.BeginTx(t.Context(), &sql.TxOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var drainingAt time.Time
	if err := drainTx.QueryRowContext(t.Context(), `UPDATE ascp_leadership_epochs
		SET state='DRAINING',evidence_digest=$1,actor='owner-safe',updated_at=updated_at+interval '1 second'
		WHERE organization_id='org-test' RETURNING updated_at`, leadershipHash("2")).Scan(&drainingAt); err != nil {
		t.Fatal(err)
	}
	if _, err := drainTx.ExecContext(t.Context(), `INSERT INTO ascp_leadership_events
		(organization_id,previous_epoch,new_epoch,previous_state,new_state,evidence_digest,actor,created_at)
		VALUES ('org-test',1,1,'ACTIVE','DRAINING',$1,'owner-safe',$2)`, leadershipHash("2"), drainingAt); err != nil {
		t.Fatal(err)
	}
	if err := drainTx.Commit(); err != nil {
		t.Fatal(err)
	}
	assertInFlightEffects(t, db, "org-test", 1, 1)
	advanceConn, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer advanceConn.Close()
	if _, err := advanceConn.ExecContext(t.Context(), fmt.Sprintf(`CREATE TEMP TABLE ascp_leadership_effects
		(LIKE "%s".ascp_leadership_effects INCLUDING ALL)`, schema)); err != nil {
		t.Fatal(err)
	}
	advanceTx, err := advanceConn.BeginTx(t.Context(), &sql.TxOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := advanceTx.ExecContext(t.Context(), `UPDATE ascp_leadership_epochs
		SET epoch=2,state='ACTIVE',evidence_digest=$1,actor='owner-safe',updated_at=updated_at+interval '1 second'
		WHERE organization_id='org-test'`, leadershipHash("3")); err == nil {
		t.Fatal("temporary effect table bypassed direct advance rejection")
	}
	_ = advanceTx.Rollback()
	release()
	if err := <-fenceDone; err != nil {
		t.Fatal(err)
	}
	if _, err := store.Advance(t.Context(), "org-test", 1, "owner-safe", leadershipHash("4")); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresLeadershipConnectionLossLeavesDurableFenceUntilExplicitAbandonment(t *testing.T) {
	controllerDB, schema := leadershipDatabase(t)
	controller, err := NewPostgres(controllerDB, schema)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := controller.Bootstrap(t.Context(), "org-test", "owner-safe", leadershipHash("1")); err != nil {
		t.Fatal(err)
	}
	effectDB := openLeadershipDatabase(t, schema)
	effectStore, err := NewPostgres(effectDB, schema)
	if err != nil {
		t.Fatal(err)
	}
	effectStarted := make(chan struct{})
	releaseEffect := make(chan struct{})
	fenceDone := make(chan error, 1)
	go func() {
		fenceDone <- effectStore.Fence(context.Background(), "org-test", 1, func(context.Context) error {
			close(effectStarted)
			<-releaseEffect
			return nil
		})
	}()
	<-effectStarted
	var effectID string
	if err := controllerDB.QueryRowContext(t.Context(), `SELECT effect_id FROM ascp_leadership_effects
		WHERE organization_id='org-test' AND epoch=1 AND state='IN_FLIGHT'`).Scan(&effectID); err != nil {
		t.Fatal(err)
	}
	assertInFlightEffects(t, controllerDB, "org-test", 1, 1)
	if err := effectDB.Close(); err != nil {
		t.Fatal(err)
	}
	drainDone := make(chan error, 1)
	go func() {
		_, drainErr := controller.BeginDrain(context.Background(), "org-test", 1, "owner-safe", leadershipHash("2"))
		drainDone <- drainErr
	}()
	if err := waitForLeadershipState(t.Context(), controllerDB, Draining); err != nil {
		t.Fatal(err)
	}
	close(releaseEffect)
	if err := <-fenceDone; err == nil || !strings.Contains(err.Error(), effectID) {
		t.Fatal("lost effect database connection reported durable completion")
	}
	assertInFlightEffects(t, controllerDB, "org-test", 1, 1)
	if effectIDs, err := controller.InFlightEffectIDs(t.Context(), "org-test", 1); err != nil || len(effectIDs) != 1 || effectIDs[0] != effectID {
		t.Fatalf("durable recovery queue=%v err=%v", effectIDs, err)
	}
	if err := controller.AbandonEffect(t.Context(), "org-test", 1, effectID, "recovery-operator", leadershipHash("3")); err != nil {
		t.Fatal(err)
	}
	if err := <-drainDone; err != nil {
		t.Fatal(err)
	}
	var state, actor, evidence string
	if err := controllerDB.QueryRowContext(t.Context(), `SELECT state,resolution_actor,resolution_evidence_digest
		FROM ascp_leadership_effects WHERE effect_id=$1`, effectID).Scan(&state, &actor, &evidence); err != nil {
		t.Fatal(err)
	}
	if state != "ABANDONED" || actor != "recovery-operator" || evidence != leadershipHash("3") {
		t.Fatalf("abandonment evidence: state=%s actor=%s evidence=%s", state, actor, evidence)
	}
	if effectIDs, err := controller.InFlightEffectIDs(t.Context(), "org-test", 1); err != nil || len(effectIDs) != 0 {
		t.Fatalf("resolved recovery queue=%v err=%v", effectIDs, err)
	}
	if _, err := controller.Advance(t.Context(), "org-test", 1, "owner-safe", leadershipHash("4")); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresLeadershipConcurrentDrainHasOneCASWinner(t *testing.T) {
	db, schema := leadershipDatabase(t)
	store, _ := NewPostgres(db, schema)
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

func leadershipDatabase(t *testing.T) (*sql.DB, string) {
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
	return db, schema
}

func openLeadershipDatabase(t *testing.T, schema string) *sql.DB {
	t.Helper()
	config, err := pgx.ParseConfig(os.Getenv("FLOWOPS_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	config.RuntimeParams["search_path"] = schema
	db := stdlib.OpenDB(*config)
	db.SetMaxOpenConns(8)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func waitForLeadershipState(ctx context.Context, db *sql.DB, expected State) error {
	deadline := time.Now().Add(5 * time.Second)
	for {
		var state State
		if err := db.QueryRowContext(ctx, `SELECT state FROM ascp_leadership_epochs
			WHERE organization_id='org-test'`).Scan(&state); err != nil {
			return err
		}
		if state == expected {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("leadership never reached %s; current state is %s", expected, state)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func assertInFlightEffects(t *testing.T, db *sql.DB, organizationID string, epoch uint64, expected int) {
	t.Helper()
	var count int
	if err := db.QueryRowContext(t.Context(), `SELECT count(*) FROM ascp_leadership_effects
		WHERE organization_id=$1 AND epoch=$2 AND state='IN_FLIGHT'`, organizationID, epoch).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != expected {
		t.Fatalf("in-flight effects=%d, want %d", count, expected)
	}
}

func sameRecord(left, right Record) bool {
	return left.OrganizationID == right.OrganizationID && left.Epoch == right.Epoch && left.State == right.State &&
		left.EvidenceDigest == right.EvidenceDigest && left.Actor == right.Actor && left.UpdatedAt.Equal(right.UpdatedAt)
}

func leadershipHash(s string) string {
	for len(s) < 64 {
		s += s
	}
	return "0x" + s[:64]
}
