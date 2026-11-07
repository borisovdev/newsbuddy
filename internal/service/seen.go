package service

import (
	"context"
	"log/slog"
	"time"

	"github.com/borisovdev/newsbuddy/internal/cache"
	"github.com/borisovdev/newsbuddy/internal/storage"
)

// SeenIndex is the poller's single collaborator for dedup state. The cache
// accelerates the read-side filter only; the database is always the
// authority.
type SeenIndex struct {
	st   storage.Storage
	seen *cache.SeenKeys
	log  *slog.Logger
}

// NewSeenIndex returns a SeenIndex over the given storage and cache view.
func NewSeenIndex(st storage.Storage, seen *cache.SeenKeys, log *slog.Logger) *SeenIndex {
	if log == nil {
		log = slog.Default()
	}
	return &SeenIndex{st: st, seen: seen, log: log}
}

// Unseen returns the subset of keys with no seen-item row for sourceID,
// for the poller's unseen filter. The cached key set is a ONE-WAY filter: a hit
// lets us skip a key, a miss or an outright cache failure only means more
// DB work — never a different answer.
func (idx *SeenIndex) Unseen(ctx context.Context, sourceID int64, keys []string) ([]string, error) {
	set, err := idx.seen.Load(ctx, sourceID)
	if err != nil {
		cacheWarn(ctx, idx.log, "seen.load", err)
	}

	maybeNew := make([]string, 0, len(keys))
	for _, k := range keys {
		if !set.Has(k) {
			maybeNew = append(maybeNew, k)
		}
	}

	unseen, err := idx.st.FilterUnseen(ctx, sourceID, maybeNew)
	if err != nil {
		return nil, err
	}

	// The set learns every key the DB already knew about.
	stillUnseen := cache.NewKeySet(len(unseen))
	stillUnseen.Add(unseen...)
	learned := make([]string, 0, len(maybeNew))
	for _, k := range maybeNew {
		if !stillUnseen.Has(k) {
			learned = append(learned, k)
		}
	}
	set.Add(learned...)

	if err := idx.seen.Store(ctx, sourceID, set); err != nil {
		cacheWarn(ctx, idx.log, "seen.store", err)
	}

	return unseen, nil
}

// InsertPending writes the dedup row that gates every post.
// It deliberately does not touch the cache: the cache is never the dedup
// authority.
func (idx *SeenIndex) InsertPending(ctx context.Context, sourceID int64, key string) (bool, error) {
	return idx.st.InsertPending(ctx, sourceID, key)
}

// MarkPosted stamps a pending row as posted. Cache-free for the same reason
// as InsertPending.
func (idx *SeenIndex) MarkPosted(ctx context.Context, sourceID int64, key string, messageID int64, at time.Time) error {
	return idx.st.MarkPosted(ctx, sourceID, key, messageID, at)
}
