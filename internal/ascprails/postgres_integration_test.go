package ascprails

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gnanam1990/flowops/internal/controlapi"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

func TestPostgresSellerEgressConcurrentClaimRecoveryAndImmutability(t *testing.T) {
	db := sellerDatabase(t)
	fixture := railsFixture(t)
	now := fixture.now
	seedSellerPaymentOperation(t, db, fixture)
	store, err := NewPostgresStore(db, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	type enqueueResult struct {
		job    Job
		replay bool
		err    error
	}
	enqueues := make(chan enqueueResult, 16)
	var enqueueGroup sync.WaitGroup
	for range 16 {
		enqueueGroup.Add(1)
		go func() {
			defer enqueueGroup.Done()
			job, replay, enqueueErr := store.Enqueue(context.Background(), fixture.input)
			enqueues <- enqueueResult{job, replay, enqueueErr}
		}()
	}
	enqueueGroup.Wait()
	close(enqueues)
	var enqueued Job
	first, replays := 0, 0
	for result := range enqueues {
		if result.err != nil {
			t.Fatalf("concurrent enqueue: %v", result.err)
		}
		enqueued = result.job
		if result.replay {
			replays++
		} else {
			first++
		}
	}
	if first != 1 || replays != 15 {
		t.Fatalf("first=%d replays=%d", first, replays)
	}
	gate, err := NewPostgresOperationGate(db)
	if err != nil || gate.Check(context.Background(), enqueued) != nil {
		t.Fatalf("operation gate: %v", err)
	}
	missingOperation := enqueued
	missingOperation.OperationID = testHash("98")
	if err := gate.Check(t.Context(), missingOperation); !errors.Is(err, ErrOperationNotExecutable) {
		t.Fatalf("missing operation gate=%v", err)
	}
	changedGate := enqueued
	changedGate.Binding.CommitmentHash = testHash("99")
	if err := gate.Check(context.Background(), changedGate); !errors.Is(err, ErrOperationNotExecutable) {
		t.Fatalf("substituted operation gate=%v", err)
	}
	mutation, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mutation.ExecContext(t.Context(), `CREATE TEMP TABLE seller_job_mutation ON COMMIT DROP AS SELECT * FROM ascp_seller_jobs WHERE job_id=$1`, fixture.input.JobID); err != nil {
		t.Fatal(err)
	}
	if _, err := mutation.ExecContext(t.Context(), `DELETE FROM ascp_seller_jobs WHERE job_id=$1`, fixture.input.JobID); err != nil {
		t.Fatal(err)
	}
	if _, err := mutation.ExecContext(t.Context(), `UPDATE seller_job_mutation SET offer_json=offer_json #- '{accepted,asset}'`); err != nil {
		t.Fatal(err)
	}
	if _, err := mutation.ExecContext(t.Context(), `INSERT INTO ascp_seller_jobs SELECT * FROM seller_job_mutation`); err == nil {
		t.Fatal("database accepted missing offer asset binding")
	}
	_ = mutation.Rollback()
	if _, replay, err := store.Enqueue(context.Background(), fixture.input); err != nil || !replay {
		t.Fatalf("exact replay=%t err=%v", replay, err)
	}
	changed := fixture.input
	changed.DeliverBy++
	if _, _, err := store.Enqueue(context.Background(), changed); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("changed replay=%v", err)
	}

	var winners atomic.Int32
	claimed := make(chan Lease, 16)
	var group sync.WaitGroup
	for range 16 {
		group.Add(1)
		go func() {
			defer group.Done()
			lease, claimErr := store.ClaimDispatch(context.Background(), "rails-worker", 20*time.Second)
			if claimErr == nil {
				winners.Add(1)
				claimed <- lease
			} else if !errors.Is(claimErr, ErrNoWork) {
				t.Errorf("claim=%v", claimErr)
			}
		}()
	}
	group.Wait()
	close(claimed)
	if winners.Load() != 1 {
		t.Fatalf("concurrent winners=%d", winners.Load())
	}
	lease := <-claimed
	job, err := store.MarkSending(context.Background(), lease, fixture.observation)
	if err != nil || job.State != StateSending || job.AttemptCount != 1 {
		t.Fatalf("sending=%+v err=%v", job, err)
	}
	// Simulate a process crash after SENDING. Only lease expiry permits a new
	// exact attempt, and the abandoned attempt becomes explicitly ambiguous.
	now = now.Add(21 * time.Second)
	lease, err = store.ClaimDispatch(context.Background(), "rails-worker", 20*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	job, err = store.MarkSending(context.Background(), lease, fixture.observation)
	if err != nil || job.AttemptCount != 2 {
		t.Fatalf("recovered sending=%+v err=%v", job, err)
	}
	var abandonedState string
	if err := db.QueryRowContext(t.Context(), `SELECT state FROM ascp_seller_attempts WHERE job_id=$1 AND attempt_number=1`, fixture.input.JobID).Scan(&abandonedState); err != nil || abandonedState != "AMBIGUOUS" {
		t.Fatalf("abandoned state=%s err=%v", abandonedState, err)
	}
	lease.Job = job
	response := StoredResponse{Attempt: 2, Status: 200, ContentType: "application/json", PaymentResponse: "payment-response-placeholder",
		Body: []byte("durable result"), Digest: testHash("45"), ReceivedAt: now}
	job, err = store.RecordResponse(context.Background(), lease, response, StateResponseStored, "RESPONSE_STORED", time.Time{})
	if err != nil || job.State != StateResponseStored {
		t.Fatalf("stored=%+v err=%v", job, err)
	}
	if err := store.ReleaseLease(context.Background(), lease); err != nil {
		t.Fatal(err)
	}

	finalLease, err := store.ClaimFinalization(context.Background(), "rails-worker", 20*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	finalObservation := ChainObservation{Timestamp: fixture.observation.Timestamp + 5, EvidenceDigest: testHash("46"), ObservedAt: now}
	job, err = store.FinalizeCapture(context.Background(), finalLease, finalObservation)
	if err != nil || job.State != StateCaptured || job.CapturedAt != finalObservation.Timestamp || string(job.ResponseBody) != "durable result" {
		t.Fatalf("captured=%+v err=%v", job, err)
	}
	if err := store.ReleaseLease(context.Background(), finalLease); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), `UPDATE ascp_seller_jobs SET request_url='https://evil.example/' WHERE job_id=$1`, fixture.input.JobID); err == nil {
		t.Fatal("database accepted immutable request substitution")
	}
	if _, err := db.ExecContext(t.Context(), `UPDATE ascp_seller_responses SET response_body='changed' WHERE job_id=$1`, fixture.input.JobID); err == nil {
		t.Fatal("database accepted response mutation")
	}
	if _, err := db.ExecContext(t.Context(), `DELETE FROM ascp_seller_attempts WHERE job_id=$1`, fixture.input.JobID); err == nil {
		t.Fatal("database accepted attempt evidence deletion")
	}
	if _, err := db.ExecContext(t.Context(), `UPDATE ascp_seller_jobs SET state='QUEUED' WHERE job_id=$1`, fixture.input.JobID); err == nil {
		t.Fatal("database reopened captured job")
	}
}

