// Package apptest is the end-to-end acceptance harness. It wires
// the real poller, the real service, the real renderer and real SQLite in a
// t.TempDir() against feedtest.FakeFetcher and tgtest.FakePublisher, with
// PostInterval forced to 0 and cycles driven manually via RunCycle, so the
// acceptance scenarios are genuinely automated and run offline.
//
// Cache boundary: this is a non-test .go file outside internal/cache, so the
// boundary grep scans it. It therefore uses no string-keyed maps and no
// concurrent-map type — log-line indexes and fixture registries are slices.
// The *_test.go files, which the grep excludes, may decode a log line into
// a generic keyed value.
package apptest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
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
	"github.com/borisovdev/newsbuddy/internal/poller"
	"github.com/borisovdev/newsbuddy/internal/service"
	"github.com/borisovdev/newsbuddy/internal/storage/sqlite"
	"github.com/borisovdev/newsbuddy/internal/telegram/tgtest"
)

// FakeToken is the scripted bot token every harness config carries. It is
// obviously not a real token, and the redaction test greps the whole log buffer for it.
const FakeToken = "424242:FAKE-TOKEN-MUST-NEVER-APPEAR-IN-LOGS"

// LogSink is the concurrency-safe bytes.Buffer the JSON handler writes into.
// The poll cycle logs from up to PollConcurrency goroutines, so the mutex is
// mandatory under -race.
type LogSink struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *LogSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

// String returns everything written so far.
func (s *LogSink) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

// Lines returns every non-empty line written so far, in order.
func (s *LogSink) Lines() []string {
	out := make([]string, 0, 32)
	for _, ln := range strings.Split(s.String(), "\n") {
		if strings.TrimSpace(ln) != "" {
			out = append(out, ln)
		}
	}
	return out
}

// msgOnly is the minimal shape needed to index lines by event name without
// decoding into a keyed map.
type msgOnly struct {
	Msg string `json:"msg"`
}

// LinesFor returns the raw JSON lines whose "msg" equals event, in order.
func (s *LogSink) LinesFor(event string) []string {
	out := make([]string, 0, 8)
	for _, ln := range s.Lines() {
		var m msgOnly
		if err := json.Unmarshal([]byte(ln), &m); err != nil {
			continue
		}
		if m.Msg == event {
			out = append(out, ln)
		}
	}
	return out
}

// Count returns how many lines carry the given event name.
func (s *LogSink) Count(event string) int { return len(s.LinesFor(event)) }

// Reset drops everything buffered so far, so a test can measure one phase
// of a scenario in isolation.
func (s *LogSink) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.buf.Reset()
}

// Harness owns one complete offline wiring: real SQLite, real service, real
// seen index, real poller and real renderer, over a scripted fetcher and a
// recording publisher.
type Harness struct {
	T   *testing.T
	Cfg config.Config

	DBPath string
	Logs   *LogSink
	Log    *slog.Logger

	Fetcher   *feedtest.FakeFetcher
	Publisher *tgtest.FakePublisher

	Store  *sqlite.Store
	Cache  cache.Cache
	Svc    *service.SourceService
	Seen   *service.SeenIndex
	Poller *poller.Poller

	closed bool
}

// New builds a harness whose database lives in t.TempDir(). The store is
// closed automatically at the end of the test.
func New(t *testing.T) *Harness {
	t.Helper()

	cfg := config.Defaults()
	cfg.TelegramBotToken = FakeToken
	cfg.TelegramChannelID = "@news_buddy_test"
	cfg.TelegramOwnerID = 4242
	cfg.DBPath = filepath.Join(t.TempDir(), "acceptance", "news-buddy.db")
	// Harness overrides: posting must not pace, and a scripted
	// failure must not sleep through the 1s/4s fetch retry backoff. PollInterval is
	// irrelevant because cycles are driven manually.
	cfg.PostInterval = 0
	cfg.FetchRetries = 0
	cfg.LogLevel = slog.LevelDebug

	h := &Harness{
		T:         t,
		Cfg:       cfg,
		DBPath:    cfg.DBPath,
		Logs:      &LogSink{},
		Fetcher:   feedtest.NewFakeFetcher(),
		Publisher: tgtest.NewFakePublisher(),
	}
	h.Log = logging.New("json", cfg.LogLevel, h.Logs)
	h.build(cache.NewMemory(cfg.CacheEntries))

	t.Cleanup(h.Close)
	return h
}

