package dbreadiness

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/gnanam1990/flowops/internal/controlapi"
)

type Check struct {
	Name   string `json:"name"`
	Passed bool   `json:"passed"`
	Detail string `json:"detail"`
}

type SQLReport struct {
	SchemaVersion int     `json:"schemaVersion"`
	Ready         bool    `json:"ready"`
	Database      string  `json:"database"`
	Schema        string  `json:"schema"`
	Role          string  `json:"role"`
	ServerVersion string  `json:"serverVersion"`
	TLSVersion    string  `json:"tlsVersion"`
	TLSCipher     string  `json:"tlsCipher"`
	Checks        []Check `json:"checks"`
}

type tableContract struct {
	name    string
	allowed map[string]bool
}

var runtimeTables = []tableContract{
	{name: "organizations", allowed: set("SELECT", "UPDATE")},
	{name: "agents", allowed: set("SELECT", "UPDATE")},
	{name: "credentials", allowed: set("SELECT")},
	{name: "policies", allowed: set("SELECT")},
	{name: "commands", allowed: set("SELECT", "INSERT", "UPDATE")},
	{name: "audit_events", allowed: set("SELECT", "INSERT")},
	{name: "control_events", allowed: set("SELECT", "INSERT")},
	{name: "sites_identity_providers", allowed: set("SELECT")},
	{name: "sites_memberships", allowed: set("SELECT")},
	{name: "flowops_schema_migrations", allowed: set("SELECT")},
	{name: "ascp_intents", allowed: set("SELECT", "INSERT")},
	{name: "ascp_policy_decisions", allowed: set("SELECT", "INSERT")},
	{name: "ascp_approvals", allowed: set("SELECT", "INSERT", "UPDATE")},
	{name: "ascp_budget_reservations", allowed: set("SELECT", "INSERT", "UPDATE")},
	{name: "ascp_budget_reservation_dimensions", allowed: set("SELECT", "INSERT")},
	{name: "ascp_execution_authorizations", allowed: set("SELECT", "INSERT")},
	{name: "ascp_bearer_handles", allowed: set("SELECT", "INSERT")},
	{name: "ascp_sign_requests", allowed: set("SELECT", "INSERT")},
	{name: "ascp_bearer_registry", allowed: set("SELECT", "INSERT")},
	{name: "ascp_signer_outbox", allowed: set("SELECT", "INSERT")},
	{name: "ascp_directory_snapshots", allowed: set("SELECT", "INSERT")},
	{name: "ascp_directory_quote_evidence", allowed: set("SELECT", "INSERT")},
	{name: "ascp_directory_heads", allowed: set("SELECT", "INSERT", "UPDATE")},
	{name: "ascp_payment_operations", allowed: set("SELECT", "INSERT")},
	{name: "ascp_payment_attempts", allowed: set("SELECT", "INSERT")},
	{name: "ascp_chain_observations", allowed: set("SELECT", "INSERT")},
	{name: "ascp_ledger_transactions", allowed: set("SELECT", "INSERT")},
	{name: "ascp_ledger_postings", allowed: set("SELECT", "INSERT")},
	{name: "ascp_seller_jobs", allowed: set("SELECT")},
	{name: "ascp_seller_responses", allowed: set("SELECT")},
	{name: "ascp_leadership_epochs", allowed: set("SELECT")},
	{name: "ascp_leadership_effects", allowed: set("SELECT")},
	{name: "ascp_events", allowed: set("SELECT", "INSERT")},
	{name: "ascp_event_checkpoints", allowed: set("SELECT")},
}

var tablePrivileges = []string{"SELECT", "INSERT", "UPDATE", "DELETE", "TRUNCATE", "REFERENCES", "TRIGGER"}

type columnUpdateContract struct {
	table   string
	columns map[string]bool
}

var runtimeColumnUpdates = []columnUpdateContract{
	{table: "ascp_bearer_handles", columns: set("state")},
	{table: "ascp_sign_requests", columns: set("prepared_handle", "state", "prepared_at", "activated_at", "primary_mirror_digest", "mirrored_at", "acknowledged_at")},
	{table: "ascp_bearer_registry", columns: set("primary_mirror_digest", "outcome")},
	{table: "ascp_signer_outbox", columns: set("state", "attempts", "delivered_at")},
	{table: "ascp_payment_operations", columns: set("state", "locked_transaction_hash", "locked_block_number", "locked_block_hash", "terminal_action", "terminal_transaction_hash", "terminal_block_number", "terminal_block_hash", "updated_at")},
	{table: "ascp_payment_attempts", columns: set("state", "resolved_at", "block_number", "block_hash", "evidence_digest", "canonical_checked_at")},
}

