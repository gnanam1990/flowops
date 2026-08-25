package controlapi

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gnanam1990/flowops/internal/ascpbearer"
	"github.com/gnanam1990/flowops/internal/ascpsignerbinding"
)

type expiryProofSigner struct{ now time.Time }

func (*expiryProofSigner) Prepare(context.Context, ascpbearer.ActivationInput) (string, error) {
	return "", errors.New("prepare must not run for expired work")
}

func (*expiryProofSigner) AcknowledgeActivation(context.Context, ascpbearer.ActivationProof) error {
	return errors.New("acknowledge must not run for expired work")
}

func (s *expiryProofSigner) ProveUnactivated(_ context.Context, request ascpbearer.ActivationRequest) (ascpbearer.UnactivatedProof, error) {
	proof := ascpbearer.UnactivatedProof{
		RequestID: request.RequestID, OperationID: request.OperationID, ActionID: request.ActionID, InputHash: request.InputHash,
		HandleID: request.PreparedHandle, Status: "EXPIRED_UNACTIVATED", ProvenAt: s.now,
	}
	proof.ProofDigest, _ = ascpbearer.UnactivatedProofDigest(proof)
	return proof, nil
}

type unusedRuntimeMirror struct{}

func (unusedRuntimeMirror) PutPrimary(context.Context, string, []byte, string) error {
	return errors.New("mirror must not run for expired work")
}

type refusingRuntimeSigner struct{}

func (*refusingRuntimeSigner) Prepare(context.Context, ascpbearer.ActivationInput) (string, error) {
	return "", &ascpbearer.RuntimeBoundaryError{Boundary: "signer", Code: "SIGNER_REFUSED", StatusCode: http.StatusUnprocessableEntity}
}

func (*refusingRuntimeSigner) AcknowledgeActivation(context.Context, ascpbearer.ActivationProof) error {
	return errors.New("acknowledge must not run for refused work")
}

func (*refusingRuntimeSigner) ProveUnactivated(context.Context, ascpbearer.ActivationRequest) (ascpbearer.UnactivatedProof, error) {
	return ascpbearer.UnactivatedProof{}, errors.New("expiry must not run for refused work")
}

