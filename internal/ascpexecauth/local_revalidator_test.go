package ascpexecauth

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gnanam1990/flowops/internal/ascpapproval"
	"github.com/gnanam1990/flowops/internal/ascpreservation"
	"github.com/gnanam1990/flowops/internal/policy"
	"github.com/gnanam1990/flowops/pkg/envelope"
	"github.com/gnanam1990/flowops/pkg/purchasespec"
	"github.com/gnanam1990/flowops/pkg/sellerquote"
)

const (
	revalidationActor  = "agent_acme"
	revalidationSigner = "0x5555555555555555555555555555555555555555"
	revalidationDir    = "0x6666666666666666666666666666666666666666"
)

func TestLocalRevalidatorCapsDirectoryFreshnessWindow(t *testing.T) {
	if _, err := NewLocalRevalidator(maximumDirectoryAge + time.Nanosecond); err == nil {
		t.Fatal("expected unsafe freshness window to be rejected")
	}
}

func TestLocalRevalidatorBindsCurrentPolicyDirectoryAndExecutionSnapshot(t *testing.T) {
	db, mock := postgresMock(t)
	input, config, quote, observationDigest := localRevalidationFixture(t)
	tx := beginRevalidation(t, db, mock)
	expectLocalIntent(mock, input, quote)
	expectLocalAgent(mock, nil, "ACTIVE", false)
	expectLocalPolicy(mock, input, config)
	expectLocalAssetHealth(mock, input, "NORMAL")
	expectLocalDirectory(mock, input, quote, observationDigest, false, authorizationNow)

	revalidator, err := NewLocalRevalidator(2 * time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	reason, err := revalidator.Revalidate(context.Background(), tx, input, authorizationNow)
	if err != nil || reason != "" {
		t.Fatalf("reason=%q err=%v", reason, err)
	}
	mock.ExpectRollback()
	_ = tx.Rollback()
	expectationsMet(t, mock)
}

func TestLocalRevalidatorTreatsDatabaseFailureAsRollbackNotBusinessInvalidation(t *testing.T) {
	db, mock := postgresMock(t)
	input, _, quote, _ := localRevalidationFixture(t)
	tx := beginRevalidation(t, db, mock)
	expectLocalIntent(mock, input, quote)
	transient := errors.New("database unavailable")
	expectLocalAgent(mock, transient, "", false)
	revalidator, _ := NewLocalRevalidator(time.Minute)
	reason, err := revalidator.Revalidate(context.Background(), tx, input, authorizationNow)
	if reason != "" || !errors.Is(err, transient) {
		t.Fatalf("reason=%q err=%v", reason, err)
	}
	mock.ExpectRollback()
	_ = tx.Rollback()
	expectationsMet(t, mock)
}

func TestLocalRevalidatorInvalidatesRevokedCurrentQuoteKey(t *testing.T) {
	db, mock := postgresMock(t)
	input, config, quote, observationDigest := localRevalidationFixture(t)
	tx := beginRevalidation(t, db, mock)
	expectLocalIntent(mock, input, quote)
	expectLocalAgent(mock, nil, "ACTIVE", false)
	expectLocalPolicy(mock, input, config)
	expectLocalAssetHealth(mock, input, "NORMAL")
	expectLocalDirectory(mock, input, quote, observationDigest, true, authorizationNow)
	revalidator, _ := NewLocalRevalidator(time.Minute)
	reason, err := revalidator.Revalidate(context.Background(), tx, input, authorizationNow)
	if err != nil || reason != reasonSellerUnavailable {
		t.Fatalf("reason=%q err=%v", reason, err)
	}
	mock.ExpectRollback()
	_ = tx.Rollback()
	expectationsMet(t, mock)
}

func TestLocalRevalidatorRejectsCallerInventedBudgetDimensionAndLimit(t *testing.T) {
	db, mock := postgresMock(t)
	input, config, quote, observationDigest := localRevalidationFixture(t)
	input.Reservation.Dimensions = []ascpreservation.Dimension{{ID: "attacker:unique:per-operation", Limit: "999999999", Refundable: true}}
	var err error
	input.ExecutionSnapshotHash, err = ExecutionSnapshotHash(input, authorizationOrganizationID, revalidationActor, observationDigest)
	if err != nil {
		t.Fatal(err)
	}
	tx := beginRevalidation(t, db, mock)
	expectLocalIntent(mock, input, quote)
	expectLocalAgent(mock, nil, "ACTIVE", false)
	expectLocalPolicy(mock, input, config)
	revalidator, _ := NewLocalRevalidator(2 * time.Minute)
	reason, err := revalidator.Revalidate(context.Background(), tx, input, authorizationNow)
	if err != nil || reason != reasonBudgetDimensions {
		t.Fatalf("reason=%q err=%v", reason, err)
	}
	mock.ExpectRollback()
	_ = tx.Rollback()
	expectationsMet(t, mock)
}

func TestLocalRevalidatorRejectsNonNormalAssetHealth(t *testing.T) {
	db, mock := postgresMock(t)
	input, config, quote, _ := localRevalidationFixture(t)
	tx := beginRevalidation(t, db, mock)
	expectLocalIntent(mock, input, quote)
	expectLocalAgent(mock, nil, "ACTIVE", false)
	expectLocalPolicy(mock, input, config)
	expectLocalAssetHealth(mock, input, "TOKEN_PAUSED")
	revalidator, _ := NewLocalRevalidator(time.Minute)
	reason, err := revalidator.Revalidate(context.Background(), tx, input, authorizationNow)
	if err != nil || reason != reasonAssetUnhealthy {
		t.Fatalf("reason=%q err=%v", reason, err)
	}
	mock.ExpectRollback()
	_ = tx.Rollback()
	expectationsMet(t, mock)
}

func TestLocalRevalidatorRejectsStaleNormalAssetHealth(t *testing.T) {
	db, mock := postgresMock(t)
	input, config, quote, _ := localRevalidationFixture(t)
	tx := beginRevalidation(t, db, mock)
	expectLocalIntent(mock, input, quote)
	expectLocalAgent(mock, nil, "ACTIVE", false)
	expectLocalPolicy(mock, input, config)
	expectLocalAssetHealthAt(mock, input, "NORMAL", authorizationNow.Add(-2*time.Minute))
	revalidator, _ := NewLocalRevalidator(time.Minute)
	reason, err := revalidator.Revalidate(context.Background(), tx, input, authorizationNow)
	if err != nil || reason != reasonAssetHealthUnavailable {
		t.Fatalf("reason=%q err=%v", reason, err)
	}
	mock.ExpectRollback()
	_ = tx.Rollback()
	expectationsMet(t, mock)
}

func localRevalidationFixture(t *testing.T) (Input, policy.Config, sellerquote.Quote, string) {
	t.Helper()
	input := postgresInput()
	purchase, err := purchasespec.Build(purchasespec.Input{
		OrgID: authorizationOrganizationID, AgentID: revalidationActor, TaskID: "task_research",
		Method: "GET", URL: "https://seller.example/v1/report",
		Response: purchasespec.ResponseContract{ContentType: "application/json", SchemaRef: "schema:report-v1"}, Category: "research",
	})
	if err != nil {
		t.Fatal(err)
	}
	config := policy.Config{
		Version: "policy_1", Enabled: true, AllowedChainIDs: []uint64{84532},
		AllowedRails: []envelope.Rail{envelope.RailEscrow}, AllowedAssets: []string{input.Review.Asset},
		AllowedRecipients: []string{input.Review.PayTo}, ApprovalRequiredRails: []envelope.Rail{envelope.RailEscrow},
		PerActionLimitAtomic: "100", AutoApproveThresholdAtomic: "10", TaskBudgetAtomic: "1000", DailyBudgetAtomic: "2000",
	}
	policyHash, err := policy.ConfigHash(config)
	if err != nil {
		t.Fatal(err)
	}
	input.Review.PolicyHash = policyHash
	snapshot, err := ascpapproval.ReviewHash(input.Review)
	if err != nil {
		t.Fatal(err)
	}
	input.ApprovalSnapshotHash = snapshot
	quote := sellerquote.Quote{
		PurchaseSpecHash: purchase.PurchaseSpecHash, SellerID: testHash(21), ResourceID: testHash(22),
		DirectoryVersion: input.Review.DirectoryVersion, SchemeVersion: 1, ChainID: input.Review.ChainID,
		Asset: input.Review.Asset, AmountBaseUnits: input.Review.AmountBaseUnits, PayTo: input.Review.PayTo,
		AckAuthority: input.Review.AckAuthority, VerificationSpecHash: input.Review.VerificationSpecHash,
		DeclaredWorkTime: 60, VerificationBudgetSeconds: 30, QuoteExpiresAt: uint64(authorizationNow.Add(time.Hour).Unix()),
		QuoteNonce: testHash(23),
	}
	input.Reservation.Dimensions, err = RequiredBudgetDimensions(config, purchase.Spec, authorizationNow)
	if err != nil {
		t.Fatal(err)
	}
	observationDigest := testHash(24)
	input.ExecutionSnapshotHash, err = ExecutionSnapshotHash(input, authorizationOrganizationID, revalidationActor, observationDigest)
	if err != nil {
		t.Fatal(err)
	}
	return input, config, quote, observationDigest
}

func beginRevalidation(t *testing.T, db *sql.DB, mock sqlmock.Sqlmock) *sql.Tx {
	t.Helper()
	mock.ExpectBegin()
	tx, err := db.BeginTx(context.Background(), &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		t.Fatal(err)
	}
	return tx
}

func expectLocalIntent(mock sqlmock.Sqlmock, input Input, quote sellerquote.Quote) {
	quoteJSON, _ := json.Marshal(quote)
	purchase, err := purchasespec.Build(purchasespec.Input{
		OrgID: authorizationOrganizationID, AgentID: revalidationActor, TaskID: "task_research",
		Method: "GET", URL: "https://seller.example/v1/report",
		Response: purchasespec.ResponseContract{ContentType: "application/json", SchemaRef: "schema:report-v1"}, Category: "research",
	})
	if err != nil {
		panic(err)
	}
	mock.ExpectQuery(regexp.QuoteMeta("SELECT organization_id, actor_id, directory_version, directory_contract, seller_signer,")).
		WithArgs(input.IntentID).
		WillReturnRows(sqlmock.NewRows([]string{"organization_id", "actor_id", "directory_version", "directory_contract", "seller_signer", "quote_json", "purchase_spec_hash", "purchase_spec_bytes", "request_body"}).
			AddRow(authorizationOrganizationID, revalidationActor, input.Review.DirectoryVersion, revalidationDir,
				revalidationSigner, quoteJSON, purchase.PurchaseSpecHash, purchase.CanonicalJSON, []byte{}))
}

func expectLocalAgent(mock sqlmock.Sqlmock, failure error, status string, paused bool) {
	expectation := mock.ExpectQuery(regexp.QuoteMeta("SELECT a.status, o.authorizations_paused")).
		WithArgs(authorizationOrganizationID, revalidationActor)
	if failure != nil {
		expectation.WillReturnError(failure)
		return
	}
	expectation.WillReturnRows(sqlmock.NewRows([]string{"status", "authorizations_paused"}).AddRow(status, paused))
}

func expectLocalPolicy(mock sqlmock.Sqlmock, input Input, config policy.Config) {
	configJSON, _ := json.Marshal(config)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT version, config")).
		WithArgs(authorizationOrganizationID, revalidationActor).
		WillReturnRows(sqlmock.NewRows([]string{"version", "config"}).AddRow(input.Review.PolicyVersion, configJSON))
}

func expectLocalDirectory(mock sqlmock.Sqlmock, input Input, quote sellerquote.Quote, observationDigest string, revoked bool, observedAt time.Time) {
	mock.ExpectQuery(regexp.QuoteMeta("SELECT h.directory_version, h.observation_digest, s.finalized_block_number, s.observed_at")).
		WithArgs(uint64(84532), revalidationDir, quote.SellerID, quote.ResourceID).
		WillReturnRows(sqlmock.NewRows([]string{
			"directory_version", "observation_digest", "finalized_block_number", "observed_at", "quote_signing_key", "key_epoch",
			"payout_address", "ack_authority", "amount_base_units", "verification_spec_hash", "declared_work_time",
			"verification_budget_seconds", "active", "quote_key_revoked",
		}).AddRow(input.Review.DirectoryVersion, observationDigest, uint64(100), observedAt, revalidationSigner, uint64(2),
			quote.PayTo, quote.AckAuthority, quote.AmountBaseUnits, quote.VerificationSpecHash, quote.DeclaredWorkTime,
			quote.VerificationBudgetSeconds, true, revoked))
}

func expectLocalAssetHealth(mock sqlmock.Sqlmock, input Input, state string) {
	expectLocalAssetHealthAt(mock, input, state, authorizationNow)
}

func expectLocalAssetHealthAt(mock sqlmock.Sqlmock, input Input, state string, observedAt time.Time) {
	mock.ExpectQuery(regexp.QuoteMeta("SELECT state,observed_at FROM ascp_asset_health")).
		WithArgs(uint64(84532), input.Review.Asset).
		WillReturnRows(sqlmock.NewRows([]string{"state", "observed_at"}).AddRow(state, observedAt))
}
