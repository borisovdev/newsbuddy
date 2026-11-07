package cache

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/borisovdev/newsbuddy/internal/model"
)

// sourceListKey is the single cache key holding the source list. The v1
// segment lets a future value-format change invalidate wholesale instead of
// mis-decoding.
const sourceListKey = "nb:sources:v1"

// seenKeysKey returns the per-source seen-keys cache key.
func seenKeysKey(sourceID int64) string {
	return "nb:seen:v1:" + strconv.FormatInt(sourceID, 10)
}

// SeenKeys is the typed view over the per-source seen item-key set.
type SeenKeys struct {
	c     Cache
	ttl   time.Duration
	limit int
}

// NewSeenKeys returns the seen-keys view over c, with a per-entry ttl and a
// per-source key limit.
func NewSeenKeys(c Cache, ttl time.Duration, limit int) *SeenKeys {
	return &SeenKeys{c: c, ttl: ttl, limit: limit}
}

// Load returns the seen-key set for a source. It returns an empty set —
// never nil — on a miss or on ANY error; the error is returned for logging,
// not for control flow.
func (v *SeenKeys) Load(ctx context.Context, sourceID int64) (*KeySet, error) {
	s := NewKeySet(v.limit)

	raw, found, err := v.c.Get(ctx, seenKeysKey(sourceID))
	if err != nil || !found {
		return s, err
	}
	if err := json.Unmarshal(raw, s); err != nil {
		return NewKeySet(v.limit), err
	}
	return s, nil
}

// Store writes the set back under the source's key.
func (v *SeenKeys) Store(ctx context.Context, sourceID int64, s *KeySet) error {
	raw, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return v.c.Set(ctx, seenKeysKey(sourceID), raw, v.ttl)
}

// Invalidate drops the source's seen-key entry.
func (v *SeenKeys) Invalidate(ctx context.Context, sourceID int64) error {
	return v.c.Delete(ctx, seenKeysKey(sourceID))
}

// SourceList is the typed view over the cached source list.
type SourceList struct {
	c   Cache
	ttl time.Duration
}

// NewSourceList returns the source-list view over c with the given ttl.
func NewSourceList(c Cache, ttl time.Duration) *SourceList {
	return &SourceList{c: c, ttl: ttl}
}

// Load returns the cached source list. found is false on a miss, on any
// cache error, and on a decode failure — the caller then reads the DB.
func (v *SourceList) Load(ctx context.Context) ([]model.Source, bool, error) {
	raw, found, err := v.c.Get(ctx, sourceListKey)
	if err != nil || !found {
		return nil, false, err
	}
	var ss []model.Source
	if err := json.Unmarshal(raw, &ss); err != nil {
		return nil, false, err
	}
	return ss, true, nil
}

// Store writes the source list.
func (v *SourceList) Store(ctx context.Context, ss []model.Source) error {
	raw, err := json.Marshal(ss)
	if err != nil {
		return err
	}
	return v.c.Set(ctx, sourceListKey, raw, v.ttl)
}

// Invalidate drops the source-list entry.
func (v *SourceList) Invalidate(ctx context.Context) error {
	return v.c.Delete(ctx, sourceListKey)
}
