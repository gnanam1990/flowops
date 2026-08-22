package ascpbearer

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/gnanam1990/flowops/pkg/executioncommitment"
	"github.com/jackc/pgx/v5/pgconn"
)

const (
	InstrumentLockAuthorization = "LOCK_AUTHORIZATION"
	maximumAuthorizationWindow  = 10 * time.Minute
)

type ActivationState string

const (
	SignRequested          ActivationState = "SIGN_REQUESTED"
	HandlePrepared         ActivationState = "PREPARED"
	ActivePendingMirror    ActivationState = "ACTIVE_PENDING_MIRROR"
	ActiveMirrored         ActivationState = "ACTIVE_MIRRORED"
	ActivationAcknowledged ActivationState = "ACTIVATION_ACKNOWLEDGED"
	ExpiredUnactivated     ActivationState = "EXPIRED_UNACTIVATED"
	SignerRefused          ActivationState = "REFUSED"
)

var (
	ErrActivationInput    = errors.New("invalid signer activation input")
	ErrActivationBinding  = errors.New("signer activation binding mismatch")
	ErrActivationState    = errors.New("invalid signer activation state")
	ErrActivationNotFound = errors.New("signer activation request not found")
	ErrSignerRefused      = errors.New("isolated signer permanently refused request")
)

type ActivationInput struct {
	RequestID            string    `json:"requestId"`
	AuthorizationID      string    `json:"authorizationId"`
	OperationID          string    `json:"operationId"`
	ReservationID        string    `json:"reservationId"`
	ActionID             string    `json:"actionId"`
	CanonicalPayload     []byte    `json:"canonicalPayload"`
	CanonicalPayloadHash string    `json:"canonicalPayloadHash"`
	EvidenceBundle       []byte    `json:"evidenceBundle"`
	EvidenceBundleHash   string    `json:"evidenceBundleHash"`
	Digest               string    `json:"digest"`
	Nonce                string    `json:"nonce"`
	InstrumentType       string    `json:"instrumentType"`
	SignerBindingVersion uint64    `json:"signerBindingVersion,omitempty"`
	SignerKeyID          string    `json:"signerKeyId"`
	KeyEpoch             uint64    `json:"keyEpoch"`
	ModuleAddress        string    `json:"moduleAddress"`
	SafeAddress          string    `json:"safeAddress"`
	KeeperID             string    `json:"keeperId"`
	ValidAfter           time.Time `json:"validAfter"`
	ValidUntil           time.Time `json:"validUntil"`
}

type ActivationRequest struct {
	ActivationInput
	InputHash           string          `json:"inputHash"`
	PreparedHandle      string          `json:"preparedHandle,omitempty"`
	State               ActivationState `json:"state"`
	PrimaryMirrorDigest string          `json:"primaryMirrorDigest,omitempty"`
	CreatedAt           time.Time       `json:"createdAt"`
	PreparedAt          time.Time       `json:"preparedAt,omitempty"`
	ActivatedAt         time.Time       `json:"activatedAt,omitempty"`
	MirroredAt          time.Time       `json:"mirroredAt,omitempty"`
	AcknowledgedAt      time.Time       `json:"acknowledgedAt,omitempty"`
}

type RegistryEntry struct {
	Digest              string    `json:"digest"`
	InstrumentType      string    `json:"instrumentType"`
	SignatureRef        string    `json:"signatureRef"`
	Nonce               string    `json:"nonce"`
	IssuedAt            time.Time `json:"issuedAt"`
	ValidUntil          time.Time `json:"validUntil"`
	SignerKeyID         string    `json:"signerKeyId"`
	KeyEpoch            uint64    `json:"keyEpoch"`
	OperationID         string    `json:"operationId"`
	AuthorizationID     string    `json:"authorizationId"`
	ReservationID       string    `json:"reservationId"`
	ModuleAddress       string    `json:"moduleAddress"`
	SafeAddress         string    `json:"safeAddress"`
	Outcome             string    `json:"outcome"`
	PrimaryMirrorDigest string    `json:"primaryMirrorDigest,omitempty"`
	CreatedAt           time.Time `json:"createdAt"`
}

type ActivationStore struct {
	db             *sql.DB
	clock          func() time.Time
	leasesRequired bool
}

func NewActivationStore(db *sql.DB, clocks ...func() time.Time) (*ActivationStore, error) {
	if db == nil || len(clocks) > 1 || len(clocks) == 1 && clocks[0] == nil {
		return nil, errors.New("database is required")
	}
	clock := time.Now
	if len(clocks) == 1 {
		clock = clocks[0]
	}
	return &ActivationStore{db: db, clock: clock}, nil
}

func (s *ActivationStore) Request(ctx context.Context, input ActivationInput) (ActivationRequest, bool, error) {
	now := s.clock().UTC()
	input.ValidAfter, input.ValidUntil = input.ValidAfter.UTC(), input.ValidUntil.UTC()
	// RequestID is excluded from the logical input hash, but every attempt must
	// still carry a valid server-generated identifier.
	if !hash(input.RequestID) {
		return ActivationRequest{}, false, ErrActivationInput
	}
	inputHash, err := activationInputHash(input)
	if err != nil {
		return ActivationRequest{}, false, err
	}
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		request, replayed, err := s.requestOnce(ctx, input, inputHash, now)
		if !activationSerializationFailure(err) {
			return request, replayed, err
		}
		lastErr = err
		if err := ctx.Err(); err != nil {
			return ActivationRequest{}, false, err
		}
	}
	return ActivationRequest{}, false, fmt.Errorf("signer activation serialization retries exhausted: %w", lastErr)
}

