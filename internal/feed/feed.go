package feed

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/borisovdev/newsbuddy/internal/model"
)

// FetchRequest is one conditional GET of a single feed URL.
type FetchRequest struct {
	URL          string // already normalized
	ETag         string // "" ⇒ no If-None-Match
	LastModified string // "" ⇒ no If-Modified-Since
}

// Fetcher performs the HTTP conditional GET and the parse. Faked wholesale
// in tests so no test touches the network.
type Fetcher interface {
	Fetch(ctx context.Context, req FetchRequest) (*model.FetchResult, error)
}

// Parser turns raw feed bytes into a model.Feed. Split out from Fetcher so
// parsing is unit-testable against testdata/ fixtures with no HTTP at all.
type Parser interface {
	Parse(data []byte, baseURL string) (*model.Feed, error)
}

// FetchError is a transport- or status-level failure. Its Temporary method
// drives the in-cycle retry policy.
type FetchError struct {
	Op         string // "dns" | "connect" | "timeout" | "status" | "toolarge" | "read"
	StatusCode int    // 0 when not an HTTP-status error
	RetryAfter time.Duration
	Err        error
}

// Error never contains credentials: the *url.Error wrapper (which carries the
// full request URL, userinfo included) is stripped before formatting.
func (e *FetchError) Error() string {
	switch e.Op {
	case "status":
		if e.RetryAfter > 0 {
			return fmt.Sprintf("http status %d (retry after %s)", e.StatusCode, e.RetryAfter)
		}
		return fmt.Sprintf("http status %d", e.StatusCode)
	case "toolarge":
		return "response body too large"
	default:
		return fmt.Sprintf("%s error: %s", e.Op, safeErrString(e.Err))
	}
}

func (e *FetchError) Unwrap() error { return e.Err }

// Temporary reports whether retrying inside the same poll cycle is worthwhile.
// A 429 is deliberately NOT temporary: the feed's own back-off is honored by
// skipping the source until the next cycle.
func (e *FetchError) Temporary() bool {
	switch e.Op {
	case "dns", "connect", "timeout", "read":
		return true
	case "status":
		return e.StatusCode >= 500 || e.StatusCode == 408 || e.StatusCode == 425
	default:
		return false
	}
}

// ParseError is a feed-parse failure. ContentType is filled in by the fetcher
// so an HTML page served with 200 produces a distinguishable message.
type ParseError struct {
	ContentType string
	Snippet     string // first 512 bytes of body, for debug logging
	Err         error
}

func (e *ParseError) Error() string {
	if e.ContentType != "" {
		return fmt.Sprintf("not a valid RSS/Atom feed (content-type: %s)", e.ContentType)
	}
	return "not a valid RSS/Atom feed"
}

func (e *ParseError) Unwrap() error { return e.Err }

// safeErrString unwraps *url.Error so the request URL never reaches a log
// line or a Telegram reply.
func safeErrString(err error) string {
	if err == nil {
		return "unknown"
	}
	var ue *url.Error
	if errors.As(err, &ue) && ue.Err != nil {
		return ue.Err.Error()
	}
	return err.Error()
}
