package controlapi

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
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
	if _, err := db.ExecContext(ctx, `UPDATE ascp_sign_requests SET last_error=NULL WHERE request_id=$1`, refusedInput.RequestID); err == nil {
		t.Fatal("database accepted a malformed refused signer request")
	}
}
