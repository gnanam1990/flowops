package ascpverifier

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

const verifierDecisionLockSeed = int64(604221973)

// DecisionJournal serializes one decision per call across retries. Execute
// must return the stored result for an identical fingerprint and reject a
// different fingerprint without invoking compute.
type DecisionJournal interface {
	Execute(context.Context, string, string, string, func(context.Context) (SignedDecision, error)) (SignedDecision, error)
}

type memoryDecisionJournal struct {
	mu     sync.Mutex
	byCall map[string]memoryDecision
}

type memoryDecision struct {
	chainID     string
	fingerprint string
	decision    SignedDecision
}

// NewMemoryDecisionJournal is for tests and one-process demonstrations only.
func NewMemoryDecisionJournal() DecisionJournal {
	return &memoryDecisionJournal{byCall: make(map[string]memoryDecision)}
}

func (j *memoryDecisionJournal) Execute(ctx context.Context, callID, chainID, fingerprint string,
	compute func(context.Context) (SignedDecision, error)) (SignedDecision, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if existing, ok := j.byCall[callID]; ok {
		if existing.chainID != chainID || existing.fingerprint != fingerprint {
			return SignedDecision{}, ErrDecisionConflict
		}
		return cloneDecision(existing.decision), nil
	}
	decision, err := compute(ctx)
	if err != nil {
		return SignedDecision{}, err
	}
	j.byCall[callID] = memoryDecision{chainID: chainID, fingerprint: fingerprint, decision: cloneDecision(decision)}
	return decision, nil
}

// PostgresDecisionJournal provides cross-replica serialization and durable
// replay. The transaction intentionally spans the bounded engine and signer
// call so two replicas cannot publish different signatures for one call.
type PostgresDecisionJournal struct{ db *sql.DB }

func NewPostgresDecisionJournal(db *sql.DB) (*PostgresDecisionJournal, error) {
	if db == nil {
		return nil, ErrInvalidConfiguration
	}
	return &PostgresDecisionJournal{db: db}, nil
}

func (j *PostgresDecisionJournal) Execute(ctx context.Context, callID, chainID, fingerprint string,
	compute func(context.Context) (SignedDecision, error)) (SignedDecision, error) {
	if !canonicalHash(callID, true) || !canonicalHash(fingerprint, true) || chainID == "" || compute == nil {
		return SignedDecision{}, ErrInvalidConfiguration
	}
	// READ COMMITTED is intentional: a waiter takes its row snapshot only after
	// the prior holder commits and releases the advisory lock. SERIALIZABLE
	// would retain a pre-wait snapshot and could miss the winner's inserted row.
	tx, err := j.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return SignedDecision{}, fmt.Errorf("%w: begin verifier decision: %v", ErrStateUnavailable, err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,$2))`, callID, verifierDecisionLockSeed); err != nil {
		return SignedDecision{}, fmt.Errorf("%w: lock verifier call: %v", ErrStateUnavailable, err)
	}
	var storedChain, storedFingerprint string
	var raw []byte
	err = tx.QueryRowContext(ctx, `SELECT chain_id,input_fingerprint,decision_json FROM ascp_verdict_decisions WHERE call_id=$1`, callID).
		Scan(&storedChain, &storedFingerprint, &raw)
	if err == nil {
		if storedChain != chainID || storedFingerprint != fingerprint {
			return SignedDecision{}, ErrDecisionConflict
		}
		decision, decodeErr := decodeStoredDecision(raw, callID, chainID)
		if decodeErr != nil {
			return SignedDecision{}, fmt.Errorf("%w: %v", ErrStateUnavailable, decodeErr)
		}
		if err := tx.Commit(); err != nil {
			return SignedDecision{}, fmt.Errorf("%w: commit verifier replay: %v", ErrStateUnavailable, err)
		}
		return decision, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return SignedDecision{}, fmt.Errorf("%w: read verifier decision: %v", ErrStateUnavailable, err)
	}
	decision, err := compute(ctx)
	if err != nil {
		return SignedDecision{}, err
	}
	if err := validateStoredDecision(decision, callID, chainID); err != nil {
		return SignedDecision{}, err
	}
	raw, err = json.Marshal(decision)
	if err != nil {
		return SignedDecision{}, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO ascp_verdict_decisions
		(call_id,chain_id,input_fingerprint,verdict_nonce,attestation_hash,decision_json,created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7)`, callID, chainID, fingerprint, decision.Attestation.VerdictNonce,
		decision.AttestationHash, raw, time.Unix(int64(decision.VerificationTime), 0).UTC())
	if err != nil {
		return SignedDecision{}, fmt.Errorf("%w: store verifier decision: %v", ErrStateUnavailable, err)
	}
	if err := tx.Commit(); err != nil {
		return SignedDecision{}, fmt.Errorf("%w: commit verifier decision: %v", ErrStateUnavailable, err)
	}
	return decision, nil
}

