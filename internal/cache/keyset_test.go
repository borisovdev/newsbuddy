package cache

import (
	"encoding/json"
	"strconv"
	"testing"
)

func TestKeySetAddAndHas(t *testing.T) {
	s := NewKeySet(10)

	if s.Len() != 0 {
		t.Fatalf("Len on new set = %d, want 0", s.Len())
	}
	if s.Has("a") {
		t.Fatal("Has on empty set = true, want false")
	}

	s.Add("a", "b")
	s.Add("c")
	if s.Len() != 3 {
		t.Fatalf("Len = %d, want 3", s.Len())
	}
	for _, k := range []string{"a", "b", "c"} {
		if !s.Has(k) {
			t.Errorf("Has(%q) = false, want true", k)
		}
	}

	s.Add("a") // duplicates are no-ops
	if s.Len() != 3 {
		t.Errorf("Len after re-adding an existing key = %d, want 3", s.Len())
	}

	s.Add() // zero keys must not panic
	if s.Len() != 3 {
		t.Errorf("Len after Add() with no keys = %d, want 3", s.Len())
	}
}

func TestKeySetFrontEvictionAtLimit(t *testing.T) {
	s := NewKeySet(3)
	s.Add("a", "b", "c", "d", "e")

	if s.Len() != 3 {
		t.Fatalf("Len = %d, want 3 (limit)", s.Len())
	}
	for _, k := range []string{"a", "b"} {
		if s.Has(k) {
			t.Errorf("Has(%q) = true, want false (oldest keys evict from the front)", k)
		}
	}
	for _, k := range []string{"c", "d", "e"} {
		if !s.Has(k) {
			t.Errorf("Has(%q) = false, want true (newest keys retained)", k)
		}
	}
}

func TestKeySetLimitHoldsAcrossManyAdds(t *testing.T) {
	s := NewKeySet(50)
	for i := 0; i < 500; i++ {
		s.Add("k" + strconv.Itoa(i))
	}
	if s.Len() != 50 {
		t.Fatalf("Len = %d, want 50", s.Len())
	}
	if s.Has("k449") {
		t.Error("Has(k449) = true, want false (evicted)")
	}
	if !s.Has("k499") {
		t.Error("Has(k499) = false, want true (newest)")
	}
}

func TestKeySetDefaultLimit(t *testing.T) {
	if got := NewKeySet(0).limit; got != DefaultKeySetLimit {
		t.Errorf("NewKeySet(0).limit = %d, want %d", got, DefaultKeySetLimit)
	}
	if got := NewKeySet(-5).limit; got != DefaultKeySetLimit {
		t.Errorf("NewKeySet(-5).limit = %d, want %d", got, DefaultKeySetLimit)
	}
}

func TestKeySetJSONRoundTrip(t *testing.T) {
	s := NewKeySet(4)
	s.Add("a", "b", "c")

	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if string(raw) != `["a","b","c"]` {
		t.Fatalf("Marshal = %s, want the order slice only", raw)
	}

	got := NewKeySet(4)
	if err := json.Unmarshal(raw, got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.Len() != 3 {
		t.Fatalf("Len after round trip = %d, want 3", got.Len())
	}
	for _, k := range []string{"a", "b", "c"} {
		if !got.Has(k) {
			t.Errorf("Has(%q) after round trip = false, want true (index rebuilt)", k)
		}
	}
	if got.Has("zzz") {
		t.Error("Has(zzz) after round trip = true, want false")
	}

	// The decoded set is still live and still bounded.
	got.Add("d", "e")
	if got.Len() != 4 {
		t.Fatalf("Len after Add past limit = %d, want 4", got.Len())
	}
	if got.Has("a") {
		t.Error("Has(a) = true, want false (evicted after decode + Add)")
	}
}

func TestKeySetMarshalEmpty(t *testing.T) {
	raw, err := json.Marshal(NewKeySet(4))
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if string(raw) != "[]" {
		t.Fatalf("Marshal of empty set = %s, want []", raw)
	}
}

func TestKeySetUnmarshalTrimsAndDedupes(t *testing.T) {
	s := NewKeySet(2)
	if err := json.Unmarshal([]byte(`["a","b","b","c"]`), s); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if s.Len() != 2 {
		t.Fatalf("Len = %d, want 2 (limit re-applied on decode)", s.Len())
	}
	if !s.Has("b") || !s.Has("c") {
		t.Error("want the two newest distinct keys retained")
	}
	if s.Has("a") {
		t.Error("Has(a) = true, want false (trimmed to limit)")
	}
}

func TestKeySetUnmarshalBadJSON(t *testing.T) {
	s := NewKeySet(4)
	if err := json.Unmarshal([]byte(`{"nope":1}`), s); err == nil {
		t.Fatal("Unmarshal of a JSON object = nil error, want an error")
	}
}
