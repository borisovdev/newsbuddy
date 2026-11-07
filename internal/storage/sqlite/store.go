// Package sqlite is the production storage.Storage implementation, backed by
// modernc.org/sqlite (pure Go, so CGO_ENABLED=0 builds work).
//
// database/sql and squirrel appear ONLY inside this directory;
// nothing outside it ever sees a driver error, a *sql.DB, or a query builder.
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Masterminds/squirrel"

	"github.com/borisovdev/newsbuddy/internal/storage"

	_ "modernc.org/sqlite" // registers the "sqlite" driver
)

// Store is the SQLite-backed storage.Storage.
type Store struct {
	db *sql.DB
	sb squirrel.StatementBuilderType
}

var _ storage.Storage = (*Store)(nil)

// Open creates the DB file and its parent directory if absent, configures the
// connection, asserts the foreign_keys pragma actually took, and runs
// the embedded migrations before returning.
func Open(ctx context.Context, path string) (*Store, error) {
	// 1. The DB file *and its parent directory* are created if absent.
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("sqlite: resolve %q: %w", path, err)
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return nil, fmt.Errorf("sqlite: create data directory for %q: %w", abs, err)
	}

	// 2. Pragmas. _txlock=immediate makes every BeginTx take the write
	// lock up front, removing the read→write upgrade deadlock class entirely.
	dsn := "file:" + abs +
		"?_pragma=busy_timeout(5000)" +
		"&_pragma=journal_mode(WAL)" +
		"&_pragma=foreign_keys(ON)" +
		"&_pragma=synchronous(NORMAL)" +
		"&_txlock=immediate"

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("sqlite: open %q: %w", abs, err)
	}

	// 3. A single connection serializes all DB access. SQLITE_BUSY becomes
	// structurally impossible and the DSN pragmas are guaranteed to apply to
	// *the* connection rather than to whichever pooled connection is picked up.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)

	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("sqlite: connect to %q: %w", abs, err)
	}

	// 4. Pragma assertion. A silently-off foreign_keys turns the delete cascade into
	// an orphan-row leak and the deleted-mid-cycle guard into a no-op — invisible until
	// the data is already wrong, so it is fatal here instead.
	var fk int
	if err := db.QueryRowContext(ctx, "PRAGMA foreign_keys;").Scan(&fk); err != nil {
		db.Close()
		return nil, fmt.Errorf("sqlite: read foreign_keys pragma: %w", err)
	}
	if fk != 1 {
		db.Close()
		return nil, fmt.Errorf("sqlite: foreign_keys pragma is %d, want 1 (ON DELETE CASCADE would not fire)", fk)
	}

	// 5. Migrations.
	if err := migrate(ctx, db); err != nil {
		db.Close()
		return nil, err
	}

	return &Store{db: db, sb: sb}, nil
}

func (s *Store) Ping(ctx context.Context) error {
	if err := s.db.PingContext(ctx); err != nil {
		return fmt.Errorf("sqlite: ping: %w", err)
	}
	return nil
}

func (s *Store) Close() error {
	if err := s.db.Close(); err != nil {
		return fmt.Errorf("sqlite: close: %w", err)
	}
	return nil
}

// fmtTime is the single timestamp-formatting site in this package:
// RFC3339 with the UTC offset Z, e.g. 2026-08-22T19:04:05Z. Lexicographic
// ordering therefore equals chronological ordering, which the 24h count relies
// on.
func fmtTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339)
}

// parseTime is fmtTime's inverse.
func parseTime(s string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("sqlite: parse timestamp %q: %w", s, err)
	}
	return t.UTC(), nil
}

// fmtTimePtr is fmtTime for a nullable column: a nil *time.Time is written as
// SQL NULL, which is nullTime's inverse.
func fmtTimePtr(t *time.Time) any {
	if t == nil {
		return nil
	}
	return fmtTime(*t)
}

// nullTime maps a nullable TEXT timestamp column to *time.Time.
func nullTime(ns sql.NullString) (*time.Time, error) {
	if !ns.Valid || ns.String == "" {
		return nil, nil
	}
	t, err := parseTime(ns.String)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// mapErr translates driver errors into the three storage sentinels.
// Nothing outside this package ever sees a driver error.
func mapErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return storage.ErrNotFound
	}
	msg := err.Error()
	switch {
	// SQLite names the table.column in a unique-index violation, not the index:
	// modernc.org/sqlite v1.57.0 reports
	//   constraint failed: UNIQUE constraint failed: sources.feed_url (2067)
	// The "ux_sources_feed_url" substring would therefore never match, so
	// sources.feed_url is the live arm. The index name is kept as a second arm
	// only in case a future SQLite build reports it that way.
	case strings.Contains(msg, "UNIQUE constraint failed: sources.feed_url"),
		strings.Contains(msg, "UNIQUE constraint failed: ux_sources_feed_url"):
		return storage.ErrDuplicate
	case strings.Contains(msg, "FOREIGN KEY constraint failed"):
		return storage.ErrSourceGone
	}
	return err
}
