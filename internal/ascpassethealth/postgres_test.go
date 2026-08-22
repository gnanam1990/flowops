package ascpassethealth

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestPostgresTransitionAtomicallyHaltsAndClassifiesOpenFunds(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	store, _ := NewPostgresStore(db)
	config := testConfig()
	now := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	decision, _ := Evaluate(config, pausedObservations(now), now)

	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO ascp_asset_health`).WithArgs(config.ChainID, config.Asset, config.ProxyImplementation, config.RuntimeCodeHash, config.Quorum, now).
		WillReturnResult(sqlmock.NewResult(0, 1))
	expectHealthRecord(mock, config, Normal, 0, nil, now)
	mock.ExpectQuery(`SELECT evidence_digest FROM ascp_asset_health_observations`).WithArgs(decision.EvidenceDigest).
		WillReturnRows(sqlmock.NewRows([]string{"evidence_digest"}))
	mock.ExpectExec(`INSERT INTO ascp_asset_health_observations`).WithArgs(
		decision.EvidenceDigest, config.ChainID, config.Asset, Normal, TokenPaused, TokenPaused, uint64(1), sqlmock.AnyArg(),
		decision.FinalizedBlock, decision.ObservedAt, now).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE ascp_asset_health`).WithArgs(config.ChainID, config.Asset, TokenPaused, uint64(1), decision.EvidenceDigest,
		sqlmock.AnyArg(), decision.FinalizedBlock, decision.ObservedAt, now).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO ascp_asset_reclassifications`).WithArgs(decision.EvidenceDigest, config.ChainID, config.Asset, now).
		WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectCommit()

	record, err := store.Transition(context.Background(), config, decision, now)
	if err != nil || record.State != TokenPaused || record.Epoch != 1 {
		t.Fatalf("record=%+v err=%v", record, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresRecoveryReversesClassificationsBeforeNormal(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	store, _ := NewPostgresStore(db)
	config := testConfig()
	now := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	record := Record{Config: config, State: Recovering, Epoch: 2, EvidenceDigest: hash(7), FinalizedBlock: 100, ObservedAt: now}
	proof := recoveryProofFor(record, now)

	mock.ExpectBegin()
	expectHealthRecord(mock, config, Recovering, 2, []string{"provider-a", "provider-b"}, now)
	expectRecoveryCounts(mock, config, now, 0, 0, 0)
	mock.ExpectExec(`INSERT INTO ascp_asset_recovery_proofs`).WithArgs(proof.EvidenceDigest, config.ChainID, config.Asset,
		proof.HealthEpoch, proof.CleanEvidenceDigest, proof.CleanFinalizedBlock, proof.ReconciledAt, now).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO ascp_asset_reclassifications`).WithArgs(proof.EvidenceDigest, config.ChainID, config.Asset, now).
		WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectExec(`UPDATE ascp_asset_health SET state='NORMAL'`).WithArgs(config.ChainID, config.Asset, proof.EvidenceDigest, now).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	actual, err := store.CompleteRecovery(context.Background(), config, proof, now)
	if err != nil || actual.State != Normal || actual.Epoch != 3 {
		t.Fatalf("record=%+v err=%v", actual, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresTransitionFreezesFirstCleanRecoveryAnchor(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	store, _ := NewPostgresStore(db)
	config := testConfig()
	anchorAt := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	now := anchorAt.Add(30 * time.Second)
	decision, _ := Evaluate(config, healthyObservations(now), now)

	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO ascp_asset_health`).WithArgs(config.ChainID, config.Asset, config.ProxyImplementation, config.RuntimeCodeHash, config.Quorum, now).
		WillReturnResult(sqlmock.NewResult(0, 0))
	expectHealthRecord(mock, config, Recovering, 2, []string{"provider-a", "provider-b"}, anchorAt)
	mock.ExpectQuery(`SELECT evidence_digest FROM ascp_asset_health_observations`).WithArgs(decision.EvidenceDigest).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectExec(`INSERT INTO ascp_asset_health_observations`).WithArgs(decision.EvidenceDigest, config.ChainID, config.Asset,
		Recovering, Normal, Recovering, uint64(2), sqlmock.AnyArg(), decision.FinalizedBlock, decision.ObservedAt, now).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	record, err := store.Transition(context.Background(), config, decision, now)
	if err != nil || record.EvidenceDigest != hash(7) || !record.ObservedAt.Equal(anchorAt) {
		t.Fatalf("record=%+v err=%v", record, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresRecoveryRechecksReadinessInsideCompletionTransaction(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	store, _ := NewPostgresStore(db)
	config := testConfig()
	now := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	record := Record{Config: config, State: Recovering, Epoch: 2, EvidenceDigest: hash(7), FinalizedBlock: 100, ObservedAt: now}
	proof := recoveryProofFor(record, now)

	mock.ExpectBegin()
	expectHealthRecord(mock, config, Recovering, 2, []string{"provider-a", "provider-b"}, now)
	expectRecoveryCounts(mock, config, now, 0, 1, 0)
	mock.ExpectRollback()

	if _, err := store.CompleteRecovery(context.Background(), config, proof, now); !errors.Is(err, ErrRecoveryIncomplete) {
		t.Fatalf("changed readiness err=%v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func expectHealthRecord(mock sqlmock.Sqlmock, config Config, state State, epoch uint64, providers []string, now time.Time) {
	var digest any
	var providerJSON any
	var block any
	var observed any
	if providers != nil {
		digest, providerJSON, block, observed = hash(7), []byte(`["provider-a","provider-b"]`), uint64(100), now
	}
	mock.ExpectQuery(`SELECT chain_id,asset,proxy_implementation,runtime_code_hash,quorum,state,epoch`).WithArgs(config.ChainID, config.Asset).
		WillReturnRows(sqlmock.NewRows([]string{"chain_id", "asset", "proxy_implementation", "runtime_code_hash", "quorum", "state", "epoch", "evidence_digest", "providers", "finalized_block", "observed_at", "updated_at"}).
			AddRow(config.ChainID, config.Asset, config.ProxyImplementation, config.RuntimeCodeHash, config.Quorum, state, epoch, digest, providerJSON, block, observed, now))
}

func expectRecoveryCounts(mock sqlmock.Sqlmock, config Config, observedAt time.Time, pending, stale, unclassified int64) {
	mock.ExpectQuery(`SELECT count\(\*\).*ascp_payment_operations o`).WithArgs(config.ChainID, config.Asset).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(pending))
	mock.ExpectQuery(`SELECT count\(\*\).*ascp_payment_attempts a`).WithArgs(config.ChainID, config.Asset, observedAt).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(stale))
	mock.ExpectQuery(`SELECT count\(\*\).*ascp_payment_operations o`).WithArgs(config.ChainID, config.Asset).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(unclassified))
}

func pausedObservations(now time.Time) []Observation {
	values := healthyObservations(now)
	for index := range values {
		values[index].Paused = true
	}
	return values
}
