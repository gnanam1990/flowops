package ascpbearer

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

type runtimeTablePrivileges struct {
	selectAllowed bool
	insertAllowed bool
}

type runtimeRoleDatabase interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

var bearerRuntimeTablePrivileges = map[string]runtimeTablePrivileges{
	"ascp_sign_requests":            {selectAllowed: true},
	"ascp_signer_outbox":            {selectAllowed: true},
	"ascp_execution_authorizations": {selectAllowed: true},
	"ascp_budget_reservations":      {selectAllowed: true},
	"ascp_policy_decisions":         {selectAllowed: true},
	"ascp_bearer_registry":          {selectAllowed: true},
	"ascp_bearer_handles":           {},
	"ascp_payment_operations":       {},
}

var bearerRuntimeColumnUpdates = map[string]map[string]bool{
	"ascp_sign_requests": {
		"lease_owner": true, "lease_token": true, "lease_expires_at": true, "attempt_count": true,
		"next_attempt_at": true, "last_error": true, "prepared_handle": true, "state": true,
		"prepared_at": true, "activated_at": true, "primary_mirror_digest": true, "mirrored_at": true,
		"acknowledged_at": true, "unactivated_proof": true, "expired_at": true,
	},
	"ascp_budget_reservations": {"state": true},
	"ascp_bearer_registry":     {"primary_mirror_digest": true},
	"ascp_signer_outbox": {
		"state": true, "attempts": true, "delivered_at": true, "cancelled_at": true, "last_error": true,
	},
}

var bearerRuntimeColumnInserts = map[string]map[string]bool{
	"ascp_bearer_handles": {
		"handle_id": true, "operation_id": true, "payload_hash": true, "digest": true, "nonce": true,
		"state": true, "valid_until": true, "created_at": true,
	},
	"ascp_bearer_registry": {
		"digest": true, "instrument_type": true, "signature_ref": true, "nonce": true, "issued_at": true,
		"valid_until": true, "signer_key_id": true, "key_epoch": true, "operation_id": true,
		"authorization_id": true, "reservation_id": true, "module_address": true, "safe_address": true,
		"outcome": true, "created_at": true,
	},
	"ascp_payment_operations": {
		"operation_id": true, "organization_id": true, "agent_id": true, "authorization_id": true,
		"reservation_id": true, "bearer_digest": true, "commitment_hash": true, "call_id": true,
		"chain_id": true, "escrow_contract": true, "asset": true, "buyer": true, "pay_to": true,
		"amount_base_units": true, "settle_by": true, "state": true, "created_at": true, "updated_at": true,
	},
	"ascp_signer_outbox": {
		"event_id": true, "request_id": true, "operation_id": true, "kind": true, "payload": true,
		"state": true, "created_at": true,
	},
}

