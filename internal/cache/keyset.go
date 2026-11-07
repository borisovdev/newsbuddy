package cache

import "encoding/json"

// DefaultKeySetLimit is the per-source seen-key bound.
const DefaultKeySetLimit = 5000

// KeySet is a bounded, insertion-ordered string set. It is the value type
// stored under a seen-keys cache entry. The map is an implementation detail
// that never escapes package cache.
type KeySet struct {
	order []string // oldest → newest
	index map[string]struct{}
	limit int
}

// NewKeySet returns an empty set bounded at limit keys. A non-positive
// limit falls back to DefaultKeySetLimit.
func NewKeySet(limit int) *KeySet {
	if limit <= 0 {
		limit = DefaultKeySetLimit
	}
	return &KeySet{index: make(map[string]struct{}), limit: limit}
}

// Has reports whether key is in the set.
func (s *KeySet) Has(key string) bool {
	_, ok := s.index[key]
	return ok
}

// Add appends keys that are not already present, evicting the oldest
// entries from the front once the set exceeds its limit.
func (s *KeySet) Add(keys ...string) {
	for _, k := range keys {
		if _, ok := s.index[k]; ok {
			continue
		}
		s.index[k] = struct{}{}
		s.order = append(s.order, k)
	}
	s.trim()
}

// Len returns the number of keys held.
func (s *KeySet) Len() int { return len(s.order) }

// MarshalJSON encodes the ordered keys only; the index is rebuilt on decode.
func (s *KeySet) MarshalJSON() ([]byte, error) {
	if s.order == nil {
		return []byte("[]"), nil
	}
	return json.Marshal(s.order)
}

// UnmarshalJSON decodes the ordered keys and rebuilds the index. The limit
// of the receiver is preserved and re-applied to the decoded contents.
func (s *KeySet) UnmarshalJSON(b []byte) error {
	var order []string
	if err := json.Unmarshal(b, &order); err != nil {
		return err
	}
	if s.limit <= 0 {
		s.limit = DefaultKeySetLimit
	}
	s.order = s.order[:0]
	s.index = make(map[string]struct{}, len(order))
	for _, k := range order {
		if _, ok := s.index[k]; ok {
			continue
		}
		s.index[k] = struct{}{}
		s.order = append(s.order, k)
	}
	s.trim()
	return nil
}

// trim evicts from the front until the set fits its limit.
func (s *KeySet) trim() {
	if len(s.order) <= s.limit {
		return
	}
	drop := len(s.order) - s.limit
	for _, k := range s.order[:drop] {
		delete(s.index, k)
	}
	s.order = append(s.order[:0], s.order[drop:]...)
}
