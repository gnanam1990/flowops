package controlapi

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/gnanam1990/flowops/internal/ascpbearer"
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
		RequestID: request.RequestID, ActionID: request.ActionID, InputHash: request.InputHash,
		HandleID: request.PreparedHandle, Status: "EXPIRED_UNACTIVATED", ProvenAt: s.now,
	}
	proof.ProofDigest, _ = ascpbearer.UnactivatedProofDigest(proof)
	return proof, nil
}

type unusedRuntimeMirror struct{}

func (unusedRuntimeMirror) PutPrimary(context.Context, string, []byte, string) error {
	return errors.New("mirror must not run for expired work")
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
		SignerKeyID: "signer-key-runtime", KeyEpoch: 7, ModuleAddress: ascpIntegrationModule,
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
}
