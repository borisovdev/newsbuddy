package cache

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/borisovdev/newsbuddy/internal/model"
)

func TestSeenKeysRoundTrip(t *testing.T) {
	ctx := context.Background()
	v := NewSeenKeys(NewMemory(8), time.Minute, 4)

	s, err := v.Load(ctx, 7)
	if err != nil {
		t.Fatalf("Load on miss: %v", err)
	}
	s.Add("k1", "k2")
	if err := v.Store(ctx, 7, s); err != nil {
		t.Fatalf("Store: %v", err)
	}

	got, err := v.Load(ctx, 7)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !got.Has("k1") || !got.Has("k2") {
		t.Errorf("loaded set lost keys: Len = %d", got.Len())
	}

	// A different source must not see source 7's keys.
	other, err := v.Load(ctx, 8)
	if err != nil {
		t.Fatalf("Load other source: %v", err)
	}
	if other.Len() != 0 {
		t.Errorf("source 8 Len = %d, want 0 (keys are per source)", other.Len())
	}
}

func TestSeenKeysLoadPreservesLimit(t *testing.T) {
	ctx := context.Background()
	v := NewSeenKeys(NewMemory(8), time.Minute, 2)

	s, _ := v.Load(ctx, 1)
	s.Add("a", "b", "c")
	if err := v.Store(ctx, 1, s); err != nil {
		t.Fatalf("Store: %v", err)
	}

	got, _ := v.Load(ctx, 1)
	got.Add("d")
	if got.Len() != 2 {
		t.Fatalf("Len = %d, want 2 (limit survives the round trip)", got.Len())
	}
	if !got.Has("d") || got.Has("b") {
		t.Error("want the newest keys retained after reload + Add")
	}
}

func TestSeenKeysLoadOnMissIsEmptyNotNil(t *testing.T) {
	ctx := context.Background()
	v := NewSeenKeys(NewMemory(8), time.Minute, 4)

	s, err := v.Load(ctx, 42)
	if err != nil {
		t.Fatalf("Load on miss returned err %v, want nil", err)
	}
	if s == nil {
		t.Fatal("Load on miss returned nil set, want an empty non-nil set")
	}
	if s.Len() != 0 || s.Has("anything") {
		t.Errorf("Load on miss = %d keys, want an empty set", s.Len())
	}
	s.Add("usable") // must not panic: the returned set is fully initialized
	if !s.Has("usable") {
		t.Error("set returned on miss is not usable")
	}
}

func TestSeenKeysLoadOnFailingCacheIsEmptyNotNil(t *testing.T) {
	ctx := context.Background()
	v := NewSeenKeys(FailingCache{}, time.Minute, 4)

	s, err := v.Load(ctx, 42)
	if !errors.Is(err, ErrFailing) {
		t.Fatalf("Load err = %v, want ErrFailing (returned for logging)", err)
	}
	if s == nil {
		t.Fatal("Load on cache error returned nil set, want an empty non-nil set")
	}
	if s.Len() != 0 {
		t.Errorf("Load on cache error = %d keys, want 0", s.Len())
	}
	s.Add("usable") // correct-if-empty: no panic, cache never blocks the DB path
	if !s.Has("usable") {
		t.Error("set returned on cache error is not usable")
	}
}

func TestSeenKeysLoadOnCorruptValue(t *testing.T) {
	ctx := context.Background()
	m := NewMemory(8)
	if err := m.Set(ctx, seenKeysKey(3), []byte(`{"not":"an array"}`), time.Minute); err != nil {
		t.Fatalf("Set: %v", err)
	}
	v := NewSeenKeys(m, time.Minute, 4)

	s, err := v.Load(ctx, 3)
	if err == nil {
		t.Fatal("Load of a corrupt value = nil error, want a decode error")
	}
	if s == nil || s.Len() != 0 {
		t.Fatal("Load of a corrupt value must still yield an empty non-nil set")
	}
}

func TestSeenKeysStoreAndInvalidateFailures(t *testing.T) {
	ctx := context.Background()
	v := NewSeenKeys(FailingCache{}, time.Minute, 4)

	if err := v.Store(ctx, 1, NewKeySet(4)); !errors.Is(err, ErrFailing) {
		t.Errorf("Store err = %v, want ErrFailing", err)
	}
	if err := v.Invalidate(ctx, 1); !errors.Is(err, ErrFailing) {
		t.Errorf("Invalidate err = %v, want ErrFailing", err)
	}
}

