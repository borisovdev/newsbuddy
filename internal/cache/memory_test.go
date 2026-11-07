package cache

import (
	"context"
	"testing"
	"time"
)

func TestMemoryGetSetDeleteExists(t *testing.T) {
	ctx := context.Background()
	m := NewMemory(4)

	if _, found, err := m.Get(ctx, "missing"); err != nil || found {
		t.Fatalf("Get on empty cache = (found %v, err %v), want (false, nil)", found, err)
	}
	if err := m.Set(ctx, "a", []byte("alpha"), time.Minute); err != nil {
		t.Fatalf("Set: %v", err)
	}

	val, found, err := m.Get(ctx, "a")
	if err != nil || !found || string(val) != "alpha" {
		t.Fatalf("Get = (%q, %v, %v), want (\"alpha\", true, nil)", val, found, err)
	}
	if ok, err := m.Exists(ctx, "a"); err != nil || !ok {
		t.Fatalf("Exists = (%v, %v), want (true, nil)", ok, err)
	}

	if err := m.Delete(ctx, "a"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if ok, _ := m.Exists(ctx, "a"); ok {
		t.Fatal("Exists after Delete = true, want false")
	}
	if err := m.Delete(ctx, "a"); err != nil {
		t.Fatalf("Delete of absent key: %v", err)
	}
}

func TestMemoryLRUEvictionOrder(t *testing.T) {
	ctx := context.Background()
	m := NewMemory(3)

	for _, k := range []string{"a", "b", "c"} {
		if err := m.Set(ctx, k, []byte(k), time.Minute); err != nil {
			t.Fatalf("Set %q: %v", k, err)
		}
	}
	// Touch "a" so "b" becomes the least recently used.
	if _, found, _ := m.Get(ctx, "a"); !found {
		t.Fatal("Get a = miss, want hit")
	}
	if err := m.Set(ctx, "d", []byte("d"), time.Minute); err != nil {
		t.Fatalf("Set d: %v", err)
	}

	for _, k := range []string{"a", "c", "d"} {
		if ok, _ := m.Exists(ctx, k); !ok {
			t.Errorf("key %q evicted, want retained", k)
		}
	}
	if ok, _ := m.Exists(ctx, "b"); ok {
		t.Error("key \"b\" retained, want evicted as least recently used")
	}
}

func TestMemorySetReplaceDoesNotGrow(t *testing.T) {
	ctx := context.Background()
	m := NewMemory(2)

	for i := 0; i < 5; i++ {
		if err := m.Set(ctx, "a", []byte("v"), time.Minute); err != nil {
			t.Fatalf("Set: %v", err)
		}
	}
	if err := m.Set(ctx, "b", []byte("v"), time.Minute); err != nil {
		t.Fatalf("Set: %v", err)
	}
	for _, k := range []string{"a", "b"} {
		if ok, _ := m.Exists(ctx, k); !ok {
			t.Errorf("key %q evicted after repeated replace, want retained", k)
		}
	}
}

func TestMemoryTTLExpiry(t *testing.T) {
	ctx := context.Background()
	m := NewMemory(4)

	if err := m.Set(ctx, "short", []byte("v"), time.Millisecond); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := m.Set(ctx, "forever", []byte("v"), 0); err != nil {
		t.Fatalf("Set: %v", err)
	}
	time.Sleep(20 * time.Millisecond)

	if _, found, err := m.Get(ctx, "short"); err != nil || found {
		t.Fatalf("Get expired = (found %v, err %v), want (false, nil)", found, err)
	}
	if ok, _ := m.Exists(ctx, "short"); ok {
		t.Error("Exists on expired key = true, want false")
	}
	if ok, _ := m.Exists(ctx, "forever"); !ok {
		t.Error("Exists on ttl=0 key = false, want true (no expiry)")
	}
}

func TestMemoryCopyInCopyOut(t *testing.T) {
	ctx := context.Background()
	m := NewMemory(4)

	caller := []byte("original")
	if err := m.Set(ctx, "k", caller, time.Minute); err != nil {
		t.Fatalf("Set: %v", err)
	}
	caller[0] = 'X' // mutating the caller's slice must not touch the entry

	got, found, err := m.Get(ctx, "k")
	if err != nil || !found {
		t.Fatalf("Get = (found %v, err %v), want (true, nil)", found, err)
	}
	if string(got) != "original" {
		t.Errorf("after caller mutation, stored value = %q, want %q", got, "original")
	}

	got[0] = 'Y' // mutating the returned slice must not touch the entry either
	again, _, _ := m.Get(ctx, "k")
	if string(again) != "original" {
		t.Errorf("after returned-slice mutation, stored value = %q, want %q", again, "original")
	}
}

func TestMemoryClose(t *testing.T) {
	ctx := context.Background()
	m := NewMemory(4)

	if err := m.Set(ctx, "k", []byte("v"), time.Minute); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := m.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if ok, _ := m.Exists(ctx, "k"); ok {
		t.Error("Exists after Close = true, want false")
	}
	// The cache stays usable after Close; there is no goroutine to restart.
	if err := m.Set(ctx, "k2", []byte("v"), time.Minute); err != nil {
		t.Fatalf("Set after Close: %v", err)
	}
}

func TestMemoryDefaultCapacity(t *testing.T) {
	if got := NewMemory(0).capacity; got != DefaultCapacity {
		t.Errorf("NewMemory(0).capacity = %d, want %d", got, DefaultCapacity)
	}
	if got := NewMemory(-1).capacity; got != DefaultCapacity {
		t.Errorf("NewMemory(-1).capacity = %d, want %d", got, DefaultCapacity)
	}
}

func TestMemoryConcurrentAccess(t *testing.T) {
	ctx := context.Background()
	m := NewMemory(16)
	done := make(chan struct{})

	for i := 0; i < 8; i++ {
		go func(i int) {
			defer func() { done <- struct{}{} }()
			for j := 0; j < 200; j++ {
				key := string(rune('a' + (i+j)%16))
				_ = m.Set(ctx, key, []byte(key), time.Minute)
				_, _, _ = m.Get(ctx, key)
				_, _ = m.Exists(ctx, key)
				_ = m.Delete(ctx, key)
			}
		}(i)
	}
	for i := 0; i < 8; i++ {
		<-done
	}
}

// Memory and FailingCache must satisfy Cache.
var (
	_ Cache = (*Memory)(nil)
	_ Cache = FailingCache{}
)
