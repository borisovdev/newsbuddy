package feed

import "testing"

func TestNormalizeURL(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{
			name: "trailing slash collides with no trailing slash",
			in:   "http://Ex.com/feed/",
			want: "http://ex.com/feed",
		},
		{
			name: "no trailing slash, lowercased",
			in:   "http://ex.com/feed",
			want: "http://ex.com/feed",
		},
		{
			name: "root path trailing slash becomes empty path",
			in:   "http://ex.com/",
			want: "http://ex.com",
		},
		{
			name: "query strings with different values do not collide (a=1)",
			in:   "http://ex.com/feed?a=1",
			want: "http://ex.com/feed?a=1",
		},
		{
			name: "query strings with different values do not collide (a=2)",
			in:   "http://ex.com/feed?a=2",
			want: "http://ex.com/feed?a=2",
		},
		{
			name: "fragment is dropped",
			in:   "http://ex.com/feed#section",
			want: "http://ex.com/feed",
		},
		{
			name: "credentials are stripped",
			in:   "http://user:pass@ex.com/feed",
			want: "http://ex.com/feed",
		},
		{
			name: "whitespace is trimmed",
			in:   "  http://ex.com/feed  ",
			want: "http://ex.com/feed",
		},
		{
			name: "host and port lowercased",
			in:   "HTTP://EX.COM:8080/Feed",
			want: "http://ex.com:8080/Feed",
		},
		{
			name:    "file scheme rejected",
			in:      "file:///etc/passwd",
			wantErr: true,
		},
		{
			name:    "ftp scheme rejected",
			in:      "ftp://ex.com/feed",
			wantErr: true,
		},
		{
			name:    "scheme-less URL rejected",
			in:      "ex.com/feed",
			wantErr: true,
		},
		{
			name:    "empty host rejected",
			in:      "http:///feed",
			wantErr: true,
		},
		{
			name:    "parse error rejected",
			in:      "http://[::1",
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NormalizeURL(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("NormalizeURL(%q) = %q, nil; want error", tc.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("NormalizeURL(%q) returned unexpected error: %v", tc.in, err)
			}
			if got != tc.want {
				t.Fatalf("NormalizeURL(%q) = %q; want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestNormalizeURL_TrailingSlashCollision(t *testing.T) {
	a, err := NormalizeURL("Ex.com/feed/")
	if err == nil {
		t.Fatalf("expected scheme-less URL to error, got %q", a)
	}

	a, err = NormalizeURL("http://Ex.com/feed/")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	b, err := NormalizeURL("http://ex.com/feed")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if a != b {
		t.Fatalf("expected collision: %q != %q", a, b)
	}
}

func TestNormalizeURL_QueryDoesNotCollide(t *testing.T) {
	a, err := NormalizeURL("http://ex.com/feed?a=1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	b, err := NormalizeURL("http://ex.com/feed?a=2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if a == b {
		t.Fatalf("expected no collision, both normalized to %q", a)
	}
}
