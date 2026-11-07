// Package poller owns the periodic poll cycle: it fans out over every
// source, fetches conditionally, and posts the items it has never posted
// before. Every invariant that keeps the channel duplicate-free lives here
// or in storage.
package poller

import (
	"context"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync"
	"time"

	"github.com/borisovdev/newsbuddy/internal/config"
	"github.com/borisovdev/newsbuddy/internal/feed"
	"github.com/borisovdev/newsbuddy/internal/logging"
	"github.com/borisovdev/newsbuddy/internal/model"
	"github.com/borisovdev/newsbuddy/internal/service"
	"github.com/borisovdev/newsbuddy/internal/storage"
)

// Publisher is the poller's own narrow view of its outbound channel. It is
// deliberately NOT telegram.Publisher: that interface's Send(ctx, html)
// cannot carry the plain-text body, so wiring it here would silently drop
// the plain-text fallback. Both *telegram.BotPublisher and *tgtest.FakePublisher
// satisfy this, so the correct wiring is a compile-time requirement.
type Publisher interface {
	SendWithFallback(ctx context.Context, html, plain string) (messageID int64, err error)
}

// CycleStats is the aggregate outcome of one poll cycle.
type CycleStats struct {
	Sources  int
	NewItems int
	Posted   int
	Errors   int
	Duration time.Duration
}

// sourceStats is one source's contribution to a cycle.
type sourceStats struct {
	newItems   int
	posted     int
	failed     bool
	publishErr string // set only on ErrForbidden
}

// Poller runs the poll cycle. It is safe for concurrent use: Snapshot may be
// called from the bot goroutine while a cycle is running.
type Poller struct {
	svc  *service.SourceService
	seen *service.SeenIndex
	st   storage.Storage
	f    feed.Fetcher
	pub  Publisher
	cfg  config.Config
	log  *slog.Logger

	mu               sync.Mutex
	last             CycleStats
	lastCycleAt      time.Time
	lastPublishError string
}

// New returns a Poller over the given collaborators.
func New(svc *service.SourceService, seen *service.SeenIndex, st storage.Storage, f feed.Fetcher, pub Publisher, cfg config.Config, log *slog.Logger) *Poller {
	if log == nil {
		log = slog.Default()
	}
	return &Poller{svc: svc, seen: seen, st: st, f: f, pub: pub, cfg: cfg, log: log}
}

// Run fires one cycle immediately, then one on every tick at
// cfg.PollInterval, until ctx is done. Cycles never overlap: this is a
// single goroutine calling RunCycle synchronously, so a cycle that overruns
// the interval simply delays the next tick.
func (p *Poller) Run(ctx context.Context) {
	interval := p.cfg.PollInterval
	if interval <= 0 {
		interval = config.Defaults().PollInterval
	}

	if ctx.Err() == nil {
		p.RunCycle(ctx)
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if ctx.Err() != nil {
				return
			}
			p.RunCycle(ctx)
		}
	}
}

// RunCycle polls every source once and returns the cycle's aggregate stats.
// It is exported because it is how tests drive time, and it never returns an
// error: a source that fails affects nothing but its own row.
func (p *Poller) RunCycle(ctx context.Context) CycleStats {
	t0 := time.Now()

	// Start pairs with end for EVERY cycle, including one that cannot
	// even load the source list, so it is logged before List runs. The count is
	// not known yet; cycle.end carries the real one.
	p.log.InfoContext(ctx, logging.EvCycleStart, "sources", 0)

	sources, err := p.svc.List(ctx)
	if err != nil {
		stats := CycleStats{Errors: 1, Duration: time.Since(t0)}
		p.log.ErrorContext(ctx, logging.EvCycleEnd,
			"sources", 0, "new_items", 0, "posted", 0, "errors", 1,
			"error", err.Error(), "duration_ms", stats.Duration.Milliseconds())
		p.record(stats, "")
		return stats
	}

	// Zero sources is a valid idle cycle.
	if len(sources) == 0 {
		stats := CycleStats{Duration: time.Since(t0)}
		p.log.InfoContext(ctx, logging.EvCycleEnd,
			"sources", 0, "new_items", 0, "posted", 0, "errors", 0,
			"duration_ms", stats.Duration.Milliseconds())
		p.record(stats, "")
		return stats
	}

	// Bounded worker pool. errgroup.WithContext is forbidden here:
	// it cancels siblings on the first error, which is exactly what
	// per-source isolation forbids. Each worker owns results[i], so no mutex is needed.
	concurrency := p.cfg.PollConcurrency
	if concurrency <= 0 {
		concurrency = 1
	}
	var wg sync.WaitGroup
	sem := make(chan struct{}, concurrency)
	results := make([]sourceStats, len(sources))

	for i, src := range sources {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, src model.Source) {
			defer wg.Done()
			defer func() { <-sem }()
			defer func() {
				// A panic in one source must never take down the cycle.
				if r := recover(); r != nil {
					results[i] = sourceStats{failed: true}
					p.log.ErrorContext(ctx, logging.EvSourceError,
						"source_id", src.ID,
						"error", fmt.Sprintf("panic: %v", r),
						"consecutive_failures", src.ConsecutiveFailures+1,
						"stack", string(debug.Stack()))
				}
			}()
			results[i] = p.pollSource(ctx, src)
		}(i, src)
	}
	wg.Wait()

	stats := CycleStats{Sources: len(sources)}
	var publishErr string
	for _, r := range results {
		stats.NewItems += r.newItems
		stats.Posted += r.posted
		if r.failed {
			stats.Errors++
		}
		if r.publishErr != "" {
			publishErr = r.publishErr
		}
	}
	stats.Duration = time.Since(t0)

	p.record(stats, publishErr)
	p.log.InfoContext(ctx, logging.EvCycleEnd,
		"sources", stats.Sources, "new_items", stats.NewItems,
		"posted", stats.Posted, "errors", stats.Errors,
		"duration_ms", stats.Duration.Milliseconds())

	return stats
}

// Snapshot returns the last cycle's state for /status.
func (p *Poller) Snapshot() model.Status {
	p.mu.Lock()
	defer p.mu.Unlock()

	out := model.Status{
		LastCycleDuration: p.last.Duration,
		LastCycleErrors:   p.last.Errors,
		LastPublishError:  p.lastPublishError,
	}
	if !p.lastCycleAt.IsZero() {
		at := p.lastCycleAt
		out.LastCycleAt = &at
	}
	return out
}

// record stores the cycle result behind the Snapshot mutex. A publish error
// is sticky: it stays visible until a later cycle reports a new one.
func (p *Poller) record(stats CycleStats, publishErr string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.last = stats
	p.lastCycleAt = time.Now().UTC()
	if publishErr != "" {
		p.lastPublishError = publishErr
	}
}
