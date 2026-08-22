package controlapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/gnanam1990/flowops/internal/ascpadaptation"
	"github.com/gnanam1990/flowops/internal/ascpapproval"
	"github.com/gnanam1990/flowops/internal/ascpexecauth"
	"github.com/gnanam1990/flowops/internal/ascporchestration"
	"github.com/gnanam1990/flowops/internal/policy"
	"github.com/gnanam1990/flowops/pkg/envelope"
	"github.com/gnanam1990/flowops/pkg/purchasespec"
	"github.com/gnanam1990/flowops/pkg/sellerquote"
)

func TestASCPOrchestrationRealPostgresHumanAndAutomaticPaths(t *testing.T) {
	db := ascpIntegrationDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	now := time.Now().UTC().Truncate(time.Second)
	if _, err := db.ExecContext(ctx, `INSERT INTO organizations (id,name) VALUES ('org_orch_it','Orchestration IT')`); err != nil {
		t.Fatal(err)
	}
	for _, agentID := range []string{"agent_human_it", "agent_auto_it", "agent_adapted_it", "agent_blocked_it", "agent_wrong_seller_it"} {
		if _, err := db.ExecContext(ctx, `
			INSERT INTO agents (organization_id,id,customer_id,name,status)
			VALUES ('org_orch_it',$1,$2,$1,'ACTIVE')`, agentID, "customer_"+agentID); err != nil {
			t.Fatal(err)
		}
	}
	humanPolicy := orchestrationPolicy("policy_human_it", true)
	autoPolicy := orchestrationPolicy("policy_auto_it", false)
	adaptedPolicy := orchestrationPolicy("policy_adapted_it", false)
	adaptedPolicy.PerActionLimitAtomic = "5"
	adaptedPolicy.AutoApproveThresholdAtomic = "5"
	blockedPolicy := orchestrationPolicy("policy_blocked_it", false)
	blockedPolicy.BlockedCategories = []string{"research"}
	wrongSellerPolicy := orchestrationPolicy("policy_wrong_seller_it", false)
	for agentID, config := range map[string]policy.Config{"agent_human_it": humanPolicy, "agent_auto_it": autoPolicy, "agent_adapted_it": adaptedPolicy, "agent_blocked_it": blockedPolicy, "agent_wrong_seller_it": wrongSellerPolicy} {
		raw, _ := json.Marshal(config)
		if _, err := db.ExecContext(ctx, `
			INSERT INTO policies (organization_id,agent_id,version,config,active,activated_at)
			VALUES ('org_orch_it',$1,$2,$3,true,$4)`, agentID, config.Version, raw, now); err != nil {
			t.Fatal(err)
		}
	}
	observationDigest := ascpIntegrationHash(2500)
	sellerID, resourceID, verificationHash := ascpIntegrationHash(2501), ascpIntegrationHash(2502), ascpIntegrationHash(2503)
	if _, err := db.ExecContext(ctx, `
		INSERT INTO ascp_directory_snapshots
			(observation_digest,chain_id,directory_contract,directory_version,directory_root,
			 finalized_block_number,finalized_block_hash,providers,observed_at)
		VALUES ($1,84532,$2,9,$3,200,$4,'["alpha","bravo"]'::jsonb,$5)`,
		observationDigest, ascpIntegrationDirectory, ascpIntegrationHash(2504), ascpIntegrationHash(2505), now); err != nil {
		t.Fatal(err)
	}
	badSellerID, badResourceID := ascpIntegrationHash(2560), ascpIntegrationHash(2561)
	badPayee := "0x9999999999999999999999999999999999999999"
	if _, err := db.ExecContext(ctx, `
		INSERT INTO ascp_directory_quote_evidence
			(observation_digest,seller_id,resource_id,quote_signing_key,key_epoch,payout_address,
			 ack_authority,amount_base_units,verification_spec_hash,declared_work_time,
			 verification_budget_seconds,active,quote_key_revoked)
		VALUES ($1,$2,$3,$4,1,$5,$6,'10',$7,60,30,true,false)`, observationDigest,
		badSellerID, badResourceID, ascpIntegrationSigner, badPayee, ascpIntegrationAck, verificationHash); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO ascp_directory_quote_evidence
			(observation_digest,seller_id,resource_id,quote_signing_key,key_epoch,payout_address,
			 ack_authority,amount_base_units,verification_spec_hash,declared_work_time,
			 verification_budget_seconds,active,quote_key_revoked)
		VALUES ($1,$2,$3,$4,1,$5,$6,'10',$7,60,30,true,false)`, observationDigest,
		sellerID, resourceID, ascpIntegrationSigner, ascpIntegrationPayee, ascpIntegrationAck, verificationHash); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO ascp_directory_heads
			(chain_id,directory_contract,observation_digest,directory_version,finalized_block_number,updated_at)
		VALUES (84532,$1,$2,9,200,$3)`, ascpIntegrationDirectory, observationDigest, now); err != nil {
		t.Fatal(err)
	}
	humanOperation := insertOrchestrationIntent(t, db, now, "agent_human_it", ascpIntegrationHash(2510), ascpIntegrationHash(2511), sellerID, resourceID, verificationHash)
	autoOperation := insertOrchestrationIntent(t, db, now, "agent_auto_it", ascpIntegrationHash(2520), ascpIntegrationHash(2521), sellerID, resourceID, verificationHash)
	originalAdaptationOperation := insertOrchestrationIntent(t, db, now, "agent_adapted_it", ascpIntegrationHash(2530), ascpIntegrationHash(2531), sellerID, resourceID, verificationHash)
	blockedOperation := insertOrchestrationIntent(t, db, now, "agent_blocked_it", ascpIntegrationHash(2550), ascpIntegrationHash(2551), sellerID, resourceID, verificationHash)
	wrongSellerOperation := insertOrchestrationIntentWithPayTo(t, db, now, "agent_wrong_seller_it", ascpIntegrationHash(2562), ascpIntegrationHash(2563), badSellerID, badResourceID, verificationHash, badPayee)
	adaptationKey, err := crypto.HexToECDSA(strings.Repeat("9", 64))
	if err != nil {
		t.Fatal(err)
	}
	var adaptationSignerCalls atomic.Int32
	adaptationIssuer, err := ascpadaptation.NewIssuer(adaptationIntegrationSigner{key: adaptationKey, calls: &adaptationSignerCalls}, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	adaptationStore, err := ascpadaptation.NewPostgresStore(db)
	if err != nil {
		t.Fatal(err)
	}
	adaptations, err := ascpadaptation.NewService(adaptationIssuer, adaptationStore)
	if err != nil {
		t.Fatal(err)
	}

	revalidator, err := ascpexecauth.NewLocalRevalidator(2 * time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	executionStore, err := ascpexecauth.NewPostgresStore(db, revalidator, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	flowStore, err := ascporchestration.NewPostgresStore(db)
	if err != nil {
		t.Fatal(err)
	}
	service, err := ascporchestration.New(ascporchestration.Config{
		DatabaseStore: flowStore, Authorization: executionStore, EscrowContract: ascpIntegrationModule,
		SettleWindow: time.Hour, Clock: func() time.Time { return now }, Adaptations: adaptations,
	})
	if err != nil {
		t.Fatal(err)
	}
	blockedIdentity := ascporchestration.Identity{OrganizationID: "org_orch_it", AgentID: "agent_blocked_it"}
	blockedDecision, err := service.Evaluate(ctx, blockedIdentity, blockedOperation)
	if err != nil || blockedDecision.Outcome != policy.Deny || blockedDecision.Reason != policy.ReasonBlockedCategory || blockedDecision.AdaptationGrantID != "" {
		t.Fatalf("inappropriate decision=%+v err=%v", blockedDecision, err)
	}
	wrongSellerIdentity := ascporchestration.Identity{OrganizationID: "org_orch_it", AgentID: "agent_wrong_seller_it"}
	wrongSellerDecision, err := service.Evaluate(ctx, wrongSellerIdentity, wrongSellerOperation)
	if err != nil || wrongSellerDecision.Outcome != policy.Deny || wrongSellerDecision.Reason != policy.ReasonRecipientNotAllowed || wrongSellerDecision.AdaptationGrantID == "" {
		t.Fatalf("wrong-seller decision=%+v err=%v", wrongSellerDecision, err)
	}
	wrongSellerGrant, err := adaptationStore.GetGrant(ctx, "org_orch_it", "agent_wrong_seller_it", wrongSellerDecision.AdaptationGrantID)
	if err != nil || wrongSellerGrant.ReasonClass != ascpadaptation.ReasonWrongSeller || len(wrongSellerGrant.Artifact.Grant.AllowedSellerSet) != 1 || wrongSellerGrant.Artifact.Grant.AllowedSellerSet[0] != sellerID {
		t.Fatalf("wrong-seller grant=%+v err=%v", wrongSellerGrant, err)
	}
	adaptedIdentity := ascporchestration.Identity{OrganizationID: "org_orch_it", AgentID: "agent_adapted_it"}
	initialAdaptationDecision, err := service.Evaluate(ctx, adaptedIdentity, originalAdaptationOperation)
	if err != nil || initialAdaptationDecision.Outcome != policy.Deny || initialAdaptationDecision.Reason != policy.ReasonPerActionLimit || initialAdaptationDecision.AdaptationGrantID == "" {
		t.Fatalf("initial adaptation decision=%+v err=%v", initialAdaptationDecision, err)
	}
	adaptationGrant, err := adaptationStore.GetGrant(ctx, "org_orch_it", "agent_adapted_it", initialAdaptationDecision.AdaptationGrantID)
	if err != nil || adaptationGrant.Artifact.Grant.MaxAmountAtomic != "5" || adaptationGrant.Artifact.Grant.OriginalIntentID != originalAdaptationOperation {
		t.Fatalf("automatic adaptation grant=%+v err=%v", adaptationGrant, err)
	}
	initialAdaptationReplay, err := service.Evaluate(ctx, adaptedIdentity, originalAdaptationOperation)
	if err != nil || !initialAdaptationReplay.Replayed || initialAdaptationReplay.AdaptationGrantID != initialAdaptationDecision.AdaptationGrantID || adaptationSignerCalls.Load() != 2 {
		t.Fatalf("adaptation replay=%+v signerCalls=%d err=%v", initialAdaptationReplay, adaptationSignerCalls.Load(), err)
	}
	readAdaptationDecision, err := service.Decision(ctx, adaptedIdentity, originalAdaptationOperation)
	if err != nil || readAdaptationDecision.AdaptationGrantID != initialAdaptationDecision.AdaptationGrantID || adaptationSignerCalls.Load() != 2 {
		t.Fatalf("adaptation read=%+v signerCalls=%d err=%v", readAdaptationDecision, adaptationSignerCalls.Load(), err)
	}
	updatedPolicy := adaptedPolicy
	updatedPolicy.Version = "policy_adapted_it_2"
	updatedPolicy.PerActionLimitAtomic, updatedPolicy.AutoApproveThresholdAtomic = "4", "4"
	updatedPolicyJSON, _ := json.Marshal(updatedPolicy)
	policyTx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := policyTx.ExecContext(ctx, `UPDATE policies SET active=false WHERE organization_id='org_orch_it' AND agent_id='agent_adapted_it' AND active=true`); err != nil {
		t.Fatal(err)
	}
	if _, err := policyTx.ExecContext(ctx, `
		INSERT INTO policies (organization_id,agent_id,version,config,active,activated_at)
		VALUES ('org_orch_it','agent_adapted_it',$1,$2,true,$3)`, updatedPolicy.Version, updatedPolicyJSON, now); err != nil {
		t.Fatal(err)
	}
	if err := policyTx.Commit(); err != nil {
		t.Fatal(err)
	}
	adaptedOperation := insertOrchestrationIntentWithAdaptation(t, db, now, "agent_adapted_it", ascpIntegrationHash(2540), ascpIntegrationHash(2541), sellerID, resourceID, verificationHash, adaptationGrant)
	humanIdentity := ascporchestration.Identity{OrganizationID: "org_orch_it", AgentID: "agent_human_it"}
	humanDecision, err := service.Evaluate(ctx, humanIdentity, humanOperation)
	if err != nil || humanDecision.Outcome != policy.RequireApproval || humanDecision.Approval == nil ||
		humanDecision.Commitment.OperationID != humanOperation || humanDecision.CommitmentHash != humanDecision.Review.CommitmentHash {
		t.Fatalf("human decision=%+v err=%v", humanDecision, err)
	}
	replay, err := service.Evaluate(ctx, humanIdentity, humanOperation)
	if err != nil || !replay.Replayed || replay.DecisionID != humanDecision.DecisionID || replay.Approval.ApprovalID != humanDecision.Approval.ApprovalID {
		t.Fatalf("human replay=%+v err=%v", replay, err)
	}
	wrongAgent := ascporchestration.Identity{OrganizationID: "org_orch_it", AgentID: "agent_auto_it"}
	if _, err := service.Decision(ctx, wrongAgent, humanOperation); !errors.Is(err, ascporchestration.ErrNotFound) {
		t.Fatalf("cross-agent decision read error=%v", err)
	}
	if _, err := service.Authorize(ctx, wrongAgent, humanOperation); !errors.Is(err, ascporchestration.ErrNotFound) {
		t.Fatalf("cross-agent authorization error=%v", err)
	}
	if _, err := service.Authorize(ctx, humanIdentity, humanOperation); !errors.Is(err, ascporchestration.ErrApprovalPending) {
		t.Fatalf("pre-approval authorization error=%v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE ascp_approvals SET expires_at=$2 WHERE approval_id=$1`, humanDecision.Approval.ApprovalID, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	expiredExecutionStore, err := ascpexecauth.NewPostgresStore(db, revalidator, func() time.Time { return now.Add(2 * time.Second) })
	if err != nil {
		t.Fatal(err)
	}
	expiredService, err := ascporchestration.New(ascporchestration.Config{
		DatabaseStore: flowStore, Authorization: expiredExecutionStore, EscrowContract: ascpIntegrationModule,
		SettleWindow: time.Hour, Clock: func() time.Time { return now.Add(2 * time.Second) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := expiredService.Authorize(ctx, humanIdentity, humanOperation); !errors.Is(err, ascporchestration.ErrApprovalUnavailable) {
		t.Fatalf("expired pending approval authorization error=%v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE ascp_approvals SET expires_at=$2 WHERE approval_id=$1`, humanDecision.Approval.ApprovalID, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Approval(ctx, "org_other", humanDecision.Approval.ApprovalID); !errors.Is(err, ascporchestration.ErrNotFound) {
		t.Fatalf("cross-tenant approval read error=%v", err)
	}
	approved, err := service.DecideApproval(ctx, "org_orch_it", humanDecision.Approval.ApprovalID, humanDecision.ReviewSnapshotHash, true, "approver_it")
	if err != nil || approved.State != ascpapproval.Approved {
		t.Fatalf("approved=%+v err=%v", approved, err)
	}
	humanAuthorization, err := service.Authorize(ctx, humanIdentity, humanOperation)
	if err != nil || humanAuthorization.State != ascpexecauth.ValidatedAndReserved || humanAuthorization.ApprovalID != approved.ApprovalID {
		t.Fatalf("human authorization=%+v err=%v", humanAuthorization, err)
	}
	humanAuthorizationReplay, err := service.Authorize(ctx, humanIdentity, humanOperation)
	if err != nil || humanAuthorizationReplay.AuthorizationID != humanAuthorization.AuthorizationID {
		t.Fatalf("human authorization replay=%+v err=%v", humanAuthorizationReplay, err)
	}

	autoIdentity := ascporchestration.Identity{OrganizationID: "org_orch_it", AgentID: "agent_auto_it"}
	type decisionResult struct {
		decision ascporchestration.Decision
		err      error
	}
	decisionResults := make(chan decisionResult, 6)
	var wait sync.WaitGroup
	for range 6 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			decision, err := service.Evaluate(ctx, autoIdentity, autoOperation)
			decisionResults <- decisionResult{decision, err}
		}()
	}
	wait.Wait()
	close(decisionResults)
	var autoDecision ascporchestration.Decision
	for result := range decisionResults {
		if result.err != nil || result.decision.Outcome != policy.AutoApprove || result.decision.Approval != nil {
			t.Fatalf("concurrent auto decision=%+v err=%v", result.decision, result.err)
		}
		if autoDecision.DecisionID == "" {
			autoDecision = result.decision
		} else if result.decision.DecisionID != autoDecision.DecisionID {
			t.Fatalf("concurrent decision IDs disagree: %s != %s", result.decision.DecisionID, autoDecision.DecisionID)
		}
	}
	type authorizationResult struct {
		authorization ascporchestration.Authorization
		err           error
	}
	authorizationResults := make(chan authorizationResult, 6)
	for range 6 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			authorization, err := service.Authorize(ctx, autoIdentity, autoOperation)
			authorizationResults <- authorizationResult{authorization, err}
		}()
	}
	wait.Wait()
	close(authorizationResults)
	var autoAuthorization ascporchestration.Authorization
	for result := range authorizationResults {
		if result.err != nil || result.authorization.State != ascpexecauth.ValidatedAndReserved ||
			result.authorization.ApprovalID != "" || result.authorization.DecisionID != autoDecision.DecisionID {
			t.Fatalf("concurrent auto authorization=%+v err=%v", result.authorization, result.err)
		}
		if autoAuthorization.AuthorizationID == "" {
			autoAuthorization = result.authorization
		} else if result.authorization.AuthorizationID != autoAuthorization.AuthorizationID || result.authorization.ReservationID != autoAuthorization.ReservationID {
			t.Fatalf("concurrent authorization disagrees: %+v != %+v", result.authorization, autoAuthorization)
		}
	}
	if _, err := db.ExecContext(ctx, `UPDATE policies SET active=false WHERE organization_id='org_orch_it' AND agent_id='agent_auto_it'`); err != nil {
		t.Fatal(err)
	}
	autoReplayWithoutPolicy, err := service.Evaluate(ctx, autoIdentity, autoOperation)
	if err != nil || !autoReplayWithoutPolicy.Replayed || autoReplayWithoutPolicy.DecisionID != autoDecision.DecisionID {
		t.Fatalf("auto replay without active policy=%+v err=%v", autoReplayWithoutPolicy, err)
	}
	autoAuthorizationReplay, err := service.Authorize(ctx, autoIdentity, autoOperation)
	if err != nil || autoAuthorizationReplay.AuthorizationID != autoAuthorization.AuthorizationID {
		t.Fatalf("auto authorization replay without active policy=%+v err=%v", autoAuthorizationReplay, err)
	}
	adaptedDecision, err := service.Evaluate(ctx, adaptedIdentity, adaptedOperation)
	if err != nil || adaptedDecision.Outcome != policy.RequireApproval || adaptedDecision.Reason != policy.ReasonAdaptationRejected || adaptedDecision.Approval == nil {
		t.Fatalf("second adaptation decision=%+v err=%v", adaptedDecision, err)
	}
	var approvals, decisions, reservations, grantCount int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM ascp_approvals`).Scan(&approvals); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM ascp_policy_decisions`).Scan(&decisions); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM ascp_budget_reservations`).Scan(&reservations); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM ascp_adaptation_grants`).Scan(&grantCount); err != nil {
		t.Fatal(err)
	}
	if approvals != 2 || decisions != 6 || reservations != 2 || grantCount != 2 {
		t.Fatalf("approvals=%d decisions=%d reservations=%d grants=%d", approvals, decisions, reservations, grantCount)
	}
	if _, err := db.ExecContext(ctx, `UPDATE ascp_policy_decisions SET reason='ALLOWED' WHERE decision_id=$1`, autoDecision.DecisionID); err == nil {
		t.Fatal("append-only policy decision accepted an update")
	}
}

func orchestrationPolicy(version string, requireHuman bool) policy.Config {
	config := policy.Config{
		Version: version, Enabled: true, AllowedChainIDs: []uint64{84532},
		AllowedRails: []envelope.Rail{envelope.RailEscrow}, AllowedAssets: []string{ascpIntegrationUSDC},
		AllowedRecipients: []string{ascpIntegrationPayee}, PerActionLimitAtomic: "100",
		AutoApproveThresholdAtomic: "100", TaskBudgetAtomic: "100", DailyBudgetAtomic: "100",
	}
	if requireHuman {
		config.ApprovalRequiredRails = []envelope.Rail{envelope.RailEscrow}
	}
	return config
}

func insertOrchestrationIntent(t *testing.T, db *sql.DB, now time.Time, agentID, operationID, nonce, sellerID, resourceID, verificationHash string) string {
	return insertOrchestrationIntentRecord(t, db, now, agentID, operationID, nonce, sellerID, resourceID, verificationHash, "10", ascpIntegrationPayee, "", "")
}

func insertOrchestrationIntentWithPayTo(t *testing.T, db *sql.DB, now time.Time, agentID, operationID, nonce, sellerID, resourceID, verificationHash, payTo string) string {
	return insertOrchestrationIntentRecord(t, db, now, agentID, operationID, nonce, sellerID, resourceID, verificationHash, "10", payTo, "", "")
}

func insertOrchestrationIntentWithAdaptation(t *testing.T, db *sql.DB, now time.Time, agentID, operationID, nonce, sellerID, resourceID, verificationHash string, grant ascpadaptation.Record) string {
	return insertOrchestrationIntentRecord(t, db, now, agentID, operationID, nonce, sellerID, resourceID, verificationHash, grant.Artifact.Grant.MaxAmountAtomic, ascpIntegrationPayee, grant.Artifact.Grant.GrantID, grant.Digest)
}

func insertOrchestrationIntentRecord(t *testing.T, db *sql.DB, now time.Time, agentID, operationID, nonce, sellerID, resourceID, verificationHash, amount, payTo, grantID, grantDigest string) string {
	t.Helper()
	purchase, err := purchasespec.Build(purchasespec.Input{
		OrgID: "org_orch_it", AgentID: agentID, TaskID: "task_" + agentID,
		Method: "GET", URL: "https://seller.example/v1/report",
		Response: purchasespec.ResponseContract{ContentType: "application/json", SchemaRef: "schema:orchestration-v1"}, Category: "research",
	})
	if err != nil {
		t.Fatal(err)
	}
	quote := sellerquote.Quote{
		PurchaseSpecHash: purchase.PurchaseSpecHash, SellerID: sellerID, ResourceID: resourceID,
		DirectoryVersion: 9, SchemeVersion: 1, ChainID: "84532", Asset: ascpIntegrationUSDC,
		AmountBaseUnits: amount, PayTo: payTo, AckAuthority: ascpIntegrationAck,
		VerificationSpecHash: verificationHash, DeclaredWorkTime: 60, VerificationBudgetSeconds: 30,
		QuoteExpiresAt: uint64(now.Add(time.Hour).Unix()), QuoteNonce: nonce,
	}
	quoteHash, err := quote.Digest(ascpIntegrationDirectory)
	if err != nil {
		t.Fatal(err)
	}
	quoteJSON, _ := json.Marshal(quote)
	tx, err := db.BeginTx(t.Context(), &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(t.Context(), `
		INSERT INTO ascp_intents
			(operation_id,organization_id,actor_id,endpoint,idempotency_key,canonical_input_hash,
			 quote_hash,purchase_spec_hash,quote_nonce,directory_version,directory_contract,seller_signer,
			 quote_json,purchase_spec_json,purchase_spec_bytes,request_body,adaptation_grant_id,adaptation_grant_digest,created_at)
			VALUES ($1,'org_orch_it',$2,'ascp.intent.create',$3,$4,$5,$6,$7,9,$8,$9,$10,$11,$12,$13,NULLIF($14,''),NULLIF($15,''),$16)`,
		operationID, agentID, "idem_"+operationID[2:], operationID[2:], quoteHash.Hex(), purchase.PurchaseSpecHash,
		nonce, ascpIntegrationDirectory, ascpIntegrationSigner, quoteJSON, purchase.CanonicalJSON,
		purchase.CanonicalJSON, []byte{}, grantID, grantDigest, now); err != nil {
		t.Fatal(err)
	}
	if grantID != "" {
		result, err := tx.ExecContext(t.Context(), `
			UPDATE ascp_adaptation_grants
			SET state='CONSUMED',remaining_attempts=0,consumed_operation_id=$2,consumed_at=statement_timestamp()
			WHERE grant_id=$1 AND grant_digest=$3 AND state='ISSUED' AND remaining_attempts=1`, grantID, operationID, grantDigest)
		if err != nil {
			t.Fatal(err)
		}
		if rows, err := result.RowsAffected(); err != nil || rows != 1 {
			t.Fatalf("consume adaptation grant rows=%d err=%v", rows, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return operationID
}
