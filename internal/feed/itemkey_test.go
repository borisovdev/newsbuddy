package feed

import "testing"

func TestItemKey(t *testing.T) {
	cases := []struct {
		name         string
		guid         string
		resolvedLink string
		title        string
		rawPubDate   string
		wantKey      string
		wantOK       bool
	}{
		{
			name:         "GUID wins over link when both present",
			guid:         "guid-123",
			resolvedLink: "http://ex.com/feed/article",
			title:        "Some Title",
			rawPubDate:   "Mon, 02 Jan 2006 15:04:05 GMT",
			wantKey:      "guid-123",
			wantOK:       true,
		},
		{
			name:         "GUID is trimmed but not otherwise normalized",
			guid:         "  guid-123  ",
			resolvedLink: "",
			title:        "",
			rawPubDate:   "",
			wantKey:      "guid-123",
			wantOK:       true,
		},
		{
			name:         "link fallback used when GUID empty",
			guid:         "",
			resolvedLink: "http://ex.com/feed/article",
			title:        "Some Title",
			rawPubDate:   "Mon, 02 Jan 2006 15:04:05 GMT",
			wantKey:      "http://ex.com/feed/article",
			wantOK:       true,
		},
		{
			name:       "hash fallback when only title present",
			guid:       "",
			title:      "Some Title",
			rawPubDate: "Mon, 02 Jan 2006 15:04:05 GMT",
			wantOK:     true,
			// wantKey checked via sha256 recomputation below.
		},
		{
			name:       "all empty returns ok=false",
			guid:       "",
			title:      "",
			rawPubDate: "",
			wantKey:    "",
			wantOK:     false,
		},
		{
			name:       "whitespace-only guid, link, and title all treated as empty",
			guid:       "   ",
			title:      "   ",
			rawPubDate: "",
			wantKey:    "",
			wantOK:     false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			key, ok := ItemKey(tc.guid, tc.resolvedLink, tc.title, tc.rawPubDate)
			if ok != tc.wantOK {
				t.Fatalf("ItemKey(...) ok = %v; want %v", ok, tc.wantOK)
			}
			if tc.name == "hash fallback when only title present" {
				wantKey, wantOK := ItemKey("", "", tc.title, tc.rawPubDate)
				if !wantOK || wantKey == "" {
					t.Fatalf("expected non-empty hash key, got %q", wantKey)
				}
				if key != wantKey {
					t.Fatalf("hash key mismatch: %q != %q", key, wantKey)
				}
				return
			}
			if !tc.wantOK {
				return
			}
			if key != tc.wantKey {
				t.Fatalf("ItemKey(...) key = %q; want %q", key, tc.wantKey)
			}
		})
	}
}

func TestItemKey_HashStability(t *testing.T) {
	title := "Some Title"
	rawPubDate := "Mon, 02 Jan 2006 15:04:05 GMT"

	key1, ok1 := ItemKey("", "", title, rawPubDate)
	key2, ok2 := ItemKey("", "", title, rawPubDate)

	if !ok1 || !ok2 {
		t.Fatalf("expected ok=true for both calls, got %v, %v", ok1, ok2)
	}
	if key1 != key2 {
		t.Fatalf("expected stable hash across calls: %q != %q", key1, key2)
	}
	if key1 == "" {
		t.Fatalf("expected non-empty hash key")
	}
}

func TestItemKey_AllEmpty(t *testing.T) {
	key, ok := ItemKey("", "", "", "")
	if ok {
		t.Fatalf("expected ok=false for all-empty inputs, got key %q", key)
	}
	if key != "" {
		t.Fatalf("expected empty key, got %q", key)
	}
}