func TestASCPCapacitySecurityDefinerRejectsTemporaryRelationSubstitution(t *testing.T) {
	db := ascpIntegrationDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var schema string
	if err := conn.QueryRowContext(ctx, `SELECT current_schema()`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	base := time.Now().UTC().Truncate(time.Microsecond)
	operationID, reservationID := ascpIntegrationHash(900001), ascpIntegrationHash(900002)
	if _, err := conn.ExecContext(ctx, `INSERT INTO organizations (id,name) VALUES ('org_capacity_shadow','Capacity Shadow')`); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, `INSERT INTO agents (organization_id,id,customer_id,name,status)
		VALUES ('org_capacity_shadow','agent_capacity_shadow','customer_capacity_shadow','Capacity Agent','ACTIVE')`); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, `INSERT INTO ascp_intents
		(operation_id,organization_id,actor_id,endpoint,idempotency_key,canonical_input_hash,quote_hash,
		 purchase_spec_hash,quote_nonce,directory_version,directory_contract,seller_signer,quote_json,
		 purchase_spec_json,purchase_spec_bytes,request_body,created_at)
		VALUES ($1,'org_capacity_shadow','agent_capacity_shadow','ascp.intent.create','capacity_shadow',$2,$3,$4,$5,
		 1,$6,$7,'{}'::jsonb,'{}'::jsonb,'{}'::bytea,''::bytea,$8)`, operationID, fmt.Sprintf("%064x", 900003),
		ascpIntegrationHash(900004), ascpIntegrationHash(900005), ascpIntegrationHash(900006), ascpIntegrationDirectory,
		ascpIntegrationSigner, base); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, `INSERT INTO ascp_budget_reservations
		(reservation_id,operation_id,amount_base_units,state,dimensions,created_at,expires_at)
		VALUES ($1,$2,'10','RESERVED','[]'::jsonb,$3,$4)`, reservationID, operationID, base, base.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, `
		CREATE TEMP TABLE ascp_capacity_counters (
			scope text PRIMARY KEY,max_active_operations integer,active_operations integer,updated_at timestamptz
		);
		INSERT INTO ascp_capacity_counters VALUES ('GLOBAL',999,999,now());
		CREATE TEMP TABLE ascp_budget_reservations (reservation_id text,operation_id text,state text);
		CREATE TEMP TABLE ascp_capacity_admissions (
			operation_id text,reservation_id text,scope text,state text,acquired_at timestamptz,
			released_at timestamptz,release_reservation_state text
		)`); err != nil {
		t.Fatal(err)
	}
	var outcome string
	if err := conn.QueryRowContext(ctx, fmt.Sprintf(`SELECT %s.ascp_acquire_capacity($1,$2,1000,$3)`, schema), operationID, reservationID, base).Scan(&outcome); err != nil || outcome != "ACQUIRED" {
		t.Fatalf("secure capacity acquisition outcome=%q err=%v", outcome, err)
	}
	if _, err := conn.ExecContext(ctx, fmt.Sprintf(`UPDATE %s.ascp_budget_reservations SET state='RELEASED' WHERE reservation_id=$1`, schema), reservationID); err != nil {
		t.Fatalf("secure capacity release: %v", err)
	}
	var admissionState string
	var applicationCount, temporaryCount int
	if err := conn.QueryRowContext(ctx, fmt.Sprintf(`SELECT state FROM %s.ascp_capacity_admissions WHERE operation_id=$1`, schema), operationID).Scan(&admissionState); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRowContext(ctx, fmt.Sprintf(`SELECT active_operations FROM %s.ascp_capacity_counters WHERE scope='GLOBAL'`, schema)).Scan(&applicationCount); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRowContext(ctx, `SELECT active_operations FROM pg_temp.ascp_capacity_counters WHERE scope='GLOBAL'`).Scan(&temporaryCount); err != nil {
		t.Fatal(err)
	}
	if admissionState != "RELEASED" || applicationCount != 0 || temporaryCount != 999 {
		t.Fatalf("admission=%s application_count=%d temporary_count=%d", admissionState, applicationCount, temporaryCount)
	}
}

