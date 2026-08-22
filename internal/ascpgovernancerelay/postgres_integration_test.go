package ascpgovernancerelay

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/gnanam1990/flowops/internal/ascpworkflow"
	"github.com/gnanam1990/flowops/internal/controlapi"
	"github.com/gnanam1990/flowops/pkg/governanceworkflow"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

func TestGovernanceRelayRealPostgresExactRetryProofAndTrigger(t *testing.T) {
	db := governanceRelayDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	now := time.Now().UTC().Truncate(time.Second)
	workflowStore, err := ascpworkflow.NewPostgresStore(db)
	if err != nil {
		t.Fatal(err)
	}
	workflowService, err := ascpworkflow.New(workflowStore, nil, func() time.Time { return now }, nil,
		ascpworkflow.WithGovernanceActionGate(relayActionGate{}))
	if err != nil {
		t.Fatal(err)
	}
	workflowID := relayHash(150)
	action := governanceworkflow.Action{Type: governanceworkflow.ActionSpendPause, ChainID: 84532,
		ContractAddress: "0x2222222222222222222222222222222222222222",
		SpendPause:      &governanceworkflow.SpendPauseAction{Current: false, Next: true}}
	proposer := ascpworkflow.Actor{OrganizationID: "org_governance_relay", PrincipalID: "owner-proposer", Role: ascpworkflow.OrgAdmin,
		StepUpAt: now.Add(-time.Minute), StepUpUntil: now.Add(time.Minute)}
	workflow, err := workflowService.Create(ctx, proposer, "create-governance", ascpworkflow.CreateRequest{Kind: ascpworkflow.BreakGlass, WorkflowID: workflowID, Action: &action})
	if err != nil {
		t.Fatal(err)
	}
	approver := ascpworkflow.Actor{OrganizationID: proposer.OrganizationID, PrincipalID: "owner-approver", Role: ascpworkflow.IncidentResponder,
		StepUpAt: now.Add(-time.Minute), StepUpUntil: now.Add(time.Minute)}
	workflow, err = workflowService.Approve(ctx, approver, workflow.WorkflowID, "approve-governance")
	if err != nil || workflow.State != ascpworkflow.ApprovedPendingChain {
		t.Fatalf("workflow=%+v err=%v", workflow, err)
	}
	receiptRecoveryTx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	recoveredHash := relayHash(147)
	if _, err := receiptRecoveryTx.ExecContext(ctx, `INSERT INTO ascp_workflow_receipt_ownership
		(chain_id,transaction_hash,log_index,workflow_id,organization_id,completion_digest,claimed_at)
		VALUES ($1,$2,0,$3,$4,$5,$6)`, workflow.ChainID, recoveredHash, workflowID,
		workflow.OrganizationID, relayHash(146), now); err != nil {
		_ = receiptRecoveryTx.Rollback()
		t.Fatal(err)
	}
	if _, err := receiptRecoveryTx.ExecContext(ctx, `UPDATE ascp_proposal_workflows SET state='SUBMITTED',
		submission_transaction_hash=$2,submitted_at=$3 WHERE workflow_id=$1`, workflowID, recoveredHash, now); err != nil {
		_ = receiptRecoveryTx.Rollback()
		t.Fatalf("database rejected receipt-owned missing submission recovery: %v", err)
	}
	if err := receiptRecoveryTx.Rollback(); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Second)
	fakeOutboxID := relayHash(149)
	if _, err := db.ExecContext(ctx, `INSERT INTO ascp_workflow_outbox
		(outbox_id,workflow_id,organization_id,topic,payload_json,created_at)
		SELECT $1,workflow_id,organization_id,topic,jsonb_set(payload_json,'{payloadHash}',to_jsonb($2::text)),created_at + interval '1 hour'
		FROM ascp_workflow_outbox WHERE workflow_id=$3 AND topic='ascp.governance.execute'`,
		fakeOutboxID, relayHash(148), workflowID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO ascp_governance_relay_jobs
		(outbox_id,workflow_id,organization_id,command_json,state,created_at,updated_at)
		SELECT outbox_id,workflow_id,organization_id,payload_json,'AWAITING_SIGNATURES',$2,$2
		FROM ascp_workflow_outbox WHERE outbox_id=$1`, fakeOutboxID, now); err == nil {
		t.Fatal("database accepted a relay command that diverged from the approved workflow")
	}

	store, err := NewPostgresStore(db, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	job, replay, err := store.ConsumeCommand(ctx)
	if err != nil || replay || job.Command.WorkflowID != workflowID {
		t.Fatalf("job=%+v replay=%t err=%v", job, replay, err)
	}
	keys, owners := relayOwners(t, 3)
	snapshot := relaySnapshot(job.Command, owners, now)
	digest, _ := safeDigestForCommand(job.Command, snapshot)
	prepared, _, err := Prepare(job.Command, snapshot.SafeAddress, snapshot, relaySignatures(t, digest, keys[:2]), 2, now)
	if err != nil {
		t.Fatal(err)
	}
	job, replay, err = store.Authorize(ctx, job.Command.OrganizationID, workflowID, "safe-signatures-1", relayHash(151), prepared, "artifact-safe-1", now)
	if err != nil || replay || job.State != StateReady {
		t.Fatalf("job=%+v replay=%t err=%v", job, replay, err)
	}
	if _, _, err := store.Authorize(ctx, job.Command.OrganizationID, workflowID, "safe-signatures-1", relayHash(152), prepared, "artifact-safe-2", now); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("authorization substitution err=%v", err)
	}
	lease, err := store.ClaimRelay(ctx, "relay-worker", 20*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	outerOne := OuterArtifact{Handle: "outer-one", TransactionHash: relayHash(153), SafeTxHash: prepared.SafeTxHash,
		ExecCalldataHash: prepared.ExecCalldataHash, PreparedAt: now}
	job, err = store.RecordOuterPrepared(ctx, lease, outerOne, now)
	if err != nil || job.State != StateBroadcasting {
		t.Fatalf("job=%+v err=%v", job, err)
	}
	lease.Job = job
	if _, err := workflowService.RecordSubmission(ctx, job.Command.OrganizationID, workflowID, outerOne.TransactionHash); err != nil {
		t.Fatal(err)
	}
	job, err = store.RecordSubmitted(ctx, lease, outerOne.TransactionHash, now)
	if err != nil || job.AttemptCount != 1 {
		t.Fatalf("job=%+v err=%v", job, err)
	}
	if err := store.ReleaseLease(ctx, lease); err != nil {
		t.Fatal(err)
	}

	observationLease, err := store.ClaimObservation(ctx, "relay-worker", 20*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	evidence := exactOutcome(prepared, outerOne.TransactionHash, OutcomeDropped, false, now)
	decision, err := DecideRetry(prepared, outerOne.TransactionHash, evidence, 2, now)
	if err != nil || decision.Decision != DecisionRetryExact {
		t.Fatalf("decision=%+v err=%v", decision, err)
	}
	if _, err := workflowService.RecordChainFailure(ctx, job.Command.OrganizationID, workflowID, ascpworkflow.TimedOut, ascpworkflow.SubmissionTimeout); err != nil {
		t.Fatal(err)
	}
	malformedEvidence := evidence
	malformedEvidence.SafeTxHash = relayHash(155)
	malformedJSON, err := json.Marshal(malformedEvidence)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE ascp_governance_relay_jobs
		SET state='RETRYABLE_EXACT',last_outcome_json=$2::jsonb WHERE workflow_id=$1`, workflowID, malformedJSON); err == nil {
		t.Fatal("database accepted retryable state with substituted Safe transaction hash")
	}
	if _, err := db.ExecContext(ctx, `UPDATE ascp_proposal_workflows SET state='SUBMITTED',
		submission_transaction_hash=$2,submitted_at=$3,terminal_reason=NULL,terminal_at=NULL
		WHERE workflow_id=$1`, workflowID, relayHash(156), now); err == nil {
		t.Fatal("database accepted workflow retry without a joined Safe retry proof")
	}
	job, err = store.ApplyDecision(ctx, observationLease, evidence, decision, now)
	if err != nil || job.State != StateRetryable {
		t.Fatalf("job=%+v err=%v", job, err)
	}
	now = now.Add(time.Second)
	refreshedEvidence := exactOutcome(prepared, outerOne.TransactionHash, OutcomeDropped, false, now)
	refreshedDecision, err := DecideRetry(prepared, outerOne.TransactionHash, refreshedEvidence, 2, now)
	if err != nil {
		t.Fatal(err)
	}
	observationLease.Job = job
	job, err = store.ApplyDecision(ctx, observationLease, refreshedEvidence, refreshedDecision, now)
	if err != nil || job.State != StateRetryable || !job.LastOutcome.ObservedAt.Equal(now) {
		t.Fatalf("refreshed retry job=%+v err=%v", job, err)
	}
	if err := store.ReleaseLease(ctx, observationLease); err != nil {
		t.Fatal(err)
	}

	retryLease, err := store.ClaimRelay(ctx, "relay-worker", 20*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	outerTwo := OuterArtifact{Handle: "outer-two", TransactionHash: relayHash(154), SafeTxHash: prepared.SafeTxHash,
		ExecCalldataHash: prepared.ExecCalldataHash, PreparedAt: now}
	job, err = store.RecordOuterPrepared(ctx, retryLease, outerTwo, now)
	if err != nil || job.State != StateBroadcasting {
		t.Fatalf("job=%+v err=%v", job, err)
	}
	retryLease.Job = job
	wrongRetryHash := relayHash(157)
	wrongProof := safeRetryProof(job, wrongRetryHash)
	if _, err := workflowService.RecordProvenRetry(ctx, job.Command.OrganizationID, workflowID,
		wrongRetryHash, wrongProof); err == nil {
		t.Fatal("database accepted a retry transaction hash that was not the persisted outer artifact")
	}
	proof := safeRetryProof(job, outerTwo.TransactionHash)
	workflow, err = workflowService.RecordProvenRetry(ctx, job.Command.OrganizationID, workflowID, outerTwo.TransactionHash, proof)
	if err != nil || workflow.State != ascpworkflow.Submitted || workflow.SubmissionTxHash != outerTwo.TransactionHash {
		t.Fatalf("workflow=%+v proof=%+v err=%v", workflow, proof, err)
	}
	if replay, err := workflowService.RecordProvenRetry(ctx, job.Command.OrganizationID, workflowID, outerTwo.TransactionHash, proof); err != nil || !replay.Replayed {
		t.Fatalf("exact PostgreSQL retry replay=%+v err=%v", replay, err)
	}
	mutatedProof := proof
	mutatedProof.EvidenceDigest = relayHash(158)
	if _, err := workflowService.RecordProvenRetry(ctx, job.Command.OrganizationID, workflowID, outerTwo.TransactionHash, mutatedProof); !errors.Is(err, ascpworkflow.ErrStateConflict) {
		t.Fatalf("mutated PostgreSQL retry replay err=%v", err)
	}
	job, err = store.RecordSubmitted(ctx, retryLease, outerTwo.TransactionHash, now)
	if err != nil || job.AttemptCount != 2 {
		t.Fatalf("job=%+v err=%v", job, err)
	}
	if err := store.ReleaseLease(ctx, retryLease); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE ascp_proposal_workflows SET state='CONFIRMED',
		submission_transaction_hash=$2,confirmed_at=$3 WHERE workflow_id=$1`, workflowID, relayHash(160), now); err == nil {
		t.Fatal("database accepted an unowned, unrecorded canonical winner")
	}
	canonicalWinnerTx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := canonicalWinnerTx.ExecContext(ctx, `INSERT INTO ascp_workflow_receipt_ownership
		(chain_id,transaction_hash,log_index,workflow_id,organization_id,completion_digest,claimed_at)
		VALUES ($1,$2,0,$3,$4,$5,$6)`, job.Command.ChainID, outerOne.TransactionHash, workflowID,
		job.Command.OrganizationID, relayHash(159), now); err != nil {
		_ = canonicalWinnerTx.Rollback()
		t.Fatal(err)
	}
	if _, err := canonicalWinnerTx.ExecContext(ctx, `UPDATE ascp_proposal_workflows SET state='CONFIRMED',
		submission_transaction_hash=$2,confirmed_at=$3 WHERE workflow_id=$1`, workflowID, outerOne.TransactionHash, now); err != nil {
		_ = canonicalWinnerTx.Rollback()
		t.Fatalf("database rejected the owned earlier proven Safe winner: %v", err)
	}
	if err := canonicalWinnerTx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if _, err := workflowService.RecordChainFailure(ctx, job.Command.OrganizationID, workflowID,
		ascpworkflow.TimedOut, ascpworkflow.SubmissionTimeout); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO ascp_workflow_receipt_ownership
		(chain_id,transaction_hash,log_index,workflow_id,organization_id,completion_digest,claimed_at)
		VALUES ($1,$2,0,$3,$4,$5,$6)`, job.Command.ChainID, outerOne.TransactionHash, workflowID,
		job.Command.OrganizationID, relayHash(159), now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE ascp_proposal_workflows SET state='SUBMITTED',
		submission_transaction_hash=$2,submitted_at=$3,confirmed_at=NULL,terminal_reason=NULL,terminal_at=NULL
		WHERE workflow_id=$1`, workflowID, relayHash(161), now); err == nil {
		t.Fatal("database accepted a side-state receipt from an unrecorded transaction")
	}
	if _, err := db.ExecContext(ctx, `UPDATE ascp_proposal_workflows SET state='SUBMITTED',
		submission_transaction_hash=$2,submitted_at=$3,confirmed_at=NULL,terminal_reason=NULL,terminal_at=NULL
		WHERE workflow_id=$1`, workflowID, outerOne.TransactionHash, now); err != nil {
		t.Fatalf("database rejected a finalized receipt from the earlier proven Safe attempt: %v", err)
	}

	if _, err := db.ExecContext(ctx, `UPDATE ascp_workflow_safe_retry_proofs SET safe_nonce=safe_nonce+1 WHERE workflow_id=$1`, workflowID); err == nil {
		t.Fatal("database accepted Safe retry proof mutation")
	}
	if _, err := db.ExecContext(ctx, `UPDATE ascp_governance_relay_jobs SET command_json=jsonb_set(command_json,'{value}','\"1\"') WHERE workflow_id=$1`, workflowID); err == nil {
		t.Fatal("database accepted relay command mutation")
	}
	if _, err := db.ExecContext(ctx, `UPDATE ascp_governance_relay_jobs SET attempt_count=11 WHERE workflow_id=$1`, workflowID); err == nil {
		t.Fatal("database accepted an eleventh submitted outer attempt")
	}
}

type relayActionGate struct{}

func (relayActionGate) ValidateGovernanceAction(governanceworkflow.BoundAction) error { return nil }

func governanceRelayDatabase(t *testing.T) *sql.DB {
	t.Helper()
	databaseURL := os.Getenv("FLOWOPS_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("FLOWOPS_TEST_DATABASE_URL is not configured")
	}
	admin, err := sql.Open("pgx", databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("flowops_governance_relay_it_%d", time.Now().UnixNano())
	if _, err := admin.Exec(`CREATE SCHEMA ` + schema); err != nil {
		t.Fatal(err)
	}
	config, err := pgx.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	config.RuntimeParams["search_path"] = schema
	db := stdlib.OpenDB(*config)
	if err := controlapi.ApplyMigrations(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO organizations (id,name,created_at) VALUES ('org_governance_relay','Governance relay test',$1)`, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = db.Close()
		_, _ = admin.Exec(`DROP SCHEMA IF EXISTS ` + schema + ` CASCADE`)
		_ = admin.Close()
	})
	return db
}
