package ascpgovernancerelay

import (
	"database/sql"
	"regexp"
	"sort"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func governanceRuntimeRoleMock(t *testing.T, unsafeTable, unsafeColumn string, unsafeRoutine bool) (*sql.DB, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	mock.ExpectQuery(`(?s)SELECT current_user, r\.rolsuper`).WillReturnRows(sqlmock.NewRows([]string{
		"current_user", "rolsuper", "rolcreaterole", "rolcreatedb", "rolreplication", "rolbypassrls", "rolinherit",
		"membership", "schema_usage", "schema_create", "other_schema_authority", "database_create", "database_temp", "owns_object",
	}).AddRow("flowops_governance_relayer", false, false, false, false, false, false, false, true, false, false, false, false, false))
	tableRows := sqlmock.NewRows([]string{"schema", "table", "select", "insert", "update", "delete", "truncate", "references", "trigger"})
	tables := make([]string, 0, len(governanceRuntimeTablePrivileges))
	for table := range governanceRuntimeTablePrivileges {
		tables = append(tables, table)
	}
	sort.Strings(tables)
	for _, table := range tables {
		expected := governanceRuntimeTablePrivileges[table]
		tableRows.AddRow("public", table, expected.selectAllowed, expected.insertAllowed, table == unsafeTable, false, false, false, false)
	}
	mock.ExpectQuery(`(?s)SELECT n\.nspname,c\.relname,.*FROM pg_class`).WillReturnRows(tableRows)
	if unsafeTable == "" {
		columnRows := sqlmock.NewRows([]string{"table_name", "column_name"})
		tables = tables[:0]
		for table := range governanceRuntimeColumnUpdates {
			tables = append(tables, table)
		}
		sort.Strings(tables)
		for _, table := range tables {
			columns := make([]string, 0, len(governanceRuntimeColumnUpdates[table]))
			for column := range governanceRuntimeColumnUpdates[table] {
				columns = append(columns, column)
			}
			sort.Strings(columns)
			for _, column := range columns {
				columnRows.AddRow(table, column)
			}
		}
		if unsafeColumn != "" {
			columnRows.AddRow("ascp_governance_relay_jobs", unsafeColumn)
		}
		mock.ExpectQuery(`(?s)SELECT table_name,column_name.*information_schema\.columns`).WillReturnRows(columnRows)
		if unsafeColumn == "" {
			routineCount := 1
			if unsafeRoutine {
				routineCount = 2
			}
			mock.ExpectQuery(regexp.QuoteMeta("SELECT\n\t\t(SELECT count(*) FROM pg_proc")).WillReturnRows(
				sqlmock.NewRows([]string{"routines", "allowed_routines", "sequences"}).AddRow(routineCount, 1, 0))
		}
	}
	return db, mock
}

func TestVerifyGovernanceRuntimeRoleAcceptsExactEffectivePrivileges(t *testing.T) {
	db, mock := governanceRuntimeRoleMock(t, "", "", false)
	if err := VerifyRuntimeRole(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyGovernanceRuntimeRoleRejectsSurplusAuthority(t *testing.T) {
	t.Run("table-wide-update", func(t *testing.T) {
		db, _ := governanceRuntimeRoleMock(t, "ascp_governance_relay_jobs", "", false)
		if err := VerifyRuntimeRole(t.Context(), db); err == nil {
			t.Fatal("table-wide governance relay UPDATE authority was accepted")
		}
	})
	t.Run("immutable-command-update", func(t *testing.T) {
		db, _ := governanceRuntimeRoleMock(t, "", "command_json", false)
		if err := VerifyRuntimeRole(t.Context(), db); err == nil {
			t.Fatal("governance command UPDATE authority was accepted")
		}
	})
	t.Run("surplus-routine", func(t *testing.T) {
		db, _ := governanceRuntimeRoleMock(t, "", "", true)
		if err := VerifyRuntimeRole(t.Context(), db); err == nil {
			t.Fatal("surplus executable database routine was accepted")
		}
	})
}