var runtimeColumnInserts = []columnUpdateContract{
	{table: "ascp_seller_jobs", columns: set("job_id", "operation_id", "organization_id", "chain_id", "leadership_epoch", "deliver_by", "method", "request_url",
		"headers_json", "request_body", "canonical_spec_json", "offer_json", "payment_json", "binding_json", "locked_transaction_hash", "payer",
		"validated_chain_time", "input_hash", "eligible_after", "created_at", "updated_at")},
}

func set(values ...string) map[string]bool {
	result := make(map[string]bool, len(values))
	for _, value := range values {
		result[value] = true
	}
	return result
}

// VerifyRuntimeSQL proves properties of the exact runtime connection. It is
// intentionally unable to attest provider backups, PITR, or encryption at
// rest; those controls use VerifyProviderEvidence.
func VerifyRuntimeSQL(ctx context.Context, db *sql.DB) (SQLReport, error) {
	report := SQLReport{SchemaVersion: 1}
	if db == nil {
		return report, fmt.Errorf("database is required")
	}
	var tls bool
	if err := db.QueryRowContext(ctx, `
		SELECT current_database(), current_schema(), current_user, current_setting('server_version'),
		       COALESCE(s.ssl, false), COALESCE(s.version, ''), COALESCE(s.cipher, '')
		FROM (SELECT pg_backend_pid() AS pid) current_backend
		LEFT JOIN pg_stat_ssl s ON s.pid = current_backend.pid`).Scan(
		&report.Database, &report.Schema, &report.Role, &report.ServerVersion, &tls, &report.TLSVersion, &report.TLSCipher,
	); err != nil {
		return report, fmt.Errorf("inspect PostgreSQL session: %w", err)
	}
	report.add("tls_session", tls && report.TLSVersion != "" && report.TLSCipher != "", "current backend must use negotiated TLS")
	report.add("runtime_schema", report.Schema == "public", "capped-pilot runtime must resolve objects from the reviewed public schema")

	var superuser, createRole, createDB, replication, bypassRLS bool
	if err := db.QueryRowContext(ctx, `
		SELECT rolsuper, rolcreaterole, rolcreatedb, rolreplication, rolbypassrls
		FROM pg_roles WHERE rolname = current_user`).Scan(&superuser, &createRole, &createDB, &replication, &bypassRLS); err != nil {
		return report, fmt.Errorf("inspect runtime role: %w", err)
	}
	report.add("runtime_role_flags", !superuser && !createRole && !createDB && !replication && !bypassRLS,
		"runtime role must be NOSUPERUSER NOCREATEROLE NOCREATEDB NOREPLICATION NOBYPASSRLS")

	var dangerousMembership bool
	if err := db.QueryRowContext(ctx, `
		WITH RECURSIVE reachable(oid) AS (
			SELECT oid FROM pg_roles WHERE rolname = current_user
			UNION
			SELECT membership.roleid
			FROM pg_auth_members membership
			JOIN reachable ON membership.member = reachable.oid
		)
		SELECT COALESCE(bool_or(role.rolsuper OR role.rolcreaterole OR role.rolcreatedb OR role.rolreplication OR role.rolbypassrls), false)
		FROM reachable JOIN pg_roles role ON role.oid = reachable.oid`).Scan(&dangerousMembership); err != nil {
		return report, fmt.Errorf("inspect runtime role memberships: %w", err)
	}
	report.add("runtime_role_memberships", !dangerousMembership, "runtime role must not reach a privileged role through SET ROLE")

	var schemaUsage, schemaCreate bool
	if err := db.QueryRowContext(ctx, `
		SELECT has_schema_privilege(current_user, current_schema(), 'USAGE'),
		       has_schema_privilege(current_user, current_schema(), 'CREATE')`).Scan(&schemaUsage, &schemaCreate); err != nil {
		return report, fmt.Errorf("inspect schema privileges: %w", err)
	}
	report.add("schema_usage", schemaUsage, "runtime role needs schema USAGE")
	report.add("schema_ddl_denied", !schemaCreate, "runtime role must not create schema objects")

	manifest, err := controlapi.MigrationManifest()
	if err != nil {
		return report, err
	}
	rows, err := db.QueryContext(ctx, `SELECT name, checksum FROM flowops_schema_migrations ORDER BY name`)
	if err != nil {
		return report, fmt.Errorf("read applied migrations: %w", err)
	}
	applied := make(map[string]string)
	for rows.Next() {
		var name, checksum string
		if err := rows.Scan(&name, &checksum); err != nil {
			rows.Close()
			return report, fmt.Errorf("scan applied migration: %w", err)
		}
		applied[name] = checksum
	}
	if err := rows.Close(); err != nil {
		return report, fmt.Errorf("close applied migrations: %w", err)
	}
	if err := rows.Err(); err != nil {
		return report, fmt.Errorf("iterate applied migrations: %w", err)
	}
	migrationsMatch := len(applied) == len(manifest)
	for _, migration := range manifest {
		migrationsMatch = migrationsMatch && applied[migration.Name] == migration.Checksum
	}
	report.add("migration_manifest", migrationsMatch, fmt.Sprintf("database must match all %d embedded migration checksums with no extras", len(manifest)))

	for _, table := range runtimeTables {
		for _, privilege := range tablePrivileges {
			var granted bool
			if err := db.QueryRowContext(ctx, `SELECT has_table_privilege(current_user, $1, $2)`, table.name, privilege).Scan(&granted); err != nil {
				return report, fmt.Errorf("inspect %s %s privilege: %w", table.name, privilege, err)
			}
			want := table.allowed[privilege]
			report.add("table_"+table.name+"_"+strings.ToLower(privilege), granted == want,
				fmt.Sprintf("%s must be %v for runtime role", privilege, want))
		}
	}
	for _, contract := range runtimeColumnUpdates {
		rows, err := db.QueryContext(ctx, `
			SELECT column_name
			FROM information_schema.columns
			WHERE table_schema=current_schema() AND table_name=$1
			  AND has_column_privilege(
			      current_user,
			      quote_ident(table_schema) || '.' || quote_ident(table_name),
			      column_name,
			      'UPDATE')
			ORDER BY ordinal_position`, contract.table)
		if err != nil {
			return report, fmt.Errorf("inspect %s column UPDATE privileges: %w", contract.table, err)
		}
		actual := map[string]bool{}
		for rows.Next() {
			var column string
			if err := rows.Scan(&column); err != nil {
				_ = rows.Close()
				return report, fmt.Errorf("scan %s column UPDATE privilege: %w", contract.table, err)
			}
			actual[column] = true
		}
		if err := rows.Close(); err != nil {
			return report, fmt.Errorf("close %s column UPDATE privileges: %w", contract.table, err)
		}
		if err := rows.Err(); err != nil {
			return report, fmt.Errorf("iterate %s column UPDATE privileges: %w", contract.table, err)
		}
		report.add("columns_"+contract.table+"_update", sameSet(actual, contract.columns),
			fmt.Sprintf("column UPDATE privileges must match the reviewed mutable fields for %s", contract.table))
	}
	for _, contract := range runtimeColumnInserts {
		rows, err := db.QueryContext(ctx, `
			SELECT column_name
			FROM information_schema.columns
			WHERE table_schema=current_schema() AND table_name=$1
			  AND has_column_privilege(
			      current_user,
			      quote_ident(table_schema) || '.' || quote_ident(table_name),
			      column_name,
			      'INSERT')
			ORDER BY ordinal_position`, contract.table)
		if err != nil {
			return report, fmt.Errorf("inspect %s column INSERT privileges: %w", contract.table, err)
		}
		actual := map[string]bool{}
		for rows.Next() {
			var column string
			if err := rows.Scan(&column); err != nil {
				_ = rows.Close()
				return report, fmt.Errorf("scan %s column INSERT privilege: %w", contract.table, err)
			}
			actual[column] = true
		}
		if err := rows.Close(); err != nil {
			return report, fmt.Errorf("close %s column INSERT privileges: %w", contract.table, err)
		}
		if err := rows.Err(); err != nil {
			return report, fmt.Errorf("iterate %s column INSERT privileges: %w", contract.table, err)
		}
		report.add("columns_"+contract.table+"_insert", sameSet(actual, contract.columns),
			fmt.Sprintf("column INSERT privileges must match the reviewed enqueue fields for %s", contract.table))
	}
	report.Ready = true
	for _, check := range report.Checks {
		report.Ready = report.Ready && check.Passed
	}
	return report, nil
}

func sameSet(left, right map[string]bool) bool {
	if len(left) != len(right) {
		return false
	}
	for value := range left {
		if !right[value] {
			return false
		}
	}
	return true
}

func (r *SQLReport) add(name string, passed bool, detail string) {
	r.Checks = append(r.Checks, Check{Name: name, Passed: passed, Detail: detail})
}

func RuntimeTableNames() []string {
	names := make([]string, 0, len(runtimeTables))
	for _, table := range runtimeTables {
		names = append(names, table.name)
	}
	sort.Strings(names)
	return names
}

func ValidateRuntimeURL(raw string) error {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") || parsed.Host == "" || parsed.Path == "" {
		return fmt.Errorf("FLOWOPS_DATABASE_URL must be a PostgreSQL URL")
	}
	if parsed.Query().Get("sslmode") != "verify-full" {
		return fmt.Errorf("FLOWOPS_DATABASE_URL must set sslmode=verify-full")
	}
	return nil
}
