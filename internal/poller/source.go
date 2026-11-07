package poller

import (
	"context"
	"errors"
	"math/rand/v2"
	"sort"
	"time"

	"github.com/borisovdev/newsbuddy/internal/feed"
	"github.com/borisovdev/newsbuddy/internal/logging"
	"github.com/borisovdev/newsbuddy/internal/model"
	"github.com/borisovdev/newsbuddy/internal/render"
	"github.com/borisovdev/newsbuddy/internal/storage"
	"github.com/borisovdev/newsbuddy/internal/telegram"
)

// retryBackoffs is the base sleep before retry attempt i+1 (1s then
// 4s, each jittered ±25%). A package var, not a const slice, purely so tests
// can shrink it; production never reassigns it.
var retryBackoffs = []time.Duration{time.Second, 4 * time.Second}

// sendTimeout bounds one SendWithFallback call so a hung send cannot pin a
// worker forever. It is deliberately far above the publisher's own worst
// case (pacing + 429 retries) so it can never pre-empt the publisher's retry loop.
const sendTimeout = 2 * time.Minute

// errMessageMax is the rune budget for a stored last_error.
const errMessageMax = 500

// pollSource runs steps 1-10 below for one source. It never returns an
// error: a failing source is recorded on its own row and nowhere else
// , and it is never auto-disabled.
func (p *Poller) pollSource(ctx context.Context, src model.Source) sourceStats {
	t0 := time.Now()
	var stats sourceStats

	// 1. Start.
	p.log.DebugContext(ctx, logging.EvSourceStart, "source_id", src.ID)

	// 2. Fetch with retry.
	fetch, err := p.fetchWithRetry(ctx, src)
	if err == nil && fetch == nil {
		err = &feed.ParseError{Err: errors.New("empty fetch result")}
	}
	if err == nil && !fetch.NotModified && fetch.Feed == nil {
		err = &feed.ParseError{Err: errors.New("fetch result carried no feed")}
	}

	now := time.Now().UTC()

	// 3. Failure: record it, log it, return. Never propagate.
	if err != nil {
		stats.failed = true
		p.recordFailure(ctx, src, now, err)
		return stats
	}

	// 4. 304. The fetcher echoes the REQUEST's validators back into
	// the result, so carrying them here is what keeps conditional GET alive
	// on the next cycle. Constructing this literal with zero-valued
	// ETag/LastModified would wipe the stored validators on every 304 and
	// defeat conditional GET permanently. Do not "simplify" it.
	if fetch.NotModified {
		p.updatePollResult(ctx, model.PollResult{
			SourceID: src.ID, PolledAt: now, Success: true, StoreValidators: true,
			ETag: fetch.ETag, LastModified: fetch.LastModified,
		})
		p.log.InfoContext(ctx, logging.EvSourceOK,
			"source_id", src.ID, "http_status", 304,
			"items", 0, "new", 0, "posted", 0,
			"duration_ms", time.Since(t0).Milliseconds())
		return stats
	}

	items := fetch.Feed.Items

	// 5. Filter to unseen (the cache only ever skips work).
	keys := make([]string, 0, len(items))
	for _, it := range items {
		keys = append(keys, it.Key)
	}
	unseenKeys, err := p.seen.Unseen(ctx, src.ID, keys)
	if err != nil {
		stats.failed = true
		p.recordFailure(ctx, src, now, err)
		return stats
	}

	newItems := selectByKeys(items, unseenKeys)
	stats.newItems = len(newItems)

	// 6. Order: dated items first, ascending by date; then undated
	// items in feed document order.
	sort.SliceStable(newItems, func(i, j int) bool {
		a, b := newItems[i], newItems[j]
		if (a.Published != nil) != (b.Published != nil) {
			return a.Published != nil
		}
		if a.Published != nil && !a.Published.Equal(*b.Published) {
			return a.Published.Before(*b.Published)
		}
		return a.Order < b.Order
	})

	// 7. Cap.
	truncated := false
	if max := p.cfg.MaxItemsPerPoll; max > 0 && len(newItems) > max {
		newItems = newItems[:max]
		truncated = true
	}

	// 8. Post loop — write, then send, then mark.
	opts := render.Options{
		TitleMax:   p.cfg.TitleMaxLen,
		ExcerptMax: p.cfg.ExcerptLen,
		HardLimit:  p.cfg.MessageMaxUTF16,
	}
	for _, it := range newItems {
		if ctx.Err() != nil {
			break // shutdown between items; the in-flight send already finished
		}

		inserted, err := p.seen.InsertPending(ctx, src.ID, it.Key)
		switch {
		case errors.Is(err, storage.ErrSourceGone):
			// /remove raced this cycle. Discard the rest.
			p.log.WarnContext(ctx, logging.EvSourceRemovedMid, "source_id", src.ID)
			return stats
		case err != nil:
			// No write happened, so we must not send.
			p.log.ErrorContext(ctx, logging.EvPostFailed,
				"source_id", src.ID, "item_key", it.Key,
				"error", err.Error(), "retry_decision", "insert_failed")
			continue
		case !inserted:
			continue // already known: NEVER post a duplicate
		}

		html, unlinked := render.Message(it, src.Title, opts)
		plain := render.PlainMessage(it, src.Title, opts)
		if unlinked {
			p.log.WarnContext(ctx, logging.EvPostUnlinked, "source_id", src.ID, "item_key", it.Key)
		}

		// The in-flight send must survive shutdown, so it does not
		// inherit ctx's cancellation. The ctx.Err() check above is what stops
		// the loop from starting new work.
		shutdownProof := context.WithoutCancel(ctx)
		sendCtx, cancel := context.WithTimeout(shutdownProof, sendTimeout)
		msgID, err := p.pub.SendWithFallback(sendCtx, html, plain)
		cancel()
		if err != nil {
			p.log.ErrorContext(ctx, logging.EvPostFailed,
				"source_id", src.ID, "item_key", it.Key,
				"error", err.Error(), "retry_decision", retryDecision(err))
			if errors.Is(err, telegram.ErrForbidden) {
				// The bot was ejected. Abort the source; the row
				// stays pending and is abandoned on the next restart.
				stats.publishErr = err.Error()
				return stats
			}
			continue
		}

		// The mark must survive shutdown for the same reason the send does: an
		// item that WAS posted but never marked is reported as abandoned on the
		// next boot, so it does not inherit ctx's cancellation either.
		markCtx, markCancel := context.WithTimeout(shutdownProof, sendTimeout)
		err = p.seen.MarkPosted(markCtx, src.ID, it.Key, msgID, time.Now().UTC())
		markCancel()
		if err != nil {
			p.log.ErrorContext(ctx, logging.EvPostFailed,
				"source_id", src.ID, "item_key", it.Key,
				"error", err.Error(), "retry_decision", "mark_posted_failed")
		}
		stats.posted++
		p.log.InfoContext(ctx, logging.EvPostSent,
			"source_id", src.ID, "item_key", it.Key, "message_id", msgID)
	}

	// 9. Persist the successful poll result. StoreValidators: !truncated is a
	// CORRECTNESS requirement, not an optimization: storing the new
	// validators after a truncated batch would make the next fetch a 304 and
	// the overflow items the per-cycle cap promises to pick up next cycle would never be
	// seen again.
	p.updatePollResult(ctx, model.PollResult{
		SourceID: src.ID, PolledAt: now, Success: true,
		StoreValidators: !truncated,
		ETag:            fetch.ETag, LastModified: fetch.LastModified,
	})

	// 10. Result line.
	p.log.InfoContext(ctx, logging.EvSourceOK,
		"source_id", src.ID, "http_status", fetch.StatusCode,
		"items", len(items), "new", stats.newItems, "posted", stats.posted,
		"duration_ms", time.Since(t0).Milliseconds())

	return stats
}

