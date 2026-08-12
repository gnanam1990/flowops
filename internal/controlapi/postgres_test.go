package controlapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gnanam1990/flowops/internal/controlplane"
	"github.com/gnanam1990/flowops/internal/policy"
	"github.com/gnanam1990/flowops/pkg/envelope"
)

func newMockStore(t *testing.T) (*PostgresStore, sqlmock.Sqlmock, *sql.DB) {
	t.Helper()
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewPostgresStore(db)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	return store, mock, db
}

func TestPostgresAuthenticationReturnsValidatedClaimsAndHidesMisses(t *testing.T) {
	store, mock, db := newMockStore(t)
	defer db.Close()
	now := time.Date(2026, 8, 12, 9, 0, 0, 0, time.UTC)
	digest := TokenDigest("fo_sbx_test_000000000000000000000000")
	columns := []string{"principal_id", "organization_id", "principal_kind", "role", "agent_id", "scopes", "step_up_until"}
	mock.ExpectQuery(`SELECT principal_id, organization_id, principal_kind`).WithArgs(digest[:]).WillReturnRows(
		sqlmock.NewRows(columns).AddRow("credential_a", "org_a", "AGENT", "AGENT", "agent_a", []byte(`["intents:create"]`), nil),
	)
	principal, err := store.Authenticate(context.Background(), digest)
	if err != nil || principal.AgentID != "agent_a" || !principal.Can(PermissionCreateIntent) {
		t.Fatalf("principal = %+v, %v", principal, err)
	}

	missing := TokenDigest("fo_sbx_missing_000000000000000000000")
	mock.ExpectQuery(`SELECT principal_id, organization_id, principal_kind`).WithArgs(missing[:]).WillReturnError(sql.ErrNoRows)
	if _, err := store.Authenticate(context.Background(), missing); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("missing credential error = %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
	_ = now
}

func TestPostgresCommandIdempotencyIsOrganizationScoped(t *testing.T) {
	store, mock, db := newMockStore(t)
	defer db.Close()
	now := time.Date(2026, 8, 12, 9, 0, 0, 0, time.UTC)
	command := Command{
		ID: "cmd_1", OrganizationID: "org_a", ActorID: "agent_a", Kind: "intent.create",
		TargetID: "intent_1", IdempotencyKey: "intent_1", InputDigest: "0xabc", State: CommandPending, CreatedAt: now,
	}
	columns := []string{"id", "organization_id", "actor_id", "kind", "target_id", "idempotency_key", "input_digest", "state", "result", "error_code", "created_at", "completed_at"}
	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO commands`).WithArgs(
		command.ID, command.OrganizationID, command.ActorID, command.Kind, command.TargetID,
		command.IdempotencyKey, command.InputDigest, now,
	).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectQuery(`SELECT id, organization_id, actor_id, kind`).WithArgs("org_a", "intent.create", "intent_1").WillReturnRows(
		sqlmock.NewRows(columns).AddRow("cmd_1", "org_a", "agent_a", "intent.create", "intent_1", "intent_1", "0xabc", "PENDING", nil, "", now, nil),
	)
	mock.ExpectCommit()
	stored, created, err := store.BeginCommand(context.Background(), command)
	if err != nil || !created || stored.ID != "cmd_1" {
		t.Fatalf("begin command = %+v, %v, %v", stored, created, err)
	}

	conflict := command
	conflict.ID = "cmd_2"
	conflict.InputDigest = "0xchanged"
	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO commands`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`SELECT id, organization_id, actor_id, kind`).WithArgs("org_a", "intent.create", "intent_1").WillReturnRows(
		sqlmock.NewRows(columns).AddRow("cmd_1", "org_a", "agent_a", "intent.create", "intent_1", "intent_1", "0xabc", "SUCCEEDED", []byte(`{"requestId":"req_1"}`), "", now, now),
	)
	mock.ExpectRollback()
	if _, _, err := store.BeginCommand(context.Background(), conflict); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("conflict error = %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresPauseIsTenantBoundAndAuditedInOneTransaction(t *testing.T) {
	store, mock, db := newMockStore(t)
	defer db.Close()
	now := time.Date(2026, 8, 12, 9, 0, 0, 0, time.UTC)
	columns := []string{"organization_id", "id", "customer_id", "name", "purpose", "status", "updated_at"}
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT organization_id, id, customer_id, name, purpose, status, updated_at[\s\S]+FOR UPDATE`).WithArgs("org_a", "agent_a").WillReturnRows(
		sqlmock.NewRows(columns).AddRow("org_a", "agent_a", "customer_a", "Research", "Evidence", "ACTIVE", now),
	)
	mock.ExpectQuery(`UPDATE agents[\s\S]+RETURNING organization_id`).WithArgs("org_a", "agent_a", AgentPaused).WillReturnRows(
		sqlmock.NewRows(columns).AddRow("org_a", "agent_a", "customer_a", "Research", "Evidence", "PAUSED", now.Add(time.Second)),
	)
	mock.ExpectExec(`INSERT INTO audit_events`).WithArgs(
		"audit_1", "org_a", "owner_a", "agent_a", sqlmock.AnyArg(), sqlmock.AnyArg(),
	).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	agent, err := store.SetAgentStatus(context.Background(), "org_a", "agent_a", AgentPaused, "owner_a", "audit_1")
	if err != nil || agent.Status != AgentPaused {
		t.Fatalf("pause = %+v, %v", agent, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCommandCompletionStoresJSONNotBinaryPayload(t *testing.T) {
	store, mock, db := newMockStore(t)
	defer db.Close()
	now := time.Date(2026, 8, 12, 9, 0, 0, 0, time.UTC)
	result := json.RawMessage(`{"requestId":"req_1"}`)
	columns := []string{"id", "organization_id", "actor_id", "kind", "target_id", "idempotency_key", "input_digest", "state", "result", "error_code", "created_at", "completed_at"}
	mock.ExpectQuery(regexp.QuoteMeta(`
		UPDATE commands
		SET state = $3, result = $4, error_code = $5, completed_at = GREATEST(now(), created_at)
		WHERE organization_id = $1 AND id = $2 AND state = 'PENDING'
		RETURNING id, organization_id, actor_id, kind, target_id, idempotency_key, input_digest,
		          state, result, error_code, created_at, completed_at`)).WithArgs(
		"org_a", "cmd_1", CommandSucceeded, string(result), "",
	).WillReturnRows(sqlmock.NewRows(columns).AddRow(
		"cmd_1", "org_a", "agent_a", "intent.create", "intent_1", "intent_1", "0xabc", "SUCCEEDED", []byte(result), "", now, now,
	))
	command, err := store.CompleteCommand(context.Background(), "org_a", "cmd_1", CommandSucceeded, result, "")
	if err != nil || string(command.Result) != string(result) {
		t.Fatalf("completion = %+v, %v", command, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresPolicyProviderResolvesPolicyPerTenantAgent(t *testing.T) {
	_, mock, db := newMockStore(t)
	defer db.Close()
	provider, err := NewPostgresPolicyProvider(db)
	if err != nil {
		t.Fatal(err)
	}
	config := policy.Config{
		Version: "policy_org_a_1", Enabled: true, AllowedChainIDs: []uint64{84532},
		AllowedRails: []envelope.Rail{envelope.RailX402}, AllowedAssets: []string{testUSDC},
		AllowedRecipients: []string{testRecipient}, PerActionLimitAtomic: "200",
		AutoApproveThresholdAtomic: "100", TaskBudgetAtomic: "500", DailyBudgetAtomic: "1000",
	}
	raw, _ := json.Marshal(config)
	mock.ExpectQuery(`SELECT version, config[\s\S]+FROM policies`).WithArgs("org_a", "agent_a").WillReturnRows(
		sqlmock.NewRows([]string{"version", "config"}).AddRow(config.Version, raw),
	)
	decision, err := provider.Evaluate(context.Background(), controlplane.PaymentIntent{
		IntentID: "intent_1", OrganizationID: "org_a", CustomerID: "customer_a", AgentID: "agent_a",
		TaskID: "task_1", ActionID: "action_1", Rail: envelope.RailX402, ChainID: 84532,
		Recipient: testRecipient, Asset: testUSDC, AmountAtomic: "150", Resource: "https://example.test", Category: "research", Purpose: "proof",
	}, policy.SpendSnapshot{TaskSpentAtomic: "0", TaskReservedAtomic: "0", DailySpentAtomic: "0", DailyReservedAtomic: "0"})
	if err != nil || decision.Outcome != policy.RequireApproval || decision.PolicyVersion != config.Version {
		t.Fatalf("decision = %+v, %v", decision, err)
	}
	mock.ExpectQuery(`SELECT version[\s\S]+FROM policies`).WithArgs("org_a", "agent_a").WillReturnRows(
		sqlmock.NewRows([]string{"version"}).AddRow(config.Version),
	)
	version, err := provider.ActiveVersion(context.Background(), "org_a", "agent_a")
	if err != nil || version != config.Version {
		t.Fatalf("active version = %q, %v", version, err)
	}
	mock.ExpectQuery(`SELECT version[\s\S]+FROM policies`).WithArgs("org_a", "agent_missing").WillReturnError(sql.ErrNoRows)
	if _, err := provider.ActiveVersion(context.Background(), "org_a", "agent_missing"); !errors.Is(err, controlplane.ErrPolicyUnavailable) {
		t.Fatalf("missing active policy error = %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
