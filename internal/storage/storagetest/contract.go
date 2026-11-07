// Package storagetest holds the shared storage.Storage contract suite. The same table of cases runs against memstore and sqlite so the fake
// can never silently drift from the real implementation.
//
// This is a non-test file that imports "testing" on purpose: it is a helper
// package consumed by the _test.go files of each implementation, and it
// compiles with no implementation present because it is generic over the
// newStore constructor.
//
// This file is a non-test .go file outside internal/cache/, so its
// boundary check scans it. Key-set comparisons are built from sorted
// []string and compared with slices.Equal — no string-keyed maps anywhere.
package storagetest

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/borisovdev/newsbuddy/internal/model"
	"github.com/borisovdev/newsbuddy/internal/storage"
)

// baseTime is the fixed wall clock every case works from. Fixed literals keep
// the assertions independent of the monotonic clock and of a store that
// round-trips timestamps through RFC3339 (second precision).
var baseTime = time.Date(2024, 3, 1, 12, 0, 0, 0, time.UTC)

// RunContractTests runs the full storage.Storage contract against the store
// returned by newStore. newStore must hand back a fresh, empty store on every
// call; cleanup (Close, temp dirs) belongs to the caller via t.Cleanup.
func RunContractTests(t *testing.T, newStore func(t *testing.T) storage.Storage) {
	t.Helper()

	t.Run("AddSourceAssignsIDAndSeeds", func(t *testing.T) {
		store := newStore(t)
		ctx := context.Background()
		seed := []string{"seed-a", "seed-b", "seed-c"}

		src := mustAdd(t, store, "https://example.com/a.xml", "Alpha", seed)
		if src.FeedURL != "https://example.com/a.xml" || src.Title != "Alpha" {
			t.Fatalf("AddSource returned %+v, want feed_url/title preserved", src)
		}

		got, err := store.GetSourceByID(ctx, src.ID)
		if err != nil {
			t.Fatalf("GetSourceByID(%d): %v", src.ID, err)
		}
		if got.ID != src.ID || got.FeedURL != src.FeedURL || got.Title != src.Title {
			t.Fatalf("GetSourceByID: got %+v, want %+v", got, src)
		}

		byURL, err := store.GetSourceByURL(ctx, src.FeedURL)
		if err != nil {
			t.Fatalf("GetSourceByURL(%q): %v", src.FeedURL, err)
		}
		if byURL.ID != src.ID {
			t.Fatalf("GetSourceByURL: got id %d, want %d", byURL.ID, src.ID)
		}

		// The seed rows exist, so FilterUnseen must not report them as unseen.
		unseen, err := store.FilterUnseen(ctx, src.ID, []string{"seed-a", "fresh", "seed-b", "seed-c"})
		if err != nil {
			t.Fatalf("FilterUnseen: %v", err)
		}
		wantKeys(t, unseen, []string{"fresh"}, "FilterUnseen after seeding")

		// seeded = 1 is observable only through AbandonPending, which must skip
		// watermark rows; posted_at IS NULL is observable through CountPostedSince.
		abandoned, err := store.AbandonPending(ctx, baseTime)
		if err != nil {
			t.Fatalf("AbandonPending: %v", err)
		}
		if len(abandoned) != 0 {
			t.Fatalf("AbandonPending stamped %d seeded rows, want 0 (seeded=1 rows are never pending)", len(abandoned))
		}
		posted, err := store.CountPostedSince(ctx, baseTime.Add(-24*time.Hour))
		if err != nil {
			t.Fatalf("CountPostedSince: %v", err)
		}
		if posted != 0 {
			t.Fatalf("CountPostedSince = %d, want 0 (seed rows have posted_at IS NULL)", posted)
		}
	})

	// SourceService.Add stamps last_polled_at/last_success_at on the source it hands
	// to AddSource, because the add already performed a successful fetch. A
	// backend that drops them makes /list say "last ok: never" right after a
	// successful /add.
	t.Run("AddSourcePersistsPollTimestamps", func(t *testing.T) {
		store := newStore(t)
		ctx := context.Background()

		src := newSource("https://example.com/stamped.xml", "Stamped")
		src.LastPolledAt = &baseTime
		src.LastSuccessAt = &baseTime

		added, err := store.AddSource(ctx, src, nil)
		if err != nil {
			t.Fatalf("AddSource: %v", err)
		}

		got, err := store.GetSourceByID(ctx, added.ID)
		if err != nil {
			t.Fatalf("GetSourceByID(%d): %v", added.ID, err)
		}
		if got.LastPolledAt == nil || !sameSecond(*got.LastPolledAt, baseTime) {
			t.Errorf("last_polled_at = %v, want %v", got.LastPolledAt, baseTime)
		}
		if got.LastSuccessAt == nil || !sameSecond(*got.LastSuccessAt, baseTime) {
			t.Errorf("last_success_at = %v, want %v", got.LastSuccessAt, baseTime)
		}
	})

	t.Run("AddSourceEmptySeedKeys", func(t *testing.T) {
		store := newStore(t)
		ctx := context.Background()

		src := mustAdd(t, store, "https://example.com/empty.xml", "Empty", nil)

		unseen, err := store.FilterUnseen(ctx, src.ID, []string{"k1", "k2"})
		if err != nil {
			t.Fatalf("FilterUnseen: %v", err)
		}
		wantKeys(t, unseen, []string{"k1", "k2"}, "FilterUnseen with no seed rows")
	})

	t.Run("AddSourceDuplicateURL", func(t *testing.T) {
		store := newStore(t)
		ctx := context.Background()
		const url = "https://example.com/dup.xml"

		mustAdd(t, store, url, "First", []string{"k1"})

		_, err := store.AddSource(ctx, newSource(url, "Second"), []string{"k2"})
		if !errors.Is(err, storage.ErrDuplicate) {
			t.Fatalf("AddSource duplicate feed_url: got %v, want ErrDuplicate", err)
		}

		list, err := store.ListSources(ctx)
		if err != nil {
			t.Fatalf("ListSources: %v", err)
		}
		if len(list) != 1 {
			t.Fatalf("ListSources: got %d sources after a rejected duplicate, want 1", len(list))
		}
	})

	t.Run("GetSourceMissing", func(t *testing.T) {
		store := newStore(t)
		ctx := context.Background()

		if _, err := store.GetSourceByID(ctx, 4242); !errors.Is(err, storage.ErrNotFound) {
			t.Fatalf("GetSourceByID(missing): got %v, want ErrNotFound", err)
		}
		if _, err := store.GetSourceByURL(ctx, "https://example.com/missing.xml"); !errors.Is(err, storage.ErrNotFound) {
			t.Fatalf("GetSourceByURL(missing): got %v, want ErrNotFound", err)
		}
	})

	t.Run("ListSourcesOrderedByID", func(t *testing.T) {
		store := newStore(t)
		ctx := context.Background()

		a := mustAdd(t, store, "https://example.com/c.xml", "C", nil)
		b := mustAdd(t, store, "https://example.com/a.xml", "A", nil)
		c := mustAdd(t, store, "https://example.com/b.xml", "B", nil)

		list, err := store.ListSources(ctx)
		if err != nil {
			t.Fatalf("ListSources: %v", err)
		}
		if len(list) != 3 {
			t.Fatalf("ListSources: got %d sources, want 3", len(list))
		}
		for i := 1; i < len(list); i++ {
			if list[i-1].ID >= list[i].ID {
				t.Fatalf("ListSources not ordered by id ASC: %d before %d", list[i-1].ID, list[i].ID)
			}
		}
		gotURLs := []string{list[0].FeedURL, list[1].FeedURL, list[2].FeedURL}
		wantKeys(t, gotURLs, []string{a.FeedURL, b.FeedURL, c.FeedURL}, "ListSources insertion order")
	})

	t.Run("DeleteSourceCascadesSeenItems", func(t *testing.T) {
		store := newStore(t)
		ctx := context.Background()
		const url = "https://example.com/cascade.xml"
		seed := []string{"seed-a", "seed-b"}

		src := mustAdd(t, store, url, "Cascade", seed)
		if err := store.DeleteSource(ctx, src.ID); err != nil {
			t.Fatalf("DeleteSource(%d): %v", src.ID, err)
		}
		if _, err := store.GetSourceByID(ctx, src.ID); !errors.Is(err, storage.ErrNotFound) {
			t.Fatalf("GetSourceByID after delete: got %v, want ErrNotFound", err)
		}

		// The seen_items rows went with the source, so the old id knows
		// nothing about the seeded keys any more.
		unseen, err := store.FilterUnseen(ctx, src.ID, seed)
		if err != nil {
			t.Fatalf("FilterUnseen on deleted source: %v", err)
		}
		wantKeys(t, unseen, seed, "FilterUnseen on deleted source")

		// Re-adding the same feed_url must succeed and start from a clean slate.
		reAdded := mustAdd(t, store, url, "Cascade again", nil)
		unseen, err = store.FilterUnseen(ctx, reAdded.ID, seed)
		if err != nil {
			t.Fatalf("FilterUnseen after re-add: %v", err)
		}
		wantKeys(t, unseen, seed, "FilterUnseen after re-add")
	})

	t.Run("DeleteSourceMissing", func(t *testing.T) {
		store := newStore(t)

		if err := store.DeleteSource(context.Background(), 4242); !errors.Is(err, storage.ErrNotFound) {
			t.Fatalf("DeleteSource(missing): got %v, want ErrNotFound", err)
		}
	})

	t.Run("IDsNeverReused", func(t *testing.T) {
		store := newStore(t)
		ctx := context.Background()

		a := mustAdd(t, store, "https://example.com/a.xml", "A", nil)
		if err := store.DeleteSource(ctx, a.ID); err != nil {
			t.Fatalf("DeleteSource(%d): %v", a.ID, err)
		}
		b := mustAdd(t, store, "https://example.com/b.xml", "B", nil)
		if b.ID <= a.ID {
			t.Fatalf("id reused after delete: first id %d, next id %d, want strictly greater", a.ID, b.ID)
		}
	})

	t.Run("UpdateSourcePollResultSuccess", func(t *testing.T) {
		store := newStore(t)
		src := mustAdd(t, store, "https://example.com/poll.xml", "Poll", nil)

		firstPoll := baseTime.Add(time.Hour)
		mustUpdate(t, store, model.PollResult{
			SourceID:        src.ID,
			PolledAt:        firstPoll,
			Success:         true,
			ETag:            "etag-1",
			LastModified:    "Mon, 01 Mar 2024 12:00:00 GMT",
			StoreValidators: true,
		})

		got := mustGet(t, store, src.ID)
		if got.LastPolledAt == nil || !sameSecond(*got.LastPolledAt, firstPoll) {
			t.Fatalf("last_polled_at = %v, want %v", got.LastPolledAt, firstPoll)
		}
		if got.LastSuccessAt == nil || !sameSecond(*got.LastSuccessAt, firstPoll) {
			t.Fatalf("last_success_at = %v, want %v", got.LastSuccessAt, firstPoll)
		}
		if got.ConsecutiveFailures != 0 || got.LastError != "" {
			t.Fatalf("after success: failures=%d last_error=%q, want 0 and \"\"", got.ConsecutiveFailures, got.LastError)
		}
		if got.ETag != "etag-1" || got.LastModified != "Mon, 01 Mar 2024 12:00:00 GMT" {
			t.Fatalf("validators not stored: etag=%q last_modified=%q", got.ETag, got.LastModified)
		}

		// A failure in between must be wiped by the next success.
		mustUpdate(t, store, model.PollResult{
			SourceID:   src.ID,
			PolledAt:   firstPoll.Add(time.Hour),
			Success:    false,
			ErrMessage: "dial tcp: connection refused",
		})
		secondPoll := firstPoll.Add(2 * time.Hour)
		mustUpdate(t, store, model.PollResult{
			SourceID:        src.ID,
			PolledAt:        secondPoll,
			Success:         true,
			ETag:            "etag-2",
			LastModified:    "Mon, 01 Mar 2024 14:00:00 GMT",
			StoreValidators: true,
		})

		got = mustGet(t, store, src.ID)
		if got.ConsecutiveFailures != 0 {
			t.Fatalf("consecutive_failures = %d after success, want 0", got.ConsecutiveFailures)
		}
		if got.LastError != "" {
			t.Fatalf("last_error = %q after success, want cleared", got.LastError)
		}
		if got.LastSuccessAt == nil || !sameSecond(*got.LastSuccessAt, secondPoll) {
			t.Fatalf("last_success_at = %v, want %v", got.LastSuccessAt, secondPoll)
		}
		if got.ETag != "etag-2" {
			t.Fatalf("etag = %q, want etag-2", got.ETag)
		}
	})

	t.Run("UpdateSourcePollResultFailureLeavesValidators", func(t *testing.T) {
		// StoreValidators is honored only when Success is
		// true. A failure result must never touch etag/last_modified, whatever
		// its validator fields hold.
		store := newStore(t)
		src := mustAdd(t, store, "https://example.com/fail.xml", "Fail", nil)

		okAt := baseTime.Add(time.Hour)
		mustUpdate(t, store, model.PollResult{
			SourceID:        src.ID,
			PolledAt:        okAt,
			Success:         true,
			ETag:            "etag-keep",
			LastModified:    "lm-keep",
			StoreValidators: true,
		})

		// Failure with the zero-valued StoreValidators — the natural literal.
		failAt := okAt.Add(time.Hour)
		mustUpdate(t, store, model.PollResult{
			SourceID:   src.ID,
			PolledAt:   failAt,
			Success:    false,
			ErrMessage: "http 500",
		})

		got := mustGet(t, store, src.ID)
		if got.ConsecutiveFailures != 1 {
			t.Fatalf("consecutive_failures = %d after 1 failure, want 1", got.ConsecutiveFailures)
		}
		if got.LastError != "http 500" {
			t.Fatalf("last_error = %q, want %q", got.LastError, "http 500")
		}
		if got.ETag != "etag-keep" || got.LastModified != "lm-keep" {
			t.Fatalf("failure wiped validators: etag=%q last_modified=%q, want etag-keep/lm-keep (F3)", got.ETag, got.LastModified)
		}
		if got.LastSuccessAt == nil || !sameSecond(*got.LastSuccessAt, okAt) {
			t.Fatalf("last_success_at = %v after a failure, want it unchanged at %v", got.LastSuccessAt, okAt)
		}
		if got.LastPolledAt == nil || !sameSecond(*got.LastPolledAt, failAt) {
			t.Fatalf("last_polled_at = %v, want %v", got.LastPolledAt, failAt)
		}

		// A second failure increments again, and StoreValidators: true on a
		// failure result is still ignored (F3).
		mustUpdate(t, store, model.PollResult{
			SourceID:        src.ID,
			PolledAt:        failAt.Add(time.Hour),
			Success:         false,
			ErrMessage:      "http 502",
			StoreValidators: true,
		})

		got = mustGet(t, store, src.ID)
		if got.ConsecutiveFailures != 2 {
			t.Fatalf("consecutive_failures = %d after 2 failures, want 2", got.ConsecutiveFailures)
		}
		if got.ETag != "etag-keep" || got.LastModified != "lm-keep" {
			t.Fatalf("failure with StoreValidators:true cleared validators: etag=%q last_modified=%q (F3)", got.ETag, got.LastModified)
		}
	})

	t.Run("SuccessWithoutStoreValidatorsClearsThem", func(t *testing.T) {
		// A truncated cycle must drop the validators so the next poll
		// refetches the full body.
		store := newStore(t)
		src := mustAdd(t, store, "https://example.com/trunc.xml", "Truncated", nil)

		at := baseTime.Add(time.Hour)
		mustUpdate(t, store, model.PollResult{
			SourceID:        src.ID,
			PolledAt:        at,
			Success:         true,
			ETag:            "etag-1",
			LastModified:    "lm-1",
			StoreValidators: true,
		})
		mustUpdate(t, store, model.PollResult{
			SourceID:        src.ID,
			PolledAt:        at.Add(time.Hour),
			Success:         true,
			ETag:            "ignored",
			LastModified:    "ignored",
			StoreValidators: false,
		})

		got := mustGet(t, store, src.ID)
		if got.ETag != "" || got.LastModified != "" {
			t.Fatalf("StoreValidators:false on success left etag=%q last_modified=%q, want both cleared", got.ETag, got.LastModified)
		}
	})

	t.Run("ValidatorsSurvive304RoundTrip", func(t *testing.T) {
		// A 304 echoes the request validators back, so
		// two consecutive 304 cycles must leave the stored etag non-empty.
		store := newStore(t)
		src := mustAdd(t, store, "https://example.com/cond.xml", "Conditional", nil)

		at := baseTime.Add(time.Hour)
		mustUpdate(t, store, model.PollResult{
			SourceID:        src.ID,
			PolledAt:        at,
			Success:         true,
			ETag:            "etag-304",
			LastModified:    "lm-304",
			StoreValidators: true,
		})

		for round := 1; round <= 2; round++ {
			cur := mustGet(t, store, src.ID)
			if cur.ETag == "" {
				t.Fatalf("round %d: etag empty before the 304 cycle, want it preserved (F9)", round)
			}
			mustUpdate(t, store, model.PollResult{
				SourceID:        src.ID,
				PolledAt:        at.Add(time.Duration(round) * time.Hour),
				Success:         true,
				ETag:            cur.ETag,         // echoed back by the 304 path
				LastModified:    cur.LastModified, // ditto
				StoreValidators: true,
			})
			after := mustGet(t, store, src.ID)
			if after.ETag != "etag-304" || after.LastModified != "lm-304" {
				t.Fatalf("round %d: 304 round trip left etag=%q last_modified=%q, want etag-304/lm-304 (F9/F10)", round, after.ETag, after.LastModified)
			}
		}
	})

	t.Run("FilterUnseenPreservesOrder", func(t *testing.T) {
		store := newStore(t)
		ctx := context.Background()
		src := mustAdd(t, store, "https://example.com/order.xml", "Order", []string{"known-b", "known-d"})

		unseen, err := store.FilterUnseen(ctx, src.ID, []string{"new-e", "known-d", "new-a", "known-b", "new-c"})
		if err != nil {
			t.Fatalf("FilterUnseen: %v", err)
		}
		wantKeys(t, unseen, []string{"new-e", "new-a", "new-c"}, "FilterUnseen input order")
	})

	t.Run("FilterUnseenLargeInput", func(t *testing.T) {
		store := newStore(t)
		ctx := context.Background()

		const total = 1200
		all := make([]string, 0, total)
		seed := make([]string, 0, total/2)
		want := make([]string, 0, total/2)
		for i := 0; i < total; i++ {
			key := fmt.Sprintf("key-%04d", i)
			all = append(all, key)
			if i%2 == 0 {
				seed = append(seed, key)
			} else {
				want = append(want, key)
			}
		}

		src := mustAdd(t, store, "https://example.com/large.xml", "Large", seed)
		unseen, err := store.FilterUnseen(ctx, src.ID, all)
		if err != nil {
			t.Fatalf("FilterUnseen(%d keys): %v", total, err)
		}
		if !slices.Equal(unseen, want) {
			t.Fatalf("FilterUnseen(%d keys): got %d keys, want %d in input order (first mismatch matters)", total, len(unseen), len(want))
		}
		// Sorted-set cross-check: the same keys, order aside (sorted
		// []string, never a string-keyed map).
		if !slices.Equal(sortedCopy(unseen), sortedCopy(want)) {
			t.Fatalf("FilterUnseen(%d keys): key set differs from the unseeded half", total)
		}
	})

	t.Run("InsertPendingDedupGate", func(t *testing.T) {
		// Invariant 1: the single gate that keeps a duplicate post impossible.
		store := newStore(t)
		ctx := context.Background()
		src := mustAdd(t, store, "https://example.com/dedup.xml", "Dedup", nil)

		inserted, err := store.InsertPending(ctx, src.ID, "item-1")
		if err != nil {
			t.Fatalf("InsertPending first call: %v", err)
		}
		if !inserted {
			t.Fatal("InsertPending first call: got inserted=false, want true")
		}

		inserted, err = store.InsertPending(ctx, src.ID, "item-1")
		if err != nil {
			t.Fatalf("InsertPending second call: %v", err)
		}
		if inserted {
			t.Fatal("InsertPending second call: got inserted=true, want false (dedup gate)")
		}

		inserted, err = store.InsertPending(ctx, src.ID, "item-2")
		if err != nil {
			t.Fatalf("InsertPending other key: %v", err)
		}
		if !inserted {
			t.Fatal("InsertPending other key: got inserted=false, want true")
		}

		unseen, err := store.FilterUnseen(ctx, src.ID, []string{"item-1", "item-2", "item-3"})
		if err != nil {
			t.Fatalf("FilterUnseen: %v", err)
		}
		wantKeys(t, unseen, []string{"item-3"}, "FilterUnseen after InsertPending")
	})

	t.Run("InsertPendingDeletedSource", func(t *testing.T) {
		store := newStore(t)
		ctx := context.Background()
		src := mustAdd(t, store, "https://example.com/gone.xml", "Gone", nil)

		if err := store.DeleteSource(ctx, src.ID); err != nil {
			t.Fatalf("DeleteSource(%d): %v", src.ID, err)
		}
		if _, err := store.InsertPending(ctx, src.ID, "item-1"); !errors.Is(err, storage.ErrSourceGone) {
			t.Fatalf("InsertPending on a deleted source: got %v, want ErrSourceGone", err)
		}
	})

	t.Run("MarkPostedRecordsPost", func(t *testing.T) {
		store := newStore(t)
		ctx := context.Background()
		src := mustAdd(t, store, "https://example.com/post.xml", "Post", nil)

		if _, err := store.InsertPending(ctx, src.ID, "item-1"); err != nil {
			t.Fatalf("InsertPending: %v", err)
		}
		postedAt := baseTime.Add(2 * time.Hour)
		if err := store.MarkPosted(ctx, src.ID, "item-1", 987654, postedAt); err != nil {
			t.Fatalf("MarkPosted: %v", err)
		}

		n, err := store.CountPostedSince(ctx, postedAt.Add(-time.Hour))
		if err != nil {
			t.Fatalf("CountPostedSince: %v", err)
		}
		if n != 1 {
			t.Fatalf("CountPostedSince = %d after MarkPosted, want 1 (posted_at was not set)", n)
		}

		// A posted row is still a seen row: it never re-enters the dedup gate.
		inserted, err := store.InsertPending(ctx, src.ID, "item-1")
		if err != nil {
			t.Fatalf("InsertPending after MarkPosted: %v", err)
		}
		if inserted {
			t.Fatal("InsertPending after MarkPosted: got inserted=true, want false")
		}
	})

	t.Run("AbandonPendingStampsOnce", func(t *testing.T) {
		// Only posted_at IS NULL AND seeded=0 AND abandoned_at IS
		// NULL rows are stamped, and each is returned exactly once.
		store := newStore(t)
		ctx := context.Background()

		seeded := mustAdd(t, store, "https://example.com/seeded.xml", "Seeded", []string{"watermark-1"})
		src := mustAdd(t, store, "https://example.com/pending.xml", "Pending", nil)

		if _, err := store.InsertPending(ctx, src.ID, "pending-1"); err != nil {
			t.Fatalf("InsertPending(pending-1): %v", err)
		}
		if _, err := store.InsertPending(ctx, src.ID, "posted-1"); err != nil {
			t.Fatalf("InsertPending(posted-1): %v", err)
		}
		if err := store.MarkPosted(ctx, src.ID, "posted-1", 1, baseTime.Add(time.Hour)); err != nil {
			t.Fatalf("MarkPosted: %v", err)
		}

		got, err := store.AbandonPending(ctx, baseTime.Add(2*time.Hour))
		if err != nil {
			t.Fatalf("AbandonPending: %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("AbandonPending returned %d rows, want exactly 1 (seeded and posted rows are excluded); got %v", len(got), abandonedKeys(got))
		}
		if got[0].SourceID != src.ID || got[0].ItemKey != "pending-1" {
			t.Fatalf("AbandonPending returned %+v, want {SourceID:%d ItemKey:pending-1}", got[0], src.ID)
		}
		if got[0].SourceID == seeded.ID {
			t.Fatal("AbandonPending stamped a seeded row, want seeded=1 rows skipped")
		}

		again, err := store.AbandonPending(ctx, baseTime.Add(3*time.Hour))
		if err != nil {
			t.Fatalf("AbandonPending second call: %v", err)
		}
		if len(again) != 0 {
			t.Fatalf("AbandonPending second call returned %v, want nothing (logged once)", abandonedKeys(again))
		}
	})

	t.Run("CountPostedSinceCutoff", func(t *testing.T) {
		store := newStore(t)
		ctx := context.Background()
		src := mustAdd(t, store, "https://example.com/count.xml", "Count", nil)

		early := baseTime.Add(time.Hour)
		late := baseTime.Add(3 * time.Hour)
		for _, k := range []string{"item-1", "item-2"} {
			if _, err := store.InsertPending(ctx, src.ID, k); err != nil {
				t.Fatalf("InsertPending(%s): %v", k, err)
			}
		}
		if err := store.MarkPosted(ctx, src.ID, "item-1", 1, early); err != nil {
			t.Fatalf("MarkPosted(item-1): %v", err)
		}
		if err := store.MarkPosted(ctx, src.ID, "item-2", 2, late); err != nil {
			t.Fatalf("MarkPosted(item-2): %v", err)
		}

		cases := []struct {
			name  string
			since time.Time
			want  int
		}{
			{"before both", baseTime, 2},
			{"at the first post", early, 2},
			{"after the first post", early.Add(time.Second), 1},
			{"at the second post", late, 1},
			{"after both", late.Add(time.Second), 0},
		}
		for _, tc := range cases {
			n, err := store.CountPostedSince(ctx, tc.since)
			if err != nil {
				t.Fatalf("CountPostedSince(%s): %v", tc.name, err)
			}
			if n != tc.want {
				t.Fatalf("CountPostedSince(%s) = %d, want %d", tc.name, n, tc.want)
			}
		}
	})

	t.Run("PingAndClose", func(t *testing.T) {
		store := newStore(t)

		if err := store.Ping(context.Background()); err != nil {
			t.Fatalf("Ping: %v", err)
		}
		if err := store.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	})
}

func newSource(url, title string) model.Source {
	return model.Source{FeedURL: url, Title: title, CreatedAt: baseTime}
}

func mustAdd(t *testing.T, store storage.Storage, url, title string, seedKeys []string) model.Source {
	t.Helper()
	src, err := store.AddSource(context.Background(), newSource(url, title), seedKeys)
	if err != nil {
		t.Fatalf("AddSource(%q): %v", url, err)
	}
	if src.ID <= 0 {
		t.Fatalf("AddSource(%q): got id %d, want an assigned id > 0", url, src.ID)
	}
	return src
}

func mustGet(t *testing.T, store storage.Storage, id int64) model.Source {
	t.Helper()
	src, err := store.GetSourceByID(context.Background(), id)
	if err != nil {
		t.Fatalf("GetSourceByID(%d): %v", id, err)
	}
	return src
}

func mustUpdate(t *testing.T, store storage.Storage, r model.PollResult) {
	t.Helper()
	if err := store.UpdateSourcePollResult(context.Background(), r); err != nil {
		t.Fatalf("UpdateSourcePollResult(%+v): %v", r, err)
	}
}

// wantKeys compares two key slices in order. Order is part of the FilterUnseen
// contract, so this never sorts; use sortedCopy where order is irrelevant.
func wantKeys(t *testing.T, got, want []string, what string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Fatalf("%s: got %v, want %v", what, got, want)
	}
}

// sortedCopy returns a sorted copy so order-insensitive key comparisons still
// go through slices.Equal on []string (the cache-boundary check forbids a string-keyed map here).
func sortedCopy(keys []string) []string {
	out := slices.Clone(keys)
	slices.Sort(out)
	return out
}

func abandonedKeys(items []model.AbandonedItem) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it.ItemKey)
	}
	return out
}

// sameSecond compares timestamps at second precision, the resolution a store
// that round-trips through RFC3339 can promise.
func sameSecond(a, b time.Time) bool {
	return a.UTC().Truncate(time.Second).Equal(b.UTC().Truncate(time.Second))
}
