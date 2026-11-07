package poller

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/borisovdev/newsbuddy/internal/cache"
	"github.com/borisovdev/newsbuddy/internal/config"
	"github.com/borisovdev/newsbuddy/internal/feed"
	"github.com/borisovdev/newsbuddy/internal/feed/feedtest"
	"github.com/borisovdev/newsbuddy/internal/logging"
	"github.com/borisovdev/newsbuddy/internal/model"
	"github.com/borisovdev/newsbuddy/internal/service"
	"github.com/borisovdev/newsbuddy/internal/storage/memstore"
	"github.com/borisovdev/newsbuddy/internal/telegram/tgtest"
)

// ---- harness ---------------------------------------------------------

// safeBuf is a mutex-guarded log sink: cycle goroutines write to it while
// the test goroutine reads it.
type safeBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *safeBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *safeBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

type harness struct {
	t     *testing.T
	cfg   config.Config
	st    *memstore.Store
	fetch *feedtest.FakeFetcher
	pub   *tgtest.FakePublisher
	svc   *service.SourceService
	idx   *service.SeenIndex
	p     *Poller
	logs  *safeBuf
}

// testConfig is Defaults() with the pacing removed; the fake publisher does
// no pacing of its own, so PostInterval is irrelevant to it either way.
func testConfig() config.Config {
	cfg := config.Defaults()
	cfg.PostInterval = 0
	cfg.PollInterval = time.Hour // tests drive time via RunCycle
	return cfg
}

func newHarness(t *testing.T, cfg config.Config, pub *tgtest.FakePublisher) *harness {
	t.Helper()

	// The real 1s/4s fetch retry backoff would make every failure test multi-second.
	prev := retryBackoffs
	retryBackoffs = []time.Duration{time.Millisecond, time.Millisecond}
	t.Cleanup(func() { retryBackoffs = prev })

	logs := &safeBuf{}
	log := logging.New("json", slog.LevelDebug, logs)

	st := memstore.New()
	mem := cache.NewMemory(cfg.CacheEntries)
	seenView := cache.NewSeenKeys(mem, cfg.CacheSeenTTL, cfg.CacheSeenPerSrc)
	listView := cache.NewSourceList(mem, cfg.CacheSourcesTTL)
	ff := feedtest.NewFakeFetcher()
	svc := service.New(st, seenView, listView, ff, cfg, log)
	idx := service.NewSeenIndex(st, seenView, log)

	return &harness{
		t:     t,
		cfg:   cfg,
		st:    st,
		fetch: ff,
		pub:   pub,
		svc:   svc,
		idx:   idx,
		p:     New(svc, idx, st, ff, pub, cfg, log),
		logs:  logs,
	}
}

// script queues one successful fetch outcome for url.
func (h *harness) script(url string, res *model.FetchResult) {
	h.fetch.AddResponse(url, feedtest.Response{Result: res})
}

// scriptErr queues the same fetch error n times for url.
func (h *harness) scriptErr(url string, err error, n int) {
	for i := 0; i < n; i++ {
		h.fetch.AddResponse(url, feedtest.Response{Err: err})
	}
}

// add scripts res as the /add-time fetch and adds the source for real.
func (h *harness) add(url, title string, res *model.FetchResult) model.Source {
	h.t.Helper()
	h.script(url, res)
	src, _, err := h.svc.Add(context.Background(), url, title)
	if err != nil {
		h.t.Fatalf("add %s: %v", url, err)
	}
	return src
}

// countLines returns how many emitted log records carry the given event.
func (h *harness) countLines(event string) int {
	return strings.Count(h.logs.String(), `"msg":"`+event+`"`)
}

func (h *harness) source(id int64) model.Source {
	h.t.Helper()
	src, err := h.st.GetSourceByID(context.Background(), id)
	if err != nil {
		h.t.Fatalf("GetSourceByID(%d): %v", id, err)
	}
	return src
}

// ---- fixtures --------------------------------------------------------

func item(key string, order int, published *time.Time) model.FeedItem {
	return model.FeedItem{
		Key:       key,
		Title:     "Item-" + key,
		Link:      "https://example.com/" + key,
		Excerpt:   "excerpt of " + key,
		Published: published,
		Order:     order,
	}
}

func items(keys ...string) []model.FeedItem {
	out := make([]model.FeedItem, 0, len(keys))
	for i, k := range keys {
		out = append(out, item(k, i, nil))
	}
	return out
}

func okResult(etag, lastMod string, its []model.FeedItem) *model.FetchResult {
	return &model.FetchResult{
		StatusCode:   200,
		ETag:         etag,
		LastModified: lastMod,
		Feed:         &model.Feed{Title: "Example", Link: "https://example.com", Items: its},
	}
}

// notModified mirrors the real 304 path: the fetcher echoes the request's
// validators back into the 304 result.
func notModified(etag, lastMod string) *model.FetchResult {
	return &model.FetchResult{StatusCode: 304, NotModified: true, ETag: etag, LastModified: lastMod}
}

func at(s string) *time.Time {
	ts, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return &ts
}

// ---- scenarios -------------------------------------------------------

// Adding seeds the existing items; the next cycle posts only the one
// item that appeared afterwards.
func TestOneNewItemPostsExactlyOneMessage(t *testing.T) {
	const url = "https://example.com/feed"
	h := newHarness(t, testConfig(), tgtest.NewFakePublisher())

	h.add(url, "Example", okResult("e1", "", items("a", "b", "c")))
	if got := h.pub.Count(); got != 0 {
		t.Fatalf("add published %d messages, want 0", got)
	}

	h.script(url, okResult("e2", "", items("a", "b", "c", "d")))
	stats := h.p.RunCycle(context.Background())

	if stats.NewItems != 1 || stats.Posted != 1 || stats.Errors != 0 {
		t.Fatalf("stats = %+v, want NewItems=1 Posted=1 Errors=0", stats)
	}
	if got := h.pub.Count(); got != 1 {
		t.Fatalf("published %d messages, want exactly 1", got)
	}
	if body := h.pub.Sends()[0].HTML; !strings.Contains(body, "Item-d") {
		t.Fatalf("published body %q does not carry the new item", body)
	}
}