// PostgresNonceSource allocates globally unique positive nonces. PostgreSQL
// sequence values are intentionally not rolled back, so crashes create gaps
// rather than nonce reuse.
type PostgresNonceSource struct{ db *sql.DB }

func (*PostgresNonceSource) durableNonceSource() {}

func NewPostgresNonceSource(db *sql.DB) (*PostgresNonceSource, error) {
	if db == nil {
		return nil, ErrInvalidConfiguration
	}
	return &PostgresNonceSource{db: db}, nil
}

func (s *PostgresNonceSource) Next(ctx context.Context) (*big.Int, error) {
	var raw string
	if err := s.db.QueryRowContext(ctx, `SELECT nextval('public.ascp_verdict_nonce_seq')::numeric::text`).Scan(&raw); err != nil {
		return nil, fmt.Errorf("%w: allocate verdict nonce: %v", ErrStateUnavailable, err)
	}
	nonce, ok := new(big.Int).SetString(raw, 10)
	if !ok || nonce.Sign() <= 0 || nonce.BitLen() > 256 {
		return nil, fmt.Errorf("%w: invalid verdict nonce state", ErrStateUnavailable)
	}
	return nonce, nil
}

var _ durableNonceSource = (*PostgresNonceSource)(nil)

func decodeStoredDecision(raw []byte, callID, chainID string) (SignedDecision, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var decision SignedDecision
	if err := decoder.Decode(&decision); err != nil {
		return SignedDecision{}, fmt.Errorf("decode stored verifier decision: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return SignedDecision{}, errors.New("stored verifier decision has trailing JSON")
	}
	if err := validateStoredDecision(decision, callID, chainID); err != nil {
		return SignedDecision{}, err
	}
	return decision, nil
}

func validateStoredDecision(decision SignedDecision, callID, chainID string) error {
	if decision.Attestation.CallID != callID || decision.CommitmentHash != decision.Attestation.CommitmentHash ||
		decision.SpecHash != decision.Attestation.VerificationSpecHash || decision.DeliveryHash != decision.Attestation.DeliveryHash ||
		decision.EvidenceHash != decision.Attestation.EvidenceHash || decision.VerificationTime != decision.Attestation.IssuedAt ||
		!canonicalAddress(decision.Signer) || decision.Signature == "" ||
		crypto.Keccak256Hash(common.HexToHash(decision.CommitmentHash).Bytes()).Hex() != callID {
		return errors.New("stored verifier decision bindings are invalid")
	}
	canonicalSpec, err := ParseSpec(decision.CanonicalSpec)
	if err != nil || canonicalSpec.Hash != decision.SpecHash || validateDecisionResult(EngineResult{
		Verdict: decision.Verdict, Code: decision.Code, Notes: decision.Notes,
	}) != nil {
		return errors.New("stored verifier decision metadata is invalid")
	}
	wantContractVerdict := ContractVerdictRelease
	if decision.Outcome == OutcomeRefund {
		wantContractVerdict = ContractVerdictEarlyRefund
	}
	if decision.Outcome != OutcomeRelease && decision.Outcome != OutcomeRefund || decision.Attestation.Verdict != wantContractVerdict ||
		decision.Verdict == VerdictPass && decision.Outcome != OutcomeRelease || decision.Verdict == VerdictFail && decision.Outcome != OutcomeRefund ||
		decision.Verdict == VerdictPassWithNotes && !identifier(decision.DecisionID) || decision.Verdict != VerdictPassWithNotes && decision.DecisionID != "" {
		return errors.New("stored verifier decision outcome is invalid")
	}
	digest, err := decision.Attestation.Digest(chainID)
	if err != nil || digest.Hex() != decision.AttestationHash {
		return errors.New("stored verifier decision digest is invalid")
	}
	signature := common.FromHex(decision.Signature)
	normalized, signer, err := normalizeAndRecover(signature, digest)
	if err != nil || !bytes.Equal(signature, normalized) || signer.Hex() != common.HexToAddress(decision.Signer).Hex() {
		return errors.New("stored verifier decision signature is invalid")
	}
	return nil
}

func validateDecisionResult(result EngineResult) error {
	if validateEngineResult(result) == nil {
		return nil
	}
	if result.Verdict != VerdictFail || len(result.Notes) != 0 {
		return ErrInvalidEngineResult
	}
	switch result.Code {
	case "LATE_DELIVERY", "FORMAT_CONTENT_DIGEST", "FORMAT_HTTP_STATUS", "FORMAT_NON_EMPTY",
		"FORMAT_CONTENT_TYPE", "FORMAT_MINIMUM_BYTES", "FORMAT_MAXIMUM_BYTES", "FORMAT_SHA256":
		return nil
	default:
		return ErrInvalidEngineResult
	}
}

func cloneDecision(input SignedDecision) SignedDecision {
	copy := input
	copy.Notes = append([]string(nil), input.Notes...)
	copy.CanonicalSpec = append([]byte(nil), input.CanonicalSpec...)
	return copy
}
