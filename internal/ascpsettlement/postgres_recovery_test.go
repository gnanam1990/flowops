package ascpsettlement

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gnanam1990/flowops/internal/reconciliation"
)

func TestFinalizedUncheckedIncludesChecksStaleForRecoveringAsset(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	store, _ := NewPostgresStore(db)
	now := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	mock.ExpectQuery(`state='FINALIZED'.*ascp_asset_health`).WithArgs(10).WillReturnRows(
		sqlmock.NewRows([]string{"operation_id", "action", "transaction_hash", "delivery_hash", "evidence_hash", "state",
			"registered_at", "resolved_at", "block_number", "block_hash", "evidence_digest", "canonical_checked_at"}).
			AddRow(testSettlementHash(1), reconciliation.ASCPReceiptLock, testSettlementHash(2), nil, nil, AttemptFinalized,
				now.Add(-time.Hour), now.Add(-time.Hour), 100, testSettlementHash(3), testSettlementHash(4), now.Add(-time.Hour)),
	)
	attempts, err := store.FinalizedUnchecked(context.Background(), 10)
	if err != nil || len(attempts) != 1 || attempts[0].CanonicalCheckedAt.IsZero() {
		t.Fatalf("attempts=%+v err=%v", attempts, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestConfirmCanonicalRefreshesOlderCheck(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	now := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	store, _ := NewPostgresStore(db, func() time.Time { return now })
	result := ReorgResult{OperationID: testSettlementHash(1), Action: reconciliation.ASCPReceiptLock,
		TransactionHash: testSettlementHash(2), BlockNumber: 100, OriginalBlockHash: testSettlementHash(3),
		CanonicalBlockHash: testSettlementHash(3), ObservedHead: 120, Providers: []string{"provider-a", "provider-b"},
		ObservedAt: now, verified: true}
	result.EvidenceDigest = reorgDigest(result)
	result.seal = reorgSeal(result)
	mock.ExpectExec(`UPDATE ascp_payment_attempts SET canonical_checked_at=\$6`).WithArgs(result.OperationID, result.Action,
		result.TransactionHash, result.BlockNumber, result.OriginalBlockHash, now).WillReturnResult(sqlmock.NewResult(0, 1))
	if err := store.ConfirmCanonical(context.Background(), result); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
