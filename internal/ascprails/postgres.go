package ascprails

import (
	"context"
	cryptorand "crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

type PostgresStore struct {
	db    *sql.DB
	clock func() time.Time
}

// PostgresOperationGate reads the reconciliation-owned payment operation. It
// has no update path and cannot make a lock finalized by itself.
type PostgresOperationGate struct{ db *sql.DB }

func NewPostgresOperationGate(db *sql.DB) (*PostgresOperationGate, error) {
	if db == nil {
		return nil, ErrInvalidConfig
	}
	return &PostgresOperationGate{db: db}, nil
}

func (g *PostgresOperationGate) Check(ctx context.Context, job Job) error {
	var organizationID, callID, commitmentHash, escrowContract, asset, payTo, amount, state, lockHash, payer string
	var chainID uint64
	var settleBy time.Time
	err := g.db.QueryRowContext(ctx, `SELECT organization_id,call_id,commitment_hash,escrow_contract,asset,pay_to,
		amount_base_units,state,chain_id,settle_by,locked_transaction_hash,buyer FROM ascp_payment_operations WHERE operation_id=$1`, job.OperationID).
		Scan(&organizationID, &callID, &commitmentHash, &escrowContract, &asset, &payTo, &amount, &state, &chainID, &settleBy, &lockHash, &payer)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrOperationNotExecutable
		}
		return err
	}
	if state != "LOCKED_FINALIZED" || organizationID != job.OrganizationID || callID != job.JobID || chainID != job.ChainID ||
		commitmentHash != job.Binding.CommitmentHash || escrowContract != job.Binding.EscrowContract || asset != job.Offer.Accepted.Asset ||
		payTo != job.Offer.Accepted.PayTo || amount != job.Offer.Accepted.Amount || lockHash != job.LockedTransactionHash || payer != job.Payer || settleBy.Unix() <= int64(job.DeliverBy) {
		return ErrOperationNotExecutable
	}
	return nil
}

func NewPostgresStore(db *sql.DB, clocks ...func() time.Time) (*PostgresStore, error) {
	if db == nil || len(clocks) > 1 || len(clocks) == 1 && clocks[0] == nil {
		return nil, ErrInvalidConfig
	}
	clock := time.Now
	if len(clocks) == 1 {
		clock = clocks[0]
	}
	return &PostgresStore{db: db, clock: clock}, nil
}

func (s *PostgresStore) Enqueue(ctx context.Context, input EnqueueInput) (Job, bool, error) {
	fingerprint, _, _, _, _, err := encodeInput(input)
	if err != nil {
		return Job{}, false, err
	}
	for retry := 0; retry < maxAttempts; retry++ {
		job, replay, enqueueErr := s.enqueueOnce(ctx, input)
		if enqueueErr == nil {
			return job, replay, nil
		}
		if postgresErrorCode(enqueueErr) == "23505" {
			var stored string
			if readErr := s.db.QueryRowContext(ctx, `SELECT input_hash FROM ascp_seller_jobs WHERE job_id=$1`, input.JobID).Scan(&stored); readErr == nil {
				if stored != fingerprint {
					return Job{}, false, ErrStateConflict
				}
				existing, readErr := s.Get(ctx, input.JobID)
				return existing, true, readErr
			}
			return Job{}, false, ErrStateConflict
		}
		if postgresErrorCode(enqueueErr) != "40001" || ctx.Err() != nil {
			return Job{}, false, classifyPostgres(enqueueErr)
		}
	}
	return Job{}, false, errors.New("seller egress enqueue serialization retries exhausted")
}

