package ascpgovernancerelay

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

type runtimeRoleDatabase interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

type runtimeTablePrivileges struct {
	selectAllowed bool
	insertAllowed bool
}

var governanceRuntimeTablePrivileges = map[string]runtimeTablePrivileges{
	"ascp_workflow_outbox":                 {selectAllowed: true, insertAllowed: true},
	"ascp_proposal_workflows":              {selectAllowed: true},
	"ascp_workflow_actions":                {selectAllowed: true, insertAllowed: true},
	"ascp_workflow_events":                 {selectAllowed: true, insertAllowed: true},
	"ascp_governance_relay_jobs":           {selectAllowed: true, insertAllowed: true},
	"ascp_governance_relay_authorizations": {selectAllowed: true, insertAllowed: true},
	"ascp_workflow_safe_retry_proofs":      {selectAllowed: true, insertAllowed: true},
}

var governanceRuntimeColumnUpdates = map[string]map[string]bool{
	"ascp_governance_relay_jobs": {
		"state": true, "prepared_json": true, "artifact_handle": true, "authorization_key": true,
		"authorization_hash": true, "outer_json": true, "last_outcome_json": true, "attempt_count": true,
		"lease_owner": true, "lease_token": true, "lease_expires_at": true, "updated_at": true,
	},
	"ascp_proposal_workflows": {
		"state": true, "submission_transaction_hash": true, "submitted_at": true,
		"confirmed_at": true, "terminal_reason": true, "terminal_at": true,
	},
}