// VerifyRuntimeRole rejects a bearer worker connection with any missing or
// surplus database authority. This checks effective privileges, including
// privileges inherited through PUBLIC or role memberships.
func VerifyRuntimeRole(ctx context.Context, db runtimeRoleDatabase) error {
	if db == nil {
		return errors.New("bearer runtime database is required")
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
		return fmt.Errorf("inspect bearer runtime role: %w", err)
	}
	if !identifier(role) || superuser || createRole || createDB || replication || bypassRLS || inherit || membership ||
		!schemaUsage || schemaCreate || otherSchemaAuthority || databaseCreate || databaseTemp || ownsObject {
		return errors.New("bearer runtime role flags, memberships, ownership, schema, or temporary authority are unsafe")
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
		return fmt.Errorf("inspect bearer runtime table privileges: %w", err)
	}
	defer func() { _ = rows.Close() }()
	seen := map[string]bool{}
	for rows.Next() {
		var schema, table string
		var selectGranted, insertGranted, updateGranted, deleteGranted, truncateGranted, referencesGranted, triggerGranted bool
		if err := rows.Scan(&schema, &table, &selectGranted, &insertGranted, &updateGranted, &deleteGranted, &truncateGranted, &referencesGranted, &triggerGranted); err != nil {
			return fmt.Errorf("scan bearer runtime table privileges: %w", err)
		}
		expected := runtimeTablePrivileges{}
		if schema == "public" {
			expected = bearerRuntimeTablePrivileges[table]
		}
		if selectGranted != expected.selectAllowed || insertGranted != expected.insertAllowed || updateGranted ||
			deleteGranted || truncateGranted || referencesGranted || triggerGranted {
			return fmt.Errorf("bearer runtime table privilege mismatch on %s.%s", schema, table)
		}
		if schema == "public" {
			_, required := bearerRuntimeTablePrivileges[table]
			if required {
				seen[table] = true
			}
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate bearer runtime table privileges: %w", err)
	}
	if len(seen) != len(bearerRuntimeTablePrivileges) {
		return errors.New("bearer runtime required tables are missing")
	}

	columnRows, err := db.QueryContext(ctx, `
		SELECT table_name,column_name
		FROM information_schema.columns
		WHERE table_schema='public' AND has_column_privilege(current_user,
		      quote_ident(table_schema)||'.'||quote_ident(table_name),column_name,'UPDATE')
		ORDER BY table_name,column_name`)
	if err != nil {
		return fmt.Errorf("inspect bearer runtime column privileges: %w", err)
	}
	defer func() { _ = columnRows.Close() }()
	seenColumns := 0
	for columnRows.Next() {
		var table, column string
		if err := columnRows.Scan(&table, &column); err != nil {
			return fmt.Errorf("scan bearer runtime column privilege: %w", err)
		}
		if !bearerRuntimeColumnUpdates[table][column] {
			return fmt.Errorf("bearer runtime UPDATE privilege is not allowed on %s.%s", table, column)
		}
		seenColumns++
	}
	if err := columnRows.Err(); err != nil {
		return fmt.Errorf("iterate bearer runtime column privileges: %w", err)
	}
	expectedColumns := 0
	for _, columns := range bearerRuntimeColumnUpdates {
		expectedColumns += len(columns)
	}
	if seenColumns != expectedColumns {
		return errors.New("bearer runtime required column privileges are missing")
	}

	insertRows, err := db.QueryContext(ctx, `
		SELECT table_name,column_name
		FROM information_schema.columns
		WHERE table_schema='public' AND has_column_privilege(current_user,
		      quote_ident(table_schema)||'.'||quote_ident(table_name),column_name,'INSERT')
		ORDER BY table_name,column_name`)
	if err != nil {
		return fmt.Errorf("inspect bearer runtime insert privileges: %w", err)
	}
	defer func() { _ = insertRows.Close() }()
	seenInserts := 0
	for insertRows.Next() {
		var table, column string
		if err := insertRows.Scan(&table, &column); err != nil {
			return fmt.Errorf("scan bearer runtime insert privilege: %w", err)
		}
		if !bearerRuntimeColumnInserts[table][column] {
			return fmt.Errorf("bearer runtime INSERT privilege is not allowed on %s.%s", table, column)
		}
		seenInserts++
	}
	if err := insertRows.Err(); err != nil {
		return fmt.Errorf("iterate bearer runtime insert privileges: %w", err)
	}
	expectedInserts := 0
	for _, columns := range bearerRuntimeColumnInserts {
		expectedInserts += len(columns)
	}
	if seenInserts != expectedInserts {
		return errors.New("bearer runtime required insert privileges are missing")
	}

	var executableRoutines, usableSequences int
	if err := db.QueryRowContext(ctx, `SELECT
		(SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace
		 WHERE n.nspname='public' AND has_function_privilege(current_user,p.oid,'EXECUTE')),
		(SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
		 WHERE n.nspname='public' AND c.relkind='S' AND
		 (has_sequence_privilege(current_user,c.oid,'USAGE') OR has_sequence_privilege(current_user,c.oid,'SELECT') OR
		  has_sequence_privilege(current_user,c.oid,'UPDATE')))`).Scan(&executableRoutines, &usableSequences); err != nil {
		return fmt.Errorf("inspect bearer runtime routine and sequence privileges: %w", err)
	}
	if executableRoutines != 0 || usableSequences != 0 {
		return errors.New("bearer runtime must not execute database routines or use sequences")
	}
	return nil
}