// build wires every subsystem over the current DB path and the given cache.
func (h *Harness) build(c cache.Cache) {
	h.T.Helper()

	store, err := sqlite.Open(context.Background(), h.DBPath)
	if err != nil {
		h.T.Fatalf("sqlite.Open(%s): %v", h.DBPath, err)
	}

	seenKeys := cache.NewSeenKeys(c, h.Cfg.CacheSeenTTL, h.Cfg.CacheSeenPerSrc)
	sourceList := cache.NewSourceList(c, h.Cfg.CacheSourcesTTL)

	h.Store = store
	h.Cache = c
	h.Svc = service.New(store, seenKeys, sourceList, h.Fetcher, h.Cfg, h.Log.With("component", "service"))
	h.Seen = service.NewSeenIndex(store, seenKeys, h.Log.With("component", "service"))
	h.Poller = poller.New(h.Svc, h.Seen, store, h.Fetcher, h.Publisher, h.Cfg,
		h.Log.With("component", "poller"))
	h.closed = false
}

// Reopen closes the store and cache and rebuilds everything against the SAME
// database file with a brand-new in-memory cache. This is the in-process
// approximation of a SIGKILL and restart, and of "cache state
// deleted, DB intact".
func (h *Harness) Reopen() { h.ReopenWithCache(cache.NewMemory(h.Cfg.CacheEntries)) }

// ReopenWithCache is Reopen with a caller-chosen cache backend, so a test can
// re-run the same scenario against cache.FailingCache.
func (h *Harness) ReopenWithCache(c cache.Cache) {
	h.T.Helper()
	h.Close()
	h.build(c)
}

// Close releases the store and the cache. It is idempotent.
func (h *Harness) Close() {
	if h.closed {
		return
	}
	h.closed = true
	if h.Store != nil {
		_ = h.Store.Close()
	}
	if h.Cache != nil {
		_ = h.Cache.Close()
	}
}

// Ctx returns a context bounded by a generous per-test deadline.
func (h *Harness) Ctx() context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	h.T.Cleanup(cancel)
	return ctx
}

// Script queues n copies of res as the fetcher's answer for url.
func (h *Harness) Script(url string, res *model.FetchResult, n int) {
	for i := 0; i < n; i++ {
		h.Fetcher.AddResponse(url, feedtest.Response{Result: res})
	}
}

// ScriptErr queues n copies of err as the fetcher's answer for url. A plain
// error is never a temporary *feed.FetchError, so the poller does not retry
// it and the cycle stays fast.
func (h *Harness) ScriptErr(url string, err error, n int) {
	for i := 0; i < n; i++ {
		h.Fetcher.AddResponse(url, feedtest.Response{Err: err})
	}
}

// Add runs the real /add use case and fails the test if it errors. It
// consumes exactly one scripted response for url.
func (h *Harness) Add(url, displayName string) model.Source {
	h.T.Helper()
	src, seeded, err := h.Svc.Add(h.Ctx(), url, displayName)
	if err != nil {
		h.T.Fatalf("service.Add(%s): %v", url, err)
	}
	h.T.Logf("added source #%d %q seeded=%d", src.ID, src.Title, seeded)
	return src
}

// Cycle drives exactly one poll cycle.
func (h *Harness) Cycle() poller.CycleStats { return h.Poller.RunCycle(h.Ctx()) }

// Cycles drives n poll cycles and returns the last cycle's stats.
func (h *Harness) Cycles(n int) poller.CycleStats {
	var last poller.CycleStats
	for i := 0; i < n; i++ {
		last = h.Cycle()
	}
	return last
}

// Sends is the number of publish attempts recorded so far.
func (h *Harness) Sends() int { return h.Publisher.Count() }

// ---- fixtures ----

// FixturePath resolves a root testdata/ fixture from this package's
// directory, per the path convention in testdata/README.md.
func FixturePath(name string) string { return filepath.Join("..", "..", "..", "testdata", name) }

