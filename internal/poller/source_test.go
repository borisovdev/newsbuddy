package poller

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/borisovdev/newsbuddy/internal/feed"
	"github.com/borisovdev/newsbuddy/internal/logging"
	"github.com/borisovdev/newsbuddy/internal/model"
	"github.com/borisovdev/newsbuddy/internal/telegram"
	"github.com/borisovdev/newsbuddy/internal/telegram/tgtest"
)

// Dated items first, ascending by date; undated items after them, in
// feed document order.
func TestFR16_OrderingDatedAscendingThenUndatedInFeedOrder(t *testing.T) {
	const url = "https://example.com/feed"
	h := newHarness(t, testConfig(), tgtest.NewFakePublisher())

	h.add(url, "Example", okResult("e1", "", nil))

	batch := []model.FeedItem{
		item("undated-first", 0, nil),
		item("jan03", 1, at("2020-01-03T00:00:00Z")),
		item("jan01", 2, at("2020-01-01T00:00:00Z")),
		item("undated-second", 3, nil),
		item("jan02", 4, at("2020-01-02T00:00:00Z")),
	}
	h.script(url, okResult("e2", "", batch))

	if stats := h.p.RunCycle(context.Background()); stats.Posted != 5 {
		t.Fatalf("posted %d items, want 5", stats.Posted)
	}

	want := []string{"Item-jan01", "Item-jan02", "Item-jan03", "Item-undated-first", "Item-undated-second"}
	sends := h.pub.Sends()
	if len(sends) != len(want) {
		t.Fatalf("%d sends, want %d", len(sends), len(want))
	}
	for i, w := range want {
		if !strings.Contains(sends[i].HTML, w) {
			t.Fatalf("send %d = %q, want it to carry %s", i, sends[i].HTML, w)
		}
	}
}

// The cap truncates the batch, and a truncated batch must NOT
// store the new validators — otherwise the next fetch 304s and the overflow
// items are never picked up.
func TestFR17_CapTruncatesAndClearsValidators(t *testing.T) {
	const url = "https://example.com/feed"
	cfg := testConfig()
	cfg.MaxItemsPerPoll = 3
	h := newHarness(t, cfg, tgtest.NewFakePublisher())

	src := h.add(url, "Example", okResult("e1", "Mon, 01 Jan 2020 00:00:00 GMT", nil))
	if before := h.source(src.ID); before.ETag != "e1" {
		t.Fatalf("etag after add = %q, want e1", before.ETag)
	}

	h.script(url, okResult("e2", "Tue, 02 Jan 2020 00:00:00 GMT", items("a", "b", "c", "d", "e")))
	stats := h.p.RunCycle(context.Background())

	if stats.NewItems != 5 || stats.Posted != 3 {
		t.Fatalf("stats = %+v, want NewItems=5 Posted=3", stats)
	}
	if got := h.pub.Count(); got != 3 {
		t.Fatalf("published %d messages, want MaxItemsPerPoll=3", got)
	}

	after := h.source(src.ID)
	if after.ETag != "" || after.LastModified != "" {
		t.Fatalf("validators after a truncated cycle = (%q, %q), want both cleared",
			after.ETag, after.LastModified)
	}
}

// A send failure leaves the row pending (posted_at IS NULL) and the item is
// never retried inside the same cycle.
func TestSendFailure_ItemStaysPendingAndIsNotResentThisCycle(t *testing.T) {
	const url = "https://example.com/feed"
	start := time.Now().UTC().Add(-time.Minute)
	h := newHarness(t, testConfig(), tgtest.NewFakePublisher(errors.New("telegram exploded")))

	src := h.add(url, "Example", okResult("e1", "", nil))
	h.script(url, okResult("e2", "", items("a", "b")))

	stats := h.p.RunCycle(context.Background())

	if stats.NewItems != 2 || stats.Posted != 1 {
		t.Fatalf("stats = %+v, want NewItems=2 Posted=1", stats)
	}
	// Exactly one attempt per item: the failed item is not re-sent.
	if got := h.pub.Count(); got != 2 {
		t.Fatalf("%d send attempts, want exactly 2 (one per item, no in-cycle retry)", got)
	}
	if got := h.countLines(logging.EvPostFailed); got != 1 {
		t.Fatalf("%d %s lines, want 1", got, logging.EvPostFailed)
	}

	posted, err := h.st.CountPostedSince(context.Background(), start)
	if err != nil {
		t.Fatal(err)
	}
	if posted != 1 {
		t.Fatalf("%d posted rows, want 1", posted)
	}

	// The failed item's row exists and is still pending.
	pending, err := h.st.AbandonPending(context.Background(), time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].SourceID != src.ID || pending[0].ItemKey != "a" {
		t.Fatalf("pending rows = %+v, want exactly the failed item a", pending)
	}
}

