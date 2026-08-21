package ascpbearer

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

type runtimeLeaseContextKey struct{}

func withRuntimeLease(ctx context.Context, lease RuntimeLease) context.Context {
	return context.WithValue(ctx, runtimeLeaseContextKey{}, lease)
}

func NewRuntimeActivationStore(db *sql.DB, clocks ...func() time.Time) (*ActivationStore, error) {
	store, err := NewActivationStore(db, clocks...)
	if err != nil {
		return nil, err
	}
	store.leasesRequired = true
	return store, nil
}

func (s *ActivationStore) Claim(ctx context.Context, claim RuntimeClaim) (RuntimeLease, bool, error) {
	return s.claimRuntime(ctx, claim, false)
}

func (s *ActivationStore) ClaimExpired(ctx context.Context, claim RuntimeClaim) (RuntimeLease, bool, error) {
	return s.claimRuntime(ctx, claim, true)
}

func (s *ActivationStore) claimRuntime(ctx context.Context, claim RuntimeClaim, expired bool) (RuntimeLease, bool, error) {
	if !s.leasesRequired || !identifier(claim.WorkerID) || !identifier(claim.SignerKeyID) || claim.KeyEpoch == 0 ||
		!identifier(claim.KeeperID) || claim.LeaseDuration < time.Second || claim.LeaseDuration > time.Minute {
		return RuntimeLease{}, false, ErrActivationInput
	}
	now := s.clock().UTC()
	token, err := runtimeLeaseToken()
	if err != nil {
		return RuntimeLease{}, false, err
	}
	expires := now.Add(claim.LeaseDuration)
	expiryPredicate := "AND state IN ('SIGN_REQUESTED','PREPARED') AND valid_until <= $5"
	if !expired {
		expiryPredicate = "AND ((state='SIGN_REQUESTED' AND valid_until > $5) OR (state='PREPARED' AND valid_after <= $5 AND valid_until > $5) OR state IN ('ACTIVE_PENDING_MIRROR','ACTIVE_MIRRORED'))"
	}
	query := `WITH candidate AS (
		SELECT request_id FROM ascp_sign_requests
		WHERE signer_key_id=$1 AND key_epoch=$2 AND keeper_id=$3
		  AND next_attempt_at <= $5
		  AND (lease_expires_at IS NULL OR lease_expires_at <= $5)
		  ` + expiryPredicate + `
		ORDER BY next_attempt_at, created_at, request_id
		FOR UPDATE SKIP LOCKED LIMIT 1
	)
	UPDATE ascp_sign_requests r
	SET lease_owner=$4, lease_token=$6, lease_expires_at=$7, attempt_count=attempt_count+1, last_error=NULL
	FROM candidate WHERE r.request_id=candidate.request_id
	RETURNING ` + qualifiedActivationColumns("r")
	var request ActivationRequest
	err = scanActivationRequest(s.db.QueryRowContext(ctx, query, claim.SignerKeyID, claim.KeyEpoch, claim.KeeperID,
		claim.WorkerID, now, token, expires), &request)
	if errors.Is(err, sql.ErrNoRows) {
		return RuntimeLease{}, false, nil
	}
	if err != nil {
		return RuntimeLease{}, false, fmt.Errorf("claim bearer activation: %w", err)
	}
	return RuntimeLease{Request: request, WorkerID: claim.WorkerID, Token: token, ExpiresAt: expires}, true, nil
}

func qualifiedActivationColumns(alias string) string {
	columns := strings.Split(strings.TrimSpace(activationColumns), ",")
	for index := range columns {
		columns[index] = alias + "." + strings.TrimSpace(columns[index])
	}
	return strings.Join(columns, ", ")
}

func (s *ActivationStore) CompleteLease(ctx context.Context, lease RuntimeLease) error {
	result, err := s.db.ExecContext(ctx, `UPDATE ascp_sign_requests
		SET lease_owner=NULL, lease_token=NULL, lease_expires_at=NULL, next_attempt_at=$4, last_error=NULL
		WHERE request_id=$1 AND lease_owner=$2 AND lease_token=$3`, lease.Request.RequestID, lease.WorkerID, lease.Token, s.clock().UTC())
	return exactLeaseMutation(result, err, "complete bearer activation lease")
}

