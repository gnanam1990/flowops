package ascpkeeper

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gnanam1990/flowops/internal/controlapi"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestPostgresKeeperDurableNonceAndBroadcastLifecycle(t *testing.T) {
	db := keeperDatabase(t)
	var runtimeIndexes int
	if err := db.QueryRow(`SELECT count(*) FROM pg_indexes WHERE schemaname=current_schema() AND indexname IN
		('ascp_keeper_jobs_runtime_claim_idx','ascp_keeper_jobs_runtime_observation_idx')`).Scan(&runtimeIndexes); err != nil || runtimeIndexes != 2 {
		t.Fatalf("keeper runtime indexes=%d err=%v", runtimeIndexes, err)
	}
	now := time.Unix(1_800_000_000, 0).UTC()
	store, err := NewPostgresStore(db, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	input := signedInput(now)
	if _, replay, err := store.Enqueue(context.Background(), input); err != nil || replay {
		t.Fatalf("enqueue replay=%t err=%v", replay, err)
	}
	if _, replay, err := store.Enqueue(context.Background(), input); err != nil || !replay {
		t.Fatalf("replay=%t err=%v", replay, err)
	}
	changed := input
	changed.Target = "0x4444444444444444444444444444444444444444"
	if _, _, err := store.Enqueue(context.Background(), changed); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("substitution=%v", err)
	}

	var successful atomic.Int32
	claimed := make(chan Lease, 20)
	var group sync.WaitGroup
	for range 20 {
		group.Add(1)
		go func() {
			defer group.Done()
			lease, err := store.Claim(context.Background(), input.KeeperID, input.GasPayer, input.ChainID, 20*time.Second)
			if err == nil {
				successful.Add(1)
				claimed <- lease
			} else if !errors.Is(err, ErrNoWork) {
				t.Errorf("claim error=%v", err)
			}
		}()
	}
	group.Wait()
	close(claimed)
	if successful.Load() != 1 {
		t.Fatalf("successful concurrent claims=%d", successful.Load())
	}
	lease := <-claimed
	nonce, err := store.AllocateNonce(context.Background(), lease, 17)
	if err != nil || nonce != 17 {
		t.Fatalf("nonce=%d err=%v", nonce, err)
	}
	replayNonce, err := store.AllocateNonce(context.Background(), lease, 99)
	if err != nil || replayNonce != nonce {
		t.Fatalf("reserved nonce=%d err=%v", replayNonce, err)
	}
	attempt := Attempt{JobID: input.JobID, Number: 1, Nonce: nonce, GasPayer: input.GasPayer,
		Fee: Fee{"100", "2"}, TransactionHash: testHash(90), SealedRawTransaction: []byte("ciphertext"),
		SealingKeyID: "keeper-kms-v1", State: AttemptPrepared, PreparedAt: now}
	if job, err := store.RecordPrepared(context.Background(), lease, attempt); err != nil || job.State != StatePrepared {
		t.Fatalf("prepared=%+v err=%v", job, err)
	}
	if job, err := store.MarkBroadcasting(context.Background(), lease, 1); err != nil || job.State != StateBroadcasting {
		t.Fatalf("broadcasting=%+v err=%v", job, err)
	}
	if job, err := store.MarkSubmitted(context.Background(), lease, 1, attempt.TransactionHash); err != nil || job.State != StateSubmitted {
		t.Fatalf("submitted=%+v err=%v", job, err)
	}
	if err := store.ReleaseLease(context.Background(), lease); err != nil {
		t.Fatal(err)
	}
	observationLease, err := store.ClaimObservation(context.Background(), input.KeeperID, input.GasPayer, input.ChainID, 20*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	outcome := Outcome{JobID: input.JobID, AttemptNumber: 1, TransactionHash: attempt.TransactionHash,
		State: StateConfirmed, EvidenceDigest: testHash(70), ObservedAt: now}
	if job, err := store.ApplyOutcome(context.Background(), observationLease, outcome); err != nil || job.State != StateConfirmed {
		t.Fatalf("confirmed=%+v err=%v", job, err)
	}
	if err := store.ReleaseLease(context.Background(), observationLease); err != nil {
		t.Fatal(err)
	}
	observationLease, err = store.ClaimObservation(context.Background(), input.KeeperID, input.GasPayer, input.ChainID, 20*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	outcome.State, outcome.EvidenceDigest = StateFinalized, testHash(71)
	if job, err := store.ApplyOutcome(context.Background(), observationLease, outcome); err != nil || job.State != StateFinalized {
		t.Fatalf("finalized=%+v err=%v", job, err)
	}
	if err := store.ReleaseLease(context.Background(), observationLease); err != nil {
		t.Fatal(err)
	}

	var gasPayer, persistedNonce string
	if err := db.QueryRow(`SELECT gas_payer,nonce::text FROM ascp_keeper_tx_attempts WHERE job_id=$1`, input.JobID).Scan(&gasPayer, &persistedNonce); err != nil {
		t.Fatal(err)
	}
	if gasPayer != input.GasPayer || persistedNonce != "17" {
		t.Fatalf("gas payer=%s nonce=%s", gasPayer, persistedNonce)
	}
	if _, err := db.Exec(`UPDATE ascp_keeper_tx_attempts SET gas_payer='0x9999999999999999999999999999999999999999' WHERE job_id=$1`, input.JobID); err == nil {
		t.Fatal("database accepted attempt gas-payer substitution")
	}

	replacementInput := signedInput(now)
	replacementInput.JobID = testHash(101)
	replacementInput.OperationID = testHash(102)
	replacementInput.AuthorizationDigest = testHash(103)
	if _, _, err := store.Enqueue(context.Background(), replacementInput); err != nil {
		t.Fatal(err)
	}
	replacementLease, err := store.Claim(context.Background(), replacementInput.KeeperID, replacementInput.GasPayer, replacementInput.ChainID, 20*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	replacementNonce, err := store.AllocateNonce(context.Background(), replacementLease, 18)
	if err != nil {
		t.Fatal(err)
	}
	firstAttempt := Attempt{JobID: replacementInput.JobID, Number: 1, Nonce: replacementNonce, GasPayer: replacementInput.GasPayer,
		Fee: Fee{"100", "2"}, TransactionHash: testHash(104), SealedRawTransaction: []byte("ciphertext-one"), SealingKeyID: "keeper-kms-v1", State: AttemptPrepared, PreparedAt: now}
	if _, err := store.RecordPrepared(context.Background(), replacementLease, firstAttempt); err != nil {
		t.Fatal(err)
	}
	if _, err := store.MarkBroadcasting(context.Background(), replacementLease, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := store.MarkRejected(context.Background(), replacementLease, 1, StateTimedOut, "replacement underpriced"); err != nil {
		t.Fatal(err)
	}
	if err := store.ReleaseLease(context.Background(), replacementLease); err != nil {
		t.Fatal(err)
	}
	replacementLease, err = store.Claim(context.Background(), replacementInput.KeeperID, replacementInput.GasPayer, replacementInput.ChainID, 20*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	previous, err := store.CurrentAttempt(context.Background(), replacementInput.JobID)
	if err != nil {
		t.Fatal(err)
	}
	secondAttempt := Attempt{JobID: replacementInput.JobID, Number: 2, Nonce: previous.Nonce, GasPayer: previous.GasPayer,
		Fee: Fee{"120", "2"}, TransactionHash: testHash(105), SealedRawTransaction: []byte("ciphertext-two"), SealingKeyID: "keeper-kms-v1", State: AttemptPrepared, PreparedAt: now}
	if job, err := store.RecordReplacement(context.Background(), replacementLease, previous, secondAttempt); err != nil || job.State != StatePrepared {
		t.Fatalf("replacement=%+v err=%v", job, err)
	}
	if current, err := store.CurrentAttempt(context.Background(), replacementInput.JobID); err != nil || current.Number != 2 || current.Nonce != replacementNonce {
		t.Fatalf("current=%+v err=%v", current, err)
	}
	if err := store.ReleaseLease(context.Background(), replacementLease); err != nil {
		t.Fatal(err)
	}

	foreign := signedInput(now)
	foreign.JobID, foreign.OperationID = testHash(110), testHash(111)
	foreign.AuthorizationDigest = testHash(112)
	foreign.KeeperID = "keeper-foreign-scope"
	foreign.GasPayer = "0x9999999999999999999999999999999999999999"
	if _, _, err := store.Enqueue(context.Background(), foreign); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Claim(context.Background(), foreign.KeeperID, input.GasPayer, foreign.ChainID, 20*time.Second); !errors.Is(err, ErrNoWork) {
		t.Fatalf("another gas payer poisoned the worker claim: %v", err)
	}
	if lease, err := store.Claim(context.Background(), foreign.KeeperID, foreign.GasPayer, foreign.ChainID, 20*time.Second); err != nil || lease.Job.JobID != foreign.JobID {
		t.Fatalf("exact gas-payer worker did not claim its job: lease=%+v err=%v", lease, err)
	}

	crossChain := signedInput(now)
	crossChain.JobID, crossChain.OperationID, crossChain.AuthorizationDigest = testHash(120), testHash(121), testHash(122)
	crossChain.KeeperID, crossChain.ChainID = "keeper-chain-scope", 8453
	if _, _, err := store.Enqueue(context.Background(), crossChain); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Claim(context.Background(), crossChain.KeeperID, crossChain.GasPayer, 84532, 20*time.Second); !errors.Is(err, ErrNoWork) {
		t.Fatalf("another Base chain poisoned the worker claim: %v", err)
	}
	if lease, err := store.Claim(context.Background(), crossChain.KeeperID, crossChain.GasPayer, crossChain.ChainID, 20*time.Second); err != nil || lease.Job.JobID != crossChain.JobID {
		t.Fatalf("exact-chain worker did not claim its job: lease=%+v err=%v", lease, err)
	}
}

func keeperDatabase(t *testing.T) *sql.DB {
	t.Helper()
	databaseURL := os.Getenv("FLOWOPS_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("FLOWOPS_TEST_DATABASE_URL is not configured")
	}
	admin, err := sql.Open("pgx", databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("flowops_keeper_it_%d", time.Now().UnixNano())
	if _, err := admin.Exec(`CREATE SCHEMA ` + schema); err != nil {
		t.Fatal(err)
	}
	config, err := pgx.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	config.RuntimeParams["search_path"] = schema
	db := stdlib.OpenDB(*config)
	if err := controlapi.ApplyMigrations(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO organizations (id,name,created_at) VALUES ('org-test','Keeper test',$1)`, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = db.Close()
		_, _ = admin.Exec(`DROP SCHEMA IF EXISTS ` + schema + ` CASCADE`)
		_ = admin.Close()
	})
	return db
}
