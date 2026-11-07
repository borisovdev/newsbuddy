// Package app is the composition root. It is the only place
// that knows how every subsystem is wired together, the only place that
// constructs the single shared *tgbot.Bot, and the only place that attaches
// the `component` log attribute — exactly once per subsystem, at
// construction time. Per-call `component` attributes are forbidden.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	tgbot "github.com/go-telegram/bot"

	"github.com/borisovdev/newsbuddy/internal/bot"
	"github.com/borisovdev/newsbuddy/internal/cache"
	"github.com/borisovdev/newsbuddy/internal/config"
	"github.com/borisovdev/newsbuddy/internal/feed"
	"github.com/borisovdev/newsbuddy/internal/logging"
	"github.com/borisovdev/newsbuddy/internal/poller"
	"github.com/borisovdev/newsbuddy/internal/service"
	"github.com/borisovdev/newsbuddy/internal/storage"
	"github.com/borisovdev/newsbuddy/internal/storage/sqlite"
	"github.com/borisovdev/newsbuddy/internal/telegram"
)

// PreflightFunc is the startup probe: it must prove the token works
// and that the bot can post to cfg.TelegramChannelID, and it must never
// send a probe message to the channel. A non-nil return is fatal.
type PreflightFunc func(ctx context.Context, b *tgbot.Bot, cfg config.Config, log *slog.Logger) error

// Option customises New. The two seams exist so an acceptance harness can
// wire a complete App against a fake Telegram endpoint without a real bot
// token; production passes no options and gets the real behaviour.
type Option func(*options)

type options struct {
	preflight  PreflightFunc
	botOptions []tgbot.Option
}

// WithPreflight replaces the preflight step. Pass a func that returns
// nil to skip preflight entirely (tests only).
func WithPreflight(f PreflightFunc) Option {
	return func(o *options) { o.preflight = f }
}

// WithBotOptions appends extra options to the tgbot.New call, after
// bot.Options(h), so later options win. Tests use this for
// tgbot.WithServerURL / tgbot.WithSkipGetMe: tgbot.New itself calls getMe
// at construction time, so redirecting it is the only way to build an App
// off the network.
func WithBotOptions(opts ...tgbot.Option) Option {
	return func(o *options) { o.botOptions = append(o.botOptions, opts...) }
}

// App owns every long-lived subsystem and the process lifecycle.
type App struct {
	cfg    config.Config
	log    *slog.Logger
	store  storage.Storage
	cache  cache.Cache
	poller *poller.Poller
	bot    *tgbot.Bot
}

// lazyPublisher breaks the one genuine construction cycle in New. The
// poller must exist before bot.NewHandlers (which takes *poller.Poller for
// /status), the handlers must exist before tgbot.New (handlers are
// construction-time-only options), and the publisher can only be built from
// that same *tgbot.Bot — so poller → publisher → bot → handlers → poller.
// Rather than mutate the frozen poller package with a setter, the poller is
// handed this indirection at New and the real publisher is installed into
// it once the bot exists. Nothing can call through it before then: the
// poller only publishes from Run/RunCycle, which start after Run is called.
type lazyPublisher struct {
	mu  sync.RWMutex
	pub poller.Publisher
}

func (l *lazyPublisher) set(p poller.Publisher) {
	l.mu.Lock()
	l.pub = p
	l.mu.Unlock()
}

func (l *lazyPublisher) SendWithFallback(ctx context.Context, html, plain string) (int64, error) {
	l.mu.RLock()
	p := l.pub
	l.mu.RUnlock()
	if p == nil {
		return 0, errors.New("app: publisher not installed yet")
	}
	return p.SendWithFallback(ctx, html, plain)
}

// New performs the construction sequence. Every step is fatal on
// error: there is no partial-boot degraded mode.
func New(ctx context.Context, cfg config.Config, log *slog.Logger, opts ...Option) (*App, error) {
	if log == nil {
		log = slog.Default()
	}

	o := options{preflight: Preflight}
	for _, f := range opts {
		f(&o)
	}

	// 1. Storage: creates the parent dir, applies DSN pragmas, asserts
	// foreign_keys == 1, runs migrations.
	store, err := sqlite.Open(ctx, cfg.DBPath)
	if err != nil {
		return nil, fmt.Errorf("open storage: %w", err)
	}

	// 2. Cache and its two typed views.
	mem := cache.NewMemory(cfg.CacheEntries)
	seenKeys := cache.NewSeenKeys(mem, cfg.CacheSeenTTL, cfg.CacheSeenPerSrc)
	sourceList := cache.NewSourceList(mem, cfg.CacheSourcesTTL)

	// 3. Fetcher.
	fetcher := feed.NewHTTPFetcher(cfg, feed.NewGofeedParser())

	// 4/5. Service and the seen index.
	svc := service.New(store, seenKeys, sourceList, fetcher, cfg, log.With("component", "service"))
	seenIdx := service.NewSeenIndex(store, seenKeys, log.With("component", "service"))

	// 6. Poller, with the publisher indirection described above.
	lazyPub := &lazyPublisher{}
	plr := poller.New(svc, seenIdx, store, fetcher, lazyPub, cfg, log.With("component", "poller"))

	// 7/8. Handlers first, then the single shared *tgbot.Bot. Exactly one
	// *tgbot.Bot exists in the process; a second would collide on
	// getUpdates with a 409.
	h := bot.NewHandlers(svc, plr, cfg, log.With("component", "bot"))
	b, err := tgbot.New(cfg.TelegramBotToken, append(bot.Options(h), o.botOptions...)...)
	if err != nil {
		store.Close()
		mem.Close()
		return nil, fmt.Errorf("construct telegram bot: %w", err)
	}

	// 9. Publisher over that same bot, installed into the poller.
	lazyPub.set(telegram.NewBotPublisher(b, cfg, log.With("component", "telegram")))

	// 10. Preflight, fatal.
	if o.preflight != nil {
		if err := o.preflight(ctx, b, cfg, log); err != nil {
			store.Close()
			mem.Close()
			return nil, fmt.Errorf("preflight: %w", err)
		}
	}

	// 11. Abandon sweep BEFORE the poller runs. Rows created by
	// this run are therefore never swept, which is what makes "logged once
	// ever" true: AbandonPending stamps abandoned_at and never returns the
	// same row again.
	abandoned, err := store.AbandonPending(ctx, time.Now().UTC())
	if err != nil {
		store.Close()
		mem.Close()
		return nil, fmt.Errorf("abandon sweep: %w", err)
	}
	for _, it := range abandoned {
		log.WarnContext(ctx, logging.EvPostAbandoned,
			"source_id", it.SourceID, "item_key", it.ItemKey)
	}

	// 12. Startup log with the redacted config (Config implements
	// slog.LogValuer, so the token can never leak here) and the version.
	log.InfoContext(ctx, logging.EvStartup,
		"config", cfg, "version", logging.Version)

	return &App{
		cfg:    cfg,
		log:    log,
		store:  store,
		cache:  mem,
		poller: plr,
		bot:    b,
	}, nil
}
