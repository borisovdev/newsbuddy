package sqlite_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/borisovdev/newsbuddy/internal/model"
	"github.com/borisovdev/newsbuddy/internal/storage"
	"github.com/borisovdev/newsbuddy/internal/storage/sqlite"
	"github.com/borisovdev/newsbuddy/internal/storage/storagetest"
)

// TestContract runs the shared storage.Storage suite against real SQLite files
// . The fake and the real implementation cannot silently drift.
func TestContract(t *testing.T) {
	storagetest.RunContractTests(t, func(t *testing.T) storage.Storage {
		s, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "test.db"))
		if err != nil {
			t.Fatalf("sqlite.Open: %v", err)
		}
		t.Cleanup(func() { _ = s.Close() })
		return s
	})
}

// open is the per-test helper for the SQLite-specific cases.
func open(t *testing.T, path string) *sqlite.Store {
	t.Helper()
	s, err := sqlite.Open(context.Background(), path)
	if err != nil {
		t.Fatalf("sqlite.Open(%q): %v", path, err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func newStore(t *testing.T) *sqlite.Store {
	t.Helper()
	return open(t, filepath.Join(t.TempDir(), "news.db"))
}

func addSource(t *testing.T, s *sqlite.Store, url string, seed []string) model.Source {
	t.Helper()
	src, err := s.AddSource(context.Background(), model.Source{
		FeedURL:   url,
		Title:     "T",
		CreatedAt: time.Date(2024, 3, 1, 12, 0, 0, 0, time.UTC),
	}, seed)
	if err != nil {
		t.Fatalf("AddSource(%q): %v", url, err)
	}
	return src
}

// TestOpenCreatesParentDirectory: a first boot with no data
// directory at all must still come up.
func TestOpenCreatesParentDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "deeper", "news.db")
	s := open(t, path)

	if err := s.Ping(context.Background()); err != nil {
		t.Fatalf("Ping: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("DB file was not created at %s: %v", path, err)
	}
}

// TestForeignKeysPragmaOn is the assertion Open makes, verified from the
// outside: without it, the delete cascade silently degrades to an orphan-row leak.
func TestForeignKeysPragmaOn(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	// A cascade delete only fires with foreign_keys=ON, and an insert against a
	// missing parent only fails with foreign_keys=ON. Both are exercised here;
	// see TestForeignKeyCascadeDeletesSeenItems and the ErrSourceGone case.
	src := addSource(t, s, "https://example.com/fk.xml", nil)
	if _, err := s.InsertPending(ctx, src.ID+9999, "orphan"); !errors.Is(err, storage.ErrSourceGone) {
		t.Fatalf("InsertPending against a nonexistent source: got %v, want ErrSourceGone (foreign_keys is off)", err)
	}
}

// TestForeignKeyCascadeDeletesSeenItems asserts the cascade at the row level,
// not just through the FilterUnseen proxy the contract suite uses.
func TestForeignKeyCascadeDeletesSeenItems(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	src := addSource(t, s, "https://example.com/cascade.xml", []string{"a", "b", "c"})
	if _, err := s.InsertPending(ctx, src.ID, "pending"); err != nil {
		t.Fatalf("InsertPending: %v", err)
	}

	// Sanity: the rows are there before the delete.
	unseen, err := s.FilterUnseen(ctx, src.ID, []string{"a", "b", "c", "pending"})
	if err != nil {
		t.Fatalf("FilterUnseen: %v", err)
	}
	if len(unseen) != 0 {
		t.Fatalf("FilterUnseen before delete = %v, want none unseen", unseen)
	}

	if err := s.DeleteSource(ctx, src.ID); err != nil {
		t.Fatalf("DeleteSource: %v", err)
	}

	// After the cascade there is nothing left to abandon or count, and every key
	// looks unseen again.
	unseen, err = s.FilterUnseen(ctx, src.ID, []string{"a", "b", "c", "pending"})
	if err != nil {
		t.Fatalf("FilterUnseen after delete: %v", err)
	}
	if len(unseen) != 4 {
		t.Fatalf("FilterUnseen after cascade = %v, want all 4 keys unseen (seen_items rows leaked)", unseen)
	}
	abandoned, err := s.AbandonPending(ctx, time.Now())
	if err != nil {
		t.Fatalf("AbandonPending: %v", err)
	}
	if len(abandoned) != 0 {
		t.Fatalf("AbandonPending found %d orphan pending rows after the cascade, want 0", len(abandoned))
	}
}

// TestIDsNeverReusedAcrossReopen: ids are never reused, with the AUTOINCREMENT counter
// under test: a plain INTEGER PRIMARY KEY would hand the freed id back out,
// letting a stale id from the user's scrollback remove the wrong source.
func TestIDsNeverReusedAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ids.db")
	ctx := context.Background()

	s := open(t, path)
	a := addSource(t, s, "https://example.com/a.xml", nil)
	b := addSource(t, s, "https://example.com/b.xml", nil)
	if err := s.DeleteSource(ctx, b.ID); err != nil {
		t.Fatalf("DeleteSource(%d): %v", b.ID, err)
	}
	if err := s.DeleteSource(ctx, a.ID); err != nil {
		t.Fatalf("DeleteSource(%d): %v", a.ID, err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// The counter survives both the deletes and the reopen (sqlite_sequence).
	s2 := open(t, path)
	c := addSource(t, s2, "https://example.com/c.xml", nil)
	if c.ID <= b.ID {
		t.Fatalf("id reused: got %d after deleting ids %d and %d, want > %d", c.ID, a.ID, b.ID, b.ID)
	}
}

// TestWALFilesCreated confirms journal_mode=WAL actually took on the
// connection rather than being silently ignored in the DSN.
func TestWALFilesCreated(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wal.db")
	s := open(t, path)

	addSource(t, s, "https://example.com/wal.xml", []string{"k1", "k2"})

	if _, err := os.Stat(path + "-wal"); err != nil {
		t.Fatalf("no %s-wal file after a write: %v (journal_mode=WAL did not apply)", filepath.Base(path), err)
	}
}

// TestErrorMappingHidesDriverErrors: nothing outside this package ever sees a
// driver error, so the three sentinels must come back from the three paths that
// produce them.
func TestErrorMappingHidesDriverErrors(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	src := addSource(t, s, "https://example.com/dup.xml", nil)

	_, err := s.AddSource(ctx, model.Source{
		FeedURL:   "https://example.com/dup.xml",
		Title:     "Second",
		CreatedAt: time.Now(),
	}, nil)
	if !errors.Is(err, storage.ErrDuplicate) {
		t.Fatalf("duplicate feed_url: got %v, want ErrDuplicate", err)
	}

	if _, err := s.GetSourceByID(ctx, src.ID+9999); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("GetSourceByID(missing): got %v, want ErrNotFound", err)
	}
	if err := s.DeleteSource(ctx, src.ID+9999); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("DeleteSource(missing): got %v, want ErrNotFound", err)
	}
}

// TestTimestampsRoundTripAsRFC3339Z: timestamps are stored so that
// lexicographic ordering equals chronological ordering, which the 24h count in
// CountPostedSince relies on.
func TestTimestampsRoundTripAsRFC3339Z(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	// A non-UTC input must come back as the same instant.
	loc := time.FixedZone("UTC+5", 5*60*60)
	created := time.Date(2024, 3, 1, 17, 4, 5, 0, loc)
	src, err := s.AddSource(ctx, model.Source{
		FeedURL:   "https://example.com/tz.xml",
		Title:     "TZ",
		CreatedAt: created,
	}, nil)
	if err != nil {
		t.Fatalf("AddSource: %v", err)
	}

	got, err := s.GetSourceByID(ctx, src.ID)
	if err != nil {
		t.Fatalf("GetSourceByID: %v", err)
	}
	if !got.CreatedAt.Equal(created) {
		t.Fatalf("created_at round trip: got %v, want the same instant as %v", got.CreatedAt, created)
	}
	if got.CreatedAt.Location() != time.UTC {
		t.Fatalf("created_at location = %v, want UTC", got.CreatedAt.Location())
	}
	if got.LastPolledAt != nil || got.LastSuccessAt != nil {
		t.Fatalf("unset nullable timestamps: got %v/%v, want nil/nil", got.LastPolledAt, got.LastSuccessAt)
	}
}