func (s *PostgresStore) enqueueOnce(ctx context.Context, input EnqueueInput) (Job, bool, error) {
	now := s.clock().UTC()
	fingerprint, headersJSON, offerJSON, paymentJSON, bindingJSON, err := encodeInput(input)
	if err != nil {
		return Job{}, false, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return Job{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	var existingHash string
	err = tx.QueryRowContext(ctx, `SELECT input_hash FROM ascp_seller_jobs WHERE job_id=$1`, input.JobID).Scan(&existingHash)
	if err == nil {
		if existingHash != fingerprint {
			return Job{}, false, ErrStateConflict
		}
		job, loadErr := loadSellerJob(ctx, tx, input.JobID)
		if loadErr != nil {
			return Job{}, false, loadErr
		}
		if err := tx.Commit(); err != nil {
			return Job{}, false, err
		}
		return job, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Job{}, false, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO ascp_seller_jobs
		(job_id,operation_id,organization_id,chain_id,leadership_epoch,deliver_by,method,request_url,headers_json,
		 request_body,canonical_spec_json,offer_json,payment_json,binding_json,locked_transaction_hash,payer,validated_chain_time,input_hash,
		 eligible_after,created_at,updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$19,$19)`,
		input.JobID, input.OperationID, input.OrganizationID, input.ChainID, input.LeadershipEpoch, input.DeliverBy,
		input.Method, input.URL, headersJSON, input.Body, input.CanonicalSpecJSON, offerJSON, paymentJSON, bindingJSON,
		input.LockedTransactionHash, input.Payer, input.ValidatedChainTime, fingerprint, now)
	if err != nil {
		return Job{}, false, err
	}
	job, err := loadSellerJob(ctx, tx, input.JobID)
	if err != nil {
		return Job{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return Job{}, false, err
	}
	return job, false, nil
}

func (s *PostgresStore) ClaimDispatch(ctx context.Context, worker string, duration time.Duration) (Lease, error) {
	return s.claim(ctx, worker, duration, false)
}

func (s *PostgresStore) ClaimFinalization(ctx context.Context, worker string, duration time.Duration) (Lease, error) {
	return s.claim(ctx, worker, duration, true)
}

func (s *PostgresStore) claim(ctx context.Context, worker string, duration time.Duration, finalization bool) (Lease, error) {
	if !identifierPattern.MatchString(worker) || duration < time.Second || duration > time.Minute {
		return Lease{}, ErrInvalidConfig
	}
	now := s.clock().UTC()
	token, err := randomLeaseToken()
	if err != nil {
		return Lease{}, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return Lease{}, err
	}
	defer func() { _ = tx.Rollback() }()
	states := "state='RESPONSE_STORED'"
	if !finalization {
		states = "state IN ('QUEUED','RETRY_WAIT','SENDING') AND eligible_after <= $1"
	}
	query := `SELECT job_id FROM ascp_seller_jobs WHERE ` + states + `
		AND (lease_expires_at IS NULL OR lease_expires_at <= $1)
		ORDER BY eligible_after,created_at,job_id FOR UPDATE SKIP LOCKED LIMIT 1`
	var jobID string
	err = tx.QueryRowContext(ctx, query, now).Scan(&jobID)
	if errors.Is(err, sql.ErrNoRows) {
		return Lease{}, ErrNoWork
	}
	if err != nil {
		return Lease{}, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE ascp_seller_jobs SET lease_owner=$2,lease_token=$3,lease_expires_at=$4,updated_at=$5 WHERE job_id=$1`, jobID, worker, token, now.Add(duration), now)
	if err != nil || rowsAffected(result) != 1 {
		return Lease{}, errors.Join(err, ErrLeaseLost)
	}
	job, err := loadSellerJob(ctx, tx, jobID)
	if err != nil {
		return Lease{}, err
	}
	if err := tx.Commit(); err != nil {
		return Lease{}, err
	}
	return Lease{Job: job, Token: token}, nil
}

func (s *PostgresStore) MarkSending(ctx context.Context, lease Lease, observation ChainObservation) (Job, error) {
	if err := validateObservation(observation); err != nil {
		return Job{}, err
	}
	now := s.clock().UTC()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return Job{}, err
	}
	defer func() { _ = tx.Rollback() }()
	job, err := loadLeasedSellerJob(ctx, tx, lease, now)
	if err != nil {
		return Job{}, err
	}
	if job.State != StateQueued && job.State != StateRetryWait && job.State != StateSending || job.AttemptCount >= maxAttempts {
		return Job{}, ErrStateConflict
	}
	if job.State == StateSending {
		result, updateErr := tx.ExecContext(ctx, `UPDATE ascp_seller_attempts SET state='AMBIGUOUS',completed_at=$3,result_code='LEASE_EXPIRED_RESPONSE_UNKNOWN'
			WHERE job_id=$1 AND attempt_number=$2 AND state='STARTED'`, job.JobID, job.AttemptCount, now)
		if updateErr != nil || rowsAffected(result) != 1 {
			return Job{}, errors.Join(updateErr, ErrStateConflict)
		}
	}
	attempt := job.AttemptCount + 1
	_, err = tx.ExecContext(ctx, `INSERT INTO ascp_seller_attempts
		(job_id,attempt_number,state,chain_time_before_send,chain_evidence_digest,chain_observed_at,started_at)
		VALUES ($1,$2,'STARTED',$3,$4,$5,$6)`, job.JobID, attempt, observation.Timestamp, observation.EvidenceDigest, observation.ObservedAt, now)
	if err != nil {
		return Job{}, classifyPostgres(err)
	}
	result, err := tx.ExecContext(ctx, `UPDATE ascp_seller_jobs SET state='SENDING',attempt_count=$2,last_error=NULL,updated_at=$3
		WHERE job_id=$1`, job.JobID, attempt, now)
	if err != nil || rowsAffected(result) != 1 {
		return Job{}, errors.Join(err, ErrStateConflict)
	}
	job, err = loadSellerJob(ctx, tx, job.JobID)
	if err != nil {
		return Job{}, err
	}
	if err := tx.Commit(); err != nil {
		return Job{}, err
	}
	return job, nil
}

func (s *PostgresStore) RecordResponse(ctx context.Context, lease Lease, response StoredResponse, state State, code string, eligible time.Time) (Job, error) {
	if response.Attempt < 1 || response.Status < 100 || response.Status > 599 || !nonZeroHash(response.Digest) ||
		len(response.Body) > MaxResponseBytes || response.ReceivedAt.IsZero() || len(code) == 0 || len(code) > 256 ||
		(state != StateRetryWait && state != StateResponseStored && state != StateMissing && state != StateDeadLetter) {
		return Job{}, ErrInvalidJob
	}
	now := s.clock().UTC()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return Job{}, err
	}
	defer func() { _ = tx.Rollback() }()
	job, err := loadLeasedSellerJob(ctx, tx, lease, now)
	if err != nil {
		return Job{}, err
	}
	if job.State != StateSending || job.AttemptCount != response.Attempt {
		return Job{}, ErrStateConflict
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO ascp_seller_responses
		(job_id,attempt_number,http_status,content_type,content_encoding,payment_response,response_body,content_digest,received_at)
		VALUES ($1,$2,$3,$4,NULLIF($5,''),NULLIF($6,''),$7,$8,$9)`, job.JobID, response.Attempt, response.Status,
		response.ContentType, response.ContentEncoding, response.PaymentResponse, response.Body, response.Digest, response.ReceivedAt)
	if err != nil {
		return Job{}, classifyPostgres(err)
	}
	_, err = tx.ExecContext(ctx, `UPDATE ascp_seller_attempts SET state='HTTP_RESPONSE',completed_at=$3,result_code=$4
		WHERE job_id=$1 AND attempt_number=$2 AND state='STARTED'`, job.JobID, response.Attempt, now, code)
	if err != nil {
		return Job{}, err
	}
	if state != StateRetryWait {
		eligible = time.Time{}
	}
	result, err := tx.ExecContext(ctx, `UPDATE ascp_seller_jobs SET state=$2,eligible_after=COALESCE($3,eligible_after),last_error=$4,updated_at=$5 WHERE job_id=$1`,
		job.JobID, state, nullTimestamp(eligible), code, now)
	if err != nil || rowsAffected(result) != 1 {
		return Job{}, errors.Join(err, ErrStateConflict)
	}
	job, err = loadSellerJob(ctx, tx, job.JobID)
	if err != nil {
		return Job{}, err
	}
	if err := tx.Commit(); err != nil {
		return Job{}, err
	}
	return job, nil
}

func (s *PostgresStore) RecordTransportFailure(ctx context.Context, lease Lease, code string, state State, eligible time.Time) (Job, error) {
	if len(code) == 0 || len(code) > 256 || state != StateRetryWait && state != StateDeadLetter {
		return Job{}, ErrInvalidJob
	}
	now := s.clock().UTC()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return Job{}, err
	}
	defer func() { _ = tx.Rollback() }()
	job, err := loadLeasedSellerJob(ctx, tx, lease, now)
	if err != nil {
		return Job{}, err
	}
	if job.State != StateQueued && job.State != StateRetryWait && job.State != StateSending {
		return Job{}, ErrStateConflict
	}
	if job.State == StateSending {
		result, updateErr := tx.ExecContext(ctx, `UPDATE ascp_seller_attempts SET state='AMBIGUOUS',completed_at=$3,result_code=$4
			WHERE job_id=$1 AND attempt_number=$2 AND state='STARTED'`, job.JobID, job.AttemptCount, now, code)
		if updateErr != nil || rowsAffected(result) != 1 {
			return Job{}, errors.Join(updateErr, ErrStateConflict)
		}
	}
	if state != StateRetryWait {
		eligible = time.Time{}
	}
	result, err := tx.ExecContext(ctx, `UPDATE ascp_seller_jobs SET state=$2,eligible_after=COALESCE($3,eligible_after),last_error=$4,updated_at=$5 WHERE job_id=$1`,
		job.JobID, state, nullTimestamp(eligible), code, now)
	if err != nil || rowsAffected(result) != 1 {
		return Job{}, errors.Join(err, ErrStateConflict)
	}
	job, err = loadSellerJob(ctx, tx, job.JobID)
	if err != nil {
		return Job{}, err
	}
	if err := tx.Commit(); err != nil {
		return Job{}, err
	}
	return job, nil
}

func (s *PostgresStore) MarkDeadlineMissing(ctx context.Context, lease Lease, observation ChainObservation, code string) (Job, error) {
	if validateObservation(observation) != nil || observation.Timestamp < lease.Job.DeliverBy || len(code) == 0 {
		return Job{}, ErrInvalidJob
	}
	now := s.clock().UTC()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return Job{}, err
	}
	defer func() { _ = tx.Rollback() }()
	job, err := loadLeasedSellerJob(ctx, tx, lease, now)
	if err != nil {
		return Job{}, err
	}
	if job.State != StateQueued && job.State != StateRetryWait && job.State != StateSending {
		return Job{}, ErrStateConflict
	}
	if job.State == StateSending {
		result, updateErr := tx.ExecContext(ctx, `UPDATE ascp_seller_attempts SET state='AMBIGUOUS',completed_at=$3,result_code=$4
			WHERE job_id=$1 AND attempt_number=$2 AND state='STARTED'`, job.JobID, job.AttemptCount, now, code)
		if updateErr != nil || rowsAffected(result) != 1 {
			return Job{}, errors.Join(updateErr, ErrStateConflict)
		}
	}
	result, err := tx.ExecContext(ctx, `UPDATE ascp_seller_jobs SET state='MISSING',deadline_evidence_digest=$2,last_error=$3,updated_at=$4 WHERE job_id=$1`,
		job.JobID, observation.EvidenceDigest, code, now)
	if err != nil || rowsAffected(result) != 1 {
		return Job{}, errors.Join(err, ErrStateConflict)
	}
	job, err = loadSellerJob(ctx, tx, job.JobID)
	if err != nil {
		return Job{}, err
	}
	if err := tx.Commit(); err != nil {
		return Job{}, err
	}
	return job, nil
}

func (s *PostgresStore) FinalizeCapture(ctx context.Context, lease Lease, observation ChainObservation) (Job, error) {
	if validateObservation(observation) != nil {
		return Job{}, ErrInvalidJob
	}
	now := s.clock().UTC()
	var sendChainTime uint64
	if err := s.db.QueryRowContext(ctx, `SELECT a.chain_time_before_send FROM ascp_seller_responses r JOIN ascp_seller_attempts a USING (job_id,attempt_number)
		WHERE r.job_id=$1 ORDER BY r.attempt_number DESC LIMIT 1`, lease.Job.JobID).Scan(&sendChainTime); err != nil {
		return Job{}, err
	}
	if observation.Timestamp < sendChainTime {
		return Job{}, ErrInvalidJob
	}
	result, err := s.db.ExecContext(ctx, `UPDATE ascp_seller_jobs SET state='CAPTURED',captured_at=$4,capture_evidence_digest=$5,last_error=NULL,updated_at=$6
		WHERE job_id=$1 AND lease_owner=$2 AND lease_token=$3 AND lease_expires_at>$6 AND state='RESPONSE_STORED'`,
		lease.Job.JobID, lease.Job.LeaseOwner, lease.Token, observation.Timestamp, observation.EvidenceDigest, now)
	if err != nil || rowsAffected(result) != 1 {
		return Job{}, errors.Join(err, ErrLeaseLost)
	}
	return s.Get(ctx, lease.Job.JobID)
}

func (s *PostgresStore) ReleaseLease(ctx context.Context, lease Lease) error {
	if lease.Job.JobID == "" || lease.Token == "" {
		return ErrInvalidJob
	}
	result, err := s.db.ExecContext(ctx, `UPDATE ascp_seller_jobs SET lease_owner=NULL,lease_token=NULL,lease_expires_at=NULL,updated_at=$4
		WHERE job_id=$1 AND lease_owner=$2 AND lease_token=$3`, lease.Job.JobID, lease.Job.LeaseOwner, lease.Token, s.clock().UTC())
	if err != nil {
		return err
	}
	if rowsAffected(result) != 1 {
		return ErrLeaseLost
	}
	return nil
}

func (s *PostgresStore) Get(ctx context.Context, jobID string) (Job, error) {
	return loadSellerJob(ctx, s.db, jobID)
}

type sellerQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func loadSellerJob(ctx context.Context, queryer sellerQueryer, jobID string) (Job, error) {
	var job Job
	var headersJSON, offerJSON, paymentJSON, bindingJSON []byte
	var leaseOwner, leaseToken, lastError, responseType, responseDigest, paymentResponse, captureEvidence sql.NullString
	var leaseExpiry sql.NullTime
	var responseStatus, capturedAt sql.NullInt64
	err := queryer.QueryRowContext(ctx, `SELECT
		j.job_id,j.operation_id,j.organization_id,j.chain_id,j.leadership_epoch,j.deliver_by,j.method,j.request_url,j.headers_json,
		j.request_body,j.canonical_spec_json,j.offer_json,j.payment_json,j.binding_json,j.locked_transaction_hash,j.payer,j.validated_chain_time,j.state,j.attempt_count,
		j.eligible_after,j.lease_owner,j.lease_token,j.lease_expires_at,j.last_error,j.created_at,j.updated_at,
		r.http_status,r.content_type,r.payment_response,r.response_body,r.content_digest,
		j.captured_at,j.capture_evidence_digest
	FROM ascp_seller_jobs j LEFT JOIN LATERAL (
		SELECT * FROM ascp_seller_responses WHERE job_id=j.job_id ORDER BY attempt_number DESC LIMIT 1
	) r ON true WHERE j.job_id=$1`, jobID).Scan(
		&job.JobID, &job.OperationID, &job.OrganizationID, &job.ChainID, &job.LeadershipEpoch, &job.DeliverBy, &job.Method, &job.URL, &headersJSON,
		&job.Body, &job.CanonicalSpecJSON, &offerJSON, &paymentJSON, &bindingJSON, &job.LockedTransactionHash, &job.Payer, &job.ValidatedChainTime, &job.State, &job.AttemptCount,
		&job.EligibleAfter, &leaseOwner, &leaseToken, &leaseExpiry, &lastError, &job.CreatedAt, &job.UpdatedAt,
		&responseStatus, &responseType, &paymentResponse, &job.ResponseBody, &responseDigest, &capturedAt, &captureEvidence)
	if err != nil {
		return Job{}, err
	}
	if err := json.Unmarshal(headersJSON, &job.Headers); err != nil || json.Unmarshal(offerJSON, &job.Offer) != nil ||
		json.Unmarshal(paymentJSON, &job.Payment) != nil || json.Unmarshal(bindingJSON, &job.Binding) != nil {
		return Job{}, ErrInvalidJob
	}
	job.LeaseOwner, job.LeaseToken, job.LastError = leaseOwner.String, leaseToken.String, lastError.String
	if leaseExpiry.Valid {
		job.LeaseExpiresAt = leaseExpiry.Time
	}
	if responseStatus.Valid {
		job.ResponseStatus = int(responseStatus.Int64)
	}
	job.ResponseType, job.PaymentResponse, job.ResponseDigest = responseType.String, paymentResponse.String, responseDigest.String
	job.CaptureEvidence = captureEvidence.String
	if capturedAt.Valid {
		job.CapturedAt = uint64(capturedAt.Int64)
	}
	return job, nil
}

func loadLeasedSellerJob(ctx context.Context, tx *sql.Tx, lease Lease, now time.Time) (Job, error) {
	job, err := loadSellerJob(ctx, tx, lease.Job.JobID)
	if err != nil {
		return Job{}, err
	}
	if job.LeaseOwner != lease.Job.LeaseOwner || job.LeaseToken != lease.Token || !job.LeaseExpiresAt.After(now) {
		return Job{}, ErrLeaseLost
	}
	return job, nil
}

func encodeInput(input EnqueueInput) (string, []byte, []byte, []byte, []byte, error) {
	headers, err := json.Marshal(input.Headers)
	if err != nil {
		return "", nil, nil, nil, nil, ErrInvalidJob
	}
	offer, err := json.Marshal(input.Offer)
	if err != nil {
		return "", nil, nil, nil, nil, ErrInvalidJob
	}
	payment, err := json.Marshal(input.Payment)
	if err != nil {
		return "", nil, nil, nil, nil, ErrInvalidJob
	}
	binding, err := json.Marshal(input.Binding)
	if err != nil {
		return "", nil, nil, nil, nil, ErrInvalidJob
	}
	fingerprintInput := struct {
		Input EnqueueInput `json:"input"`
	}{Input: input}
	encoded, err := json.Marshal(fingerprintInput)
	if err != nil {
		return "", nil, nil, nil, nil, ErrInvalidJob
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), headers, offer, payment, binding, nil
}

func randomLeaseToken() (string, error) {
	value := make([]byte, 32)
	if _, err := cryptorand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

func rowsAffected(result sql.Result) int64 {
	if result == nil {
		return 0
	}
	rows, _ := result.RowsAffected()
	return rows
}

func nullTimestamp(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value
}

func classifyPostgres(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && (pgErr.Code == "23505" || pgErr.Code == "23514" || pgErr.Code == "23503") {
		return fmt.Errorf("%w: %s", ErrStateConflict, pgErr.ConstraintName)
	}
	return err
}

func postgresErrorCode(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

var _ Store = (*PostgresStore)(nil)
var _ OperationGate = (*PostgresOperationGate)(nil)
