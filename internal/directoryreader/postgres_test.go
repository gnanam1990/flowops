package directoryreader

import (
	"context"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestPostgresStoreRecordsSealedFinalizedObservationAndAdvancesHead(t *testing.T) {
	now := time.Unix(1800000000, 0).UTC()
	result := readerResultAt(t, now)
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, _ := NewPostgresStore(db, func() time.Time { return now })
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO ascp_directory_snapshots")).
		WithArgs(result.ObservationDigest, result.ChainID, result.DirectoryContract, result.DirectoryVersion,
			result.DirectoryRoot, result.FinalizedBlockNumber, result.FinalizedBlockHash, sqlmock.AnyArg(), result.ObservedAt).
		WillReturnResult(sqlmock.NewResult(1, 1))
	evidence := result.Evidence
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO ascp_directory_quote_evidence")).
		WithArgs(result.ObservationDigest, evidence.SellerID, evidence.ResourceID, evidence.QuoteSigningKey,
			evidence.KeyEpoch, evidence.PayoutAddress, evidence.AckAuthority, evidence.AmountBaseUnits,
			evidence.VerificationSpecHash, evidence.DeclaredWorkTime, evidence.VerificationBudgetSeconds,
			evidence.Active, evidence.QuoteKeyRevoked).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT observation_digest, finalized_block_number")).
		WithArgs(result.ChainID, result.DirectoryContract).
		WillReturnRows(sqlmock.NewRows([]string{"observation_digest", "finalized_block_number"}))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO ascp_directory_heads")).
		WithArgs(result.ChainID, result.DirectoryContract, result.ObservationDigest, result.DirectoryVersion,
			result.FinalizedBlockNumber, now).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	if err := store.Record(context.Background(), result); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresStoreRejectsHeldQuorumResultInsteadOfRefreshingItAtWriteTime(t *testing.T) {
	observedAt := time.Unix(1800000000, 0).UTC()
	result := readerResultAt(t, observedAt)
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, _ := NewPostgresStore(db, func() time.Time { return observedAt.Add(maximumRecordDelay + time.Second) })
	if err := store.Record(context.Background(), result); !errors.Is(err, ErrObservationExpired) {
		t.Fatalf("error=%v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresStoreRejectsMutationAfterReaderVerification(t *testing.T) {
	result := readerResult(t)
	result.Evidence.QuoteKeyRevoked = true
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, _ := NewPostgresStore(db)
	if err := store.Record(context.Background(), result); !errors.Is(err, ErrInvalidObservation) {
		t.Fatalf("error=%v", err)
	}
}

func TestPostgresStoreDoesNotClassifyTransientWriteAsChainConflict(t *testing.T) {
	result := readerResult(t)
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, _ := NewPostgresStore(db)
	transient := errors.New("database unavailable")
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO ascp_directory_snapshots")).WillReturnError(transient)
	mock.ExpectRollback()
	err = store.Record(context.Background(), result)
	if !errors.Is(err, transient) || errors.Is(err, ErrObservationConflict) {
		t.Fatalf("error=%v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func readerResult(t *testing.T) Result {
	return readerResultAt(t, time.Now().UTC())
}

func readerResultAt(t *testing.T, observedAt time.Time) Result {
	t.Helper()
	observation, quote := fixture(t)
	reader := newReaderAt(t, observedAt, source{name: "alpha", observation: observation}, source{name: "bravo", observation: observation})
	result, err := reader.EvidenceForQuote(context.Background(), quote)
	if err != nil {
		t.Fatal(err)
	}
	return result
}