// Edge 34: a "can't parse entities" 400 is retried once with the plain-text
// body. Reaching that path at all requires SendWithFallback, not Send.
func TestEdge34_PlainTextFallbackIsExercised(t *testing.T) {
	const url = "https://example.com/feed"
	start := time.Now().UTC().Add(-time.Minute)
	h := newHarness(t, testConfig(), tgtest.NewFakePublisher(tgtest.ErrParseEntities))

	h.add(url, "Example", okResult("e1", "", nil))
	h.script(url, okResult("e2", "", items("a")))

	if stats := h.p.RunCycle(context.Background()); stats.Posted != 1 {
		t.Fatalf("posted %d, want 1 after the plain-text fallback", stats.Posted)
	}

	sends := h.pub.Sends()
	if len(sends) != 2 {
		t.Fatalf("%d attempts recorded, want 2 (html then plain)", len(sends))
	}
	if sends[0].HTML == "" || sends[0].Plain != "" {
		t.Fatalf("first attempt = %+v, want the HTML body", sends[0])
	}
	if sends[1].Plain == "" || sends[1].HTML != "" {
		t.Fatalf("second attempt = %+v, want the plain body", sends[1])
	}
	if strings.Contains(sends[1].Plain, "<b>") || strings.Contains(sends[1].Plain, "<a href") {
		t.Fatalf("fallback body %q still carries HTML markup", sends[1].Plain)
	}
	if !strings.Contains(sends[1].Plain, "Item-a") {
		t.Fatalf("fallback body %q does not carry the item", sends[1].Plain)
	}

	posted, err := h.st.CountPostedSince(context.Background(), start)
	if err != nil {
		t.Fatal(err)
	}
	if posted != 1 {
		t.Fatalf("%d posted rows, want the item marked posted", posted)
	}
}

// F10: a 304 must never wipe the stored validators, so the NEXT cycle still
// sends the conditional-GET headers.
func TestF10_Consecutive304sKeepValidators(t *testing.T) {
	const url = "https://example.com/feed"
	h := newHarness(t, testConfig(), tgtest.NewFakePublisher())

	src := h.add(url, "Example", okResult("etag-1", "Mon, 01 Jan 2020 00:00:00 GMT", items("a")))

	// The fetcher echoes the request's validators back on a 304.
	h.script(url, notModified("etag-1", "Mon, 01 Jan 2020 00:00:00 GMT"))
	h.script(url, notModified("etag-1", "Mon, 01 Jan 2020 00:00:00 GMT"))

	for i := 0; i < 2; i++ {
		stats := h.p.RunCycle(context.Background())
		if stats.Errors != 0 || stats.Posted != 0 || stats.NewItems != 0 {
			t.Fatalf("cycle %d: stats = %+v, want a clean no-op", i+1, stats)
		}
		after := h.source(src.ID)
		if after.ETag != "etag-1" || after.LastModified == "" {
			t.Fatalf("after cycle %d the validators are (%q, %q), want them preserved",
				i+1, after.ETag, after.LastModified)
		}
	}

	calls := h.fetch.Calls()
	if len(calls) != 3 {
		t.Fatalf("%d fetch calls, want 3 (add + 2 cycles)", len(calls))
	}
	for i, c := range calls[1:] {
		if c.ETag != "etag-1" || c.LastModified == "" {
			t.Fatalf("cycle %d requested with (%q, %q), want the stored validators",
				i+1, c.ETag, c.LastModified)
		}
	}
	if got := h.countLines(logging.EvSourceOK); got != 2 {
		t.Fatalf("%d %s lines, want 2", got, logging.EvSourceOK)
	}
}

