package ascporchestration

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gnanam1990/flowops/internal/ascpapproval"
	"github.com/gnanam1990/flowops/internal/policy"
	"github.com/gnanam1990/flowops/pkg/executioncommitment"
)

func TestPostgresEvaluateReplaysBeforeReadingMutableOperationMaterial(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, err := NewPostgresStore(db)
	if err != nil {
		t.Fatal(err)
	}

	identity := Identity{OrganizationID: "org_a", AgentID: "agent_a"}
	operationID := testHash(2)
	policyHash := testHash(9)
	commitment := executioncommitment.Commitment{
		OrgDomain: testHash(1), OperationID: operationID,
		Rail: executioncommitment.RailEscrow, SchemeVersion: executioncommitment.SchemeVersionV1,
		Protection:       executioncommitment.ProtectionEscrow,
		EscrowContract:   "0x1111111111111111111111111111111111111111",
		PurchaseSpecHash: testHash(3), QuoteHash: testHash(4), VerificationSpecHash: testHash(5),
		DeclaredWorkTime: 300, VerificationBudgetSeconds: 120, DirectoryVersion: 9,
		SellerID: testHash(6), ResourceID: testHash(7),
		PayTo:        "0x3333333333333333333333333333333333333333",
		AckAuthority: "0x4444444444444444444444444444444444444444",
		Amount:       "42", ChainID: "84532", Asset: "0x036cbd53842c5426634e7929541ec2318f3dcf7e",
		QuoteExpiresAt: 1_900_000_000, AcceptBy: 1_900_000_100,
		DeliverBy: 1_900_000_500, SettleBy: 1_900_002_400,
	}
	commitmentHash, err := commitment.Digest(commitment.EscrowContract, commitment.ChainID)
	if err != nil {
		t.Fatal(err)
	}
	review := ascpapproval.Review{
		CommitmentHash: commitmentHash.Hex(), PolicyVersion: "policy_v1", PolicyHash: policyHash,
		DirectoryVersion: commitment.DirectoryVersion, PayTo: commitment.PayTo,
		AckAuthority: commitment.AckAuthority, AmountBaseUnits: commitment.Amount,
		VerificationSpecHash: commitment.VerificationSpecHash, Protection: "ESCROW",
		ChainID: commitment.ChainID, Asset: commitment.Asset,
	}
	reviewHash, err := ascpapproval.ReviewHash(review)
	if err != nil {
		t.Fatal(err)
	}
	commitmentJSON, err := json.Marshal(commitment)
	if err != nil {
		t.Fatal(err)
	}
	reviewJSON, err := json.Marshal(review)
	if err != nil {
		t.Fatal(err)
	}
	evaluatedAt := time.Unix(1_800_000_000, 0).UTC()

	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)SELECT d\.decision_id.*FROM ascp_policy_decisions d`).
		WithArgs(operationID, identity.OrganizationID, identity.AgentID).
		WillReturnRows(sqlmock.NewRows([]string{
			"decision_id", "organization_id", "agent_id", "operation_id", "outcome", "reason",
			"policy_version", "policy_hash", "commitment_hash", "commitment_json", "review_json",
			"review_snapshot_hash", "evaluated_at", "approval_id", "approval_state",
			"approval_snapshot", "approval_requested", "approval_expires", "approval_decided",
			"decided_by", "cancel_reason",
		}).AddRow(
			testHash(8), identity.OrganizationID, identity.AgentID, operationID,
			policy.AutoApprove, policy.ReasonAllowed, "policy_v1", policyHash,
			commitmentHash.Hex(), commitmentJSON, reviewJSON, reviewHash, evaluatedAt,
			nil, nil, nil, nil, nil, nil, nil, nil,
		))
	mock.ExpectCommit()

	decision, err := store.Evaluate(context.Background(), identity, operationID, EvaluationConfig{
		DecisionID: testHash(10), ApprovalID: testHash(11), Now: evaluatedAt.Add(24 * time.Hour),
		EscrowContract: commitment.EscrowContract, AcceptWindow: 5 * time.Minute, SettleWindow: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !decision.Replayed || decision.DecisionID != testHash(8) || decision.OperationID != operationID {
		t.Fatalf("decision=%+v", decision)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("replay touched mutable operation material: %v", err)
	}
}
