package ascpbearer

import (
	"context"
	"database/sql"
	"os"
	"regexp"
	"sort"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func runtimeRoleMock(t *testing.T, unsafeTable, unsafeColumn, unsafeInsert string) (*sql.DB, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	mock.ExpectQuery(`(?s)SELECT current_user, r\.rolsuper`).WillReturnRows(sqlmock.NewRows([]string{
		"current_user", "rolsuper", "rolcreaterole", "rolcreatedb", "rolreplication", "rolbypassrls", "rolinherit",
		"membership", "schema_usage", "schema_create", "other_schema_authority", "database_create", "database_temp", "owns_object",
	}).AddRow("flowops_bearer", false, false, false, false, false, false, false, true, false, false, false, false, false))
	tableRows := sqlmock.NewRows([]string{"schemaname", "tablename", "select", "insert", "update", "delete", "truncate", "references", "trigger"})
	tables := make([]string, 0, len(bearerRuntimeTablePrivileges))
	for table := range bearerRuntimeTablePrivileges {
		tables = append(tables, table)
	}
	sort.Strings(tables)
	for _, table := range tables {
		expected := bearerRuntimeTablePrivileges[table]
		tableRows.AddRow("public", table, expected.selectAllowed, expected.insertAllowed, table == unsafeTable, false, false, false, false)
	}
	mock.ExpectQuery(`(?s)SELECT n\.nspname,c\.relname,.*FROM pg_class`).WillReturnRows(tableRows)
	if unsafeTable == "" {
		columnRows := sqlmock.NewRows([]string{"table_name", "column_name"})
		tables = tables[:0]
		for table := range bearerRuntimeColumnUpdates {
			tables = append(tables, table)
		}
		sort.Strings(tables)
		for _, table := range tables {
			columns := make([]string, 0, len(bearerRuntimeColumnUpdates[table]))
			for column := range bearerRuntimeColumnUpdates[table] {
				columns = append(columns, column)
			}
			sort.Strings(columns)
			for _, column := range columns {
				columnRows.AddRow(table, column)
			}
		}
		if unsafeColumn != "" {
			columnRows.AddRow("ascp_sign_requests", unsafeColumn)
		}
		mock.ExpectQuery(`(?s)SELECT table_name,column_name.*information_schema\.columns`).WillReturnRows(columnRows)
		if unsafeColumn == "" {
			insertRows := sqlmock.NewRows([]string{"table_name", "column_name"})
			tables = tables[:0]
			for table := range bearerRuntimeColumnInserts {
				tables = append(tables, table)
			}
			sort.Strings(tables)
			for _, table := range tables {
				columns := make([]string, 0, len(bearerRuntimeColumnInserts[table]))
				for column := range bearerRuntimeColumnInserts[table] {
					columns = append(columns, column)
				}
				sort.Strings(columns)
				for _, column := range columns {
					insertRows.AddRow(table, column)
				}
			}
			if unsafeInsert != "" {
				insertRows.AddRow("ascp_bearer_handles", unsafeInsert)
			}
			mock.ExpectQuery(`(?s)SELECT table_name,column_name.*information_schema\.columns`).WillReturnRows(insertRows)
		}
		if unsafeColumn == "" && unsafeInsert == "" {
			mock.ExpectQuery(regexp.QuoteMeta("SELECT\n\t\t(SELECT count(*) FROM pg_proc")).WillReturnRows(
				sqlmock.NewRows([]string{"routines", "sequences"}).AddRow(0, 0))
		}
	}
	return db, mock
}

func TestVerifyRuntimeRolePostgresIntegration(t *testing.T) {
	databaseURL, role := os.Getenv("FLOWOPS_BEARER_ROLE_TEST_DATABASE_URL"), os.Getenv("FLOWOPS_BEARER_ROLE_TEST_ROLE")
	if databaseURL == "" || role == "" {
		t.Skip("bearer role integration environment is not configured")
	}
	if !identifier(role) {
		t.Fatal("integration role is not canonical")
	}
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	connection, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = connection.Close() }()
	if _, err := connection.ExecContext(ctx, `SET ROLE "`+role+`"`); err != nil {
		t.Fatal(err)
	}
	if err := VerifyRuntimeRole(ctx, connection); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyRuntimeRoleAcceptsExactEffectivePrivileges(t *testing.T) {
	db, mock := runtimeRoleMock(t, "", "", "")
	if err := VerifyRuntimeRole(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyRuntimeRoleRejectsTableWideAndSecretColumnUpdate(t *testing.T) {
	t.Run("table-wide", func(t *testing.T) {
		db, _ := runtimeRoleMock(t, "ascp_sign_requests", "", "")
		if err := VerifyRuntimeRole(t.Context(), db); err == nil {
			t.Fatal("table-wide signer UPDATE authority was accepted")
		}
	})
	t.Run("secret-column", func(t *testing.T) {
		db, _ := runtimeRoleMock(t, "", "canonical_payload", "")
		if err := VerifyRuntimeRole(t.Context(), db); err == nil {
			t.Fatal("canonical signer payload UPDATE authority was accepted")
		}
	})
	t.Run("secret-insert-column", func(t *testing.T) {
		db, _ := runtimeRoleMock(t, "", "", "encrypted_artifact")
		if err := VerifyRuntimeRole(t.Context(), db); err == nil {
			t.Fatal("encrypted artifact INSERT authority was accepted")
		}
	})
}
