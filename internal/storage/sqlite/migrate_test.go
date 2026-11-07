// This file is an internal test (package sqlite) so it can inspect
// schema_migrations directly through the unexported db handle, rather than
// growing a test-only exported accessor on Store.
package sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/borisovdev/newsbuddy/internal/model"
)

func openInternal(t *testing.T, path string) *Store {
	t.Helper()
	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("Open(%q): %v", path, err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func appliedList(t *testing.T, s *Store) []string {
	t.Helper()
	rows, err := s.db.QueryContext(context.Background(), "SELECT version FROM schema_migrations ORDER BY version")
	if err != nil {
		t.Fatalf("read schema_migrations: %v", err)
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			t.Fatalf("scan schema_migrations: %v", err)
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read schema_migrations: %v", err)
	}
	return out
}

// TestMigrationsRecordVersion asserts the runner records what it applied. That
// record is what makes a reopen a no-op rather than a lucky IF NOT EXISTS.
func TestMigrationsRecordVersion(t *testing.T) {
	s := openInternal(t, filepath.Join(t.TempDir(), "versions.db"))

	got := appliedList(t, s)
	if len(got) != 1 || got[0] != "0001_init.sql" {
		t.Fatalf("schema_migrations = %v, want [0001_init.sql]", got)
	}
}

// TestMigrationsAreIdempotent: reopening an existing DB must not re-run an
// applied migration, and must not lose data.
func TestMigrationsAreIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "migrate.db")
	ctx := context.Background()

	s1 := openInternal(t, path)
	src, err := s1.AddSource(ctx, model.Source{
		FeedURL:   "https://example.com/keep.xml",
		Title:     "Keep",
		CreatedAt: time.Date(2024, 3, 1, 12, 0, 0, 0, time.UTC),
	}, []string{"seed-1", "seed-2"})
	if err != nil {
		t.Fatalf("AddSource: %v", err)
	}
	if err := s1.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// The reopen succeeding at all is the proof: version is a PRIMARY KEY, so a
	// re-applied migration would fail its schema_migrations INSERT and abort
	// Open. The row count then confirms no second record was written.
	s2 := openInternal(t, path)
	if got := appliedList(t, s2); len(got) != 1 {
		t.Fatalf("schema_migrations after reopen = %v, want exactly one row", got)
	}

	got, err := s2.GetSourceByID(ctx, src.ID)
	if err != nil {
		t.Fatalf("GetSourceByID after reopen: %v", err)
	}
	if got.FeedURL != src.FeedURL {
		t.Fatalf("reopen lost data: got %+v, want feed_url %q", got, src.FeedURL)
	}
	unseen, err := s2.FilterUnseen(ctx, src.ID, []string{"seed-1", "seed-2", "new"})
	if err != nil {
		t.Fatalf("FilterUnseen after reopen: %v", err)
	}
	if len(unseen) != 1 || unseen[0] != "new" {
		t.Fatalf("seed rows after reopen: FilterUnseen = %v, want [new]", unseen)
	}

	// A third open is still fine.
	if err := s2.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	s3 := openInternal(t, path)
	if err := s3.Ping(ctx); err != nil {
		t.Fatalf("Ping after third open: %v", err)
	}
}

// TestFmtParseTimeRoundTrip pins the single formatting site: RFC3339 with
// the UTC offset Z, so lexicographic order equals chronological order.
func TestFmtParseTimeRoundTrip(t *testing.T) {
	loc := time.FixedZone("UTC-3", -3*60*60)
	in := time.Date(2026, 8, 22, 19, 4, 5, 0, loc)

	s := fmtTime(in)
	if s != "2026-08-22T22:04:05Z" {
		t.Fatalf("fmtTime = %q, want 2026-08-22T22:04:05Z (UTC, Z offset)", s)
	}

	out, err := parseTime(s)
	if err != nil {
		t.Fatalf("parseTime(%q): %v", s, err)
	}
	if !out.Equal(in) {
		t.Fatalf("round trip: got %v, want the same instant as %v", out, in)
	}

	// Lexicographic ordering must equal chronological ordering.
	earlier := fmtTime(in.Add(-time.Hour))
	if !(earlier < s) {
		t.Fatalf("lexicographic order broken: %q is not < %q", earlier, s)
	}

	if _, err := parseTime("not a timestamp"); err == nil {
		t.Fatal("parseTime(garbage): got nil error, want a parse error")
	}
}