// Five further cycles over an unchanged feed publish nothing more.
func TestRepeatedCyclesPublishNothingMore(t *testing.T) {
	const url = "https://example.com/feed"
	h := newHarness(t, testConfig(), tgtest.NewFakePublisher())

	h.add(url, "Example", okResult("e1", "", items("a", "b", "c")))

	h.script(url, okResult("e2", "", items("a", "b", "c", "d")))
	h.p.RunCycle(context.Background())
	if got := h.pub.Count(); got != 1 {
		t.Fatalf("after the first cycle: %d messages, want 1", got)
	}

	for i := 0; i < 5; i++ {
		h.script(url, okResult("e2", "", items("a", "b", "c", "d")))
		stats := h.p.RunCycle(context.Background())
		if stats.Posted != 0 || stats.NewItems != 0 {
			t.Fatalf("cycle %d: stats = %+v, want no new items and no posts", i+2, stats)
		}
	}

	if got := h.pub.Count(); got != 1 {
		t.Fatalf("published %d messages in total, want exactly 1", got)
	}
}

// A persistently failing source affects nothing but its own
// row — the healthy source still posts, the cycle returns normally, and the
// failure is logged exactly once.
func TestFailingSourceDoesNotAffectHealthySource(t *testing.T) {
	const bad = "https://bad.example.com/feed"
	const good = "https://good.example.com/feed"
	h := newHarness(t, testConfig(), tgtest.NewFakePublisher())

	badSrc := h.add(bad, "Bad", okResult("", "", items("x")))
	goodSrc := h.add(good, "Good", okResult("", "", items("y")))

	// 3 attempts (1 + 2 retries) against a temporary 500.
	boom := &feed.FetchError{Op: "status", StatusCode: 500}
	h.scriptErr(bad, boom, 3)
	h.script(good, okResult("g2", "", items("y", "z")))

	stats := h.p.RunCycle(context.Background())

	if stats.Sources != 2 || stats.Errors != 1 || stats.Posted != 1 {
		t.Fatalf("stats = %+v, want Sources=2 Errors=1 Posted=1", stats)
	}
	if got := h.pub.Count(); got != 1 {
		t.Fatalf("published %d messages, want the healthy source's 1", got)
	}
	if body := h.pub.Sends()[0].HTML; !strings.Contains(body, "Item-z") {
		t.Fatalf("published body %q is not the healthy source's new item", body)
	}
	if got := h.countLines(logging.EvSourceError); got != 1 {
		t.Fatalf("%d %s lines, want exactly 1", got, logging.EvSourceError)
	}

	if b := h.source(badSrc.ID); b.ConsecutiveFailures != 1 || b.LastError == "" {
		t.Fatalf("failing source row = %+v, want one recorded failure", b)
	}
	if g := h.source(goodSrc.ID); g.ConsecutiveFailures != 0 || g.ETag != "g2" {
		t.Fatalf("healthy source row = %+v, want no failures and the new etag", g)
	}
}

// Zero sources is a valid idle cycle.
func TestRunCycle_ZeroSourcesIsIdle(t *testing.T) {
	h := newHarness(t, testConfig(), tgtest.NewFakePublisher())

	stats := h.p.RunCycle(context.Background())

	if stats.Sources != 0 || stats.Errors != 0 || stats.Posted != 0 {
		t.Fatalf("stats = %+v, want an empty idle cycle", stats)
	}
	if got := h.countLines(logging.EvCycleEnd); got != 1 {
		t.Fatalf("%d %s lines, want 1", got, logging.EvCycleEnd)
	}
	if !strings.Contains(h.logs.String(), `"duration_ms"`) {
		t.Fatal("poll.cycle.end carries no duration_ms")
	}
}

func TestSnapshot_ReflectsLastCycle(t *testing.T) {
	const url = "https://example.com/feed"
	h := newHarness(t, testConfig(), tgtest.NewFakePublisher())

	if zero := h.p.Snapshot(); zero.LastCycleAt != nil {
		t.Fatalf("snapshot before any cycle = %+v, want no LastCycleAt", zero)
	}

	h.add(url, "Example", okResult("", "", items("a")))
	h.script(url, okResult("e2", "", items("a", "b")))
	h.p.RunCycle(context.Background())

	snap := h.p.Snapshot()
	if snap.LastCycleAt == nil {
		t.Fatal("snapshot has no LastCycleAt after a cycle")
	}
	if snap.LastCycleErrors != 0 {
		t.Fatalf("LastCycleErrors = %d, want 0", snap.LastCycleErrors)
	}
}

// Run fires one cycle immediately, keeps ticking, and returns on ctx.Done.
func TestRun_ImmediateCycleThenTicksThenStops(t *testing.T) {
	cfg := testConfig()
	cfg.PollInterval = 10 * time.Millisecond
	h := newHarness(t, cfg, tgtest.NewFakePublisher())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.p.Run(ctx)
	}()

	deadline := time.Now().Add(2 * time.Second)
	for h.countLines(logging.EvCycleEnd) < 2 {
		if time.Now().After(deadline) {
			cancel()
			<-done
			t.Fatalf("only %d cycles ran within the deadline", h.countLines(logging.EvCycleEnd))
		}
		time.Sleep(2 * time.Millisecond)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after ctx cancellation")
	}
}
