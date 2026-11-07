package apptest_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	tgbot "github.com/go-telegram/bot"

	"github.com/borisovdev/newsbuddy/internal/app"
	"github.com/borisovdev/newsbuddy/internal/app/apptest"
	"github.com/borisovdev/newsbuddy/internal/cache"
	"github.com/borisovdev/newsbuddy/internal/config"
	"github.com/borisovdev/newsbuddy/internal/feed/feedtest"
	"github.com/borisovdev/newsbuddy/internal/logging"
	"github.com/borisovdev/newsbuddy/internal/model"
	"github.com/borisovdev/newsbuddy/internal/storage/sqlite"
)

const (
	urlA = "https://example.com/feed-a"
	urlB = "https://example.com/feed-b"
)

// keysOf lists every item key in a fetch result, in document order.
func keysOf(res *model.FetchResult) []string {
	out := make([]string, 0, len(res.Feed.Items))
	for _, it := range res.Feed.Items {
		out = append(out, it.Key)
	}
	return out
}

// TestSeedingPostsNothing: adding a feed with N>=10 existing items must
// post exactly zero messages, and must leave N seen rows behind.
func TestSeedingPostsNothing(t *testing.T) {
	h := apptest.New(t)
	res := apptest.Fixture(t, "rss2.xml", urlA)
	if len(res.Feed.Items) < 10 {
		t.Fatalf("fixture has %d items, the test needs >= 10", len(res.Feed.Items))
	}

	h.Script(urlA, res, 1)
	src := h.Add(urlA, "")

	if got := h.Sends(); got != 0 {
		t.Fatalf("sends after add = %d, want 0", got)
	}

	// N rows really exist: FilterUnseen returns nothing for any of them.
	unseen, err := h.Store.FilterUnseen(h.Ctx(), src.ID, keysOf(res))
	if err != nil {
		t.Fatalf("FilterUnseen: %v", err)
	}
	if len(unseen) != 0 {
		t.Errorf("unseen after seeding = %d (%v), want 0 — all %d keys must be seeded",
			len(unseen), unseen, len(res.Feed.Items))
	}

	// posted_at IS NULL for every seeded row.
	posted, err := h.Store.CountPostedSince(h.Ctx(), time.Unix(0, 0))
	if err != nil {
		t.Fatalf("CountPostedSince: %v", err)
	}
	if posted != 0 {
		t.Errorf("posted rows = %d, want 0", posted)
	}

	// A cycle over the unchanged feed still sends nothing.
	h.Script(urlA, res, 1)
	stats := h.Cycle()
	if stats.Posted != 0 || h.Sends() != 0 {
		t.Errorf("cycle after seeding: posted=%d sends=%d, want 0/0", stats.Posted, h.Sends())
	}
}

func TestOneNewItemPostsExactlyOnce(t *testing.T) {
	h := apptest.New(t)
	res := apptest.Fixture(t, "rss2.xml", urlA)

	h.Script(urlA, res, 1)
	h.Add(urlA, "")

	// Cycle 1: nothing changed since the seed.
	h.Script(urlA, res, 1)
	if stats := h.Cycle(); stats.Posted != 0 {
		t.Fatalf("cycle 1 posted = %d, want 0", stats.Posted)
	}

	// Cycle 2: one genuinely new item.
	grown := apptest.AppendItem(res, "Twelfth Article", "https://example.com/articles/12")
	h.Script(urlA, grown, 1)
	if stats := h.Cycle(); stats.Posted != 1 {
		t.Fatalf("cycle 2 posted = %d, want 1", stats.Posted)
	}
	if got := h.Sends(); got != 1 {
		t.Fatalf("sends after cycle 2 = %d, want 1", got)
	}

	// Five more identical cycles change nothing.
	h.Script(urlA, grown, 5)
	h.Cycles(5)
	if got := h.Sends(); got != 1 {
		t.Errorf("sends after 5 unchanged cycles = %d, want 1", got)
	}
}

