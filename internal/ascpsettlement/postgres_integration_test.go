package ascpsettlement

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
	"github.com/gnanam1990/flowops/internal/reconciliation"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

func TestPostgresStoreFinalityAccountingAndTerminalReorgRecovery(t *testing.T) {
	db := settlementDatabase(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	operationID := settlementHash(1)
	seedSettlementOperation(t, db, operationID, now)
	store, err := NewPostgresStore(db, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	lock := AttemptInput{OperationID: operationID, Action: reconciliation.ASCPReceiptLock, TransactionHash: settlementHash(20)}
	if attempt, replay, err := store.RegisterAttempt(ctx, lock); err != nil || replay || attempt.State != AttemptSubmitted {
		t.Fatalf("RegisterAttempt(lock) = %+v, %t, %v", attempt, replay, err)
	}
	if _, replay, err := store.RegisterAttempt(ctx, lock); err != nil || !replay {
		t.Fatalf("lock replay = %t, %v", replay, err)
	}
	changed := lock
	changed.TransactionHash = settlementHash(21)
	if _, _, err := store.RegisterAttempt(ctx, changed); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("changed lock replay error = %v", err)
	}
	expected, err := store.Expected(ctx, operationID, reconciliation.ASCPReceiptLock)
	if err != nil {
		t.Fatal(err)
	}
	operation, err := store.Apply(ctx, sealedResult(expected, Safe, 100, settlementHash(100), 102, now))
	if err != nil || operation.State != LockedSafe {
		t.Fatalf("Apply(lock safe) = %+v, %v", operation, err)
	}
	operation, err = store.Apply(ctx, sealedResult(expected, Safe, 100, settlementHash(100), 104, now))
	if err != nil || operation.State != LockedSafe {
		t.Fatalf("Apply(repeated lock safe) = %+v, %v", operation, err)
	}
	lockFinal := sealedResult(expected, Finalized, 100, settlementHash(100), 112, now)
	operation, err = store.Apply(ctx, lockFinal)
	if err != nil || operation.State != LockedFinalized {
		t.Fatalf("Apply(lock finalized) = %+v, %v", operation, err)
	}
	if _, err := store.Apply(ctx, lockFinal); err != nil {
		t.Fatalf("lock final replay = %v", err)
	}
	assertSettlementState(t, db, operationID, "COMMITTED_FINALIZED", "CONSUMED", "LOCKED_FINALIZED", 1, 2)
	assertDatabaseRejectsMisclassifiedLedger(t, db, operationID, now)

	release := AttemptInput{OperationID: operationID, Action: reconciliation.ASCPReceiptRelease, TransactionHash: settlementHash(30), DeliveryHash: settlementHash(31), EvidenceHash: settlementHash(32)}
	if _, _, err := store.RegisterAttempt(ctx, release); err != nil {
		t.Fatal(err)
	}
	expected, err = store.Expected(ctx, operationID, reconciliation.ASCPReceiptRelease)
	if err != nil {
		t.Fatal(err)
	}
	operation, err = store.Apply(ctx, sealedResult(expected, Safe, 101, settlementHash(101), 103, now))
	if err != nil || operation.State != LockedFinalized {
		t.Fatalf("Apply(release safe) = %+v, %v", operation, err)
	}
	assertSettlementState(t, db, operationID, "COMMITTED_FINALIZED", "CONSUMED", "LOCKED_FINALIZED", 1, 2)
	operation, err = store.Apply(ctx, sealedResult(expected, Finalized, 101, settlementHash(101), 113, now))
	if err != nil || operation.State != ReleasedFinalized {
		t.Fatalf("Apply(release finalized) = %+v, %v", operation, err)
	}
	assertSettlementState(t, db, operationID, "CONSUMED_ON_RELEASE", "CONSUMED", "RELEASED_FINALIZED", 2, 4)
	regression := sealedResult(expected, Finalized, 102, settlementHash(102), 114, now)
	regression.Success = false
	regression.EvidenceDigest = resultDigest(regression)
	regression.seal = resultSeal(regression)
	if _, err := store.Apply(ctx, regression); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("finalized-to-reverted regression error=%v", err)
	}
	assertSettlementState(t, db, operationID, "CONSUMED_ON_RELEASE", "CONSUMED", "RELEASED_FINALIZED", 2, 4)

	reorg := sealedReorg(operationID, reconciliation.ASCPReceiptRelease, release.TransactionHash, 101,
		settlementHash(101), settlementHash(901), 120, now)
	operation, err = store.ApplyReorg(ctx, reorg)
	if err != nil || operation.State != PendingChainRecovery {
		t.Fatalf("ApplyReorg(release) = %+v, %v", operation, err)
	}
	assertSettlementState(t, db, operationID, "COMMITTED_FINALIZED", "CONSUMED", "PENDING_CHAIN_RECOVERY", 3, 6)
	reorgReplay := sealedReorg(operationID, reconciliation.ASCPReceiptRelease, release.TransactionHash, 101,
		settlementHash(101), settlementHash(901), 121, now)
	if replayed, err := store.ApplyReorg(ctx, reorgReplay); err != nil || replayed.State != PendingChainRecovery {
		t.Fatalf("ApplyReorg(replay) = %+v, %v", replayed, err)
	}
	assertSettlementState(t, db, operationID, "COMMITTED_FINALIZED", "CONSUMED", "PENDING_CHAIN_RECOVERY", 3, 6)
	assertLedgerAccountSums(t, db, operationID, map[string]string{
		"WalletAvailableUSDC": "-10", "EscrowRestrictedUSDC": "10", "SellerExpense": "0",
	})
	if _, _, err := store.RegisterAttempt(ctx, AttemptInput{OperationID: operationID, Action: reconciliation.ASCPReceiptRefund, TransactionHash: settlementHash(40)}); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("retry during recovery error = %v", err)
	}
}

