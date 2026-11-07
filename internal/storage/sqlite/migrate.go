package sqlite

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"slices"
	"time"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// migrate applies every embedded migration that is not yet recorded in
// schema_migrations, in lexical filename order. Filenames are
// NNNN_name.sql with a zero-padded 4-digit prefix, so lexical order is numeric
// order. There are no down migrations and no checksum verification: this is a
// single-owner tool.
func migrate(ctx context.Context, db *sql.DB) error {
	const createTable = `CREATE TABLE IF NOT EXISTS schema_migrations (
	version    TEXT PRIMARY KEY,
	applied_at TEXT NOT NULL
);`
	if _, err := db.ExecContext(ctx, createTable); err != nil {
		return fmt.Errorf("sqlite: create schema_migrations: %w", err)
	}

	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		return fmt.Errorf("sqlite: read embedded migrations: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	slices.Sort(names)

	applied, err := appliedVersions(ctx, db)
	if err != nil {
		return err
	}

	for _, name := range names {
		if _, found := slices.BinarySearch(applied, name); found {
			continue
		}
		if err := applyMigration(ctx, db, name); err != nil {
			return err
		}
	}
	return nil
}

// appliedVersions returns the recorded migration filenames, sorted so callers
// can binary-search them (the cache-boundary check bans keyed-map lookups outside internal/cache).
func appliedVersions(ctx context.Context, db *sql.DB) ([]string, error) {
	q := sb.Select("version").From("schema_migrations")
	sqlStr, args, err := q.ToSql()
	if err != nil {
		return nil, fmt.Errorf("sqlite: build schema_migrations query: %w", err)
	}
	rows, err := db.QueryContext(ctx, sqlStr, args...)
	if err != nil {
		return nil, fmt.Errorf("sqlite: read schema_migrations: %w", err)
	}
	defer rows.Close()

	var versions []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, fmt.Errorf("sqlite: scan schema_migrations: %w", err)
		}
		versions = append(versions, v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlite: read schema_migrations: %w", err)
	}
	slices.Sort(versions)
	return versions, nil
}

// applyMigration runs one migration file as a single ExecContext inside one
// transaction, then records it. modernc's ExecContext accepts multiple
// ;-separated statements in one call, so the file executes as one unit. Any
// error aborts startup with the filename in the message.
func applyMigration(ctx context.Context, db *sql.DB, name string) error {
	body, err := migrationsFS.ReadFile("migrations/" + name)
	if err != nil {
		return fmt.Errorf("sqlite: read migration %s: %w", name, err)
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("sqlite: begin migration %s: %w", name, err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op once the tx is committed

	if _, err := tx.ExecContext(ctx, string(body)); err != nil {
		return fmt.Errorf("sqlite: apply migration %s: %w", name, err)
	}

	insert := sb.Insert("schema_migrations").
		Columns("version", "applied_at").
		Values(name, fmtTime(time.Now()))
	sqlStr, args, err := insert.ToSql()
	if err != nil {
		return fmt.Errorf("sqlite: build record for migration %s: %w", name, err)
	}
	if _, err := tx.ExecContext(ctx, sqlStr, args...); err != nil {
		return fmt.Errorf("sqlite: record migration %s: %w", name, err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("sqlite: commit migration %s: %w", name, err)
	}
	return nil
}