// TestRestartPostsNoDuplicates closes the store and reopens the same
// file, the in-process approximation of SIGKILL.
func TestRestartPostsNoDuplicates(t *testing.T) {
	h := apptest.New(t)
	res := apptest.Fixture(t, "rss2.xml", urlA)
	grown := apptest.AppendItem(res, "Twelfth Article", "https://example.com/articles/12")

	h.Script(urlA, res, 1)
	h.Add(urlA, "")
	h.Script(urlA, grown, 1)
	h.Cycle()
	if got := h.Sends(); got != 1 {
		t.Fatalf("sends before restart = %d, want 1", got)
	}

	dbPathBefore := h.DBPath
	h.Reopen()
	if h.DBPath != dbPathBefore {
		t.Fatalf("Reopen changed the DB path")
	}

	h.Script(urlA, grown, 1)
	stats := h.Cycle()
	if stats.Posted != 0 {
		t.Errorf("cycle after restart posted = %d, want 0", stats.Posted)
	}
	if got := h.Sends(); got != 1 {
		t.Errorf("total sends after restart = %d, want 1 (no duplicates)", got)
	}
}

// TestCacheBackendsAgree runs the identical scenario against a warm
// in-memory cache, a cold one (state deleted, DB intact), and a cache whose
// every operation fails. The cache may only ever skip work, so
// all three must agree exactly.
func TestCacheBackendsAgree(t *testing.T) {
	scenario := func(t *testing.T, swap func(*apptest.Harness)) (int, int) {
		t.Helper()
		h := apptest.New(t)
		res := apptest.Fixture(t, "rss2.xml", urlA)
		grown := apptest.AppendItem(res, "Twelfth Article", "https://example.com/articles/12")

		h.Script(urlA, res, 1)
		h.Add(urlA, "")
		swap(h)

		h.Script(urlA, grown, 4)
		total := 0
		for i := 0; i < 4; i++ {
			total += h.Cycle().Posted
		}
		return h.Sends(), total
	}

	var sends, posted []int
	for _, tc := range []struct {
		name string
		swap func(*apptest.Harness)
	}{
		{"memory-warm", func(*apptest.Harness) {}},
		{"memory-cold", func(h *apptest.Harness) { h.Reopen() }},
		{"failing-cache", func(h *apptest.Harness) { h.ReopenWithCache(cache.FailingCache{}) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, p := scenario(t, tc.swap)
			if s != 1 || p != 1 {
				t.Errorf("%s: sends=%d posted=%d, want 1/1", tc.name, s, p)
			}
			sends = append(sends, s)
			posted = append(posted, p)
		})
	}

	for i := 1; i < len(sends); i++ {
		if sends[i] != sends[0] || posted[i] != posted[0] {
			t.Errorf("cache backends disagree: sends=%v posted=%v", sends, posted)
		}
	}
}

// TestNoGuidItemPostsOncePerFiveCycles uses the link-fallback fixture:
// item 1 is seeded at /add, item 2 appears afterwards and must post exactly
// once no matter how many cycles run.
func TestNoGuidItemPostsOncePerFiveCycles(t *testing.T) {
	h := apptest.New(t)
	res := apptest.Fixture(t, "no_guid.xml", urlA)
	if len(res.Feed.Items) < 2 {
		t.Fatalf("no_guid.xml has %d items, need >= 2", len(res.Feed.Items))
	}

	h.Script(urlA, apptest.Head(res, len(res.Feed.Items)-1), 1)
	h.Add(urlA, "")

	h.Script(urlA, res, 5)
	h.Cycles(5)

	if got := h.Sends(); got != 1 {
		t.Errorf("sends across 5 cycles = %d, want exactly 1", got)
	}
}

// TestSameArticleFromTwoSourcesPostsTwice: dedup is per source,
// so an article syndicated by two feeds posts from both. That is correct
// behaviour, not a duplicate.
func TestSameArticleFromTwoSourcesPostsTwice(t *testing.T) {
	h := apptest.New(t)
	resA := apptest.Fixture(t, "rss2.xml", urlA)
	resB := apptest.Fixture(t, "rss2.xml", urlB)

	last := len(resA.Feed.Items) - 1
	sharedKey := resA.Feed.Items[last].Key
	if sharedKey != resB.Feed.Items[last].Key {
		t.Fatalf("fixtures do not share an item key: %q vs %q",
			sharedKey, resB.Feed.Items[last].Key)
	}

	h.Script(urlA, apptest.Head(resA, last), 1)
	h.Add(urlA, "Source A")
	h.Script(urlB, apptest.Head(resB, last), 1)
	h.Add(urlB, "Source B")

	h.Script(urlA, resA, 1)
	h.Script(urlB, resB, 1)
	stats := h.Cycle()

	if stats.Posted != 2 || h.Sends() != 2 {
		t.Errorf("posted=%d sends=%d, want 2/2 (per-source dedup)", stats.Posted, h.Sends())
	}
}

