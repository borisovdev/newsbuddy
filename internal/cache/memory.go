package cache

import (
	"container/list"
	"context"
	"sync"
	"time"
)

// DefaultCapacity is the entry bound used when NewMemory is given a
// non-positive capacity. Ample: one entry per source plus one for
// the source list.
const DefaultCapacity = 1024

type entry struct {
	key       string
	val       []byte
	expiresAt time.Time
	el        *list.Element
}

// Memory is the default Cache: a mutex-guarded map with container/list LRU
// recency, lazy expiry, and eviction on write. There is no background
// janitor goroutine, so there is nothing to shut down.
type Memory struct {
	mu       sync.Mutex
	items    map[string]*entry
	ll       *list.List
	capacity int
}

// NewMemory returns an empty in-memory cache bounded at capacity entries.
// A non-positive capacity falls back to DefaultCapacity.
func NewMemory(capacity int) *Memory {
	if capacity <= 0 {
		capacity = DefaultCapacity
	}
	return &Memory{
		items:    make(map[string]*entry),
		ll:       list.New(),
		capacity: capacity,
	}
}

// Get returns a copy of the stored value. An entry past its expiry is
// deleted and reported as a miss (lazy expiry).
func (m *Memory) Get(ctx context.Context, key string) ([]byte, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	e, ok := m.lookupLocked(key)
	if !ok {
		return nil, false, nil
	}
	val := make([]byte, len(e.val))
	copy(val, e.val)
	return val, true, nil
}

// Set stores a copy of value, so later mutations of the caller's slice
// cannot corrupt the entry. A ttl <= 0 means no expiry.
func (m *Memory) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	stored := make([]byte, len(value))
	copy(stored, value)

	var expiresAt time.Time
	if ttl > 0 {
		expiresAt = time.Now().Add(ttl)
	}

	if e, ok := m.items[key]; ok {
		e.val = stored
		e.expiresAt = expiresAt
		m.ll.MoveToFront(e.el)
	} else {
		e := &entry{key: key, val: stored, expiresAt: expiresAt}
		e.el = m.ll.PushFront(e)
		m.items[key] = e
	}

	for len(m.items) > m.capacity {
		back := m.ll.Back()
		if back == nil {
			break
		}
		m.removeLocked(back.Value.(*entry))
	}
	return nil
}

// Delete removes key. Deleting an absent key is not an error.
func (m *Memory) Delete(ctx context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if e, ok := m.items[key]; ok {
		m.removeLocked(e)
	}
	return nil
}

// Exists reports whether key is present and unexpired. Like Get, it
// refreshes recency.
func (m *Memory) Exists(ctx context.Context, key string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	_, ok := m.lookupLocked(key)
	return ok, nil
}

// Close clears the map. There is no goroutine to stop.
func (m *Memory) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.items = make(map[string]*entry)
	m.ll.Init()
	return nil
}

// lookupLocked returns a live entry and moves it to the front, dropping it
// if it has expired. The caller must hold m.mu.
func (m *Memory) lookupLocked(key string) (*entry, bool) {
	e, ok := m.items[key]
	if !ok {
		return nil, false
	}
	if !e.expiresAt.IsZero() && !time.Now().Before(e.expiresAt) {
		m.removeLocked(e)
		return nil, false
	}
	m.ll.MoveToFront(e.el)
	return e, true
}

// removeLocked drops e from both the map and the recency list. The caller
// must hold m.mu.
func (m *Memory) removeLocked(e *entry) {
	m.ll.Remove(e.el)
	delete(m.items, e.key)
}
