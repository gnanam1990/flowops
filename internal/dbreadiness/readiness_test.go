package dbreadiness

import (
	"context"
	"database/sql"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gnanam1990/flowops/internal/controlapi"
)

func TestVerifyRuntimeSQLAcceptsExactLeastPrivilegeContract(t *testing.T) {
	db, mock := readinessDB(t, true, nil, nil)
	report, err := VerifyRuntimeSQL(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Ready || report.Role != "flowops_runtime" || report.TLSVersion != "TLSv1.3" {
		t.Fatalf("report = %+v", report)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyRuntimeSQLFailsClosedWhenSessionIsNotTLS(t *testing.T) {
	db, mock := readinessDB(t, false, nil, nil)
	report, err := VerifyRuntimeSQL(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	if report.Ready {
		t.Fatal("non-TLS connection was marked ready")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyRuntimeSQLRejectsSurplusDeletePrivilege(t *testing.T) {
	db, mock := readinessDB(t, true, map[string]map[string]bool{"commands": {"DELETE": true}}, nil)
	report, err := VerifyRuntimeSQL(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	if report.Ready {
		t.Fatal("surplus DELETE privilege was marked ready")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyRuntimeSQLRejectsSignerTransitionUpdatePrivilege(t *testing.T) {
	db, mock := readinessDB(t, true, map[string]map[string]bool{"ascp_sign_requests": {"UPDATE": true}}, nil)
	report, err := VerifyRuntimeSQL(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	if report.Ready {
		t.Fatal("signer transition UPDATE privilege was marked ready for the control-plane role")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyRuntimeSQLRejectsPrimaryMirrorUpdatePrivilege(t *testing.T) {
	db, mock := readinessDB(t, true, nil, map[string][]string{"ascp_bearer_registry": {"primary_mirror_digest"}})
	report, err := VerifyRuntimeSQL(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	if report.Ready {
		t.Fatal("primary mirror UPDATE privilege was marked ready for the control-plane role")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyRuntimeSQLRejectsSurplusSellerJobInsertColumnPrivilege(t *testing.T) {
	db, mock := readinessDB(t, true, nil, map[string][]string{"ascp_seller_jobs": {"state"}})
	report, err := VerifyRuntimeSQL(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	if report.Ready {
		t.Fatal("surplus seller state INSERT privilege was marked ready")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestValidateRuntimeURLRequiresVerifiedTLS(t *testing.T) {
	postgresURL := func(mode string) string {
		return strings.Join([]string{"postgresql", "://runtime@", "db.example/flowops?sslmode=", mode}, "")
	}
	if err := ValidateRuntimeURL(postgresURL("verify-full")); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{
		postgresURL("require"),
		postgresURL("disable"),
		"https://db.example/flowops?sslmode=verify-full",
		"",
	} {
		if err := ValidateRuntimeURL(raw); err == nil {
			t.Fatalf("unsafe URL accepted: %q", raw)
		}
	}
}

func readinessDB(t *testing.T, tls bool, overrides map[string]map[string]bool, columnExtras map[string][]string) (*sql.DB, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	tlsVersion, cipher := "", ""
	if tls {
		tlsVersion, cipher = "TLSv1.3", "TLS_AES_256_GCM_SHA384"
	}
	mock.ExpectQuery(`(?s)SELECT current_database\(\), current_schema\(\), current_user`).WillReturnRows(
		sqlmock.NewRows([]string{"current_database", "current_schema", "current_user", "server_version", "ssl", "version", "cipher"}).
			AddRow("flowops", "public", "flowops_runtime", "17.3", tls, tlsVersion, cipher),
	)
	mock.ExpectQuery(`(?s)SELECT rolsuper, rolcreaterole`).WillReturnRows(
		sqlmock.NewRows([]string{"rolsuper", "rolcreaterole", "rolcreatedb", "rolreplication", "rolbypassrls"}).AddRow(false, false, false, false, false),
	)
	mock.ExpectQuery(`(?s)WITH RECURSIVE reachable`).WillReturnRows(sqlmock.NewRows([]string{"dangerous"}).AddRow(false))
	mock.ExpectQuery(`(?s)SELECT has_schema_privilege`).WillReturnRows(sqlmock.NewRows([]string{"usage", "create"}).AddRow(true, false))
	manifest, err := controlapi.MigrationManifest()
	if err != nil {
		t.Fatal(err)
	}
	migrationRows := sqlmock.NewRows([]string{"name", "checksum"})
	for _, migration := range manifest {
		migrationRows.AddRow(migration.Name, migration.Checksum)
	}
	mock.ExpectQuery(regexp.QuoteMeta("SELECT name, checksum FROM flowops_schema_migrations ORDER BY name")).WillReturnRows(migrationRows)
	contracts := map[string]map[string]bool{
		"organizations":                      {"SELECT": true, "UPDATE": true},
		"agents":                             {"SELECT": true, "UPDATE": true},
		"credentials":                        {"SELECT": true},
		"policies":                           {"SELECT": true},
		"commands":                           {"SELECT": true, "INSERT": true, "UPDATE": true},
		"audit_events":                       {"SELECT": true, "INSERT": true},
		"control_events":                     {"SELECT": true, "INSERT": true},
		"sites_identity_providers":           {"SELECT": true},
		"sites_memberships":                  {"SELECT": true},
		"flowops_schema_migrations":          {"SELECT": true},
		"ascp_intents":                       {"SELECT": true, "INSERT": true},
		"ascp_adaptation_grants":             {"SELECT": true, "INSERT": true},
		"ascp_proposal_workflows":            {"SELECT": true, "INSERT": true},
		"ascp_workflow_actions":              {"SELECT": true, "INSERT": true},
		"ascp_workflow_events":               {"SELECT": true, "INSERT": true},
		"ascp_workflow_outbox":               {"SELECT": true, "INSERT": true},
		"ascp_policy_decisions":              {"SELECT": true, "INSERT": true},
		"ascp_approvals":                     {"SELECT": true, "INSERT": true, "UPDATE": true},
		"ascp_budget_reservations":           {"SELECT": true, "INSERT": true, "UPDATE": true},
		"ascp_budget_reservation_dimensions": {"SELECT": true, "INSERT": true},
		"ascp_execution_authorizations":      {"SELECT": true, "INSERT": true},
		"ascp_bearer_handles":                {"SELECT": true},
		"ascp_sign_requests":                 {"SELECT": true, "INSERT": true},
		"ascp_bearer_registry":               {"SELECT": true},
		"ascp_signer_outbox":                 {"SELECT": true, "INSERT": true},
		"ascp_agent_signer_bindings":         {"SELECT": true, "INSERT": true, "UPDATE": true},
		"ascp_agent_signer_binding_history":  {"SELECT": true, "INSERT": true},
		"ascp_agent_signer_binding_changes":  {"SELECT": true, "INSERT": true},
		"ascp_directory_snapshots":           {"SELECT": true, "INSERT": true},
		"ascp_directory_quote_evidence":      {"SELECT": true, "INSERT": true},
		"ascp_directory_heads":               {"SELECT": true, "INSERT": true, "UPDATE": true},
		"ascp_payment_operations":            {"SELECT": true},
		"ascp_payment_attempts":              {"SELECT": true, "INSERT": true},
		"ascp_chain_observations":            {"SELECT": true, "INSERT": true},
		"ascp_ledger_transactions":           {"SELECT": true, "INSERT": true},
		"ascp_ledger_postings":               {"SELECT": true, "INSERT": true},
		"ascp_seller_jobs":                   {"SELECT": true},
		"ascp_seller_responses":              {"SELECT": true},
		"ascp_leadership_epochs":             {"SELECT": true},
		"ascp_leadership_effects":            {"SELECT": true},
		"ascp_events":                        {"SELECT": true, "INSERT": true},
		"ascp_event_checkpoints":             {"SELECT": true},
	}
	for _, table := range runtimeTables {
		for _, privilege := range tablePrivileges {
			granted := contracts[table.name][privilege]
			if tableOverrides, ok := overrides[table.name]; ok {
				if override, ok := tableOverrides[privilege]; ok {
					granted = override
				}
			}
			mock.ExpectQuery(regexp.QuoteMeta("SELECT has_table_privilege(current_user, $1, $2)")).
				WithArgs(table.name, privilege).
				WillReturnRows(sqlmock.NewRows([]string{"granted"}).AddRow(granted))
		}
	}
	for _, contract := range runtimeColumnUpdates {
		rows := sqlmock.NewRows([]string{"column_name"})
		columns := make([]string, 0, len(contract.columns))
		for column := range contract.columns {
			columns = append(columns, column)
		}
		sort.Strings(columns)
		columns = append(columns, columnExtras[contract.table]...)
		for _, column := range columns {
			rows.AddRow(column)
		}
		mock.ExpectQuery(`(?s)SELECT column_name.*information_schema\.columns`).
			WithArgs(contract.table).WillReturnRows(rows)
	}
	for _, contract := range runtimeColumnInserts {
		rows := sqlmock.NewRows([]string{"column_name"})
		columns := make([]string, 0, len(contract.columns))
		for column := range contract.columns {
			columns = append(columns, column)
		}
		sort.Strings(columns)
		columns = append(columns, columnExtras[contract.table]...)
		for _, column := range columns {
			rows.AddRow(column)
		}
		mock.ExpectQuery(`(?s)SELECT column_name.*information_schema\.columns`).WithArgs(contract.table).WillReturnRows(rows)
	}
	return db, mock
}
