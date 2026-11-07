package service

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/borisovdev/newsbuddy/internal/cache"
	"github.com/borisovdev/newsbuddy/internal/feed/feedtest"
	"github.com/borisovdev/newsbuddy/internal/storage"
)

// The cache is a one-way filter. The same scenario run
// against a cold cache, a warm cache, and a cache whose every operation
// fails must produce byte-identical results.
func TestUnseenIdenticalAcrossColdWarmAndFailingCache(t *testing.T) {
	const feedURL = "https://example.com/feed"
	ctx := context.Background()

	fx := loadFixture(t, "rss2.xml", feedURL)
	seedKeys := itemKeys(fx.Feed.Items)
	probe := append(append([]string{}, seedKeys...), "new-a", "new-b", "new-c")
	want := []string{"new-a", "new-b", "new-c"}

	// run adds the fixture (seeding every existing key) and then asks for
	// the unseen subset of probe.
	run := func(t *testing.T, c cache.Cache, cold bool) []string {
		t.Helper()
		h := newHarness(c)
		h.f.AddResponse(feedURL, feedtest.Response{Result: loadFixture(t, "rss2.xml", feedURL)})

		src, seeded, err := h.svc.Add(ctx, feedURL, "")
		if err != nil {
			t.Fatalf("Add: %v", err)
		}
		if seeded != len(seedKeys) {
			t.Fatalf("seeded = %d, want %d", seeded, len(seedKeys))
		}
		if cold {
			// Drop what Add warmed, simulating a restart with the DB intact.
			if err := h.seen.Invalidate(ctx, src.ID); err != nil {
				t.Fatalf("Invalidate: %v", err)
			}
		}

		got, err := h.idx.Unseen(ctx, src.ID, probe)
		if err != nil {
			t.Fatalf("Unseen: %v", err)
		}
		return got
	}

	cold := run(t, cache.NewMemory(64), true)
	warm := run(t, cache.NewMemory(64), false)
	broken := run(t, cache.FailingCache{}, false)

	if !reflect.DeepEqual(cold, want) {
		t.Errorf("cold = %v, want %v", cold, want)
	}
	if !reflect.DeepEqual(warm, cold) {
		t.Errorf("warm = %v, cold = %v — a cache hit changed the answer", warm, cold)
	}
	if !reflect.DeepEqual(broken, cold) {
		t.Errorf("failing = %v, cold = %v — a cache error changed the answer", broken, cold)
	}
}

// The cache learns what the DB already knew, so a repeat call skips the
// known keys without changing the answer.
func TestUnseenLearnsKeysTheDatabaseAlreadyKnew(t *testing.T) {
	const feedURL = "https://example.com/feed"
	ctx := context.Background()

	h := newHarness(cache.NewMemory(64))
	h.f.AddResponse(feedURL, feedtest.Response{Result: loadFixture(t, "rss2.xml", feedURL)})
	src, _, err := h.svc.Add(ctx, feedURL, "")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := h.seen.Invalidate(ctx, src.ID); err != nil {
		t.Fatalf("Invalidate: %v", err)
	}

	keys := itemKeys(loadFixture(t, "rss2.xml", feedURL).Feed.Items)

	first, err := h.idx.Unseen(ctx, src.ID, keys)
	if err != nil {
		t.Fatalf("first Unseen: %v", err)
	}
	if len(first) != 0 {
		t.Fatalf("first Unseen = %v, want none", first)
	}

	set, err := h.seen.Load(ctx, src.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if set.Len() != len(keys) {
		t.Errorf("cached set holds %d keys, want %d — the set must learn", set.Len(), len(keys))
	}

	second, err := h.idx.Unseen(ctx, src.ID, keys)
	if err != nil {
		t.Fatalf("second Unseen: %v", err)
	}
	if len(second) != 0 {
		t.Errorf("second Unseen = %v, want none", second)
	}
}

// A key the cache never saw is still checked against the DB: the cache can
// only ever skip work, never skip the authoritative check.
func TestUnseenAlwaysConsultsStorage(t *testing.T) {
	const feedURL = "https://example.com/feed"
	ctx := context.Background()

	h := newHarness(cache.FailingCache{})
	h.f.AddResponse(feedURL, feedtest.Response{Result: loadFixture(t, "empty_items.xml", feedURL)})
	src, _, err := h.svc.Add(ctx, feedURL, "")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	inserted, err := h.idx.InsertPending(ctx, src.ID, "k1")
	if err != nil || !inserted {
		t.Fatalf("InsertPending: inserted=%v err=%v", inserted, err)
	}

	got, err := h.idx.Unseen(ctx, src.ID, []string{"k1", "k2"})
	if err != nil {
		t.Fatalf("Unseen: %v", err)
	}
	if !reflect.DeepEqual(got, []string{"k2"}) {
		t.Errorf("Unseen = %v, want [k2]", got)
	}
}

func TestInsertPendingIsTheDedupGate(t *testing.T) {
	const feedURL = "https://example.com/feed"
	ctx := context.Background()

	h := newHarness(cache.NewMemory(64))
	h.f.AddResponse(feedURL, feedtest.Response{Result: loadFixture(t, "empty_items.xml", feedURL)})
	src, _, err := h.svc.Add(ctx, feedURL, "")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	if inserted, err := h.idx.InsertPending(ctx, src.ID, "k1"); err != nil || !inserted {
		t.Fatalf("first InsertPending: inserted=%v err=%v", inserted, err)
	}
	if inserted, err := h.idx.InsertPending(ctx, src.ID, "k1"); err != nil || inserted {
		t.Fatalf("second InsertPending: inserted=%v err=%v, want false", inserted, err)
	}

	if err := h.idx.MarkPosted(ctx, src.ID, "k1", 7, src.CreatedAt); err != nil {
		t.Fatalf("MarkPosted: %v", err)
	}
	n, err := h.st.CountPostedSince(ctx, src.CreatedAt.Add(-1))
	if err != nil {
		t.Fatalf("CountPostedSince: %v", err)
	}
	if n != 1 {
		t.Errorf("posted count = %d, want 1", n)
	}

	// Edge 25: a source removed mid-cycle surfaces as ErrSourceGone.
	if _, err := h.svc.Remove(ctx, "1"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := h.idx.InsertPending(ctx, src.ID, "k2"); !errors.Is(err, storage.ErrSourceGone) {
		t.Errorf("InsertPending after remove = %v, want storage.ErrSourceGone", err)
	}
}