func TestSeenKeysInvalidate(t *testing.T) {
	ctx := context.Background()
	m := NewMemory(8)
	v := NewSeenKeys(m, time.Minute, 4)

	s := NewKeySet(4)
	s.Add("k")
	if err := v.Store(ctx, 5, s); err != nil {
		t.Fatalf("Store: %v", err)
	}
	if err := v.Invalidate(ctx, 5); err != nil {
		t.Fatalf("Invalidate: %v", err)
	}

	got, err := v.Load(ctx, 5)
	if err != nil {
		t.Fatalf("Load after Invalidate: %v", err)
	}
	if got.Len() != 0 {
		t.Errorf("Len after Invalidate = %d, want 0", got.Len())
	}
}

func TestSeenKeysTTLExpiry(t *testing.T) {
	ctx := context.Background()
	v := NewSeenKeys(NewMemory(8), time.Millisecond, 4)

	s := NewKeySet(4)
	s.Add("k")
	if err := v.Store(ctx, 1, s); err != nil {
		t.Fatalf("Store: %v", err)
	}
	time.Sleep(20 * time.Millisecond)

	got, err := v.Load(ctx, 1)
	if err != nil {
		t.Fatalf("Load after ttl: %v", err)
	}
	if got.Len() != 0 {
		t.Errorf("Len after ttl = %d, want 0 (entry expired)", got.Len())
	}
}

func TestSourceListRoundTrip(t *testing.T) {
	ctx := context.Background()
	v := NewSourceList(NewMemory(8), time.Minute)

	if _, found, err := v.Load(ctx); err != nil || found {
		t.Fatalf("Load on miss = (found %v, err %v), want (false, nil)", found, err)
	}

	polled := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	want := []model.Source{
		{ID: 1, FeedURL: "https://a.example/feed", Title: "A", LastPolledAt: &polled},
		{ID: 2, FeedURL: "https://b.example/feed", Title: "B"},
	}
	if err := v.Store(ctx, want); err != nil {
		t.Fatalf("Store: %v", err)
	}

	got, found, err := v.Load(ctx)
	if err != nil || !found {
		t.Fatalf("Load = (found %v, err %v), want (true, nil)", found, err)
	}
	if len(got) != 2 || got[0].ID != 1 || got[1].FeedURL != "https://b.example/feed" {
		t.Fatalf("Load = %+v, want %+v", got, want)
	}
	if got[0].LastPolledAt == nil || !got[0].LastPolledAt.Equal(polled) {
		t.Errorf("LastPolledAt = %v, want %v", got[0].LastPolledAt, polled)
	}
}

func TestSourceListInvalidate(t *testing.T) {
	ctx := context.Background()
	v := NewSourceList(NewMemory(8), time.Minute)

	if err := v.Store(ctx, []model.Source{{ID: 1}}); err != nil {
		t.Fatalf("Store: %v", err)
	}
	if err := v.Invalidate(ctx); err != nil {
		t.Fatalf("Invalidate: %v", err)
	}
	if _, found, err := v.Load(ctx); err != nil || found {
		t.Fatalf("Load after Invalidate = (found %v, err %v), want (false, nil)", found, err)
	}
}

func TestSourceListOnFailingCache(t *testing.T) {
	ctx := context.Background()
	v := NewSourceList(FailingCache{}, time.Minute)

	ss, found, err := v.Load(ctx)
	if !errors.Is(err, ErrFailing) {
		t.Fatalf("Load err = %v, want ErrFailing", err)
	}
	if found || ss != nil {
		t.Errorf("Load = (%v, found %v), want (nil, false) so the caller reads the DB", ss, found)
	}
	if err := v.Store(ctx, []model.Source{{ID: 1}}); !errors.Is(err, ErrFailing) {
		t.Errorf("Store err = %v, want ErrFailing", err)
	}
	if err := v.Invalidate(ctx); !errors.Is(err, ErrFailing) {
		t.Errorf("Invalidate err = %v, want ErrFailing", err)
	}
}

func TestViewKeyNames(t *testing.T) {
	if got := seenKeysKey(12); got != "nb:seen:v1:12" {
		t.Errorf("seenKeysKey(12) = %q, want %q", got, "nb:seen:v1:12")
	}
	if sourceListKey != "nb:sources:v1" {
		t.Errorf("sourceListKey = %q, want %q", sourceListKey, "nb:sources:v1")
	}
}