func (s *ActivationStore) RetryLease(ctx context.Context, lease RuntimeLease, code string, delay time.Duration) error {
	if !identifier(code) || delay < time.Second || delay > time.Hour {
		return ErrActivationInput
	}
	now := s.clock().UTC()
	result, err := s.db.ExecContext(ctx, `UPDATE ascp_sign_requests
		SET lease_owner=NULL, lease_token=NULL, lease_expires_at=NULL, next_attempt_at=$4, last_error=$5
		WHERE request_id=$1 AND lease_owner=$2 AND lease_token=$3`, lease.Request.RequestID, lease.WorkerID, lease.Token, now.Add(delay), code)
	return exactLeaseMutation(result, err, "retry bearer activation lease")
}

func (s *ActivationStore) ExpireUnactivated(ctx context.Context, lease RuntimeLease, proof UnactivatedProof) (ActivationRequest, error) {
	now := s.clock().UTC()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return ActivationRequest{}, fmt.Errorf("begin unactivated bearer expiry: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	request, err := loadActivationRequestByID(ctx, tx, lease.Request.RequestID, true)
	if err != nil {
		return ActivationRequest{}, err
	}
	if err := s.requireRuntimeLease(ctx, tx, request.RequestID, now); err != nil {
		return ActivationRequest{}, err
	}
	if request.State != SignRequested && request.State != HandlePrepared || now.Before(request.ValidUntil) {
		return ActivationRequest{}, ErrActivationState
	}
	if err := validateUnactivatedProof(request, proof, now); err != nil {
		return ActivationRequest{}, err
	}
	encodedProof, err := json.Marshal(proof)
	if err != nil {
		return ActivationRequest{}, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE ascp_budget_reservations SET state='RELEASED'
		WHERE reservation_id=$1 AND state='RESERVED'`, request.ReservationID)
	if err != nil {
		return ActivationRequest{}, fmt.Errorf("release expired unactivated reservation: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return ActivationRequest{}, fmt.Errorf("read expired reservation release result: %w", err)
	}
	if changed != 1 {
		return ActivationRequest{}, ErrActivationState
	}
	preparedHandle := request.PreparedHandle
	if preparedHandle == "" {
		preparedHandle = proof.HandleID
	}
	if _, err := tx.ExecContext(ctx, `UPDATE ascp_sign_requests
		SET prepared_handle=$2, state='EXPIRED_UNACTIVATED', unactivated_proof=$3, expired_at=$4,
		    lease_owner=NULL, lease_token=NULL, lease_expires_at=NULL, last_error=NULL
		WHERE request_id=$1`, request.RequestID, nullString(preparedHandle), encodedProof, now); err != nil {
		return ActivationRequest{}, fmt.Errorf("record unactivated bearer expiry: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE ascp_signer_outbox
		SET state='CANCELLED', cancelled_at=$2, last_error='EXPIRED_UNACTIVATED'
		WHERE request_id=$1 AND state='PENDING'`, request.RequestID, now); err != nil {
		return ActivationRequest{}, fmt.Errorf("cancel expired signer outbox: %w", err)
	}
	request, err = loadActivationRequestByID(ctx, tx, request.RequestID, false)
	if err != nil {
		return ActivationRequest{}, err
	}
	if err := tx.Commit(); err != nil {
		return ActivationRequest{}, fmt.Errorf("commit unactivated bearer expiry: %w", err)
	}
	return request, nil
}

func (s *ActivationStore) requireRuntimeLease(ctx context.Context, tx *sql.Tx, requestID string, now time.Time) error {
	if !s.leasesRequired {
		return nil
	}
	lease, ok := ctx.Value(runtimeLeaseContextKey{}).(RuntimeLease)
	if !ok || lease.Request.RequestID != requestID || !identifier(lease.WorkerID) || !hash(lease.Token) {
		return ErrRuntimeLease
	}
	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (
		SELECT 1 FROM ascp_sign_requests WHERE request_id=$1 AND lease_owner=$2 AND lease_token=$3 AND lease_expires_at > $4
	)`, requestID, lease.WorkerID, lease.Token, now).Scan(&exists); err != nil {
		return fmt.Errorf("verify bearer activation lease: %w", err)
	}
	if !exists {
		return ErrRuntimeLease
	}
	return nil
}

func runtimeLeaseToken() (string, error) {
	raw := make([]byte, 32)
	defer clear(raw)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("create bearer runtime lease token: %w", err)
	}
	token := "0x" + hex.EncodeToString(raw)
	if !hash(token) {
		return "", errors.New("create bearer runtime lease token: invalid random output")
	}
	return token, nil
}

func exactLeaseMutation(result sql.Result, err error, action string) error {
	if err != nil {
		return fmt.Errorf("%s: %w", action, err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("%s result: %w", action, err)
	}
	if changed != 1 {
		return ErrRuntimeLease
	}
	return nil
}

func nullString(value string) any {
	if value == "" {
		return nil
	}
	return value
}
