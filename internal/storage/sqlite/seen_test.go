package sqlite_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/borisovdev/newsbuddy/internal/storage"
)

// TestInsertPendingOnConflictDoNothing pins the dedup gate at the SQL level: the
// PRIMARY KEY decides, and a conflict is reported as inserted=false rather than
// as an error.
func TestInsertPendingOnConflictDoNothing(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	src := addSource(t, s, "https://example.com/conflict.xml", nil)

	inserted, err := s.InsertPending(ctx, src.ID, "item-1")
	if err != nil {
		t.Fatalf("InsertPending: %v", err)
	}
	if !inserted {
		t.Fatal("first InsertPending: got inserted=false, want true")
	}

	for i := 0; i < 3; i++ {
		inserted, err = s.InsertPending(ctx, src.ID, "item-1")
		if err != nil {
			t.Fatalf("repeat InsertPending %d: %v (ON CONFLICT DO NOTHING must not error)", i, err)
		}
		if inserted {
			t.Fatalf("repeat InsertPending %d: got inserted=true, want false", i)
		}
	}

	// The same key under a different source is a different row.
	other := addSource(t, s, "https://example.com/other.xml", nil)
	inserted, err = s.InsertPending(ctx, other.ID, "item-1")
	if err != nil {
		t.Fatalf("InsertPending on another source: %v", err)
	}
	if !inserted {
		t.Fatal("InsertPending on another source: got inserted=false, want true (dedup is per-source)")
	}
}

// TestInsertPendingDeletedSourceIsErrSourceGone: the source was
// removed between the fetch and the post loop.
func TestInsertPendingDeletedSourceIsErrSourceGone(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	src := addSource(t, s, "https://example.com/gone.xml", nil)

	if err := s.DeleteSource(ctx, src.ID); err != nil {
		t.Fatalf("DeleteSource: %v", err)
	}
	if _, err := s.InsertPending(ctx, src.ID, "item-1"); !errors.Is(err, storage.ErrSourceGone) {
		t.Fatalf("InsertPending on a deleted source: got %v, want ErrSourceGone", err)
	}
}

// TestAbandonPendingDoesNotRepeat: each crashed-send row is reported
// exactly once, no matter how many times the service restarts.
func TestAbandonPendingDoesNotRepeat(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	src := addSource(t, s, "https://example.com/abandon.xml", []string{"watermark"})

	for _, k := range []string{"pending-1", "pending-2"} {
		if _, err := s.InsertPending(ctx, src.ID, k); err != nil {
			t.Fatalf("InsertPending(%s): %v", k, err)
		}
	}

	at := time.Date(2024, 3, 1, 12, 0, 0, 0, time.UTC)
	first, err := s.AbandonPending(ctx, at)
	if err != nil {
		t.Fatalf("AbandonPending: %v", err)
	}
	got := make([]string, 0, len(first))
	for _, it := range first {
		if it.SourceID != src.ID {
			t.Fatalf("AbandonPending returned source_id %d, want %d", it.SourceID, src.ID)
		}
		got = append(got, it.ItemKey)
	}
	slices.Sort(got)
	if !slices.Equal(got, []string{"pending-1", "pending-2"}) {
		t.Fatalf("AbandonPending = %v, want [pending-1 pending-2] (the seeded watermark must be skipped)", got)
	}

	// Two more sweeps: the abandoned_at stamp keeps them out for good.
	for round := 2; round <= 3; round++ {
		again, err := s.AbandonPending(ctx, at.Add(time.Duration(round)*time.Hour))
		if err != nil {
			t.Fatalf("AbandonPending round %d: %v", round, err)
		}
		if len(again) != 0 {
			t.Fatalf("AbandonPending round %d returned %d rows, want 0 (logged once)", round, len(again))
		}
	}

	// An abandoned row is still a seen row: it never re-enters the dedup gate.
	inserted, err := s.InsertPending(ctx, src.ID, "pending-1")
	if err != nil {
		t.Fatalf("InsertPending after abandon: %v", err)
	}
	if inserted {
		t.Fatal("InsertPending after abandon: got inserted=true, want false")
	}
}

// TestFilterUnseenChunking drives 1200 keys through the 500-key chunker, which
// is more than one chunk boundary and well past SQLite's older 999-variable
// limit. Order preservation must survive the reassembly.
func TestFilterUnseenChunking(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	const total = 1200
	all := make([]string, 0, total)
	seed := make([]string, 0, total/3)
	want := make([]string, 0, total)
	for i := 0; i < total; i++ {
		// Reverse-ordered keys so a chunker that accidentally sorts its output
		// cannot pass by coincidence.
		key := fmt.Sprintf("key-%04d", total-1-i)
		all = append(all, key)
		if i%3 == 0 {
			seed = append(seed, key)
		} else {
			want = append(want, key)
		}
	}

	src := addSource(t, s, "https://example.com/chunk.xml", seed)
	got, err := s.FilterUnseen(ctx, src.ID, all)
	if err != nil {
		t.Fatalf("FilterUnseen(%d keys): %v", total, err)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("FilterUnseen(%d keys): got %d keys, want %d in input order", total, len(got), len(want))
	}

	// Every seeded key is filtered out, including ones straddling the boundaries
	// of both the 200-row seed chunks and the 500-key filter chunks.
	got, err = s.FilterUnseen(ctx, src.ID, seed)
	if err != nil {
		t.Fatalf("FilterUnseen(seed): %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("FilterUnseen(seed) = %d keys, want 0 (a seed chunk was dropped)", len(got))
	}
}

// TestFilterUnseenEmptyInput: the poller calls this with an empty slice for an
// empty feed, and it must not build a degenerate IN () query.
func TestFilterUnseenEmptyInput(t *testing.T) {
	s := newStore(t)
	src := addSource(t, s, "https://example.com/emptyfilter.xml", nil)

	got, err := s.FilterUnseen(context.Background(), src.ID, nil)
	if err != nil {
		t.Fatalf("FilterUnseen(nil): %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("FilterUnseen(nil) = %v, want empty", got)
	}
}

// TestCountPostedSinceIgnoresUnpostedRows: seeded watermarks and pending rows
// have posted_at IS NULL and must never inflate the /status 24h count.
func TestCountPostedSinceIgnoresUnpostedRows(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	src := addSource(t, s, "https://example.com/count.xml", []string{"seed-1", "seed-2"})

	if _, err := s.InsertPending(ctx, src.ID, "pending-1"); err != nil {
		t.Fatalf("InsertPending: %v", err)
	}
	if _, err := s.InsertPending(ctx, src.ID, "posted-1"); err != nil {
		t.Fatalf("InsertPending: %v", err)
	}

	at := time.Date(2024, 3, 1, 12, 0, 0, 0, time.UTC)
	if err := s.MarkPosted(ctx, src.ID, "posted-1", 42, at); err != nil {
		t.Fatalf("MarkPosted: %v", err)
	}

	n, err := s.CountPostedSince(ctx, at.Add(-24*time.Hour))
	if err != nil {
		t.Fatalf("CountPostedSince: %v", err)
	}
	if n != 1 {
		t.Fatalf("CountPostedSince = %d, want 1 (seed and pending rows must not count)", n)
	}
}
