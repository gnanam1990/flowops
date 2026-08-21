package dbreadiness

import (
	"context"
	"database/sql"
	"regexp"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gnanam1990/flowops/internal/controlapi"
)

func TestVerifyRuntimeSQLAcceptsExactLeastPrivilegeContract(t *testing.T) {
	db, mock := readinessDB(t, true, nil)
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
	db, mock := readinessDB(t, false, nil)
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
	db, mock := readinessDB(t, true, map[string]map[string]bool{"commands": {"DELETE": true}})
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

func readinessDB(t *testing.T, tls bool, overrides map[string]map[string]bool) (*sql.DB, sqlmock.Sqlmock) {
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
		"ascp_approvals":                     {"SELECT": true, "INSERT": true, "UPDATE": true},
		"ascp_budget_reservations":           {"SELECT": true, "INSERT": true, "UPDATE": true},
		"ascp_budget_reservation_dimensions": {"SELECT": true, "INSERT": true},
		"ascp_execution_authorizations":      {"SELECT": true, "INSERT": true},
		"ascp_bearer_handles":                {"SELECT": true, "INSERT": true, "UPDATE": true},
		"ascp_directory_snapshots":           {"SELECT": true, "INSERT": true},
		"ascp_directory_quote_evidence":      {"SELECT": true, "INSERT": true},
		"ascp_directory_heads":               {"SELECT": true, "INSERT": true, "UPDATE": true},
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
	return db, mock
}
