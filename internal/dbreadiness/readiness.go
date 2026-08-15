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
}

var tablePrivileges = []string{"SELECT", "INSERT", "UPDATE", "DELETE", "TRUNCATE", "REFERENCES", "TRIGGER"}

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
	report.Ready = true
	for _, check := range report.Checks {
		report.Ready = report.Ready && check.Passed
	}
	return report, nil
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
