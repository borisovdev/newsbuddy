// Package storage defines the single persistence interface used by the
// rest of the service. No SQL package, no query builder, and no driver
// types leak past this package boundary.
package storage

import (
	"context"
	"errors"
	"time"

	"github.com/borisovdev/newsbuddy/internal/model"
)

var (
	ErrNotFound   = errors.New("storage: not found")
	ErrDuplicate  = errors.New("storage: feed_url already exists")
	ErrSourceGone = errors.New("storage: source no longer exists")
)

type Storage interface {
	// ---- sources ----

	// AddSource inserts the source and seeds seenKeys in ONE transaction
	//. Returns ErrDuplicate if feed_url already exists.
	// seedKeys may be empty. Rows are written with
	// seeded=1, posted_at=NULL.
	AddSource(ctx context.Context, s model.Source, seedKeys []string) (model.Source, error)

	GetSourceByID(ctx context.Context, id int64) (model.Source, error)              // ErrNotFound
	GetSourceByURL(ctx context.Context, normalizedURL string) (model.Source, error) // ErrNotFound
	ListSources(ctx context.Context) ([]model.Source, error)                        // ordered by id ASC
	DeleteSource(ctx context.Context, id int64) error                               // ErrNotFound; cascades

	// UpdateSourcePollResult writes last_polled_at plus, on success,
	// last_success_at / consecutive_failures=0 / last_error='' /
	// validators; on failure, last_error and consecutive_failures+1.
	UpdateSourcePollResult(ctx context.Context, r model.PollResult) error

	// ---- seen items ----

	// FilterUnseen returns the subset of keys with NO row for sourceID,
	// preserving input order. Chunks internally.
	FilterUnseen(ctx context.Context, sourceID int64, keys []string) ([]string, error)

	// InsertPending inserts (sourceID, key) with posted_at=NULL, seeded=0.
	// Returns inserted=false when the row already existed (ON CONFLICT DO
	// NOTHING). Returns ErrSourceGone if the source was deleted mid-cycle
	// (FK violation). This is THE dedup gate.
	InsertPending(ctx context.Context, sourceID int64, key string) (inserted bool, err error)

	MarkPosted(ctx context.Context, sourceID int64, key string, messageID int64, at time.Time) error

	// AbandonPending stamps abandoned_at on every row with
	// posted_at IS NULL AND seeded=0 AND abandoned_at IS NULL, and returns
	// what it stamped so the caller can log each once. Startup only.
	AbandonPending(ctx context.Context, at time.Time) ([]model.AbandonedItem, error)

	CountPostedSince(ctx context.Context, since time.Time) (int, error)

	// ---- lifecycle ----
	Ping(ctx context.Context) error
	Close() error
}
