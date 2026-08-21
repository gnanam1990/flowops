package ascpverifier

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math/big"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/ethereum/go-ethereum/crypto"
)

func TestPostgresDecisionJournalPersistsAndReplaysWithoutComputingAgain(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	service, _ := newTestService(t, now, &testEngine{result: EngineResult{Verdict: VerdictPass, Code: "pass"}}, nil)
	decision, err := service.VerifyAndSign(t.Context(), testInput(t, now))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(decision)
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	journal, _ := NewPostgresDecisionJournal(db)
	fingerprint := testHash(90)

	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("SELECT pg_advisory_xact_lock(hashtextextended($1,$2))")).
		WithArgs(decision.Attestation.CallID, verifierDecisionLockSeed).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT chain_id,input_fingerprint,decision_json FROM ascp_verdict_decisions WHERE call_id=$1")).
		WithArgs(decision.Attestation.CallID).WillReturnError(sql.ErrNoRows)
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO ascp_verdict_decisions")).
		WithArgs(decision.Attestation.CallID, "8453", fingerprint, decision.Attestation.VerdictNonce,
			decision.AttestationHash, sqlmock.AnyArg(), time.Unix(int64(decision.VerificationTime), 0).UTC()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	var calls atomic.Int32
	computed, err := journal.Execute(t.Context(), decision.Attestation.CallID, "8453", fingerprint, func(context.Context) (SignedDecision, error) {
		calls.Add(1)
		return decision, nil
	})
	if err != nil || computed.Signature != decision.Signature || calls.Load() != 1 {
		t.Fatalf("computed=%+v calls=%d err=%v", computed, calls.Load(), err)
	}

	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("SELECT pg_advisory_xact_lock(hashtextextended($1,$2))")).
		WithArgs(decision.Attestation.CallID, verifierDecisionLockSeed).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT chain_id,input_fingerprint,decision_json FROM ascp_verdict_decisions WHERE call_id=$1")).
		WithArgs(decision.Attestation.CallID).WillReturnRows(sqlmock.NewRows([]string{"chain_id", "input_fingerprint", "decision_json"}).
		AddRow("8453", fingerprint, raw))
	mock.ExpectCommit()
	replayed, err := journal.Execute(t.Context(), decision.Attestation.CallID, "8453", fingerprint, func(context.Context) (SignedDecision, error) {
		calls.Add(1)
		return SignedDecision{}, errors.New("must not run")
	})
	if err != nil || replayed.Signature != decision.Signature || calls.Load() != 1 {
		t.Fatalf("replayed=%+v calls=%d err=%v", replayed, calls.Load(), err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestStoredDecisionRejectsTamperingAndPostgresNonceIsPositive(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	service, _ := newTestService(t, now, &testEngine{result: EngineResult{Verdict: VerdictPass, Code: "pass"}}, nil)
	decision, _ := service.VerifyAndSign(t.Context(), testInput(t, now))
	decision.Signature = decision.Signature[:len(decision.Signature)-2] + "00"
	raw, _ := json.Marshal(decision)
	if _, err := decodeStoredDecision(raw, decision.Attestation.CallID, "8453"); err == nil {
		t.Fatal("tampered stored signature accepted")
	}
	service, _ = newTestService(t, now, &testEngine{result: EngineResult{Verdict: VerdictPass, Code: "pass"}}, nil)
	decision, _ = service.VerifyAndSign(t.Context(), testInput(t, now))
	decision.Outcome = OutcomeRefund
	raw, _ = json.Marshal(decision)
	if _, err := decodeStoredDecision(raw, decision.Attestation.CallID, "8453"); err == nil {
		t.Fatal("tampered unsigned outcome metadata accepted")
	}

	db, mock, _ := sqlmock.New()
	t.Cleanup(func() { _ = db.Close() })
	mock.ExpectQuery(regexp.QuoteMeta("SELECT nextval('public.ascp_verdict_nonce_seq')::numeric::text")).
		WillReturnRows(sqlmock.NewRows([]string{"nextval"}).AddRow("42"))
	source, _ := NewPostgresNonceSource(db)
	nonce, err := source.Next(t.Context())
	if err != nil || nonce.Cmp(big.NewInt(42)) != 0 {
		t.Fatalf("nonce=%v err=%v", nonce, err)
	}
}

func TestPostgresVerifierKeyGateRequiresFreshFinalizedActiveObservation(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	db, mock, _ := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	t.Cleanup(func() { _ = db.Close() })
	gate, _ := NewPostgresVerifierKeyGate(db, time.Minute, func() time.Time { return now })
	key, _ := crypto.HexToECDSA(strings.Repeat("11", 32))
	address := crypto.PubkeyToAddress(key.PublicKey)
	query := regexp.QuoteMeta("SELECT active,observed_at,evidence_digest")
	mock.ExpectQuery(query).WithArgs("8453", "0x1111111111111111111111111111111111111111", strings.ToLower(address.Hex()), uint64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"active", "observed_at", "evidence_digest"}).AddRow(true, now.Add(-time.Second), testHash(8)))
	if err := gate.CheckActive(t.Context(), "8453", "0x1111111111111111111111111111111111111111", address, 7); err != nil {
		t.Fatal(err)
	}
	mock.ExpectQuery(query).WithArgs("8453", "0x1111111111111111111111111111111111111111", strings.ToLower(address.Hex()), uint64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"active", "observed_at", "evidence_digest"}).AddRow(true, now.Add(-2*time.Minute), testHash(8)))
	if err := gate.CheckActive(t.Context(), "8453", "0x1111111111111111111111111111111111111111", address, 7); !errors.Is(err, ErrVerifierInactive) {
		t.Fatalf("stale observation error=%v", err)
	}
	mock.ExpectQuery(query).WithArgs("8453", "0x1111111111111111111111111111111111111111", strings.ToLower(address.Hex()), uint64(7)).
		WillReturnError(errors.New("database unavailable"))
	if err := gate.CheckActive(t.Context(), "8453", "0x1111111111111111111111111111111111111111", address, 7); !errors.Is(err, ErrStateUnavailable) {
		t.Fatalf("database error=%v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresJournalAndNonceClassifyDatabaseFailuresAsStateUnavailable(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	mock.ExpectBegin().WillReturnError(errors.New("database unavailable"))
	journal, err := NewPostgresDecisionJournal(db)
	if err != nil {
		t.Fatal(err)
	}
	_, err = journal.Execute(t.Context(), testHash(1), "8453", testHash(2), func(context.Context) (SignedDecision, error) {
		t.Fatal("compute must not run when durable state is unavailable")
		return SignedDecision{}, nil
	})
	if !errors.Is(err, ErrStateUnavailable) {
		t.Fatalf("journal error=%v", err)
	}

	mock.ExpectQuery(regexp.QuoteMeta("SELECT nextval('public.ascp_verdict_nonce_seq')::numeric::text")).
		WillReturnError(errors.New("database unavailable"))
	source, err := NewPostgresNonceSource(db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.Next(t.Context()); !errors.Is(err, ErrStateUnavailable) {
		t.Fatalf("nonce error=%v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
