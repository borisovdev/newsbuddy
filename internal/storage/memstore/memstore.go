// Package memstore is an in-memory storage.Storage implementation used for
// tests and local development. Sources and seen-items are held in slices,
// not a Go map keyed by strings, since this package sits outside
// internal/cache and its boundary check applies here too. Linear scans are
// fine at test data sizes.
package memstore

import (
	"context"
	"sync"
	"time"

	"github.com/borisovdev/newsbuddy/internal/model"
	"github.com/borisovdev/newsbuddy/internal/storage"
)

// seenItem is one row of the seen_items table.
type seenItem struct {
	sourceID    int64
	key         string
	seeded      bool
	postedAt    *time.Time
	messageID   int64
	abandonedAt *time.Time
}

// Store is an in-memory storage.Storage implementation guarded by a single
// RWMutex. Sources and seen items are stored in slices, never maps.
type Store struct {
	mu      sync.RWMutex
	sources []model.Source
	seen    []seenItem
	nextID  int64
}

// New returns an empty, ready-to-use Store.
func New() *Store {
	return &Store{nextID: 1}
}

func timePtr(t time.Time) *time.Time {
	return &t
}

func (s *Store) findSourceIndexLocked(id int64) int {
	for i := range s.sources {
		if s.sources[i].ID == id {
			return i
		}
	}
	return -1
}

func (s *Store) findSourceByURLIndexLocked(url string) int {
	for i := range s.sources {
		if s.sources[i].FeedURL == url {
			return i
		}
	}
	return -1
}

func (s *Store) AddSource(ctx context.Context, src model.Source, seedKeys []string) (model.Source, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.findSourceByURLIndexLocked(src.FeedURL) != -1 {
		return model.Source{}, storage.ErrDuplicate
	}

	src.ID = s.nextID
	s.nextID++
	s.sources = append(s.sources, src)

	for _, key := range seedKeys {
		s.seen = append(s.seen, seenItem{
			sourceID: src.ID,
			key:      key,
			seeded:   true,
		})
	}

	return src, nil
}

func (s *Store) GetSourceByID(ctx context.Context, id int64) (model.Source, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	idx := s.findSourceIndexLocked(id)
	if idx == -1 {
		return model.Source{}, storage.ErrNotFound
	}
	return s.sources[idx], nil
}

func (s *Store) GetSourceByURL(ctx context.Context, normalizedURL string) (model.Source, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	idx := s.findSourceByURLIndexLocked(normalizedURL)
	if idx == -1 {
		return model.Source{}, storage.ErrNotFound
	}
	return s.sources[idx], nil
}

func (s *Store) ListSources(ctx context.Context) ([]model.Source, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]model.Source, len(s.sources))
	copy(out, s.sources)
	// Insertion order already tracks id ASC because ids are assigned
	// monotonically and sources are appended in AddSource order, but the
	// interface promises id ASC explicitly, so sort defensively.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1].ID > out[j].ID; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out, nil
}

func (s *Store) DeleteSource(ctx context.Context, id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	idx := s.findSourceIndexLocked(id)
	if idx == -1 {
		return storage.ErrNotFound
	}
	s.sources = append(s.sources[:idx], s.sources[idx+1:]...)

	kept := s.seen[:0]
	for _, item := range s.seen {
		if item.sourceID != id {
			kept = append(kept, item)
		}
	}
	s.seen = kept

	return nil
}

func (s *Store) UpdateSourcePollResult(ctx context.Context, r model.PollResult) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	idx := s.findSourceIndexLocked(r.SourceID)
	if idx == -1 {
		return storage.ErrNotFound
	}

	src := &s.sources[idx]
	src.LastPolledAt = timePtr(r.PolledAt)

	if r.Success {
		src.LastSuccessAt = timePtr(r.PolledAt)
		src.ConsecutiveFailures = 0
		src.LastError = ""
		if r.StoreValidators {
			src.ETag = r.ETag
			src.LastModified = r.LastModified
		} else {
			src.ETag = ""
			src.LastModified = ""
		}
	} else {
		src.ConsecutiveFailures++
		src.LastError = r.ErrMessage
		// Validators are left untouched on failure regardless of
		// StoreValidators.
	}

	return nil
}

func (s *Store) FilterUnseen(ctx context.Context, sourceID int64, keys []string) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]string, 0, len(keys))
	for _, key := range keys {
		seen := false
		for _, item := range s.seen {
			if item.sourceID == sourceID && item.key == key {
				seen = true
				break
			}
		}
		if !seen {
			out = append(out, key)
		}
	}
	return out, nil
}

func (s *Store) InsertPending(ctx context.Context, sourceID int64, key string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.findSourceIndexLocked(sourceID) == -1 {
		return false, storage.ErrSourceGone
	}

	for _, item := range s.seen {
		if item.sourceID == sourceID && item.key == key {
			return false, nil
		}
	}

	s.seen = append(s.seen, seenItem{
		sourceID: sourceID,
		key:      key,
		seeded:   false,
	})
	return true, nil
}

func (s *Store) MarkPosted(ctx context.Context, sourceID int64, key string, messageID int64, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i := range s.seen {
		item := &s.seen[i]
		if item.sourceID == sourceID && item.key == key {
			item.postedAt = timePtr(at)
			item.messageID = messageID
			return nil
		}
	}
	return storage.ErrNotFound
}

func (s *Store) AbandonPending(ctx context.Context, at time.Time) ([]model.AbandonedItem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var out []model.AbandonedItem
	for i := range s.seen {
		item := &s.seen[i]
		if item.postedAt == nil && !item.seeded && item.abandonedAt == nil {
			item.abandonedAt = timePtr(at)
			out = append(out, model.AbandonedItem{
				SourceID: item.sourceID,
				ItemKey:  item.key,
			})
		}
	}
	return out, nil
}

func (s *Store) CountPostedSince(ctx context.Context, since time.Time) (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	count := 0
	for _, item := range s.seen {
		if item.postedAt != nil && !item.postedAt.Before(since) {
			count++
		}
	}
	return count, nil
}

func (s *Store) Ping(ctx context.Context) error {
	return nil
}

func (s *Store) Close() error {
	return nil
}