func TestPostgresStoreLockReorgReversesEveryClassifiedPosting(t *testing.T) {
	db := settlementDatabase(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	operationID := settlementHash(101)
	seedSettlementOperation(t, db, operationID, now)
	store, _ := NewPostgresStore(db, func() time.Time { return now })
	ctx := context.Background()
	lock := AttemptInput{OperationID: operationID, Action: reconciliation.ASCPReceiptLock, TransactionHash: settlementHash(120)}
	_, _, _ = store.RegisterAttempt(ctx, lock)
	expected, _ := store.Expected(ctx, operationID, reconciliation.ASCPReceiptLock)
	if _, err := store.Apply(ctx, sealedResult(expected, Finalized, 200, settlementHash(200), 212, now)); err != nil {
		t.Fatal(err)
	}
	reorg := sealedReorg(operationID, reconciliation.ASCPReceiptLock, lock.TransactionHash, 200,
		settlementHash(200), settlementHash(902), 220, now)
	operation, err := store.ApplyReorg(ctx, reorg)
	if err != nil || operation.State != ReorgedBack {
		t.Fatalf("ApplyReorg(lock) = %+v, %v", operation, err)
	}
	assertSettlementState(t, db, operationID, "REORGED_BACK", "CONSUMED", "REORGED_BACK", 2, 4)
	assertLedgerAccountSums(t, db, operationID, map[string]string{"WalletAvailableUSDC": "0", "EscrowRestrictedUSDC": "0"})
}

func TestPostgresStoreSerializesCompetingTerminalAttempts(t *testing.T) {
	db := settlementDatabase(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	operationID := settlementHash(301)
	seedSettlementOperation(t, db, operationID, now)
	store, _ := NewPostgresStore(db, func() time.Time { return now })
	ctx := context.Background()
	lock := AttemptInput{OperationID: operationID, Action: reconciliation.ASCPReceiptLock, TransactionHash: settlementHash(320)}
	_, _, _ = store.RegisterAttempt(ctx, lock)
	expected, _ := store.Expected(ctx, operationID, reconciliation.ASCPReceiptLock)
	if _, err := store.Apply(ctx, sealedResult(expected, Finalized, 300, settlementHash(300), 312, now)); err != nil {
		t.Fatal(err)
	}
	var group sync.WaitGroup
	var successes atomic.Int64
	for index := 0; index < 20; index++ {
		index := index
		group.Add(1)
		go func() {
			defer group.Done()
			action := reconciliation.ASCPReceiptRefund
			input := AttemptInput{OperationID: operationID, Action: action, TransactionHash: settlementHash(400 + uint64(index))}
			if index%2 == 0 {
				input.Action, input.DeliveryHash, input.EvidenceHash = reconciliation.ASCPReceiptRelease, settlementHash(500+uint64(index)), settlementHash(600+uint64(index))
			}
			if _, _, err := store.RegisterAttempt(ctx, input); err == nil {
				successes.Add(1)
			}
		}()
	}
	group.Wait()
	var terminalAttempts int
	if err := db.QueryRow(`SELECT count(*) FROM ascp_payment_attempts WHERE operation_id=$1 AND action IN ('RELEASE','REFUND')`, operationID).Scan(&terminalAttempts); err != nil {
		t.Fatal(err)
	}
	if successes.Load() != 1 || terminalAttempts != 1 {
		t.Fatalf("successful registrations=%d terminal rows=%d", successes.Load(), terminalAttempts)
	}
}

func sealedResult(expected reconciliation.ASCPExpectedReceipt, finality Finality, block uint64, blockHash string, head uint64, now time.Time) Result {
	result := Result{Expected: expected, Finality: finality, Success: true, BlockNumber: block, BlockHash: blockHash,
		ConfirmedHead: head, Providers: []string{"rpc_alpha", "rpc_beta"}, ObservedAt: now, verified: true}
	result.EvidenceDigest = resultDigest(result)
	result.seal = resultSeal(result)
	return result
}

func sealedReorg(operationID string, action reconciliation.ASCPReceiptAction, txHash string, block uint64, original, canonical string, head uint64, now time.Time) ReorgResult {
	result := ReorgResult{OperationID: operationID, Action: action, TransactionHash: txHash, BlockNumber: block,
		OriginalBlockHash: original, CanonicalBlockHash: canonical, ObservedHead: head,
		Providers: []string{"rpc_alpha", "rpc_beta"}, ObservedAt: now, Reorged: true, verified: true}
	result.EvidenceDigest = reorgDigest(result)
	result.seal = reorgSeal(result)
	return result
}

func seedSettlementOperation(t *testing.T, db *sql.DB, operationID string, now time.Time) {
	t.Helper()
	organizationID := "org_" + operationID[len(operationID)-8:]
	agentID := "agent_" + operationID[len(operationID)-8:]
	approvalID, reservationID, authorizationID, bearerDigest := hashOffset(operationID, 1), hashOffset(operationID, 2), hashOffset(operationID, 3), hashOffset(operationID, 4)
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO organizations (id,name) VALUES ($1,'Settlement Org')`, []any{organizationID}},
		{`INSERT INTO agents (organization_id,id,customer_id,name,status) VALUES ($1,$2,$2,'Settlement Agent','ACTIVE')`, []any{organizationID, agentID}},
		{`INSERT INTO ascp_intents (operation_id,organization_id,actor_id,endpoint,idempotency_key,canonical_input_hash,
			quote_hash,purchase_spec_hash,quote_nonce,directory_version,directory_contract,seller_signer,quote_json,
			purchase_spec_json,purchase_spec_bytes,request_body,created_at)
			VALUES ($1,$2,$3,'ascp.intent.create',$1,$4,$5,$6,$7,1,$8,$9,'{}','{}','{}'::bytea,''::bytea,$10)`,
			[]any{operationID, organizationID, agentID, operationID[2:], hashOffset(operationID, 5), hashOffset(operationID, 6), hashOffset(operationID, 7),
				"0x6666666666666666666666666666666666666666", "0x5555555555555555555555555555555555555555", now}},
		{`INSERT INTO ascp_approvals (approval_id,organization_id,intent_id,state,review_snapshot_hash,requested_at,expires_at,decided_at,decided_by)
			VALUES ($1,$2,$3,'APPROVED',$4,$5,$6,$5,'owner')`, []any{approvalID, organizationID, operationID, hashOffset(operationID, 8), now, now.Add(time.Hour)}},
		{`INSERT INTO ascp_budget_reservations (reservation_id,operation_id,amount_base_units,state,dimensions,created_at,expires_at)
			VALUES ($1,$2,'10','AUTHORIZATION_LIVE','[]',$3,$4)`, []any{reservationID, operationID, now, now.Add(time.Hour)}},
		{`INSERT INTO ascp_execution_authorizations (authorization_id,approval_id,intent_id,state,execution_snapshot_hash,reservation_id,created_at,evaluated_at)
			VALUES ($1,$2,$3,'VALIDATED_AND_RESERVED',$4,$5,$6,$6)`, []any{authorizationID, approvalID, operationID, hashOffset(operationID, 9), reservationID, now}},
		{`INSERT INTO ascp_bearer_registry (digest,instrument_type,signature_ref,nonce,issued_at,valid_until,signer_key_id,key_epoch,
			operation_id,authorization_id,reservation_id,module_address,safe_address,outcome,created_at)
			VALUES ($1,'LOCK_AUTHORIZATION',$2,$3,$4,$5,'key',1,$6,$7,$8,$9,$10,'LIVE',$4)`,
			[]any{bearerDigest, "handle_" + operationID[len(operationID)-16:], hashOffset(operationID, 10), now, now.Add(time.Hour), operationID, authorizationID, reservationID,
				"0x7777777777777777777777777777777777777777", "0x8888888888888888888888888888888888888888"}},
		{`INSERT INTO ascp_payment_operations (operation_id,organization_id,agent_id,authorization_id,reservation_id,bearer_digest,
			commitment_hash,call_id,chain_id,escrow_contract,asset,buyer,pay_to,amount_base_units,settle_by,state,created_at,updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,84532,$9,$10,$11,$12,'10',$13,'AUTH_SIGNED',$14,$14)`,
			[]any{operationID, organizationID, agentID, authorizationID, reservationID, bearerDigest, hashOffset(operationID, 11), hashOffset(operationID, 12),
				"0x9999999999999999999999999999999999999999", "0x036cbd53842c5426634e7929541ec2318f3dcf7e", "0x8888888888888888888888888888888888888888",
				"0x3333333333333333333333333333333333333333", now.Add(20 * time.Minute), now}},
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement.query, statement.args...); err != nil {
			t.Fatalf("seed settlement operation: %v\n%s", err, statement.query)
		}
	}
}

func assertSettlementState(t *testing.T, db *sql.DB, operationID, reservation, bearer, operation string, transactions, postings int) {
	t.Helper()
	var gotReservation, gotBearer, gotOperation string
	if err := db.QueryRow(`SELECT r.state,b.outcome,o.state FROM ascp_payment_operations o JOIN ascp_budget_reservations r USING (reservation_id) JOIN ascp_bearer_registry b ON b.digest=o.bearer_digest WHERE o.operation_id=$1`, operationID).
		Scan(&gotReservation, &gotBearer, &gotOperation); err != nil {
		t.Fatal(err)
	}
	var gotTransactions, gotPostings int
	_ = db.QueryRow(`SELECT count(*) FROM ascp_ledger_transactions WHERE operation_id=$1`, operationID).Scan(&gotTransactions)
	_ = db.QueryRow(`SELECT count(*) FROM ascp_ledger_postings p JOIN ascp_ledger_transactions t USING (transaction_id) WHERE t.operation_id=$1`, operationID).Scan(&gotPostings)
	if gotReservation != reservation || gotBearer != bearer || gotOperation != operation || gotTransactions != transactions || gotPostings != postings {
		t.Fatalf("state reservation=%s bearer=%s operation=%s transactions=%d postings=%d", gotReservation, gotBearer, gotOperation, gotTransactions, gotPostings)
	}
}

func assertLedgerAccountSums(t *testing.T, db *sql.DB, operationID string, expected map[string]string) {
	t.Helper()
	for account, want := range expected {
		var got string
		if err := db.QueryRow(`SELECT COALESCE(sum(amount_base_units::numeric),0)::text FROM ascp_ledger_postings p JOIN ascp_ledger_transactions t USING (transaction_id) WHERE t.operation_id=$1 AND p.account=$2`, operationID, account).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("account %s sum=%s want=%s", account, got, want)
		}
	}
}

func assertDatabaseRejectsMisclassifiedLedger(t *testing.T, db *sql.DB, operationID string, now time.Time) {
	t.Helper()
	var organizationID, asset, evidenceDigest string
	if err := db.QueryRow(`SELECT o.organization_id,o.asset,c.evidence_digest FROM ascp_payment_operations o JOIN ascp_chain_observations c USING (operation_id) WHERE o.operation_id=$1 AND c.action='LOCK' AND c.finality='FINALIZED'`, operationID).
		Scan(&organizationID, &asset, &evidenceDigest); err != nil {
		t.Fatal(err)
	}
	transactionID := hashOffset(operationID, 50)
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO ascp_ledger_transactions (transaction_id,organization_id,operation_id,kind,asset,evidence_digest,recorded_at)
		VALUES ($1,$2,$3,'RELEASE_FINALIZED',$4,$5,$6)`, transactionID, organizationID, operationID, asset, evidenceDigest, now); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO ascp_ledger_postings (transaction_id,line_number,account,amount_base_units)
		VALUES ($1,1,'WalletAvailableUSDC','10'),($1,2,'EscrowRestrictedUSDC','-10')`, transactionID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err == nil {
		t.Fatal("database accepted balanced but misclassified ledger postings")
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM ascp_ledger_transactions WHERE transaction_id=$1`, transactionID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("rejected ledger transaction persisted count=%d err=%v", count, err)
	}
}

func settlementDatabase(t *testing.T) *sql.DB {
	t.Helper()
	databaseURL := os.Getenv("FLOWOPS_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("FLOWOPS_TEST_DATABASE_URL is not configured")
	}
	admin, err := sql.Open("pgx", databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("flowops_settlement_it_%d", time.Now().UnixNano())
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
	t.Cleanup(func() {
		_ = db.Close()
		_, _ = admin.Exec(`DROP SCHEMA IF EXISTS ` + schema + ` CASCADE`)
		_ = admin.Close()
	})
	return db
}

func settlementHash(value uint64) string { return fmt.Sprintf("0x%064x", value) }

func hashOffset(hash string, offset uint64) string {
	var value uint64
	_, _ = fmt.Sscanf(hash[len(hash)-16:], "%x", &value)
	return settlementHash(value + offset)
}
