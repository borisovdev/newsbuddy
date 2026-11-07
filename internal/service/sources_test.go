package service

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/borisovdev/newsbuddy/internal/cache"
	"github.com/borisovdev/newsbuddy/internal/config"
	"github.com/borisovdev/newsbuddy/internal/feed"
	"github.com/borisovdev/newsbuddy/internal/feed/feedtest"
	"github.com/borisovdev/newsbuddy/internal/model"
	"github.com/borisovdev/newsbuddy/internal/storage"
	"github.com/borisovdev/newsbuddy/internal/storage/memstore"
)

// harness wires the real service over memstore, a scripted fetcher, and a
// swappable cache so the same scenario can be replayed cold, warm, and
// against a totally broken cache.
type harness struct {
	st   *memstore.Store
	f    *feedtest.FakeFetcher
	seen *cache.SeenKeys
	list *cache.SourceList
	svc  *SourceService
	idx  *SeenIndex
}

func newHarness(c cache.Cache) *harness {
	cfg := config.Defaults()
	st := memstore.New()
	seen := cache.NewSeenKeys(c, cfg.CacheSeenTTL, cfg.CacheSeenPerSrc)
	list := cache.NewSourceList(c, cfg.CacheSourcesTTL)
	f := feedtest.NewFakeFetcher()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	return &harness{
		st:   st,
		f:    f,
		seen: seen,
		list: list,
		svc:  New(st, seen, list, f, cfg, log),
		idx:  NewSeenIndex(st, seen, log),
	}
}

func fixturePath(name string) string { return filepath.Join("..", "..", "testdata", name) }

func loadFixture(t *testing.T, name, feedURL string) *model.FetchResult {
	t.Helper()
	res, err := feedtest.LoadFixture(fixturePath(name), feedURL)
	if err != nil {
		t.Fatalf("LoadFixture(%s): %v", name, err)
	}
	return res
}

func itemKeys(items []model.FeedItem) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it.Key)
	}
	return out
}

func sourceCount(t *testing.T, st storage.Storage) int {
	t.Helper()
	ss, err := st.ListSources(context.Background())
	if err != nil {
		t.Fatalf("ListSources: %v", err)
	}
	return len(ss)
}

// Adding a feed with N≥10 items seeds exactly N rows as
// already-seen and posts nothing.
func TestAddSeedsEveryExistingItemAndPostsNothing(t *testing.T) {
	const feedURL = "https://example.com/feed"
	ctx := context.Background()

	h := newHarness(cache.NewMemory(64))
	fx := loadFixture(t, "rss2.xml", feedURL)
	if len(fx.Feed.Items) < 10 {
		t.Fatalf("fixture has %d items, want >= 10", len(fx.Feed.Items))
	}
	h.f.AddResponse(feedURL, feedtest.Response{Result: fx})

	src, seeded, err := h.svc.Add(ctx, feedURL, "")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if seeded != len(fx.Feed.Items) {
		t.Errorf("seeded = %d, want %d", seeded, len(fx.Feed.Items))
	}
	if src.ID == 0 || src.FeedURL != feedURL {
		t.Errorf("src = %+v", src)
	}
	if src.Title != "Golden Test Feed" {
		t.Errorf("title = %q, want the feed's own title", src.Title)
	}

	// Every seeded key is already known, so nothing would ever be posted.
	unseen, err := h.idx.Unseen(ctx, src.ID, itemKeys(fx.Feed.Items))
	if err != nil {
		t.Fatalf("Unseen: %v", err)
	}
	if len(unseen) != 0 {
		t.Errorf("Unseen = %v, want none — seeded items must never be posted", unseen)
	}

	// seeded=1 rows are excluded from the abandon sweep, which is how we
	// observe the seeded flag through the storage interface.
	abandoned, err := h.st.AbandonPending(ctx, src.CreatedAt)
	if err != nil {
		t.Fatalf("AbandonPending: %v", err)
	}
	if len(abandoned) != 0 {
		t.Errorf("AbandonPending = %v, want none (all rows seeded=1)", abandoned)
	}
}

// A case- and trailing-slash variant of an existing URL is
// rejected by name, not silently ignored.
func TestAddDuplicateNamesExistingSource(t *testing.T) {
	const feedURL = "https://example.com/feed"
	ctx := context.Background()

	h := newHarness(cache.NewMemory(64))
	h.f.AddResponse(feedURL, feedtest.Response{Result: loadFixture(t, "rss2.xml", feedURL)})

	first, _, err := h.svc.Add(ctx, feedURL, "My Feed")
	if err != nil {
		t.Fatalf("first Add: %v", err)
	}

	_, _, err = h.svc.Add(ctx, "HTTPS://Example.COM/feed/", "")
	var dup *ErrDuplicateSource
	if !errors.As(err, &dup) {
		t.Fatalf("second Add err = %v, want *ErrDuplicateSource", err)
	}
	if dup.ExistingID != first.ID || dup.ExistingTitle != "My Feed" {
		t.Errorf("dup = %+v, want id %d title %q", dup, first.ID, "My Feed")
	}
	if n := sourceCount(t, h.st); n != 1 {
		t.Errorf("source count = %d, want 1", n)
	}
	if calls := h.f.CallCount(); calls != 1 {
		t.Errorf("fetch calls = %d, want 1 — the duplicate pre-check must short-circuit", calls)
	}
}