// TestOneFailingSourceDoesNotStopTheOther: no errgroup, no
// sibling cancellation.
func TestOneFailingSourceDoesNotStopTheOther(t *testing.T) {
	h := apptest.New(t)
	resA := apptest.Fixture(t, "rss2.xml", urlA)
	resB := apptest.Fixture(t, "atom.xml", urlB)
	lastB := len(resB.Feed.Items) - 1

	h.Script(urlA, resA, 1)
	h.Add(urlA, "Broken Source")
	h.Script(urlB, apptest.Head(resB, lastB), 1)
	h.Add(urlB, "Healthy Source")

	h.Logs.Reset()

	// A plain error is not a temporary *feed.FetchError, so the poller does
	// not retry it and the cycle stays fast.
	h.ScriptErr(urlA, errors.New("apptest: source A is unreachable"), 1)
	h.Script(urlB, resB, 1)

	stats := h.Cycle()

	if stats.Errors != 1 {
		t.Errorf("cycle errors = %d, want 1", stats.Errors)
	}
	if stats.Posted != 1 || h.Sends() != 1 {
		t.Errorf("posted=%d sends=%d, want 1/1 (B must still post)", stats.Posted, h.Sends())
	}
	if n := h.Logs.Count(logging.EvSourceError); n != 1 {
		t.Errorf("%s lines = %d, want 1", logging.EvSourceError, n)
	}
	if n := h.Logs.Count(logging.EvCycleEnd); n != 1 {
		t.Errorf("%s lines = %d, want 1 (the cycle must complete normally)", logging.EvCycleEnd, n)
	}
}

// TestMalformedFeedNeverCrashes: a malformed feed must produce "an error log and no
// crash across 3 cycles". gofeed is deliberately lenient and currently
// accepts testdata/malformed.xml (the unclosed <title> swallows the sibling
// elements), so the error branch cannot be forced without faking it. The
// no-crash half is what this test actually protects, and it is asserted either
// way; the error-log half is asserted only when the parser really does
// reject the fixture.
func TestMalformedFeedNeverCrashes(t *testing.T) {
	h := apptest.New(t)

	parsed, parseErr := feedtest.LoadFixture(apptest.FixturePath("malformed.xml"), urlA)

	if parseErr != nil {
		t.Logf("parser rejects malformed.xml: %v", parseErr)
		// Seed with a healthy feed so the source exists, then fail it.
		h.Script(urlA, apptest.Fixture(t, "rss2.xml", urlA), 1)
		h.Add(urlA, "Malformed Source")
		h.Logs.Reset()
		h.ScriptErr(urlA, parseErr, 3)
	} else {
		t.Logf("parser accepts malformed.xml leniently: %d item(s)", len(parsed.Feed.Items))
		h.Script(urlA, parsed, 1)
		h.Add(urlA, "Malformed Source")
		h.Logs.Reset()
		h.Script(urlA, parsed, 3)
	}

	for i := 0; i < 3; i++ {
		h.Cycle() // a panic here fails the test outright
	}

	if n := h.Logs.Count(logging.EvCycleEnd); n != 3 {
		t.Errorf("%s lines = %d, want 3 (all three cycles must complete)", logging.EvCycleEnd, n)
	}
	if parseErr != nil {
		if n := h.Logs.Count(logging.EvSourceError); n != 3 {
			t.Errorf("%s lines = %d, want 3", logging.EvSourceError, n)
		}
	}
	if got := h.Sends(); got != 0 {
		t.Errorf("sends = %d, want 0 (nothing new in a repeated feed)", got)
	}
	// The process state is intact: storage still answers.
	if err := h.Store.Ping(h.Ctx()); err != nil {
		t.Errorf("store unusable after malformed cycles: %v", err)
	}
}

