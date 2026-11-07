package model

import "time"

// Source is a subscribed feed.
type Source struct {
	ID                  int64
	FeedURL             string // normalized form
	Title               string // display name, ≤64 runes
	CreatedAt           time.Time
	LastPolledAt        *time.Time
	LastSuccessAt       *time.Time
	LastError           string
	ConsecutiveFailures int
	ETag                string
	LastModified        string
}

// FeedItem is one parsed, key-derived entry. Text fields are RAW
// (still HTML/entity-bearing); cleaning happens only in package render.
type FeedItem struct {
	Key       string // dedup key (feed/itemkey.go); never empty for a usable item
	Title     string
	Link      string     // absolute http(s) URL, or ""
	Excerpt   string     // first non-empty of description/summary/content
	Published *time.Time // nil when absent or unparseable
	Order     int        // 0-based index in feed document order (ordering tiebreak)
}

// Feed is a parsed feed document.
type Feed struct {
	Title string
	Link  string
	Items []FeedItem
}

// FetchResult is the outcome of one HTTP fetch attempt.
type FetchResult struct {
	NotModified  bool // HTTP 304; Feed is nil
	StatusCode   int
	Feed         *Feed
	ETag         string
	LastModified string
	FinalURL     string // after redirects; logged, never persisted
}

// PollResult is one update to the sources row.
type PollResult struct { // one update to the sources row
	SourceID        int64
	PolledAt        time.Time
	Success         bool
	ErrMessage      string // truncated to 500 runes by the caller
	ETag            string // persisted only when Success
	LastModified    string
	StoreValidators bool // false ⇒ clear etag/last_modified
}

// AbandonedItem identifies one pending seen_items row stamped as abandoned
// by the startup sweep.
type AbandonedItem struct {
	SourceID int64
	ItemKey  string
}

// Status is the /status payload.
type Status struct { // /status payload
	StartedAt         time.Time
	SourceCount       int
	PostedLast24h     int
	LastCycleAt       *time.Time
	LastCycleDuration time.Duration
	LastCycleErrors   int
	LastPublishError  string // set when the bot may no longer post to the channel
}