// Fixture parses a root testdata/ feed fixture with the real gofeed parser
// and wraps it as a successful fetch result for baseURL.
func Fixture(t *testing.T, name, baseURL string) *model.FetchResult {
	t.Helper()
	res, err := feedtest.LoadFixture(FixturePath(name), baseURL)
	if err != nil {
		t.Fatalf("LoadFixture(%s): %v", name, err)
	}
	return res
}

// Head returns a copy of res carrying only its first n items, so a test can
// seed a source with part of a fixture and then reveal the rest.
func Head(res *model.FetchResult, n int) *model.FetchResult {
	out := clone(res)
	if n < len(out.Feed.Items) {
		out.Feed.Items = out.Feed.Items[:n]
	}
	return out
}

// AppendItem returns a copy of res with one extra item appended, keyed the
// same way the real parser would key it.
func AppendItem(res *model.FetchResult, title, link string) *model.FetchResult {
	out := clone(res)
	key, ok := feed.ItemKey(link, link, title, "")
	if !ok {
		panic("apptest: AppendItem produced an unusable item key")
	}
	out.Feed.Items = append(out.Feed.Items, model.FeedItem{
		Key:     key,
		Title:   title,
		Link:    link,
		Excerpt: title + " body",
		Order:   len(out.Feed.Items),
	})
	return out
}

// clone deep-copies the parts of a fetch result a test may mutate.
func clone(res *model.FetchResult) *model.FetchResult {
	out := *res
	if res.Feed != nil {
		f := *res.Feed
		f.Items = append([]model.FeedItem(nil), res.Feed.Items...)
		out.Feed = &f
	}
	return &out
}

// ---- fake Telegram ----

// Canonical Bot API envelopes. Written as constants rather than marshalled
// keyed values, both for readability and because the cache-boundary check bans them here.
const (
	respGetMe = `{"ok":true,"result":{"id":424242,"is_bot":true,` +
		`"first_name":"News Buddy","username":"news_buddy_test_bot"}}`
	respGetChat = `{"ok":true,"result":{"id":-1001234567890,"type":"channel",` +
		`"title":"News Buddy Test Channel"}}`
	respGetChatMember = `{"ok":true,"result":{"status":"administrator",` +
		`"user":{"id":424242,"is_bot":true,"first_name":"News Buddy",` +
		`"username":"news_buddy_test_bot"},"can_post_messages":true}}`
	respSendMessage = `{"ok":true,"result":{"message_id":777,"date":1700000000,` +
		`"chat":{"id":-1001234567890,"type":"channel"}}}`
	respEmptyUpdates = `{"ok":true,"result":[]}`
	respGeneric      = `{"ok":true,"result":true}`
)

// NewFakeTelegram returns an httptest.Server that answers the Bot API
// methods app.New and app.Preflight use, so a complete App can be
// constructed and run with zero external network. Pass its URL to
// app.WithBotOptions(tgbot.WithServerURL(...)).
func NewFakeTelegram(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The client builds "<serverURL>/bot<token>/<method>".
		method := r.URL.Path
		if i := strings.LastIndex(method, "/"); i >= 0 {
			method = method[i+1:]
		}
		w.Header().Set("Content-Type", "application/json")
		switch method {
		case "getMe":
			fmt.Fprint(w, respGetMe)
		case "getChat":
			fmt.Fprint(w, respGetChat)
		case "getChatMember":
			fmt.Fprint(w, respGetChatMember)
		case "sendMessage":
			fmt.Fprint(w, respSendMessage)
		case "getUpdates":
			// A short block keeps the long-polling loop from spinning hot
			// for the lifetime of the test.
			select {
			case <-r.Context().Done():
			case <-time.After(50 * time.Millisecond):
			}
			fmt.Fprint(w, respEmptyUpdates)
		default:
			fmt.Fprint(w, respGeneric)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// EnvPair is one scripted environment variable. A slice of these backs
// Getenv, because a string-keyed map is not allowed in this file.
type EnvPair struct {
	Key   string
	Value string
}

// Getenv returns a config.Load-compatible lookup over pairs; anything not
// listed reads as unset.
func Getenv(pairs []EnvPair) func(string) string {
	return func(k string) string {
		for _, p := range pairs {
			if p.Key == k {
				return p.Value
			}
		}
		return ""
	}
}