func (s *ActivationStore) requestOnce(ctx context.Context, input ActivationInput, inputHash string, now time.Time) (ActivationRequest, bool, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return ActivationRequest{}, false, fmt.Errorf("begin sign request: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	existing, err := loadActivationRequest(ctx, tx, input.AuthorizationID)
	if err == nil {
		if existing.InputHash != inputHash {
			return ActivationRequest{}, false, ErrActivationBinding
		}
		if err := tx.Commit(); err != nil {
			return ActivationRequest{}, false, fmt.Errorf("commit sign request replay: %w", err)
		}
		return existing, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return ActivationRequest{}, false, err
	}
	// Time-window validation applies only to the first durable creation. An
	// exact idempotent replay remains readable after the window has elapsed.
	if err := validateActivationInput(input, now); err != nil {
		return ActivationRequest{}, false, err
	}

	var authorizationState, executionSnapshotHash, reservationState string
	var reservationExpiresAt time.Time
	err = tx.QueryRowContext(ctx, `
		SELECT a.state, a.execution_snapshot_hash, r.state, r.expires_at
		FROM ascp_execution_authorizations a
		JOIN ascp_budget_reservations r ON r.reservation_id=a.reservation_id
		WHERE a.authorization_id=$1 AND a.intent_id=$2 AND r.reservation_id=$3 AND r.operation_id=$2
		FOR UPDATE OF a, r`, input.AuthorizationID, input.OperationID, input.ReservationID).
		Scan(&authorizationState, &executionSnapshotHash, &reservationState, &reservationExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ActivationRequest{}, false, ErrActivationBinding
	}
	if err != nil {
		return ActivationRequest{}, false, fmt.Errorf("lock execution authorization for signing: %w", err)
	}
	if authorizationState != "VALIDATED_AND_RESERVED" || reservationState != "RESERVED" || !now.Before(reservationExpiresAt) {
		return ActivationRequest{}, false, ErrActivationState
	}
	// The signer payload is a later, action-specific object, so it need not be
	// byte-identical to the execution snapshot. Both hashes are durably bound by
	// the authorization and signer request rows.
	if !hash(executionSnapshotHash) {
		return ActivationRequest{}, false, ErrActivationBinding
	}
	if err := lockSignerBinding(ctx, tx, input); err != nil {
		return ActivationRequest{}, false, err
	}

	result, err := tx.ExecContext(ctx, `
		INSERT INTO ascp_sign_requests
			(request_id, authorization_id, operation_id, reservation_id, input_hash,
			 action_id, canonical_payload, canonical_payload_hash, evidence_bundle, evidence_bundle_hash,
			 digest, nonce, instrument_type, signer_binding_version, signer_key_id, key_epoch, module_address, safe_address,
			 keeper_id, valid_after, valid_until, state, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,'SIGN_REQUESTED',$22)
		ON CONFLICT (authorization_id) DO NOTHING`, input.RequestID, input.AuthorizationID, input.OperationID,
		input.ReservationID, inputHash, input.ActionID, input.CanonicalPayload, input.CanonicalPayloadHash,
		input.EvidenceBundle, input.EvidenceBundleHash, input.Digest, input.Nonce, input.InstrumentType,
		input.SignerBindingVersion, input.SignerKeyID, input.KeyEpoch, input.ModuleAddress, input.SafeAddress, input.KeeperID,
		input.ValidAfter, input.ValidUntil, now)
	if err != nil {
		if activationUniqueViolation(err) {
			return ActivationRequest{}, false, ErrActivationBinding
		}
		return ActivationRequest{}, false, fmt.Errorf("create sign request: %w", err)
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return ActivationRequest{}, false, fmt.Errorf("read sign request insert result: %w", err)
	}
	request, err := loadActivationRequest(ctx, tx, input.AuthorizationID)
	if err != nil {
		return ActivationRequest{}, false, err
	}
	if request.InputHash != inputHash {
		return ActivationRequest{}, false, ErrActivationBinding
	}
	if inserted == 1 {
		if err := insertOutbox(ctx, tx, request, "SIGN_PREPARE_REQUESTED", now); err != nil {
			return ActivationRequest{}, false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return ActivationRequest{}, false, fmt.Errorf("commit sign request: %w", err)
	}
	return request, inserted == 0, nil
}

func lockSignerBinding(ctx context.Context, tx *sql.Tx, input ActivationInput) error {
	var version, keyEpoch uint64
	var signerKeyID, moduleAddress, safeAddress, keeperID string
	err := tx.QueryRowContext(ctx, `
		SELECT b.version,b.signer_key_id,b.key_epoch,b.module_address,b.safe_address,b.keeper_id
		FROM ascp_intents i
		JOIN ascp_agent_signer_bindings b
		  ON b.organization_id=i.organization_id AND b.agent_id=i.actor_id
		WHERE i.operation_id=$1
		FOR SHARE OF b`, input.OperationID).Scan(
		&version, &signerKeyID, &keyEpoch, &moduleAddress, &safeAddress, &keeperID,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrActivationBinding
	}
	if err != nil {
		return fmt.Errorf("lock authoritative signer binding: %w", err)
	}
	if version != input.SignerBindingVersion || signerKeyID != input.SignerKeyID || keyEpoch != input.KeyEpoch ||
		moduleAddress != input.ModuleAddress || safeAddress != input.SafeAddress || keeperID != input.KeeperID {
		return ErrActivationBinding
	}
	return nil
}

func activationSerializationFailure(err error) bool {
	var postgresError *pgconn.PgError
	return errors.As(err, &postgresError) && (postgresError.Code == "40001" || postgresError.Code == "40P01")
}

func activationUniqueViolation(err error) bool {
	var postgresError *pgconn.PgError
	return errors.As(err, &postgresError) && postgresError.Code == "23505"
}

// ForAuthorization returns the one activation request bound to an execution
// authorization. Tenant and agent scope must be established by the application
// boundary before calling this storage primitive.
func (s *ActivationStore) ForAuthorization(ctx context.Context, authorizationID string) (ActivationRequest, error) {
	if !hash(authorizationID) {
		return ActivationRequest{}, ErrActivationInput
	}
	var request ActivationRequest
	err := scanActivationRequest(s.db.QueryRowContext(ctx, `SELECT `+activationColumns+` FROM ascp_sign_requests WHERE authorization_id=$1`, authorizationID), &request)
	if errors.Is(err, sql.ErrNoRows) {
		return ActivationRequest{}, ErrActivationNotFound
	}
	if err != nil {
		return ActivationRequest{}, fmt.Errorf("read signer activation request by authorization: %w", err)
	}
	return request, nil
}

func (s *ActivationStore) RecordPrepared(ctx context.Context, requestID, handle string) (ActivationRequest, error) {
	now := s.clock().UTC()
	if !hash(requestID) || !opaqueHandle(handle) {
		return ActivationRequest{}, ErrActivationInput
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return ActivationRequest{}, fmt.Errorf("begin prepared signer handle: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := s.requireRuntimeLease(ctx, tx, requestID, now); err != nil {
		return ActivationRequest{}, err
	}
	var request ActivationRequest
	err = scanActivationRequest(tx.QueryRowContext(ctx, `
		UPDATE ascp_sign_requests SET prepared_handle=$2, state='PREPARED', prepared_at=$3
		WHERE request_id=$1 AND state='SIGN_REQUESTED' AND valid_until > $3
		RETURNING `+activationColumns, requestID, handle, now), &request)
	if err == nil {
		if err := markOutboxDelivered(ctx, tx, requestID, "SIGN_PREPARE_REQUESTED", now); err != nil {
			return ActivationRequest{}, err
		}
		if err := tx.Commit(); err != nil {
			return ActivationRequest{}, fmt.Errorf("commit prepared signer handle: %w", err)
		}
		return request, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return ActivationRequest{}, fmt.Errorf("record prepared signer handle: %w", err)
	}
	request, err = loadActivationRequestByID(ctx, tx, requestID, false)
	if err != nil {
		return ActivationRequest{}, err
	}
	if request.PreparedHandle == handle && (request.State == HandlePrepared || request.State == ActivePendingMirror || request.State == ActiveMirrored || request.State == ActivationAcknowledged) {
		if err := tx.Commit(); err != nil {
			return ActivationRequest{}, fmt.Errorf("commit prepared signer handle replay: %w", err)
		}
		return request, nil
	}
	return request, ErrActivationState
}

func (s *ActivationStore) Activate(ctx context.Context, requestID string) (RegistryEntry, error) {
	now := s.clock().UTC()
	if !hash(requestID) {
		return RegistryEntry{}, ErrActivationInput
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return RegistryEntry{}, fmt.Errorf("begin signer activation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	request, err := loadActivationRequestByID(ctx, tx, requestID, true)
	if err != nil {
		return RegistryEntry{}, err
	}
	if err := s.requireRuntimeLease(ctx, tx, requestID, now); err != nil {
		return RegistryEntry{}, err
	}
	if request.State == ActivePendingMirror || request.State == ActiveMirrored || request.State == ActivationAcknowledged {
		entry, err := loadRegistryEntry(ctx, tx, request.Digest)
		if err != nil {
			return RegistryEntry{}, err
		}
		if err := tx.Commit(); err != nil {
			return RegistryEntry{}, fmt.Errorf("commit signer activation replay: %w", err)
		}
		return entry, nil
	}
	if request.State != HandlePrepared || request.PreparedHandle == "" || !now.Before(request.ValidUntil) {
		return RegistryEntry{}, ErrActivationState
	}
	if now.Before(request.ValidAfter) {
		return RegistryEntry{}, ErrActivationState
	}
	var authorizationState, reservationState string
	var reservationExpiresAt time.Time
	err = tx.QueryRowContext(ctx, `
		SELECT a.state, r.state, r.expires_at
		FROM ascp_execution_authorizations a
		JOIN ascp_budget_reservations r ON r.reservation_id=a.reservation_id
		WHERE a.authorization_id=$1 AND a.intent_id=$2 AND r.reservation_id=$3
		FOR UPDATE OF a, r`, request.AuthorizationID, request.OperationID, request.ReservationID).
		Scan(&authorizationState, &reservationState, &reservationExpiresAt)
	if err != nil {
		return RegistryEntry{}, fmt.Errorf("lock signer activation bindings: %w", err)
	}
	if authorizationState != "VALIDATED_AND_RESERVED" || reservationState != "RESERVED" || !now.Before(reservationExpiresAt) {
		return RegistryEntry{}, ErrActivationState
	}
	if _, err := tx.ExecContext(ctx, `UPDATE ascp_budget_reservations SET state='AUTHORIZATION_LIVE' WHERE reservation_id=$1 AND state='RESERVED'`, request.ReservationID); err != nil {
		return RegistryEntry{}, fmt.Errorf("make signer reservation live: %w", err)
	}
	entry := RegistryEntry{
		Digest: request.Digest, InstrumentType: request.InstrumentType, SignatureRef: request.PreparedHandle,
		Nonce: request.Nonce, IssuedAt: request.ValidAfter, ValidUntil: request.ValidUntil,
		SignerKeyID: request.SignerKeyID, KeyEpoch: request.KeyEpoch, OperationID: request.OperationID,
		AuthorizationID: request.AuthorizationID, ReservationID: request.ReservationID,
		ModuleAddress: request.ModuleAddress, SafeAddress: request.SafeAddress, Outcome: "LIVE", CreatedAt: now,
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO ascp_bearer_registry
			(digest, instrument_type, signature_ref, nonce, issued_at, valid_until, signer_key_id,
			 key_epoch, operation_id, authorization_id, reservation_id, module_address, safe_address, outcome, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,'LIVE',$14)`, entry.Digest,
		entry.InstrumentType, entry.SignatureRef, entry.Nonce, entry.IssuedAt, entry.ValidUntil,
		entry.SignerKeyID, entry.KeyEpoch, entry.OperationID, entry.AuthorizationID, entry.ReservationID,
		entry.ModuleAddress, entry.SafeAddress, entry.CreatedAt); err != nil {
		return RegistryEntry{}, fmt.Errorf("create bearer registry entry: %w", err)
	}
	if err := insertPaymentOperation(ctx, tx, entry, now); err != nil {
		return RegistryEntry{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO ascp_bearer_handles
			(handle_id, operation_id, payload_hash, digest, nonce, state, valid_until, created_at)
		VALUES ($1,$2,$3,$4,$5,'ACTIVE',$6,$7)`, request.PreparedHandle, request.OperationID,
		request.CanonicalPayloadHash, request.Digest, request.Nonce, request.ValidUntil, now); err != nil {
		return RegistryEntry{}, fmt.Errorf("record opaque active bearer handle: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE ascp_sign_requests SET state='ACTIVE_PENDING_MIRROR', activated_at=$2 WHERE request_id=$1 AND state='PREPARED'`, requestID, now); err != nil {
		return RegistryEntry{}, fmt.Errorf("advance sign request to active: %w", err)
	}
	request.State, request.ActivatedAt = ActivePendingMirror, now
	if err := insertOutbox(ctx, tx, request, "ACTIVATION_MIRROR_REQUESTED", now); err != nil {
		return RegistryEntry{}, err
	}
	if err := tx.Commit(); err != nil {
		return RegistryEntry{}, fmt.Errorf("commit signer activation: %w", err)
	}
	return entry, nil
}

func insertPaymentOperation(ctx context.Context, tx *sql.Tx, entry RegistryEntry, now time.Time) error {
	var organizationID, agentID, commitmentHash, reservationAmount string
	var commitmentJSON []byte
	err := tx.QueryRowContext(ctx, `
		SELECT d.organization_id, d.agent_id, d.commitment_hash, d.commitment_json, r.amount_base_units
		FROM ascp_policy_decisions d
		JOIN ascp_execution_authorizations a ON a.intent_id=d.operation_id
		JOIN ascp_budget_reservations r ON r.reservation_id=a.reservation_id
		WHERE d.operation_id=$1 AND a.authorization_id=$2 AND r.reservation_id=$3
		FOR SHARE OF d, a, r`, entry.OperationID, entry.AuthorizationID, entry.ReservationID).
		Scan(&organizationID, &agentID, &commitmentHash, &commitmentJSON, &reservationAmount)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrActivationBinding
	}
	if err != nil {
		return fmt.Errorf("read payment operation commitment: %w", err)
	}
	var commitment executioncommitment.Commitment
	if err := json.Unmarshal(commitmentJSON, &commitment); err != nil {
		return fmt.Errorf("decode payment operation commitment: %w", err)
	}
	digest, err := commitment.Digest(commitment.EscrowContract, commitment.ChainID)
	nowUnix := now.Unix()
	if err != nil || digest.Hex() != commitmentHash || commitment.OperationID != entry.OperationID ||
		commitment.Amount != reservationAmount || commitment.AcceptBy == 0 || commitment.SettleBy <= commitment.AcceptBy ||
		nowUnix < 0 || uint64(nowUnix) >= commitment.AcceptBy || entry.ValidUntil.Unix() < 0 || uint64(entry.ValidUntil.Unix()) > commitment.AcceptBy {
		return ErrActivationBinding
	}
	chainID, err := strconv.ParseUint(commitment.ChainID, 10, 64)
	if err != nil || chainID != 8453 && chainID != 84532 || commitment.SettleBy > uint64(1<<63-1) {
		return ErrActivationBinding
	}
	callID := crypto.Keccak256Hash(digest.Bytes()).Hex()
	result, err := tx.ExecContext(ctx, `
		INSERT INTO ascp_payment_operations
			(operation_id, organization_id, agent_id, authorization_id, reservation_id, bearer_digest,
			 commitment_hash, call_id, chain_id, escrow_contract, asset, buyer, pay_to,
			 amount_base_units, settle_by, state, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,to_timestamp($15),'AUTH_SIGNED',$16,$16)
		ON CONFLICT (operation_id) DO NOTHING`, entry.OperationID, organizationID, agentID,
		entry.AuthorizationID, entry.ReservationID, entry.Digest, commitmentHash, callID, chainID,
		commitment.EscrowContract, commitment.Asset, entry.SafeAddress, commitment.PayTo,
		commitment.Amount, commitment.SettleBy, now)
	if err != nil {
		return fmt.Errorf("create ASCP payment operation: %w", err)
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read ASCP payment operation insert result: %w", err)
	}
	if inserted != 1 {
		return ErrActivationBinding
	}
	return nil
}

func (s *ActivationStore) MarkPrimaryMirrored(ctx context.Context, requestID, mirrorDigest string) (ActivationRequest, error) {
	now := s.clock().UTC()
	if !hash(requestID) || !hash(mirrorDigest) {
		return ActivationRequest{}, ErrActivationInput
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return ActivationRequest{}, fmt.Errorf("begin primary mirror acknowledgment: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	request, err := loadActivationRequestByID(ctx, tx, requestID, true)
	if err != nil {
		return ActivationRequest{}, err
	}
	if err := s.requireRuntimeLease(ctx, tx, requestID, now); err != nil {
		return ActivationRequest{}, err
	}
	if request.State == ActiveMirrored || request.State == ActivationAcknowledged {
		if request.PrimaryMirrorDigest != mirrorDigest {
			return ActivationRequest{}, ErrActivationBinding
		}
		if err := tx.Commit(); err != nil {
			return ActivationRequest{}, fmt.Errorf("commit mirror replay: %w", err)
		}
		return request, nil
	}
	if request.State != ActivePendingMirror {
		return ActivationRequest{}, ErrActivationState
	}
	entry, err := loadRegistryEntry(ctx, tx, request.Digest)
	if err != nil {
		return ActivationRequest{}, err
	}
	expectedMirrorDigest, err := RegistryMirrorDigest(entry)
	if err != nil {
		return ActivationRequest{}, err
	}
	if mirrorDigest != expectedMirrorDigest {
		return ActivationRequest{}, ErrActivationBinding
	}
	if _, err := tx.ExecContext(ctx, `UPDATE ascp_bearer_registry SET primary_mirror_digest=$2 WHERE digest=$1 AND primary_mirror_digest IS NULL`, request.Digest, mirrorDigest); err != nil {
		return ActivationRequest{}, fmt.Errorf("bind bearer primary mirror: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE ascp_sign_requests SET state='ACTIVE_MIRRORED', primary_mirror_digest=$2, mirrored_at=$3 WHERE request_id=$1 AND state='ACTIVE_PENDING_MIRROR'`, requestID, mirrorDigest, now); err != nil {
		return ActivationRequest{}, fmt.Errorf("record primary mirror acknowledgment: %w", err)
	}
	request.State, request.PrimaryMirrorDigest, request.MirroredAt = ActiveMirrored, mirrorDigest, now
	if err := markOutboxDelivered(ctx, tx, requestID, "ACTIVATION_MIRROR_REQUESTED", now); err != nil {
		return ActivationRequest{}, err
	}
	if err := insertOutbox(ctx, tx, request, "ACTIVATION_ACK_REQUESTED", now); err != nil {
		return ActivationRequest{}, err
	}
	if err := insertOutbox(ctx, tx, request, "SECONDARY_MIRROR_REQUESTED", now); err != nil {
		return ActivationRequest{}, err
	}
	if err := tx.Commit(); err != nil {
		return ActivationRequest{}, fmt.Errorf("commit primary mirror acknowledgment: %w", err)
	}
	return request, nil
}

func (s *ActivationStore) MarkAcknowledged(ctx context.Context, requestID, handle string) (ActivationRequest, error) {
	now := s.clock().UTC()
	if !hash(requestID) || !opaqueHandle(handle) {
		return ActivationRequest{}, ErrActivationInput
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return ActivationRequest{}, fmt.Errorf("begin signer activation acknowledgment: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := s.requireRuntimeLease(ctx, tx, requestID, now); err != nil {
		return ActivationRequest{}, err
	}
	var request ActivationRequest
	err = scanActivationRequest(tx.QueryRowContext(ctx, `
		UPDATE ascp_sign_requests SET state='ACTIVATION_ACKNOWLEDGED', acknowledged_at=$3
		WHERE request_id=$1 AND prepared_handle=$2 AND state='ACTIVE_MIRRORED'
		RETURNING `+activationColumns, requestID, handle, now), &request)
	if err == nil {
		if err := markOutboxDelivered(ctx, tx, requestID, "ACTIVATION_ACK_REQUESTED", now); err != nil {
			return ActivationRequest{}, err
		}
		if err := tx.Commit(); err != nil {
			return ActivationRequest{}, fmt.Errorf("commit signer activation acknowledgment: %w", err)
		}
		return request, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return ActivationRequest{}, fmt.Errorf("record signer activation acknowledgment: %w", err)
	}
	request, err = loadActivationRequestByID(ctx, tx, requestID, false)
	if err != nil {
		return ActivationRequest{}, err
	}
	if request.State == ActivationAcknowledged && request.PreparedHandle == handle {
		if err := tx.Commit(); err != nil {
			return ActivationRequest{}, fmt.Errorf("commit signer activation acknowledgment replay: %w", err)
		}
		return request, nil
	}
	return request, ErrActivationState
}

func (s *ActivationStore) Registry(ctx context.Context, digest string) (RegistryEntry, error) {
	if !hash(digest) {
		return RegistryEntry{}, ErrActivationInput
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return RegistryEntry{}, fmt.Errorf("begin bearer registry read: %w", err)
	}
	defer tx.Rollback()
	entry, err := loadRegistryEntry(ctx, tx, digest)
	if err != nil {
		return RegistryEntry{}, err
	}
	if err := tx.Commit(); err != nil {
		return RegistryEntry{}, fmt.Errorf("commit bearer registry read: %w", err)
	}
	return entry, nil
}

func (s *ActivationStore) Get(ctx context.Context, requestID string) (ActivationRequest, error) {
	if !hash(requestID) {
		return ActivationRequest{}, ErrActivationInput
	}
	if s.leasesRequired {
		tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
		if err != nil {
			return ActivationRequest{}, fmt.Errorf("begin leased signer activation read: %w", err)
		}
		defer func() { _ = tx.Rollback() }()
		request, err := loadActivationRequestByID(ctx, tx, requestID, false)
		if err != nil {
			return ActivationRequest{}, err
		}
		if err := s.requireRuntimeLease(ctx, tx, requestID, s.clock().UTC()); err != nil {
			return ActivationRequest{}, err
		}
		if err := tx.Commit(); err != nil {
			return ActivationRequest{}, fmt.Errorf("commit leased signer activation read: %w", err)
		}
		return request, nil
	}
	var request ActivationRequest
	err := scanActivationRequest(s.db.QueryRowContext(ctx, `SELECT `+activationColumns+` FROM ascp_sign_requests WHERE request_id=$1`, requestID), &request)
	if errors.Is(err, sql.ErrNoRows) {
		return ActivationRequest{}, ErrActivationNotFound
	}
	if err != nil {
		return ActivationRequest{}, fmt.Errorf("read signer activation request: %w", err)
	}
	return request, nil
}

func validateActivationInput(input ActivationInput, now time.Time) error {
	if err := validateActivationInputReplay(input, now); err != nil {
		return ErrActivationInput
	}
	return validateActivationInputFreshness(input, now)
}

func validateActivationInputFreshness(input ActivationInput, now time.Time) error {
	if input.ValidAfter.Before(now.Add(-time.Minute)) || input.ValidAfter.After(now.Add(time.Minute)) {
		return ErrActivationInput
	}
	return nil
}

func validateActivationInputReplay(input ActivationInput, now time.Time) error {
	if !hash(input.RequestID) || !hash(input.AuthorizationID) || !hash(input.OperationID) ||
		!hash(input.ReservationID) || !identifier(input.ActionID) || len(input.CanonicalPayload) == 0 ||
		len(input.CanonicalPayload) > 256*1024 || input.CanonicalPayloadHash != CanonicalPayloadHash(input.CanonicalPayload) ||
		len(input.EvidenceBundle) == 0 || len(input.EvidenceBundle) > 1024*1024 || input.EvidenceBundleHash != EvidenceBundleHash(input.EvidenceBundle) ||
		!hash(input.Digest) ||
		!hash(input.Nonce) || input.InstrumentType != InstrumentLockAuthorization || input.SignerBindingVersion == 0 ||
		!identifier(input.SignerKeyID) || input.KeyEpoch == 0 || !address(input.ModuleAddress) ||
		!address(input.SafeAddress) || !identifier(input.KeeperID) || input.ValidAfter.IsZero() ||
		input.ValidUntil.IsZero() ||
		!input.ValidAfter.Before(input.ValidUntil) || !now.Before(input.ValidUntil) ||
		input.ValidUntil.Sub(input.ValidAfter) > maximumAuthorizationWindow {
		return ErrActivationInput
	}
	return nil
}

// ValidateActivationInput exposes the signer-side structural validation so
// independently deployed trust rings can reject malformed activation inputs
// before consulting verifier or HSM dependencies.
func ValidateActivationInput(input ActivationInput, now time.Time) error {
	return validateActivationInput(input, now.UTC())
}

// ValidateActivationInputReplay validates an exact already-persisted signer
// request until its validity deadline without reapplying the one-minute intake
// freshness bound. Callers must prove the full input hash is already bound
// before relying on this replay-only validation.
func ValidateActivationInputReplay(input ActivationInput, now time.Time) error {
	return validateActivationInputReplay(input, now.UTC())
}

// ValidateActivationInputFreshness reapplies only the one-minute intake clock
// bound after the complete replay-safe validation has already succeeded.
func ValidateActivationInputFreshness(input ActivationInput, now time.Time) error {
	return validateActivationInputFreshness(input, now.UTC())
}

// CanonicalPayloadHash is the module-facing calldataHash:
// keccak256(exact canonical payload bytes).
func CanonicalPayloadHash(payload []byte) string {
	return crypto.Keccak256Hash(payload).Hex()
}

// EvidenceBundleHash binds the complete immutable signer evidence packet.
func EvidenceBundleHash(evidence []byte) string {
	digest := sha256.Sum256(append([]byte("ASCP_SIGNER_EVIDENCE_V1\n"), evidence...))
	return "0x" + hex.EncodeToString(digest[:])
}

func activationInputHash(input ActivationInput) (string, error) {
	// RequestID is an attempt identifier, not part of the logical signing
	// request. A crash before the response may regenerate it; authorizationID
	// remains the permanent idempotency key.
	input.RequestID = ""
	encoded, err := json.Marshal(input)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(append([]byte("ASCP_SIGN_REQUEST_V1\n"), encoded...))
	return "0x" + hex.EncodeToString(digest[:]), nil
}

// ActivationInputHash returns the permanent logical signer-request hash. The
// attempt RequestID is deliberately excluded; AuthorizationID remains the
// durable idempotency identity.
func ActivationInputHash(input ActivationInput) (string, error) { return activationInputHash(input) }

// RegistryMirrorBytes is the exact immutable object written to WORM. Mirror
// implementation retries must use these bytes and reject an existing object
// with different content.
func RegistryMirrorBytes(entry RegistryEntry) ([]byte, error) {
	entry.PrimaryMirrorDigest = ""
	entry.IssuedAt = entry.IssuedAt.UTC()
	entry.ValidUntil = entry.ValidUntil.UTC()
	entry.CreatedAt = entry.CreatedAt.UTC()
	return json.Marshal(entry)
}

func RegistryMirrorDigest(entry RegistryEntry) (string, error) {
	encoded, err := RegistryMirrorBytes(entry)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(append([]byte("ASCP_BEARER_REGISTRY_MIRROR_V1\n"), encoded...))
	return "0x" + hex.EncodeToString(digest[:]), nil
}

func insertOutbox(ctx context.Context, tx *sql.Tx, request ActivationRequest, kind string, now time.Time) error {
	payload, err := json.Marshal(request)
	if err != nil {
		return fmt.Errorf("encode signer outbox payload: %w", err)
	}
	digest := sha256.Sum256([]byte(request.RequestID + "\n" + kind))
	eventID := "0x" + hex.EncodeToString(digest[:])
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO ascp_signer_outbox (event_id, request_id, operation_id, kind, payload, state, created_at)
		VALUES ($1,$2,$3,$4,$5,'PENDING',$6)
		ON CONFLICT (request_id, kind) DO NOTHING`, eventID, request.RequestID, request.OperationID, kind, payload, now); err != nil {
		return fmt.Errorf("create signer outbox event %s: %w", kind, err)
	}
	return nil
}

func markOutboxDelivered(ctx context.Context, tx *sql.Tx, requestID, kind string, now time.Time) error {
	result, err := tx.ExecContext(ctx, `
		UPDATE ascp_signer_outbox SET state='DELIVERED', delivered_at=$3, attempts=attempts+1
		WHERE request_id=$1 AND kind=$2 AND state='PENDING'`, requestID, kind, now)
	if err != nil {
		return fmt.Errorf("mark signer outbox event %s delivered: %w", kind, err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read signer outbox delivery result: %w", err)
	}
	if changed != 1 {
		return fmt.Errorf("signer outbox event %s is unavailable", kind)
	}
	return nil
}

const activationColumns = `
	request_id, authorization_id, operation_id, reservation_id, input_hash,
	action_id, canonical_payload, canonical_payload_hash, evidence_bundle, evidence_bundle_hash,
	digest, nonce, instrument_type, signer_binding_version, signer_key_id, key_epoch,
	module_address, safe_address, keeper_id, valid_after, valid_until, prepared_handle,
	state, primary_mirror_digest, created_at, prepared_at, activated_at, mirrored_at, acknowledged_at`

type rowScanner interface{ Scan(...any) error }

func scanActivationRequest(row rowScanner, request *ActivationRequest) error {
	var handle, mirror sql.NullString
	var preparedAt, activatedAt, mirroredAt, acknowledgedAt sql.NullTime
	err := row.Scan(&request.RequestID, &request.AuthorizationID, &request.OperationID, &request.ReservationID,
		&request.InputHash, &request.ActionID, &request.CanonicalPayload, &request.CanonicalPayloadHash,
		&request.EvidenceBundle, &request.EvidenceBundleHash, &request.Digest, &request.Nonce, &request.InstrumentType,
		&request.SignerBindingVersion, &request.SignerKeyID, &request.KeyEpoch, &request.ModuleAddress, &request.SafeAddress, &request.KeeperID,
		&request.ValidAfter, &request.ValidUntil, &handle, &request.State, &mirror, &request.CreatedAt,
		&preparedAt, &activatedAt, &mirroredAt, &acknowledgedAt)
	if err != nil {
		return err
	}
	request.PreparedHandle, request.PrimaryMirrorDigest = handle.String, mirror.String
	request.ValidAfter, request.ValidUntil, request.CreatedAt = request.ValidAfter.UTC(), request.ValidUntil.UTC(), request.CreatedAt.UTC()
	if preparedAt.Valid {
		request.PreparedAt = preparedAt.Time.UTC()
	}
	if activatedAt.Valid {
		request.ActivatedAt = activatedAt.Time.UTC()
	}
	if mirroredAt.Valid {
		request.MirroredAt = mirroredAt.Time.UTC()
	}
	if acknowledgedAt.Valid {
		request.AcknowledgedAt = acknowledgedAt.Time.UTC()
	}
	return nil
}

func loadActivationRequest(ctx context.Context, tx *sql.Tx, authorizationID string) (ActivationRequest, error) {
	var request ActivationRequest
	err := scanActivationRequest(tx.QueryRowContext(ctx, `SELECT `+activationColumns+` FROM ascp_sign_requests WHERE authorization_id=$1 FOR UPDATE`, authorizationID), &request)
	if errors.Is(err, sql.ErrNoRows) {
		return ActivationRequest{}, sql.ErrNoRows
	}
	if err != nil {
		return ActivationRequest{}, fmt.Errorf("read sign request: %w", err)
	}
	return request, nil
}

func loadActivationRequestByID(ctx context.Context, tx *sql.Tx, requestID string, lock bool) (ActivationRequest, error) {
	query := `SELECT ` + activationColumns + ` FROM ascp_sign_requests WHERE request_id=$1`
	if lock {
		query += ` FOR UPDATE`
	}
	var request ActivationRequest
	err := scanActivationRequest(tx.QueryRowContext(ctx, query, requestID), &request)
	if errors.Is(err, sql.ErrNoRows) {
		return ActivationRequest{}, ErrActivationNotFound
	}
	if err != nil {
		return ActivationRequest{}, fmt.Errorf("read signer activation request: %w", err)
	}
	return request, nil
}

func loadRegistryEntry(ctx context.Context, tx *sql.Tx, digest string) (RegistryEntry, error) {
	var entry RegistryEntry
	var mirror sql.NullString
	err := tx.QueryRowContext(ctx, `
		SELECT digest, instrument_type, signature_ref, nonce, issued_at, valid_until,
		       signer_key_id, key_epoch, operation_id, authorization_id, reservation_id,
		       module_address, safe_address, outcome, primary_mirror_digest, created_at
		FROM ascp_bearer_registry WHERE digest=$1`, digest).Scan(&entry.Digest, &entry.InstrumentType,
		&entry.SignatureRef, &entry.Nonce, &entry.IssuedAt, &entry.ValidUntil, &entry.SignerKeyID,
		&entry.KeyEpoch, &entry.OperationID, &entry.AuthorizationID, &entry.ReservationID,
		&entry.ModuleAddress, &entry.SafeAddress, &entry.Outcome, &mirror, &entry.CreatedAt)
	if err != nil {
		return RegistryEntry{}, fmt.Errorf("read bearer registry entry: %w", err)
	}
	entry.PrimaryMirrorDigest = mirror.String
	entry.IssuedAt, entry.ValidUntil, entry.CreatedAt = entry.IssuedAt.UTC(), entry.ValidUntil.UTC(), entry.CreatedAt.UTC()
	return entry, nil
}

func opaqueHandle(value string) bool {
	return len(value) >= 32 && len(value) <= 256 && !strings.ContainsAny(value, " \t\r\n")
}

func identifier(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for index, character := range value {
		if index == 0 && !alphaNumeric(character) {
			return false
		}
		if !alphaNumeric(character) && character != '.' && character != '_' && character != ':' && character != '-' {
			return false
		}
	}
	return true
}

func alphaNumeric(character rune) bool {
	return character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9'
}

func hash(value string) bool {
	if len(value) != 66 || !strings.HasPrefix(value, "0x") || strings.ToLower(value) != value || value == "0x"+strings.Repeat("0", 64) {
		return false
	}
	_, err := hex.DecodeString(value[2:])
	return err == nil
}

func address(value string) bool {
	if len(value) != 42 || !strings.HasPrefix(value, "0x") || strings.ToLower(value) != value || value == "0x"+strings.Repeat("0", 40) {
		return false
	}
	_, err := hex.DecodeString(value[2:])
	return err == nil
}