// fetchWithRetry performs up to 1+cfg.FetchRetries attempts, retrying only
// when the error is a temporary *feed.FetchError. 4xx (429 included),
// parse errors, and oversize bodies are never retried.
func (p *Poller) fetchWithRetry(ctx context.Context, src model.Source) (*model.FetchResult, error) {
	attempts := p.cfg.FetchRetries + 1
	if attempts < 1 {
		attempts = 1
	}

	req := feed.FetchRequest{URL: src.FeedURL, ETag: src.ETag, LastModified: src.LastModified}

	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		if attempt > 0 {
			if err := sleepJittered(ctx, backoffFor(attempt-1)); err != nil {
				return nil, lastErr
			}
		}

		res, err := p.f.Fetch(ctx, req)
		if err == nil {
			return res, nil
		}
		lastErr = err

		var fe *feed.FetchError
		if !errors.As(err, &fe) || !fe.Temporary() {
			return nil, err
		}
	}
	return nil, lastErr
}

// backoffFor returns the base backoff for the i-th retry, clamped to the
// last configured step.
func backoffFor(i int) time.Duration {
	if len(retryBackoffs) == 0 {
		return 0
	}
	if i >= len(retryBackoffs) {
		i = len(retryBackoffs) - 1
	}
	return retryBackoffs[i]
}

// sleepJittered waits base*(0.75 + 0.5*rand) or until ctx is done, so a
// pending retry never delays shutdown.
func sleepJittered(ctx context.Context, base time.Duration) error {
	if base <= 0 {
		return ctx.Err()
	}
	d := time.Duration(float64(base) * (0.75 + 0.5*rand.Float64()))
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// recordFailure writes the failure row and logs it. Validators
// are left untouched on a failure result.
func (p *Poller) recordFailure(ctx context.Context, src model.Source, now time.Time, err error) {
	p.updatePollResult(ctx, model.PollResult{
		SourceID:   src.ID,
		PolledAt:   now,
		Success:    false,
		ErrMessage: truncate(err.Error(), errMessageMax),
	})

	p.log.ErrorContext(ctx, logging.EvSourceError,
		"source_id", src.ID, "error", err.Error(),
		"consecutive_failures", src.ConsecutiveFailures+1)

	var pe *feed.ParseError
	if errors.As(err, &pe) && pe.Snippet != "" {
		p.log.DebugContext(ctx, logging.EvSourceError,
			"source_id", src.ID, "snippet", pe.Snippet)
	}
}

// updatePollResult persists r, logging (never propagating) a storage error:
// a source whose row cannot be updated must not break the cycle.
func (p *Poller) updatePollResult(ctx context.Context, r model.PollResult) {
	if err := p.st.UpdateSourcePollResult(ctx, r); err != nil {
		p.log.ErrorContext(ctx, logging.EvSourceError,
			"source_id", r.SourceID, "error", err.Error(),
			"consecutive_failures", 0)
	}
}

// selectByKeys returns the items whose Key appears in keys, in items order.
// A linear scan, deliberately not a map; the batches are small.
func selectByKeys(items []model.FeedItem, keys []string) []model.FeedItem {
	out := make([]model.FeedItem, 0, len(keys))
	for _, it := range items {
		for _, k := range keys {
			if k == it.Key {
				out = append(out, it)
				break
			}
		}
	}
	return out
}

// retryDecision labels a send failure for the log line.
func retryDecision(err error) string {
	if errors.Is(err, telegram.ErrForbidden) {
		return "forbidden"
	}
	return "none"
}

// truncate cuts s to at most max runes, always on a rune boundary.
func truncate(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max])
}
