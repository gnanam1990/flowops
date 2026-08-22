package ascpassethealth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestPostgresRecoveryVerifierDerivesZeroCountProof(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	now := time.Date(2026, 8, 22, 12, 1, 0, 0, time.UTC)
	record := recoveringRecord(now.Add(-time.Minute))
	verifier, _ := NewPostgresRecoveryVerifier(db, func() time.Time { return now })

	mock.ExpectBegin()
	expectRecoveryCounts(mock, record.Config, record.ObservedAt, 0, 0, 0)
	mock.ExpectCommit()

	proof, err := verifier.VerifyRecovery(context.Background(), record)
	if err != nil || proof.EvidenceDigest != recoveryEvidenceDigest(proof, recoveryCounts{}) || proof.HealthEpoch != record.Epoch {
		t.Fatalf("proof=%+v err=%v", proof, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresRecoveryVerifierRejectsPendingAndStaleState(t *testing.T) {
	for name, counts := range map[string]recoveryCounts{
		"pending operation": {PendingOperations: 1},
		"stale canonical":   {StaleCanonicalAttempts: 1},
		"unclassified lock": {UnclassifiedLocks: 1},
	} {
		t.Run(name, func(t *testing.T) {
			db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = db.Close() }()
			now := time.Date(2026, 8, 22, 12, 1, 0, 0, time.UTC)
			record := recoveringRecord(now.Add(-time.Minute))
			verifier, _ := NewPostgresRecoveryVerifier(db, func() time.Time { return now })
			mock.ExpectBegin()
			expectRecoveryCounts(mock, record.Config, record.ObservedAt, counts.PendingOperations, counts.StaleCanonicalAttempts, counts.UnclassifiedLocks)
			mock.ExpectRollback()
			if _, err := verifier.VerifyRecovery(context.Background(), record); !errors.Is(err, ErrRecoveryIncomplete) {
				t.Fatalf("err=%v", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRecoveryProofCannotBeReplayedAcrossHealthEpochs(t *testing.T) {
	now := time.Date(2026, 8, 22, 12, 1, 0, 0, time.UTC)
	record := recoveringRecord(now.Add(-time.Minute))
	proof := recoveryProofFor(record, now)
	record.Epoch++
	if err := validateRecoveryProof(record, proof, now); !errors.Is(err, ErrRecoveryIncomplete) {
		t.Fatalf("epoch-substituted proof err=%v", err)
	}
}

func recoveringRecord(observedAt time.Time) Record {
	config := testConfig()
	return Record{Config: config, State: Recovering, Epoch: 2, EvidenceDigest: hash(7), FinalizedBlock: 100,
		ObservedAt: observedAt, UpdatedAt: observedAt}
}