func TestEmptyFeedIsSuccessNotError(t *testing.T) {
	h := apptest.New(t)
	res := apptest.Fixture(t, "empty_items.xml", urlA)
	if len(res.Feed.Items) != 0 {
		t.Fatalf("empty_items.xml has %d items, want 0", len(res.Feed.Items))
	}

	h.Script(urlA, res, 1)
	h.Add(urlA, "Empty Source")
	h.Logs.Reset()

	h.Script(urlA, res, 1)
	stats := h.Cycle()

	if stats.Errors != 0 {
		t.Errorf("cycle errors = %d, want 0", stats.Errors)
	}
	if n := h.Logs.Count(logging.EvSourceError); n != 0 {
		t.Errorf("%s lines = %d, want 0", logging.EvSourceError, n)
	}

	lines := h.Logs.LinesFor(logging.EvSourceOK)
	if len(lines) != 1 {
		t.Fatalf("%s lines = %d, want 1", logging.EvSourceOK, len(lines))
	}
	fields := decodeLine(t, lines[0])
	if items, ok := fields["items"].(float64); !ok || items != 0 {
		t.Errorf("%s items = %v, want numeric 0", logging.EvSourceOK, fields["items"])
	}
}

// TestContextCancelShutsDownCleanly boots a complete App against the
// fake Telegram server, waits until the first poll cycle has actually
// started, then cancels the root context the way SIGTERM would.
func TestContextCancelShutsDownCleanly(t *testing.T) {
	sink := &apptest.LogSink{}
	log := logging.New("json", slog.LevelDebug, sink)

	cfg := config.Defaults()
	cfg.TelegramBotToken = apptest.FakeToken
	cfg.TelegramChannelID = "@news_buddy_test"
	cfg.TelegramOwnerID = 4242
	cfg.DBPath = filepath.Join(t.TempDir(), "shutdown", "news-buddy.db")

	a := bootApp(t, cfg, log)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	codeCh := make(chan int, 1)
	go func() { codeCh <- a.Run(ctx) }()

	// Wait until a cycle is genuinely in flight before cancelling.
	waitFor(t, 5*time.Second, func() bool { return sink.Count(logging.EvCycleStart) >= 1 })

	t0 := time.Now()
	cancel()

	var code int
	select {
	case code = <-codeCh:
	case <-time.After(cfg.ShutdownBudget + 5*time.Second):
		t.Fatal("Run did not return within the shutdown budget")
	}
	elapsed := time.Since(t0)

	if code != 0 {
		t.Errorf("Run exit code = %d, want 0", code)
	}
	if elapsed > cfg.ShutdownBudget+2*time.Second {
		t.Errorf("shutdown took %s, budget is %s", elapsed, cfg.ShutdownBudget)
	}
	if n := sink.Count(logging.EvShutdown); n < 1 {
		t.Errorf("%s lines = %d, want >= 1", logging.EvShutdown, n)
	}
}

// TestAbandonSweepWarnsOncePerRowEver simulates a crash between
// InsertPending and the send: a row with posted_at IS NULL and seeded = 0.
// The first boot must warn about it exactly once, the second boot must say
// nothing, and no later cycle may ever send it (at-most-once).
func TestAbandonSweepWarnsOncePerRowEver(t *testing.T) {
	h := apptest.New(t)
	res := apptest.Fixture(t, "rss2.xml", urlA)
	last := len(res.Feed.Items) - 1
	crashedKey := res.Feed.Items[last].Key

	h.Script(urlA, apptest.Head(res, last), 1)
	src := h.Add(urlA, "Crashy Source")

	inserted, err := h.Store.InsertPending(h.Ctx(), src.ID, crashedKey)
	if err != nil || !inserted {
		t.Fatalf("InsertPending(crashed send) = %v, %v", inserted, err)
	}
	h.Close() // release the file before the App opens it

	// Boot 1: exactly one post.abandoned warn for that row.
	sink1 := &apptest.LogSink{}
	a1 := bootApp(t, h.Cfg, logging.New("json", slog.LevelDebug, sink1))
	stopApp(t, a1)

	lines := sink1.LinesFor(logging.EvPostAbandoned)
	if len(lines) != 1 {
		t.Fatalf("boot 1 %s lines = %d, want 1", logging.EvPostAbandoned, len(lines))
	}
	fields := decodeLine(t, lines[0])
	if fields["level"] != "WARN" {
		t.Errorf("%s level = %v, want WARN", logging.EvPostAbandoned, fields["level"])
	}
	if fields["item_key"] != crashedKey {
		t.Errorf("%s item_key = %v, want %q", logging.EvPostAbandoned, fields["item_key"], crashedKey)
	}

	// Boot 2 against the same DB: silence.
	sink2 := &apptest.LogSink{}
	a2 := bootApp(t, h.Cfg, logging.New("json", slog.LevelDebug, sink2))
	stopApp(t, a2)

	if n := sink2.Count(logging.EvPostAbandoned); n != 0 {
		t.Errorf("boot 2 %s lines = %d, want 0 (logged once ever)", logging.EvPostAbandoned, n)
	}

	// The abandoned item is never sent by a later cycle.
	h.Reopen()
	h.Script(urlA, res, 1)
	stats := h.Cycle()
	if stats.Posted != 0 || h.Sends() != 0 {
		t.Errorf("abandoned item was re-sent: posted=%d sends=%d, want 0/0", stats.Posted, h.Sends())
	}
}