// A 404 is returned verbatim and nothing is written.
func TestAddRejectsFetchErrorWithoutWriting(t *testing.T) {
	const feedURL = "https://example.com/missing"
	ctx := context.Background()

	h := newHarness(cache.NewMemory(64))
	scripted := &feed.FetchError{Op: "status", StatusCode: 404}
	h.f.AddResponse(feedURL, feedtest.Response{Err: scripted})

	_, _, err := h.svc.Add(ctx, feedURL, "")
	if !errors.Is(err, scripted) {
		t.Fatalf("Add err = %v, want the scripted fetch error verbatim", err)
	}
	var fe *feed.FetchError
	if !errors.As(err, &fe) || fe.StatusCode != 404 {
		t.Errorf("err does not carry the 404 reason: %v", err)
	}
	if n := sourceCount(t, h.st); n != 0 {
		t.Errorf("source count = %d, want 0 — fetch must precede any write", n)
	}
}

// An HTML page is rejected with the parse reason and nothing
// is written.
func TestAddRejectsParseErrorWithoutWriting(t *testing.T) {
	const feedURL = "https://example.com/page"
	ctx := context.Background()

	data, err := os.ReadFile(fixturePath("html_page.html"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	perr := parseFailure(t, data, feedURL)

	h := newHarness(cache.NewMemory(64))
	scripted := &feed.ParseError{ContentType: "text/html", Err: perr}
	h.f.AddResponse(feedURL, feedtest.Response{Err: scripted})

	_, _, addErr := h.svc.Add(ctx, feedURL, "")
	if !errors.Is(addErr, scripted) {
		t.Fatalf("Add err = %v, want the scripted parse error verbatim", addErr)
	}
	if got := addErr.Error(); got != "not a valid RSS/Atom feed (content-type: text/html)" {
		t.Errorf("err message = %q", got)
	}
	if n := sourceCount(t, h.st); n != 0 {
		t.Errorf("source count = %d, want 0 — parse must precede any write", n)
	}
}

// parseFailure returns the real parser's error for data, failing the test if
// the bytes actually parsed as a feed.
func parseFailure(t *testing.T, data []byte, baseURL string) error {
	t.Helper()
	if _, err := feed.NewGofeedParser().Parse(data, baseURL); err != nil {
		return err
	}
	t.Fatal("the HTML fixture parsed as a feed")
	return nil
}

func TestAddRejectsUnusableURL(t *testing.T) {
	h := newHarness(cache.NewMemory(64))

	_, _, err := h.svc.Add(context.Background(), "not a url", "")
	var bad *ErrBadURL
	if !errors.As(err, &bad) {
		t.Fatalf("Add err = %v, want *ErrBadURL", err)
	}
	if n := sourceCount(t, h.st); n != 0 {
		t.Errorf("source count = %d, want 0", n)
	}
}

// Edge 10: an empty feed is a successful add with zero seeds.
func TestAddEmptyFeedSucceedsWithZeroSeeds(t *testing.T) {
	const feedURL = "https://example.com/empty"
	ctx := context.Background()

	h := newHarness(cache.NewMemory(64))
	h.f.AddResponse(feedURL, feedtest.Response{Result: loadFixture(t, "empty_items.xml", feedURL)})

	src, seeded, err := h.svc.Add(ctx, feedURL, "")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if seeded != 0 {
		t.Errorf("seeded = %d, want 0", seeded)
	}
	if src.Title == "" {
		t.Error("title is empty; a source title must never be empty")
	}
}

func TestAddFallsBackToHostWhenTitlesAreEmpty(t *testing.T) {
	const feedURL = "https://news.example.com/feed"
	ctx := context.Background()

	h := newHarness(cache.NewMemory(64))
	h.f.AddResponse(feedURL, feedtest.Response{
		Result: &model.FetchResult{StatusCode: 200, Feed: &model.Feed{Title: "  "}},
	})

	src, _, err := h.svc.Add(ctx, feedURL, "")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if src.Title != "news.example.com" {
		t.Errorf("title = %q, want the URL host", src.Title)
	}
}

// Remove by numeric id and by full URL both work, and both drop the
// source's seen rows.
func TestRemoveByIDAndByURL(t *testing.T) {
	ctx := context.Background()
	const urlA = "https://a.example.com/feed"
	const urlB = "https://b.example.com/feed"

	h := newHarness(cache.NewMemory(64))
	h.f.AddResponse(urlA, feedtest.Response{Result: loadFixture(t, "rss2.xml", urlA)})
	h.f.AddResponse(urlB, feedtest.Response{Result: loadFixture(t, "atom.xml", urlB)})

	srcA, _, err := h.svc.Add(ctx, urlA, "A")
	if err != nil {
		t.Fatalf("Add A: %v", err)
	}
	if _, _, err := h.svc.Add(ctx, urlB, "B"); err != nil {
		t.Fatalf("Add B: %v", err)
	}

	removed, err := h.svc.Remove(ctx, "1")
	if err != nil {
		t.Fatalf("Remove by id: %v", err)
	}
	if removed.ID != srcA.ID {
		t.Errorf("removed id = %d, want %d", removed.ID, srcA.ID)
	}

	// The seen rows cascaded, so the old keys read as unseen again.
	keys := itemKeys(loadFixture(t, "rss2.xml", urlA).Feed.Items)
	unseen, err := h.st.FilterUnseen(ctx, srcA.ID, keys)
	if err != nil {
		t.Fatalf("FilterUnseen: %v", err)
	}
	if len(unseen) != len(keys) {
		t.Errorf("unseen = %d, want %d — seen rows must cascade", len(unseen), len(keys))
	}

	// A trailing-slash variant of B's URL still resolves.
	if _, err := h.svc.Remove(ctx, urlB+"/"); err != nil {
		t.Fatalf("Remove by URL: %v", err)
	}

	list, err := h.svc.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("List = %v, want empty", list)
	}
}

// An unknown id is a not-found error with no state change.
func TestRemoveUnknownIDIsNotFound(t *testing.T) {
	const feedURL = "https://example.com/feed"
	ctx := context.Background()

	h := newHarness(cache.NewMemory(64))
	h.f.AddResponse(feedURL, feedtest.Response{Result: loadFixture(t, "rss2.xml", feedURL)})
	if _, _, err := h.svc.Add(ctx, feedURL, ""); err != nil {
		t.Fatalf("Add: %v", err)
	}

	if _, err := h.svc.Remove(ctx, "9999"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("Remove err = %v, want storage.ErrNotFound", err)
	}
	if n := sourceCount(t, h.st); n != 1 {
		t.Errorf("source count = %d, want 1", n)
	}
}

// A broken cache must not change List's answer.
func TestListIdenticalWithBrokenCache(t *testing.T) {
	const feedURL = "https://example.com/feed"
	ctx := context.Background()

	for _, tc := range []struct {
		name string
		c    cache.Cache
	}{
		{"memory", cache.NewMemory(64)},
		{"failing", cache.FailingCache{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(tc.c)
			h.f.AddResponse(feedURL, feedtest.Response{Result: loadFixture(t, "rss2.xml", feedURL)})
			if _, _, err := h.svc.Add(ctx, feedURL, "Feed"); err != nil {
				t.Fatalf("Add: %v", err)
			}

			// Twice: the second call exercises the warm path for memory.
			for i := 0; i < 2; i++ {
				list, err := h.svc.List(ctx)
				if err != nil {
					t.Fatalf("List %d: %v", i, err)
				}
				if len(list) != 1 || list[0].FeedURL != feedURL || list[0].Title != "Feed" {
					t.Fatalf("List %d = %+v", i, list)
				}
			}
		})
	}
}

func TestStatusFillsStorageFieldsAndKeepsCycleFields(t *testing.T) {
	const feedURL = "https://example.com/feed"
	ctx := context.Background()

	h := newHarness(cache.NewMemory(64))
	fx := loadFixture(t, "rss2.xml", feedURL)
	h.f.AddResponse(feedURL, feedtest.Response{Result: fx})
	src, _, err := h.svc.Add(ctx, feedURL, "")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	// One posted item inside the 24h window.
	if _, err := h.idx.InsertPending(ctx, src.ID, "fresh-key"); err != nil {
		t.Fatalf("InsertPending: %v", err)
	}
	if err := h.idx.MarkPosted(ctx, src.ID, "fresh-key", 42, src.CreatedAt); err != nil {
		t.Fatalf("MarkPosted: %v", err)
	}

	startedAt := src.CreatedAt
	cycle := model.Status{LastCycleErrors: 3, LastPublishError: "forbidden"}

	st, err := h.svc.Status(ctx, startedAt, cycle)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if st.SourceCount != 1 {
		t.Errorf("SourceCount = %d, want 1", st.SourceCount)
	}
	if st.PostedLast24h != 1 {
		t.Errorf("PostedLast24h = %d, want 1", st.PostedLast24h)
	}
	if !st.StartedAt.Equal(startedAt) {
		t.Errorf("StartedAt = %v, want %v", st.StartedAt, startedAt)
	}
	if st.LastCycleErrors != 3 || st.LastPublishError != "forbidden" {
		t.Errorf("cycle fields lost: %+v", st)
	}
}
