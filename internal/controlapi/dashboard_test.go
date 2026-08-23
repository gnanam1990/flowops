package controlapi

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gnanam1990/flowops/internal/ascpexecauth"
	"github.com/gnanam1990/flowops/internal/policy"
	"github.com/gnanam1990/flowops/pkg/envelope"
)

func TestPostgresDashboardProjectionUsesDurableASCPRecords(t *testing.T) {
	store, mock, db := newMockStore(t)
	defer db.Close()
	now := time.Date(2030, 1, 15, 12, 0, 0, 0, time.UTC)
	asset := "0x1111111111111111111111111111111111111111"
	recipient := "0x2222222222222222222222222222222222222222"
	approvalID := "0x" + strings.Repeat("a", 64)
	digest := "0x" + strings.Repeat("b", 64)
	operationID := "0x" + strings.Repeat("c", 64)

	mock.ExpectQuery(`SELECT a\.approval_id`).WithArgs("org_a", now).WillReturnRows(sqlmock.NewRows([]string{
		"approval_id", "review_snapshot_hash", "operation_id", "actor_id", "task_id", "category", "reason", "policy_version",
		"pay_to", "asset", "amount", "requested_at", "expires_at",
	}).AddRow(approvalID, digest, operationID, "agent_a", "task_a", "research", "HUMAN_APPROVAL_THRESHOLD", "policy_1",
		recipient, asset, "25", now.Add(-time.Minute), now.Add(time.Hour)))

	dayStart := time.Date(2030, 1, 15, 0, 0, 0, 0, time.UTC)
	mock.ExpectQuery(`SELECT t\.asset,p\.account`).WithArgs("org_a", dayStart, dayStart.Add(24*time.Hour)).WillReturnRows(sqlmock.NewRows([]string{
		"asset", "account", "amount_base_units", "today_base_units",
	}).AddRow(asset, "SellerExpense", "25", "25").AddRow(asset, "EscrowRestrictedUSDC", "-25", "-25"))
	mock.ExpectQuery(`SELECT o\.asset`).WithArgs("org_a").WillReturnRows(sqlmock.NewRows([]string{
		"asset", "reserved_atomic", "pending_atomic", "unresolved_atomic",
	}).AddRow(asset, "25", "25", "0"))

	config := policy.Config{
		Version: "policy_1", Enabled: true, AllowedChainIDs: []uint64{84532}, AllowedRails: []envelope.Rail{envelope.RailEscrow},
		AllowedAssets: []string{asset}, AllowedRecipients: []string{recipient}, PerActionLimitAtomic: "100",
		AutoApproveThresholdAtomic: "10", TaskBudgetAtomic: "500", DailyBudgetAtomic: "1000",
	}
	policyJSON, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	mock.ExpectQuery(`SELECT a\.id,p\.version,p\.config`).WithArgs("org_a").WillReturnRows(
		sqlmock.NewRows([]string{"id", "version", "config"}).AddRow("agent_a", "policy_1", policyJSON))
	dimension := ascpexecauth.BudgetDimensionID(ascpexecauth.BudgetDimensionAgentDay, "org_a", "agent_a", "2030-01-15")
	dimensionJSON, _ := json.Marshal([]string{dimension})
	mock.ExpectQuery(`SELECT i\.actor_id,rd\.dimension_id`).WithArgs("org_a", string(dimensionJSON)).WillReturnRows(sqlmock.NewRows([]string{
		"actor_id", "dimension_id", "amount_base_units", "state", "refundable",
	}).AddRow("agent_a", dimension, "25", "CONSUMED_ON_RELEASE", true))
	mock.ExpectQuery(`SELECT DISTINCT ON \(actor_id\)`).WithArgs("org_a").WillReturnRows(
		sqlmock.NewRows([]string{"actor_id", "task_id"}).AddRow("agent_a", "task_a"))
	mock.ExpectQuery(`SELECT id,kind,state`).WithArgs("org_a").WillReturnRows(sqlmock.NewRows([]string{
		"id", "kind", "state", "agent_id", "task_id", "asset", "amount_atomic", "detail", "occurred_at",
	}).AddRow(operationID, "PAYMENT_OPERATION", "LOCK_SUBMITTED", "agent_a", "task_a", asset, "25", "research", now))

	projection, err := store.DashboardProjection(t.Context(), "org_a", now)
	if err != nil {
		t.Fatal(err)
	}
	if !projection.Available || len(projection.PendingApprovals) != 1 || projection.PendingApprovals[0].ApprovalID != approvalID {
		t.Fatalf("approval projection = %+v", projection.PendingApprovals)
	}
	// Ledger-backed fields are signed posting deltas; operation exposure is unsigned.
	if len(projection.Assets) != 1 || projection.Assets[0].RecognizedExpenseAtomic != "25" ||
		projection.Assets[0].EscrowRestrictedAtomic != "-25" || projection.Assets[0].ReservedAtomic != "25" ||
		projection.Assets[0].PendingChainAtomic != "25" {
		t.Fatalf("asset projection = %+v", projection.Assets)
	}
	if len(projection.AgentBudgets) != 1 || projection.AgentBudgets[0].SpentTodayAtomic != "25" ||
		projection.AgentBudgets[0].AvailableAtomic != "975" || projection.AgentBudgets[0].CurrentTaskID != "task_a" ||
		!projection.AgentBudgets[0].PolicyConfigurationValid {
		t.Fatalf("agent budget projection = %+v", projection.AgentBudgets)
	}
	if len(projection.Activity) != 1 || projection.Activity[0].State != "LOCK_SUBMITTED" {
		t.Fatalf("activity projection = %+v", projection.Activity)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestDashboardProjectionRejectsInvalidScope(t *testing.T) {
	store, _, db := newMockStore(t)
	defer db.Close()
	if _, err := store.DashboardProjection(t.Context(), "invalid organization", time.Now()); err == nil {
		t.Fatal("expected invalid organization scope to fail closed")
	}
	if _, err := store.DashboardProjection(t.Context(), "org_a", time.Time{}); err == nil {
		t.Fatal("expected zero projection time to fail closed")
	}
}
