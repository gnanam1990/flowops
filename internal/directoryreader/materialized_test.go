package directoryreader

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gnanam1990/flowops/pkg/sellerquote"
)

func TestMaterializedResolverReturnsOnlyFreshCurrentHeadEvidence(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	db, mock := materializedMock(t)
	resolver, err := NewMaterializedResolver(db, 84532, materializedDirectory, time.Minute, 15*time.Second, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	quote := materializedQuote()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT h.directory_version, s.observed_at")).
		WithArgs(uint64(84532), materializedDirectory, quote.SellerID, quote.ResourceID).
		WillReturnRows(materializedHeadRows(uint64(9), now.Add(10*time.Second), quote))
	contract, evidence, err := resolver.EvidenceForQuote(context.Background(), quote)
	if err != nil || contract != materializedDirectory || !evidence.Verified || evidence.Version != quote.DirectoryVersion || evidence.QuoteSigningKey != materializedSigner {
		t.Fatalf("contract=%s evidence=%+v err=%v", contract, evidence, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestMaterializedResolverFailsClosedOnMissingStaleOrDifferentVersion(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	for _, test := range []struct {
		name    string
		rows    *sqlmock.Rows
		rowErr  error
		wantErr error
	}{
		{name: "missing", rows: sqlmock.NewRows(materializedColumns()), wantErr: ErrCurrentSnapshotUnavailable},
		{name: "stale", rows: materializedHeadRows(uint64(9), now.Add(-61*time.Second), materializedQuote()), wantErr: ErrCurrentSnapshotStale},
		{name: "future", rows: materializedHeadRows(uint64(9), now.Add(16*time.Second), materializedQuote()), wantErr: ErrCurrentSnapshotStale},
		{name: "version", rows: materializedHeadRows(uint64(10), now, materializedQuote()), wantErr: ErrCurrentVersionMismatch},
		{name: "database", rowErr: errors.New("connection lost")},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, mock := materializedMock(t)
			resolver, _ := NewMaterializedResolver(db, 84532, materializedDirectory, time.Minute, 15*time.Second, func() time.Time { return now })
			quote := materializedQuote()
			expectation := mock.ExpectQuery(regexp.QuoteMeta("SELECT h.directory_version, s.observed_at")).WithArgs(uint64(84532), materializedDirectory, quote.SellerID, quote.ResourceID)
			if test.rowErr != nil {
				expectation.WillReturnError(test.rowErr)
			} else {
				expectation.WillReturnRows(test.rows)
			}
			_, _, err := resolver.EvidenceForQuote(context.Background(), materializedQuote())
			if test.wantErr != nil && !errors.Is(err, test.wantErr) {
				t.Fatalf("error=%v want=%v", err, test.wantErr)
			}
			if test.rowErr != nil && (err == nil || errors.Is(err, ErrCurrentSnapshotUnavailable)) {
				t.Fatalf("database failure misclassified: %v", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestMaterializedResolverDoesNotInventEvidenceForUnknownQuote(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	db, mock := materializedMock(t)
	resolver, _ := NewMaterializedResolver(db, 84532, materializedDirectory, time.Minute, 15*time.Second, func() time.Time { return now })
	quote := materializedQuote()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT h.directory_version, s.observed_at")).
		WithArgs(uint64(84532), materializedDirectory, quote.SellerID, quote.ResourceID).
		WillReturnRows(sqlmock.NewRows(materializedColumns()).AddRow(uint64(9), now, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil))
	_, _, err := resolver.EvidenceForQuote(context.Background(), quote)
	if !errors.Is(err, ErrQuoteEvidenceUnavailable) {
		t.Fatalf("error=%v", err)
	}
}

func materializedMock(t *testing.T) (*sql.DB, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, mock
}

const (
	materializedDirectory = "0x1111111111111111111111111111111111111111"
	materializedSigner    = "0x4444444444444444444444444444444444444444"
)

func materializedQuote() sellerquote.Quote {
	return sellerquote.Quote{
		PurchaseSpecHash: materializedHash(1), SellerID: materializedHash(2), ResourceID: materializedHash(3),
		DirectoryVersion: 9, SchemeVersion: 1, ChainID: "84532", Asset: "0x036cbd53842c5426634e7929541ec2318f3dcf7e",
		AmountBaseUnits: "42", PayTo: "0x2222222222222222222222222222222222222222", AckAuthority: "0x3333333333333333333333333333333333333333",
		VerificationSpecHash: materializedHash(4), DeclaredWorkTime: 30, VerificationBudgetSeconds: 20,
		QuoteExpiresAt: 1_900_000_000, QuoteNonce: materializedHash(5),
	}
}

func materializedColumns() []string {
	return []string{"directory_version", "observed_at", "seller_id", "resource_id", "quote_signing_key", "key_epoch", "payout_address", "ack_authority", "amount_base_units", "verification_spec_hash", "declared_work_time", "verification_budget_seconds", "active", "quote_key_revoked"}
}

func materializedHeadRows(version uint64, observedAt time.Time, quote sellerquote.Quote) *sqlmock.Rows {
	return sqlmock.NewRows(materializedColumns()).AddRow(
		version, observedAt, quote.SellerID, quote.ResourceID, materializedSigner, uint64(3), quote.PayTo,
		quote.AckAuthority, quote.AmountBaseUnits, quote.VerificationSpecHash, quote.DeclaredWorkTime,
		quote.VerificationBudgetSeconds, true, false,
	)
}

func materializedHash(value uint64) string { return fmt.Sprintf("0x%064x", value) }