func TestASCPSignerRefusalMigrationReconcilesOnlyUnambiguousLegacyRows(t *testing.T) {
	script, err := migrationFiles.ReadFile("migrations/0025_ascp_signer_refusal_shape.sql")
	if err != nil {
		t.Fatal(err)
	}
	t.Run("reconciles pre-signature refusal", func(t *testing.T) {
		db := ascpRawIntegrationDatabase(t)
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		createLegacyRefusalTables(t, ctx, db)
		if _, err := db.ExecContext(ctx, `
			INSERT INTO ascp_budget_reservations VALUES ('reservation-1','RESERVED');
			INSERT INTO ascp_sign_requests (request_id,reservation_id,state,last_error)
			VALUES ('request-1','reservation-1','REFUSED',NULL);
			INSERT INTO ascp_signer_outbox (request_id,kind,state)
			VALUES ('request-1','SIGN_PREPARE_REQUESTED','PENDING')`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, string(script)); err != nil {
			t.Fatalf("upgrade refused an unambiguous legacy refusal: %v", err)
		}
		var reservationState, requestState, requestError, outboxState, outboxError string
		var cancelled bool
		if err := db.QueryRowContext(ctx, `SELECT reservation.state,request.state,request.last_error,outbox.state,outbox.last_error,outbox.cancelled_at IS NOT NULL
			FROM ascp_sign_requests request
			JOIN ascp_budget_reservations reservation ON reservation.reservation_id=request.reservation_id
			JOIN ascp_signer_outbox outbox ON outbox.request_id=request.request_id`).
			Scan(&reservationState, &requestState, &requestError, &outboxState, &outboxError, &cancelled); err != nil {
			t.Fatal(err)
		}
		if reservationState != "RELEASED" || requestState != "REFUSED" || requestError != "SIGNER_REFUSED" ||
			outboxState != "CANCELLED" || outboxError != "SIGNER_REFUSED" || !cancelled {
			t.Fatalf("reservation=%s request=%s/%s outbox=%s/%s cancelled=%t", reservationState, requestState, requestError, outboxState, outboxError, cancelled)
		}
	})
	progressedAt := time.Unix(1800000000, 0).UTC()
	blockedScenarios := []struct {
		name                string
		reservationState    string
		preparedHandle      any
		primaryMirrorDigest any
		lastError           any
		attemptCount        int
		preparedAt          any
		activatedAt         any
		mirroredAt          any
		acknowledgedAt      any
		unactivatedProof    any
		expiredAt           any
		outboxKind          any
		outboxState         any
	}{
		{name: "prepared handle", reservationState: "RESERVED", preparedHandle: "asph_progressed", outboxKind: "SIGN_PREPARE_REQUESTED", outboxState: "PENDING"},
		{name: "prepared timestamp", reservationState: "RESERVED", preparedAt: progressedAt, outboxKind: "SIGN_PREPARE_REQUESTED", outboxState: "PENDING"},
		{name: "activated timestamp", reservationState: "RESERVED", activatedAt: progressedAt, outboxKind: "SIGN_PREPARE_REQUESTED", outboxState: "PENDING"},
		{name: "mirrored timestamp", reservationState: "RESERVED", mirroredAt: progressedAt, outboxKind: "SIGN_PREPARE_REQUESTED", outboxState: "PENDING"},
		{name: "acknowledged timestamp", reservationState: "RESERVED", acknowledgedAt: progressedAt, outboxKind: "SIGN_PREPARE_REQUESTED", outboxState: "PENDING"},
		{name: "unactivated proof", reservationState: "RESERVED", unactivatedProof: `{}`, outboxKind: "SIGN_PREPARE_REQUESTED", outboxState: "PENDING"},
		{name: "expired timestamp", reservationState: "RESERVED", expiredAt: progressedAt, outboxKind: "SIGN_PREPARE_REQUESTED", outboxState: "PENDING"},
		{name: "live reservation", reservationState: "AUTHORIZATION_LIVE", outboxKind: "SIGN_PREPARE_REQUESTED", outboxState: "PENDING"},
		{name: "delivered prepare outbox", reservationState: "RESERVED", outboxKind: "SIGN_PREPARE_REQUESTED", outboxState: "DELIVERED"},
		{name: "mirror digest without timestamp", reservationState: "RESERVED", primaryMirrorDigest: "0x" + strings.Repeat("a", 64), outboxKind: "SIGN_PREPARE_REQUESTED", outboxState: "PENDING"},
		{name: "ambiguous last error", reservationState: "RESERVED", lastError: "SIGNER_CRASHED", outboxKind: "SIGN_PREPARE_REQUESTED", outboxState: "PENDING"},
		{name: "attempted request", reservationState: "RESERVED", attemptCount: 1, outboxKind: "SIGN_PREPARE_REQUESTED", outboxState: "PENDING"},
		{name: "missing prepare outbox", reservationState: "RESERVED"},
	}
	for _, scenario := range blockedScenarios {
		t.Run("blocks "+scenario.name, func(t *testing.T) {
			db := ascpRawIntegrationDatabase(t)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			createLegacyRefusalTables(t, ctx, db)
			if _, err := db.ExecContext(ctx, `INSERT INTO ascp_budget_reservations VALUES ('reservation-2',$1)`, scenario.reservationState); err != nil {
				t.Fatal(err)
			}
			if _, err := db.ExecContext(ctx, `INSERT INTO ascp_sign_requests
					(request_id,reservation_id,state,prepared_handle,primary_mirror_digest,last_error,attempt_count,
					 prepared_at,activated_at,mirrored_at,acknowledged_at,unactivated_proof,expired_at)
				VALUES ('request-2','reservation-2','REFUSED',$1,$2,$3,$4,$5,$6,$7,$8,$9::jsonb,$10)`, scenario.preparedHandle,
				scenario.primaryMirrorDigest, scenario.lastError, scenario.attemptCount, scenario.preparedAt,
				scenario.activatedAt, scenario.mirroredAt, scenario.acknowledgedAt, scenario.unactivatedProof, scenario.expiredAt); err != nil {
				t.Fatal(err)
			}
			if scenario.outboxKind != nil {
				if _, err := db.ExecContext(ctx, `INSERT INTO ascp_signer_outbox (request_id,kind,state)
					VALUES ('request-2',$1,$2)`, scenario.outboxKind, scenario.outboxState); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := db.ExecContext(ctx, string(script)); err == nil || !strings.Contains(err.Error(), "reconcile them before migration") {
				t.Fatalf("scenario=%s migration error=%v", scenario.name, err)
			}
		})
	}
}

func createLegacyRefusalTables(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	if _, err := db.ExecContext(ctx, `
		CREATE TABLE ascp_budget_reservations (
			reservation_id text PRIMARY KEY,
			state text NOT NULL
		);
		CREATE TABLE ascp_sign_requests (
			request_id text PRIMARY KEY,
			reservation_id text NOT NULL REFERENCES ascp_budget_reservations(reservation_id),
			state text NOT NULL,
			prepared_handle text,
			prepared_at timestamptz,
			activated_at timestamptz,
			mirrored_at timestamptz,
			acknowledged_at timestamptz,
			primary_mirror_digest text,
			lease_owner text,
			lease_token text,
			lease_expires_at timestamptz,
			attempt_count integer NOT NULL DEFAULT 0,
			next_attempt_at timestamptz NOT NULL DEFAULT '-infinity',
			last_error text,
			unactivated_proof jsonb,
			expired_at timestamptz
		);
		CREATE TABLE ascp_signer_outbox (
			request_id text NOT NULL REFERENCES ascp_sign_requests(request_id),
			kind text NOT NULL,
			state text NOT NULL,
			delivered_at timestamptz,
			cancelled_at timestamptz,
			last_error text
		)`); err != nil {
		t.Fatal(err)
	}
}

func TestASCPBearerRuntimeClaimsOnceAndReleasesExpiredReservationAtomically(t *testing.T) {
	db := ascpIntegrationDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	base := time.Now().UTC().Truncate(time.Microsecond)
	operationID, approvalID := ascpIntegrationHash(9101), ascpIntegrationHash(9102)
	reservationID, authorizationID := ascpIntegrationHash(9103), ascpIntegrationHash(9104)

	if _, err := db.ExecContext(ctx, `INSERT INTO organizations (id,name) VALUES ('org_bearer_runtime','Bearer Runtime')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO agents (organization_id,id,customer_id,name,status)
		VALUES ('org_bearer_runtime','agent_bearer_runtime','customer_bearer_runtime','Bearer Agent','ACTIVE')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO ascp_intents
		(operation_id,organization_id,actor_id,endpoint,idempotency_key,canonical_input_hash,quote_hash,
		 purchase_spec_hash,quote_nonce,directory_version,directory_contract,seller_signer,quote_json,
		 purchase_spec_json,purchase_spec_bytes,request_body,created_at)
		VALUES ($1,'org_bearer_runtime','agent_bearer_runtime','ascp.intent.create','bearer_runtime',$2,$3,$4,$5,
		 9,$6,$7,'{}'::jsonb,'{}'::jsonb,'{}'::bytea,''::bytea,$8)`, operationID, fmt.Sprintf("%064x", 9105),
		ascpIntegrationHash(9106), ascpIntegrationHash(9107), ascpIntegrationHash(9108), ascpIntegrationDirectory,
		ascpIntegrationSigner, base); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO ascp_approvals
		(approval_id,organization_id,intent_id,state,review_snapshot_hash,requested_at,expires_at,decided_at,decided_by)
		VALUES ($1,'org_bearer_runtime',$2,'APPROVED',$3,$4,$5,$4,'owner_bearer_runtime')`,
		approvalID, operationID, ascpIntegrationHash(9109), base, base.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO ascp_budget_reservations
		(reservation_id,operation_id,amount_base_units,state,dimensions,created_at,expires_at)
		VALUES ($1,$2,'10','RESERVED','[]'::jsonb,$3,$4)`, reservationID, operationID, base, base.Add(15*time.Minute)); err != nil {
		t.Fatal(err)
	}
	var capacityOutcome string
	if err := db.QueryRowContext(ctx, `SELECT ascp_acquire_capacity($1,$2,1000,$3)`, operationID, reservationID, base).Scan(&capacityOutcome); err != nil || capacityOutcome != "ACQUIRED" {
		t.Fatalf("capacity admission outcome=%q err=%v", capacityOutcome, err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO ascp_execution_authorizations
		(authorization_id,approval_id,intent_id,state,execution_snapshot_hash,reservation_id,created_at,evaluated_at)
		VALUES ($1,$2,$3,'VALIDATED_AND_RESERVED',$4,$5,$6,$6)`, authorizationID, approvalID, operationID,
		ascpIntegrationHash(9110), reservationID, base); err != nil {
		t.Fatal(err)
	}
	bindingStore, err := ascpsignerbinding.NewStore(db, 84532, func() time.Time { return base })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bindingStore.Put(ctx, "org_bearer_runtime", "agent_bearer_runtime", "owner_bearer_runtime", "bind_bearer_runtime_v1", ascpsignerbinding.PutRequest{
		SignerKeyID: "signer-key-runtime", KeyEpoch: 7, ModuleAddress: ascpIntegrationModule,
		SafeAddress: ascpIntegrationSafe, KeeperID: "keeper-runtime", Reason: "Bearer runtime integration binding",
	}); err != nil {
		t.Fatal(err)
	}
	requestStore, err := ascpbearer.NewActivationStore(db, func() time.Time { return base })
	if err != nil {
		t.Fatal(err)
	}
	payload, evidence := []byte("exact-runtime-payload"), []byte("exact-runtime-evidence")
	input := ascpbearer.ActivationInput{
		RequestID: ascpIntegrationHash(9111), AuthorizationID: authorizationID, OperationID: operationID,
		ReservationID: reservationID, ActionID: "bearer-runtime-action", CanonicalPayload: payload,
		CanonicalPayloadHash: ascpbearer.CanonicalPayloadHash(payload), EvidenceBundle: evidence,
		EvidenceBundleHash: ascpbearer.EvidenceBundleHash(evidence), Digest: ascpIntegrationHash(9112),
		Nonce: ascpIntegrationHash(9113), InstrumentType: ascpbearer.InstrumentLockAuthorization,
		SignerBindingVersion: 1, SignerKeyID: "signer-key-runtime", KeyEpoch: 7, ModuleAddress: ascpIntegrationModule,
		SafeAddress: ascpIntegrationSafe, KeeperID: "keeper-runtime", ValidAfter: base, ValidUntil: base.Add(time.Minute),
	}
	request, _, err := requestStore.Request(ctx, input)
	if err != nil {
		t.Fatal(err)
	}

	runtimeNow := base.Add(2 * time.Minute)
	runtimeStore, err := ascpbearer.NewRuntimeActivationStore(db, func() time.Time { return runtimeNow })
	if err != nil {
		t.Fatal(err)
	}
	claim := ascpbearer.RuntimeClaim{WorkerID: "bearer-worker-runtime", SignerKeyID: input.SignerKeyID,
		KeyEpoch: input.KeyEpoch, KeeperID: input.KeeperID, LeaseDuration: 10 * time.Second}
	var wait sync.WaitGroup
	start := make(chan struct{})
	winners := make(chan ascpbearer.RuntimeLease, 8)
	for index := 0; index < 8; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			lease, ok, claimErr := runtimeStore.ClaimExpired(ctx, claim)
			if claimErr != nil {
				t.Errorf("claim expired: %v", claimErr)
				return
			}
			if ok {
				winners <- lease
			}
		}()
	}
	close(start)
	wait.Wait()
	close(winners)
	if len(winners) != 1 {
		t.Fatalf("concurrent workers claimed the same request %d times", len(winners))
	}
	winner := <-winners
	if winner.Request.RequestID != request.RequestID {
		t.Fatalf("claimed request=%s want=%s", winner.Request.RequestID, request.RequestID)
	}
	if _, err := runtimeStore.RecordPrepared(ctx, request.RequestID, "opaque-bypass-handle-0123456789abcdef"); !errors.Is(err, ascpbearer.ErrRuntimeLease) {
		t.Fatalf("unleased signer transition err=%v", err)
	}
	if err := runtimeStore.CompleteLease(ctx, winner); err != nil {
		t.Fatal(err)
	}
	if err := runtimeStore.CompleteLease(ctx, winner); !errors.Is(err, ascpbearer.ErrRuntimeLease) {
		t.Fatalf("stale lease completion err=%v", err)
	}

	service, err := ascpbearer.NewRuntimeService(runtimeStore, &expiryProofSigner{now: runtimeNow}, unusedRuntimeMirror{}, ascpbearer.RuntimeConfig{
		Claim: claim, RetryDelay: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	step, ok, err := service.ExpireOnce(ctx)
	if err != nil || !ok || !step.Expired || step.State != ascpbearer.ExpiredUnactivated {
		t.Fatalf("step=%+v ok=%t err=%v", step, ok, err)
	}
	var reservationState, requestState, outboxState string
	var leaseToken sql.NullString
	var proofPresent bool
	if err := db.QueryRowContext(ctx, `SELECT r.state,s.state,o.state,s.lease_token,s.unactivated_proof IS NOT NULL
		FROM ascp_budget_reservations r JOIN ascp_sign_requests s ON s.reservation_id=r.reservation_id
		JOIN ascp_signer_outbox o ON o.request_id=s.request_id
		WHERE s.request_id=$1 AND o.kind='SIGN_PREPARE_REQUESTED'`, request.RequestID).
		Scan(&reservationState, &requestState, &outboxState, &leaseToken, &proofPresent); err != nil {
		t.Fatal(err)
	}
	if reservationState != "RELEASED" || requestState != string(ascpbearer.ExpiredUnactivated) ||
		outboxState != "CANCELLED" || leaseToken.Valid || !proofPresent {
		t.Fatalf("reservation=%s request=%s outbox=%s lease=%+v proof=%t", reservationState, requestState, outboxState, leaseToken, proofPresent)
	}

	refusedOperation, refusedApproval := ascpIntegrationHash(9121), ascpIntegrationHash(9122)
	refusedReservation, refusedAuthorization := ascpIntegrationHash(9123), ascpIntegrationHash(9124)
	if _, err := db.ExecContext(ctx, `INSERT INTO ascp_intents
		(operation_id,organization_id,actor_id,endpoint,idempotency_key,canonical_input_hash,quote_hash,
		 purchase_spec_hash,quote_nonce,directory_version,directory_contract,seller_signer,quote_json,
		 purchase_spec_json,purchase_spec_bytes,request_body,created_at)
		VALUES ($1,'org_bearer_runtime','agent_bearer_runtime','ascp.intent.create','bearer_refusal',$2,$3,$4,$5,
		 9,$6,$7,'{}'::jsonb,'{}'::jsonb,'{}'::bytea,''::bytea,$8)`, refusedOperation, fmt.Sprintf("%064x", 9125),
		ascpIntegrationHash(9126), ascpIntegrationHash(9127), ascpIntegrationHash(9128), ascpIntegrationDirectory,
		ascpIntegrationSigner, base); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO ascp_approvals
		(approval_id,organization_id,intent_id,state,review_snapshot_hash,requested_at,expires_at,decided_at,decided_by)
		VALUES ($1,'org_bearer_runtime',$2,'APPROVED',$3,$4,$5,$4,'owner_bearer_runtime')`,
		refusedApproval, refusedOperation, ascpIntegrationHash(9129), base, base.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO ascp_budget_reservations
		(reservation_id,operation_id,amount_base_units,state,dimensions,created_at,expires_at)
		VALUES ($1,$2,'10','RESERVED','[]'::jsonb,$3,$4)`, refusedReservation, refusedOperation, base, base.Add(15*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT ascp_acquire_capacity($1,$2,1000,$3)`, refusedOperation, refusedReservation, base).Scan(&capacityOutcome); err != nil || capacityOutcome != "ACQUIRED" {
		t.Fatalf("refused capacity admission outcome=%q err=%v", capacityOutcome, err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO ascp_execution_authorizations
		(authorization_id,approval_id,intent_id,state,execution_snapshot_hash,reservation_id,created_at,evaluated_at)
		VALUES ($1,$2,$3,'VALIDATED_AND_RESERVED',$4,$5,$6,$6)`, refusedAuthorization, refusedApproval, refusedOperation,
		ascpIntegrationHash(9130), refusedReservation, base); err != nil {
		t.Fatal(err)
	}
	refusedInput := input
	refusedInput.RequestID, refusedInput.AuthorizationID, refusedInput.OperationID = ascpIntegrationHash(9131), refusedAuthorization, refusedOperation
	refusedInput.ReservationID, refusedInput.ActionID = refusedReservation, "bearer-runtime-refusal"
	refusedInput.Digest, refusedInput.Nonce = ascpIntegrationHash(9132), ascpIntegrationHash(9133)
	refusedInput.ValidUntil = base.Add(5 * time.Minute)
	if _, _, err := requestStore.Request(ctx, refusedInput); err != nil {
		t.Fatal(err)
	}
	refusalService, err := ascpbearer.NewRuntimeService(runtimeStore, &refusingRuntimeSigner{}, unusedRuntimeMirror{}, ascpbearer.RuntimeConfig{
		Claim: claim, RetryDelay: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	refusedStep, ok, err := refusalService.AdvanceOnce(ctx)
	if err != nil || !ok || !refusedStep.Refused || refusedStep.State != ascpbearer.SignerRefused {
		t.Fatalf("refused step=%+v ok=%t err=%v", refusedStep, ok, err)
	}
	var refusalCode sql.NullString
	proofPresent = true
	if err := db.QueryRowContext(ctx, `SELECT r.state,s.state,o.state,s.lease_token,s.unactivated_proof IS NOT NULL,s.last_error
		FROM ascp_budget_reservations r JOIN ascp_sign_requests s ON s.reservation_id=r.reservation_id
		JOIN ascp_signer_outbox o ON o.request_id=s.request_id
		WHERE s.request_id=$1 AND o.kind='SIGN_PREPARE_REQUESTED'`, refusedInput.RequestID).
		Scan(&reservationState, &requestState, &outboxState, &leaseToken, &proofPresent, &refusalCode); err != nil {
		t.Fatal(err)
	}
	if reservationState != "RELEASED" || requestState != string(ascpbearer.SignerRefused) || outboxState != "CANCELLED" ||
		leaseToken.Valid || proofPresent || !refusalCode.Valid || refusalCode.String != "SIGNER_REFUSED" {
		t.Fatalf("refusal reservation=%s request=%s outbox=%s lease=%+v proof=%t code=%+v", reservationState, requestState, outboxState, leaseToken, proofPresent, refusalCode)
	}
	malformedRefusedUpdates := []struct {
		name       string
		assignment string
	}{
		{name: "prepared handle", assignment: `prepared_handle='asph_invalid'`},
		{name: "prepared timestamp", assignment: `prepared_at=now()`},
		{name: "activated timestamp", assignment: `activated_at=now()`},
		{name: "mirrored timestamp", assignment: `mirrored_at=now()`},
		{name: "acknowledged timestamp", assignment: `acknowledged_at=now()`},
		{name: "primary mirror digest", assignment: `primary_mirror_digest='0x` + strings.Repeat("a", 64) + `'`},
		{name: "unactivated proof", assignment: `unactivated_proof='{}'::jsonb`},
		{name: "expired timestamp", assignment: `expired_at=now()`},
		{name: "lease owner", assignment: `lease_owner='worker-invalid'`},
		{name: "lease token", assignment: `lease_token='0x` + strings.Repeat("b", 64) + `'`},
		{name: "lease expiry", assignment: `lease_expires_at=now()`},
		{name: "missing refusal error", assignment: `last_error=NULL`},
		{name: "wrong refusal error", assignment: `last_error='SIGNER_CRASHED'`},
	}
	for _, mutation := range malformedRefusedUpdates {
		t.Run("constraint rejects "+mutation.name, func(t *testing.T) {
			query := `UPDATE ascp_sign_requests SET ` + mutation.assignment + ` WHERE request_id=$1`
			if _, err := db.ExecContext(ctx, query, refusedInput.RequestID); err == nil {
				t.Fatalf("database accepted malformed REFUSED field: %s", mutation.name)
			}
		})
	}
}
