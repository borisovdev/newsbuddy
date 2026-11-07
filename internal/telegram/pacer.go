// Package telegram implements outbound publishing to a Telegram channel:
// rate-limited, retrying sends of pre-rendered HTML with a plain-text
// fallback for escaping bugs.4.
package telegram

import (
	"context"
	"sync"
	"time"
)

// Pacer enforces a minimum interval between successive Wait calls, so
// outbound channel posts stay under Telegram's per-channel rate limit
// . The lock is only held to compute the delay; it is never
// held across the sleep, so a canceled context returns promptly.
type Pacer struct {
	mu       sync.Mutex
	interval time.Duration
	next     time.Time
}

// NewPacer returns a Pacer that enforces at least interval between
// successive Wait calls. interval == 0 means no delay (used in tests).
func NewPacer(interval time.Duration) *Pacer {
	return &Pacer{interval: interval}
}

// Wait blocks until it is this caller's turn to send, or ctx is done,
// whichever comes first.
func (p *Pacer) Wait(ctx context.Context) error {
	p.mu.Lock()
	now := time.Now()
	wait := time.Until(p.next)
	if p.next.Before(now) {
		p.next = now
	}
	p.next = p.next.Add(p.interval)
	p.mu.Unlock()

	if wait <= 0 {
		return nil
	}

	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
