package controlapi

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"fmt"
	"sort"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

const migrationLock = int64(703247667732)

// Migration describes one migration embedded in this exact binary. The
// checksum is part of the deployment contract: applied migration bytes must
// never be edited in place.
type Migration struct {
	Name     string `json:"name"`
	Checksum string `json:"checksum"`
}

// MigrationManifest returns the ordered, checksummed migration set expected by
// this binary. Readiness tooling uses the same embedded bytes as ApplyMigrations
// rather than maintaining a second list that can drift.
func MigrationManifest() ([]Migration, error) {
	entries, err := migrationFiles.ReadDir("migrations")
	if err != nil {
		return nil, fmt.Errorf("list migrations: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	manifest := make([]Migration, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		script, err := migrationFiles.ReadFile("migrations/" + entry.Name())
		if err != nil {
			return nil, fmt.Errorf("read migration %s: %w", entry.Name(), err)
		}
		digest := sha256.Sum256(script)
		manifest = append(manifest, Migration{Name: entry.Name(), Checksum: hex.EncodeToString(digest[:])})
	}
	return manifest, nil
}

func ApplyMigrations(ctx context.Context, db *sql.DB) error {
	if db == nil {
		return fmt.Errorf("database is required")
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin migration transaction: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, migrationLock); err != nil {
		return fmt.Errorf("lock migrations: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS flowops_schema_migrations (
			name text PRIMARY KEY,
			checksum text NOT NULL,
			applied_at timestamptz NOT NULL DEFAULT now()
		)`); err != nil {
		return fmt.Errorf("create migrations table: %w", err)
	}
	manifest, err := MigrationManifest()
	if err != nil {
		return err
	}
	for _, migration := range manifest {
		script, err := migrationFiles.ReadFile("migrations/" + migration.Name)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", migration.Name, err)
		}
		var storedChecksum string
		err = tx.QueryRowContext(ctx, `SELECT checksum FROM flowops_schema_migrations WHERE name = $1`, migration.Name).Scan(&storedChecksum)
		if err == nil {
			if storedChecksum != migration.Checksum {
				return fmt.Errorf("migration %s checksum changed after application", migration.Name)
			}
			continue
		}
		if err != sql.ErrNoRows {
			return fmt.Errorf("check migration %s: %w", migration.Name, err)
		}
		if _, err := tx.ExecContext(ctx, string(script)); err != nil {
			return fmt.Errorf("apply migration %s: %w", migration.Name, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO flowops_schema_migrations (name, checksum) VALUES ($1, $2)`, migration.Name, migration.Checksum); err != nil {
			return fmt.Errorf("record migration %s: %w", migration.Name, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migrations: %w", err)
	}
	return nil
}