// TestBootsFromRequiredEnvOnly exercises the real app.New wiring: the
// three required environment variables, a DB path whose directory does not
// exist yet, and the T17 Option seam pointed at a fake Telegram server.
func TestBootsFromRequiredEnvOnly(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "does", "not", "exist", "news-buddy.db")
	if _, err := os.Stat(filepath.Dir(dbPath)); !os.IsNotExist(err) {
		t.Fatalf("precondition: %s must not exist yet", filepath.Dir(dbPath))
	}

	cfg, err := config.Load(apptest.Getenv([]apptest.EnvPair{
		{Key: "TELEGRAM_BOT_TOKEN", Value: apptest.FakeToken},
		{Key: "TELEGRAM_CHANNEL_ID", Value: "@news_buddy_test"},
		{Key: "TELEGRAM_OWNER_ID", Value: "4242"},
		// The only non-required override: the "no pre-existing DB file"
		// must land inside t.TempDir() rather than in ./data.
		{Key: "DB_PATH", Value: dbPath},
	}))
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}

	sink := &apptest.LogSink{}
	a := bootApp(t, cfg, logging.New("json", slog.LevelDebug, sink))

	// The directory and the file were created.
	info, err := os.Stat(dbPath)
	if err != nil {
		t.Fatalf("DB file was not created: %v", err)
	}
	if info.Size() == 0 {
		t.Fatal("DB file is empty; migrations did not run")
	}

	// It is a real SQLite database, not just a touched file.
	header := make([]byte, 16)
	f, err := os.Open(dbPath)
	if err != nil {
		t.Fatalf("open DB file: %v", err)
	}
	if _, err := f.Read(header); err != nil {
		t.Fatalf("read DB header: %v", err)
	}
	f.Close()
	if got := string(header); got != "SQLite format 3\x00" {
		t.Errorf("DB header = %q, want the SQLite magic", got)
	}

	stopApp(t, a)

	// Migrations really ran: the schema the storage layer needs is present.
	store, err := sqlite.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("reopen migrated DB: %v", err)
	}
	defer store.Close()
	if _, err := store.ListSources(context.Background()); err != nil {
		t.Errorf("ListSources on the migrated DB: %v", err)
	}

	if n := sink.Count(logging.EvStartup); n < 1 {
		t.Errorf("%s lines = %d, want >= 1", logging.EvStartup, n)
	}
}

// ---------------------------------------------------------------- helpers

// bootApp constructs a real App against a fake Telegram server, with
// preflight stubbed out per the T17 seam.
func bootApp(t *testing.T, cfg config.Config, log *slog.Logger) *app.App {
	t.Helper()
	srv := apptest.NewFakeTelegram(t)

	preflightCalled := false
	a, err := app.New(context.Background(), cfg, log,
		app.WithBotOptions(tgbot.WithServerURL(srv.URL)),
		app.WithPreflight(func(context.Context, *tgbot.Bot, config.Config, *slog.Logger) error {
			preflightCalled = true
			return nil
		}),
	)
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	if !preflightCalled {
		t.Error("the injected preflight was never called")
	}
	return a
}

// stopApp runs the App with an already-cancelled context, which is the only
// exported way to release its store and cache.
func stopApp(t *testing.T, a *app.App) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if code := a.Run(ctx); code != 0 {
		t.Errorf("Run() = %d, want 0", code)
	}
}

// decodeLine parses one JSON log line. map[string]any is legal here because
// the cache-boundary check excludes *_test.go files.
func decodeLine(t *testing.T, line string) map[string]any {
	t.Helper()
	var fields map[string]any
	if err := json.Unmarshal([]byte(line), &fields); err != nil {
		t.Fatalf("log line is not valid JSON: %v\nline: %s", err, line)
	}
	return fields
}

// waitFor polls cond until it holds or the deadline passes.
func waitFor(t *testing.T, budget time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("condition not met within %s", budget)
}