func seedSellerPaymentOperation(t *testing.T, db *sql.DB, fixture railsTestFixture) {
	t.Helper()
	input := fixture.input
	agentID := "agent-test"
	approvalID, reservationID, authorizationID, bearerDigest := testHash("51"), testHash("52"), testHash("53"), testHash("54")
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO agents (organization_id,id,customer_id,name,status) VALUES ('org-test',$1,$1,'Seller Agent','ACTIVE')`, []any{agentID}},
		{`INSERT INTO ascp_intents (operation_id,organization_id,actor_id,endpoint,idempotency_key,canonical_input_hash,
			quote_hash,purchase_spec_hash,quote_nonce,directory_version,directory_contract,seller_signer,quote_json,
			purchase_spec_json,purchase_spec_bytes,request_body,created_at)
			VALUES ($1,'org-test',$2,'ascp.intent.create',$1,$3,$4,$5,$6,7,$7,$8,'{}',$9::jsonb,$10,$11,$12)`,
			[]any{input.OperationID, agentID, strings.Repeat("55", 32), input.Offer.Intake.QuoteHash, fixture.purchaseSpecHash,
				testHash("56"), input.Offer.Intake.ServiceDirectory, input.Offer.Intake.QuoteSigner, string(input.CanonicalSpecJSON), input.CanonicalSpecJSON, input.Body, fixture.now}},
		{`INSERT INTO ascp_approvals (approval_id,organization_id,intent_id,state,review_snapshot_hash,requested_at,expires_at,decided_at,decided_by)
			VALUES ($1,'org-test',$2,'APPROVED',$3,$4,$5,$4,'owner')`, []any{approvalID, input.OperationID, testHash("57"), fixture.now, fixture.now.Add(time.Hour)}},
		{`INSERT INTO ascp_budget_reservations (reservation_id,operation_id,amount_base_units,state,dimensions,created_at,expires_at)
			VALUES ($1,$2,$3,'AUTHORIZATION_LIVE','[]',$4,$5)`, []any{reservationID, input.OperationID, input.Offer.Accepted.Amount, fixture.now, fixture.now.Add(time.Hour)}},
		{`INSERT INTO ascp_execution_authorizations (authorization_id,approval_id,intent_id,state,execution_snapshot_hash,reservation_id,created_at,evaluated_at)
			VALUES ($1,$2,$3,'VALIDATED_AND_RESERVED',$4,$5,$6,$6)`, []any{authorizationID, approvalID, input.OperationID, testHash("58"), reservationID, fixture.now}},
		{`INSERT INTO ascp_bearer_registry (digest,instrument_type,signature_ref,nonce,issued_at,valid_until,signer_key_id,key_epoch,
			operation_id,authorization_id,reservation_id,module_address,safe_address,outcome,created_at)
			VALUES ($1,'LOCK_AUTHORIZATION','handle_seller_test',$2,$3,$4,'key',1,$5,$6,$7,$8,$9,'LIVE',$3)`,
			[]any{bearerDigest, testHash("59"), fixture.now, fixture.now.Add(time.Hour), input.OperationID, authorizationID, reservationID,
				"0x7777777777777777777777777777777777777777", "0x8888888888888888888888888888888888888888"}},
		{`INSERT INTO ascp_payment_operations (operation_id,organization_id,agent_id,authorization_id,reservation_id,bearer_digest,
			commitment_hash,call_id,chain_id,escrow_contract,asset,buyer,pay_to,amount_base_units,settle_by,state,
			locked_transaction_hash,locked_block_number,locked_block_hash,created_at,updated_at)
			VALUES ($1,'org-test',$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,'LOCKED_FINALIZED',$15,100,$16,$17,$17)`,
			[]any{input.OperationID, agentID, authorizationID, reservationID, bearerDigest, input.Binding.CommitmentHash, input.JobID, input.ChainID,
				input.Binding.EscrowContract, input.Offer.Accepted.Asset, input.Payer, input.Offer.Accepted.PayTo,
				input.Offer.Accepted.Amount, time.Unix(int64(input.DeliverBy+300), 0).UTC(), input.LockedTransactionHash, testHash("61"), fixture.now}},
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(t.Context(), statement.query, statement.args...); err != nil {
			t.Fatalf("seed seller operation: %v\n%s", err, statement.query)
		}
	}
}

func sellerDatabase(t *testing.T) *sql.DB {
	t.Helper()
	databaseURL := os.Getenv("FLOWOPS_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("FLOWOPS_TEST_DATABASE_URL is not configured")
	}
	admin, err := sql.Open("pgx", databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("flowops_seller_it_%d", time.Now().UnixNano())
	if _, err := admin.ExecContext(t.Context(), `CREATE SCHEMA `+schema); err != nil {
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
	if _, err := db.ExecContext(t.Context(), `INSERT INTO organizations (id,name,created_at) VALUES ('org-test','Seller rails test',$1)`, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = db.Close()
		_, _ = admin.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS `+schema+` CASCADE`)
		_ = admin.Close()
	})
	return db
}
