// Package tgtest provides a no-network test double for telegram.Publisher,
// used by publisher_test.go and by the poller tests to exercise
// the 429/403/400-parse-entities paths without real Telegram calls.
package tgtest

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	tgbot "github.com/go-telegram/bot"

	"github.com/borisovdev/newsbuddy/internal/telegram"
)

// Scripted outcome markers. ErrRateLimited and ErrParseEntities are fake
// stand-ins for Telegram's 429 and 400 "can't parse entities" responses.
// telegram.ErrForbidden is reused directly for the 403 case since that is
// the exact error BotPublisher.Send returns to callers.
//
// ErrParseEntities deliberately wraps tgbot.ErrorBadRequest and carries the
// literal "can't parse entities" text, so it satisfies the SAME predicate
// (telegram.IsParseEntitiesError) the production publisher branches on. A
// look-alike error here would leave that predicate untested.
var (
	ErrRateLimited   = errors.New("tgtest: too many requests (fake 429)")
	ErrParseEntities = fmt.Errorf("tgtest: %w: can't parse entities (fake 400)", tgbot.ErrorBadRequest)
)

// RecordedSend captures one attempt made against a FakePublisher. An
// initial html attempt has Plain == ""; a plain fallback attempt (taken
// after a scripted ErrParseEntities outcome) has HTML == "" and Plain set.
type RecordedSend struct {
	HTML  string
	Plain string
	Time  time.Time
}

// FakePublisher is a test double implementing telegram.Publisher (and the
// SendWithFallback method the poller also depends on). Every attempt is
// recorded, in call order, in a slice — never a map. Failures are
// scripted per attempt index via NewFakePublisher.
type FakePublisher struct {
	mu       sync.Mutex
	sends    []RecordedSend
	outcomes []error
	nextID   int64
}

var _ telegram.Publisher = (*FakePublisher)(nil)

// NewFakePublisher returns a FakePublisher. outcomes[i] is the result of
// the i-th attempt made against the fake (nil means success); attempts
// beyond len(outcomes) succeed. Each call to SendWithFallback counts as
// one attempt, plus a second if it falls back to plain.
func NewFakePublisher(outcomes ...error) *FakePublisher {
	return &FakePublisher{outcomes: outcomes}
}

// Count returns the number of recorded attempts, including any plain
// fallback attempt.
func (f *FakePublisher) Count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sends)
}

// Sends returns a copy of every recorded attempt, in call order.
func (f *FakePublisher) Sends() []RecordedSend {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]RecordedSend, len(f.sends))
	copy(out, f.sends)
	return out
}

// Send records the html attempt and returns its scripted outcome. It never
// attempts a plain fallback; use SendWithFallback for that.
func (f *FakePublisher) Send(_ context.Context, html string) (int64, error) {
	return f.attempt(html, "")
}

// SendWithFallback mirrors BotPublisher.SendWithFallback: it tries html
// first; if that attempt's scripted outcome satisfies the production
// predicate telegram.IsParseEntitiesError and plain is non-empty, it retries
// once with plain. Any other error, or an empty plain, is returned as-is
// with no second attempt.
func (f *FakePublisher) SendWithFallback(_ context.Context, html, plain string) (int64, error) {
	id, err := f.attempt(html, "")
	if err == nil || plain == "" || !telegram.IsParseEntitiesError(err) {
		return id, err
	}
	return f.attempt("", plain)
}

func (f *FakePublisher) attempt(html, plain string) (int64, error) {
	f.mu.Lock()
	idx := len(f.sends)
	f.sends = append(f.sends, RecordedSend{HTML: html, Plain: plain, Time: time.Now()})
	var err error
	if idx < len(f.outcomes) {
		err = f.outcomes[idx]
	}
	f.mu.Unlock()

	if err != nil {
		return 0, err
	}

	f.mu.Lock()
	f.nextID++
	id := f.nextID
	f.mu.Unlock()
	return id, nil
}