// VerifyRuntimeRole rejects a governance relayer connection with any missing
// or surplus effective database authority. PUBLIC and role-membership grants
// are therefore evaluated as part of the same fail-closed startup gate.
func VerifyRuntimeRole(ctx context.Context, db runtimeRoleDatabase) error {
	if db == nil {
		return errors.New("governance relay runtime database is required")
	}
	var role string
	var superuser, createRole, createDB, replication, bypassRLS, inherit bool
	var membership, schemaUsage, schemaCreate, otherSchemaAuthority, databaseCreate, databaseTemp, ownsObject bool
	if err := db.QueryRowContext(ctx, `
		SELECT current_user, r.rolsuper, r.rolcreaterole, r.rolcreatedb, r.rolreplication, r.rolbypassrls, r.rolinherit,
		       EXISTS (SELECT 1 FROM pg_auth_members WHERE member=r.oid OR roleid=r.oid),
		       has_schema_privilege(current_user,'public','USAGE'),
		       has_schema_privilege(current_user,'public','CREATE'),
		       EXISTS (SELECT 1 FROM pg_namespace n WHERE n.nspname <> 'public' AND n.nspname <> 'information_schema'
		               AND n.nspname NOT LIKE 'pg_%' AND
		               (has_schema_privilege(current_user,n.oid,'USAGE') OR has_schema_privilege(current_user,n.oid,'CREATE'))),
		       has_database_privilege(current_user,current_database(),'CREATE'),
		       has_database_privilege(current_user,current_database(),'TEMP'),
		       EXISTS (SELECT 1 FROM pg_class WHERE relowner=r.oid) OR
		       EXISTS (SELECT 1 FROM pg_proc WHERE proowner=r.oid) OR
		       EXISTS (SELECT 1 FROM pg_namespace WHERE nspowner=r.oid)
		FROM pg_roles r WHERE r.rolname=current_user`).Scan(&role, &superuser, &createRole, &createDB, &replication,
		&bypassRLS, &inherit, &membership, &schemaUsage, &schemaCreate, &otherSchemaAuthority, &databaseCreate,
		&databaseTemp, &ownsObject); err != nil {
		return fmt.Errorf("inspect governance relay runtime role: %w", err)
	}
	if !identifierPattern.MatchString(role) || superuser || createRole || createDB || replication || bypassRLS || inherit || membership ||
		!schemaUsage || schemaCreate || otherSchemaAuthority || databaseCreate || databaseTemp || ownsObject {
		return errors.New("governance relay runtime role flags, memberships, ownership, schema, or temporary authority are unsafe")
	}

	rows, err := db.QueryContext(ctx, `
		SELECT n.nspname,c.relname,
		       has_table_privilege(current_user,c.oid,'SELECT'),
		       has_table_privilege(current_user,c.oid,'INSERT'),
		       has_table_privilege(current_user,c.oid,'UPDATE'),
		       has_table_privilege(current_user,c.oid,'DELETE'),
		       has_table_privilege(current_user,c.oid,'TRUNCATE'),
		       has_table_privilege(current_user,c.oid,'REFERENCES'),
		       has_table_privilege(current_user,c.oid,'TRIGGER')
		FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
		WHERE c.relkind IN ('r','p','v','m','f') AND n.nspname <> 'information_schema' AND n.nspname NOT LIKE 'pg_%'
		ORDER BY n.nspname,c.relname`)
	if err != nil {
		return fmt.Errorf("inspect governance relay runtime table privileges: %w", err)
	}
	defer func() { _ = rows.Close() }()
	seen := map[string]bool{}
	for rows.Next() {
		var schema, table string
		var selectGranted, insertGranted, updateGranted, deleteGranted, truncateGranted, referencesGranted, triggerGranted bool
		if err := rows.Scan(&schema, &table, &selectGranted, &insertGranted, &updateGranted, &deleteGranted, &truncateGranted, &referencesGranted, &triggerGranted); err != nil {
			return fmt.Errorf("scan governance relay runtime table privileges: %w", err)
		}
		expected := runtimeTablePrivileges{}
		if schema == "public" {
			expected = governanceRuntimeTablePrivileges[table]
		}
		if selectGranted != expected.selectAllowed || insertGranted != expected.insertAllowed || updateGranted ||
			deleteGranted || truncateGranted || referencesGranted || triggerGranted {
			return fmt.Errorf("governance relay runtime table privilege mismatch on %s.%s", schema, table)
		}
		if schema == "public" {
			if _, required := governanceRuntimeTablePrivileges[table]; required {
				seen[table] = true
			}
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate governance relay runtime table privileges: %w", err)
	}
	if len(seen) != len(governanceRuntimeTablePrivileges) {
		return errors.New("governance relay runtime required tables are missing")
	}

	columnRows, err := db.QueryContext(ctx, `
		SELECT table_name,column_name
		FROM information_schema.columns
		WHERE table_schema='public' AND has_column_privilege(current_user,
		      quote_ident(table_schema)||'.'||quote_ident(table_name),column_name,'UPDATE')
		ORDER BY table_name,column_name`)
	if err != nil {
		return fmt.Errorf("inspect governance relay runtime column privileges: %w", err)
	}
	defer func() { _ = columnRows.Close() }()
	seenColumns := 0
	for columnRows.Next() {
		var table, column string
		if err := columnRows.Scan(&table, &column); err != nil {
			return fmt.Errorf("scan governance relay runtime column privilege: %w", err)
		}
		if !governanceRuntimeColumnUpdates[table][column] {
			return fmt.Errorf("governance relay runtime UPDATE privilege is not allowed on %s.%s", table, column)
		}
		seenColumns++
	}
	if err := columnRows.Err(); err != nil {
		return fmt.Errorf("iterate governance relay runtime column privileges: %w", err)
	}
	expectedColumns := 0
	for _, columns := range governanceRuntimeColumnUpdates {
		expectedColumns += len(columns)
	}
	if seenColumns != expectedColumns {
		return errors.New("governance relay runtime required column privileges are missing")
	}

	var executableRoutines, allowedRoutines, usableSequences int
	if err := db.QueryRowContext(ctx, `SELECT
		(SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace
		 WHERE n.nspname='public' AND has_function_privilege(current_user,p.oid,'EXECUTE')),
		(SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace
		 WHERE n.nspname='public' AND has_function_privilege(current_user,p.oid,'EXECUTE')
		   AND p.oid='public.flowops_governance_observers_valid(jsonb)'::regprocedure),
		(SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
		 WHERE n.nspname='public' AND c.relkind='S' AND
		 (has_sequence_privilege(current_user,c.oid,'USAGE') OR has_sequence_privilege(current_user,c.oid,'SELECT') OR
		  has_sequence_privilege(current_user,c.oid,'UPDATE')))`).Scan(&executableRoutines, &allowedRoutines, &usableSequences); err != nil {
		return fmt.Errorf("inspect governance relay runtime routine and sequence privileges: %w", err)
	}
	if executableRoutines != 1 || allowedRoutines != 1 || usableSequences != 0 {
		return errors.New("governance relay runtime routine or sequence privileges are unsafe")
	}
	return nil
}