// Temporary fetch errors are retried up to 3 attempts; 4xx is not.
func TestFetchRetry_TemporaryOnly(t *testing.T) {
	t.Run("temporary 500 retries to 3 attempts", func(t *testing.T) {
		const url = "https://example.com/feed"
		h := newHarness(t, testConfig(), tgtest.NewFakePublisher())
		h.add(url, "Example", okResult("", "", items("a")))
		h.scriptErr(url, &feed.FetchError{Op: "status", StatusCode: 500}, 3)

		if stats := h.p.RunCycle(context.Background()); stats.Errors != 1 {
			t.Fatalf("stats = %+v, want one failed source", stats)
		}
		if got := h.fetch.CallCount(); got != 4 {
			t.Fatalf("%d fetch calls, want 4 (add + 3 attempts)", got)
		}
	})

	t.Run("404 is not retried", func(t *testing.T) {
		const url = "https://example.com/feed"
		h := newHarness(t, testConfig(), tgtest.NewFakePublisher())
		h.add(url, "Example", okResult("", "", items("a")))
		h.scriptErr(url, &feed.FetchError{Op: "status", StatusCode: 404}, 3)

		h.p.RunCycle(context.Background())
		if got := h.fetch.CallCount(); got != 2 {
			t.Fatalf("%d fetch calls, want 2 (add + 1 attempt, no retry)", got)
		}
	})

	t.Run("429 is not retried", func(t *testing.T) {
		const url = "https://example.com/feed"
		h := newHarness(t, testConfig(), tgtest.NewFakePublisher())
		h.add(url, "Example", okResult("", "", items("a")))
		h.scriptErr(url, &feed.FetchError{Op: "status", StatusCode: 429, RetryAfter: time.Minute}, 3)

		h.p.RunCycle(context.Background())
		if got := h.fetch.CallCount(); got != 2 {
			t.Fatalf("%d fetch calls, want 2 (add + 1 attempt)", got)
		}
	})
}

// Edge 35: a 403 aborts the source without marking anything posted, and
// surfaces in /status.
func TestEdge35_ForbiddenAbortsSourceAndSurfacesInStatus(t *testing.T) {
	const url = "https://example.com/feed"
	h := newHarness(t, testConfig(), tgtest.NewFakePublisher(telegram.ErrForbidden))

	h.add(url, "Example", okResult("e1", "", nil))
	h.script(url, okResult("e2", "", items("a", "b")))

	stats := h.p.RunCycle(context.Background())

	if stats.Posted != 0 {
		t.Fatalf("posted %d, want 0", stats.Posted)
	}
	if got := h.pub.Count(); got != 1 {
		t.Fatalf("%d send attempts, want 1 (the source is abandoned after 403)", got)
	}
	if snap := h.p.Snapshot(); !strings.Contains(snap.LastPublishError, "forbidden") {
		t.Fatalf("LastPublishError = %q, want the forbidden error", snap.LastPublishError)
	}
}

// Edge 25: /remove races the cycle. The stale cached source list still names
// the source, so InsertPending reports ErrSourceGone and the rest of the
// source's items are discarded.
func TestEdge25_SourceRemovedMidCycle(t *testing.T) {
	const url = "https://example.com/feed"
	h := newHarness(t, testConfig(), tgtest.NewFakePublisher())

	src := h.add(url, "Example", okResult("e1", "", items("a")))

	// Cycle 1 warms the source-list cache.
	h.script(url, okResult("e2", "", items("a")))
	h.p.RunCycle(context.Background())

	// Delete straight through storage so the cached list stays stale.
	if err := h.st.DeleteSource(context.Background(), src.ID); err != nil {
		t.Fatal(err)
	}

	h.script(url, okResult("e3", "", items("a", "b", "c")))
	stats := h.p.RunCycle(context.Background())

	if stats.Posted != 0 {
		t.Fatalf("posted %d after the source was removed, want 0", stats.Posted)
	}
	if got := h.pub.Count(); got != 0 {
		t.Fatalf("%d send attempts, want 0", got)
	}
	if got := h.countLines(logging.EvSourceRemovedMid); got != 1 {
		t.Fatalf("%d %s lines, want 1", got, logging.EvSourceRemovedMid)
	}
}

// An item with no link is still posted, with a warn line.
func TestFR24_UnlinkedItemIsStillPosted(t *testing.T) {
	const url = "https://example.com/feed"
	h := newHarness(t, testConfig(), tgtest.NewFakePublisher())

	h.add(url, "Example", okResult("e1", "", nil))
	it := item("a", 0, nil)
	it.Link = ""
	h.script(url, okResult("e2", "", []model.FeedItem{it}))

	if stats := h.p.RunCycle(context.Background()); stats.Posted != 1 {
		t.Fatalf("posted %d, want 1", stats.Posted)
	}
	if got := h.countLines(logging.EvPostUnlinked); got != 1 {
		t.Fatalf("%d %s lines, want 1", got, logging.EvPostUnlinked)
	}
}

func TestTruncate(t *testing.T) {
	if got := truncate("héllo", 500); got != "héllo" {
		t.Fatalf("truncate short = %q", got)
	}
	long := strings.Repeat("é", 600)
	got := truncate(long, 500)
	if r := []rune(got); len(r) != 500 {
		t.Fatalf("truncate produced %d runes, want 500", len(r))
	}
	if !strings.HasPrefix(long, got) {
		t.Fatal("truncate did not cut on a rune boundary")
	}
}
