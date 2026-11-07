package feed

import (
	"compress/gzip"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/borisovdev/newsbuddy/internal/config"
	"github.com/borisovdev/newsbuddy/internal/model"
)

// TestMain relaxes the SSRF dial guard's loopback rule for this package:
// every server below is an httptest.Server on 127.0.0.1, which the guard
// refuses by default. TestFetchRefusesNonPublicAddress flips it back off to
// assert the guard itself still fires.
func TestMain(m *testing.M) {
	allowLoopbackDial = true
	os.Exit(m.Run())
}

func testConfig() config.Config {
	cfg := config.Defaults()
	cfg.HTTPTimeout = 5 * time.Second
	return cfg
}

func newTestFetcher(cfg config.Config, p Parser) *HTTPFetcher {
	if p == nil {
		p = NewGofeedParser()
	}
	return NewHTTPFetcher(cfg, p)
}

// countingParser records how many times Parse was called, so the 304
// short-circuit is assertable.
type countingParser struct{ calls atomic.Int32 }

func (c *countingParser) Parse(_ []byte, _ string) (*model.Feed, error) {
	c.calls.Add(1)
	return &model.Feed{}, nil
}

func asFetchError(t *testing.T, err error) *FetchError {
	t.Helper()
	var fe *FetchError
	if !errors.As(err, &fe) {
		t.Fatalf("err = %T (%v), want *FetchError", err, err)
	}
	return fe
}

func TestFetchConditionalGET(t *testing.T) {
	var gotINM, gotIMS, gotUA, gotAccept, gotAcceptEnc string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotINM = r.Header.Get("If-None-Match")
		gotIMS = r.Header.Get("If-Modified-Since")
		gotUA = r.Header.Get("User-Agent")
		gotAccept = r.Header.Get("Accept")
		gotAcceptEnc = r.Header.Get("Accept-Encoding")
		w.Header().Set("Content-Type", "application/rss+xml")
		w.Header().Set("ETag", `"v2"`)
		w.Header().Set("Last-Modified", "Wed, 03 Jan 2024 00:00:00 GMT")
		_, _ = w.Write(fixture(t, "rss2.xml"))
	}))
	defer srv.Close()

	f := newTestFetcher(testConfig(), nil)
	res, err := f.Fetch(context.Background(), FetchRequest{
		URL:          srv.URL,
		ETag:         `"v1"`,
		LastModified: "Tue, 02 Jan 2024 00:00:00 GMT",
	})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	if gotINM != `"v1"` {
		t.Errorf("If-None-Match = %q", gotINM)
	}
	if gotIMS != "Tue, 02 Jan 2024 00:00:00 GMT" {
		t.Errorf("If-Modified-Since = %q", gotIMS)
	}
	if !strings.HasPrefix(gotUA, "news-buddy/") {
		t.Errorf("User-Agent = %q", gotUA)
	}
	if !strings.Contains(gotAccept, "application/rss+xml") {
		t.Errorf("Accept = %q", gotAccept)
	}
	// We never set Accept-Encoding ourselves, so the transport adds gzip and
	// keeps decompression transparent.
	if !strings.Contains(gotAcceptEnc, "gzip") {
		t.Errorf("Accept-Encoding = %q, want the transport's transparent gzip", gotAcceptEnc)
	}

	if res.StatusCode != 200 || res.NotModified {
		t.Errorf("StatusCode = %d, NotModified = %v", res.StatusCode, res.NotModified)
	}
	if res.ETag != `"v2"` {
		t.Errorf("ETag = %q", res.ETag)
	}
	if res.LastModified != "Wed, 03 Jan 2024 00:00:00 GMT" {
		t.Errorf("LastModified = %q", res.LastModified)
	}
	if res.FinalURL != srv.URL {
		t.Errorf("FinalURL = %q, want %q", res.FinalURL, srv.URL)
	}
	if res.Feed == nil || len(res.Feed.Items) != 11 {
		t.Fatalf("Feed = %+v, want 11 items", res.Feed)
	}
}

func TestFetchNoValidatorsSendsNoConditionalHeaders(t *testing.T) {
	var hasINM, hasIMS bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, hasINM = r.Header["If-None-Match"]
		_, hasIMS = r.Header["If-Modified-Since"]
		_, _ = w.Write(fixture(t, "rss2.xml"))
	}))
	defer srv.Close()

	if _, err := newTestFetcher(testConfig(), nil).Fetch(context.Background(), FetchRequest{URL: srv.URL}); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if hasINM || hasIMS {
		t.Errorf("conditional headers sent with empty validators: INM=%v IMS=%v", hasINM, hasIMS)
	}
}

func TestFetch304NeverParsesBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotModified)
	}))
	defer srv.Close()

	parser := &countingParser{}
	res, err := newTestFetcher(testConfig(), parser).Fetch(context.Background(), FetchRequest{
		URL:          srv.URL,
		ETag:         `"v1"`,
		LastModified: "Tue, 02 Jan 2024 00:00:00 GMT",
	})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if !res.NotModified || res.StatusCode != http.StatusNotModified {
		t.Errorf("NotModified = %v, StatusCode = %d", res.NotModified, res.StatusCode)
	}
	if res.Feed != nil {
		t.Errorf("Feed = %+v, want nil on 304", res.Feed)
	}
	// The stored validators are echoed back so a 304 cannot wipe them.
	if res.ETag != `"v1"` || res.LastModified != "Tue, 02 Jan 2024 00:00:00 GMT" {
		t.Errorf("validators not echoed: etag=%q lm=%q", res.ETag, res.LastModified)
	}
	if n := parser.calls.Load(); n != 0 {
		t.Errorf("parser called %d times on a 304, want 0", n)
	}
}

func TestFetchStatusMapping(t *testing.T) {
	cases := []struct {
		status        int
		wantTemporary bool
	}{
		{http.StatusForbidden, false},
		{http.StatusNotFound, false},
		{http.StatusGone, false},
		{http.StatusRequestTimeout, true},
		{http.StatusTooEarly, true},
		{http.StatusTooManyRequests, false},
		{http.StatusInternalServerError, true},
		{http.StatusBadGateway, true},
		{http.StatusServiceUnavailable, true},
	}
	for _, tc := range cases {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
			}))
			defer srv.Close()

			_, err := newTestFetcher(testConfig(), nil).Fetch(context.Background(), FetchRequest{URL: srv.URL})
			fe := asFetchError(t, err)
			if fe.Op != "status" {
				t.Errorf("Op = %q, want status", fe.Op)
			}
			if fe.StatusCode != tc.status {
				t.Errorf("StatusCode = %d, want %d", fe.StatusCode, tc.status)
			}
			if fe.Temporary() != tc.wantTemporary {
				t.Errorf("Temporary() = %v, want %v", fe.Temporary(), tc.wantTemporary)
			}
			if !strings.Contains(fe.Error(), http.StatusText(tc.status)) &&
				!strings.Contains(fe.Error(), "http status") {
				t.Errorf("Error() = %q", fe.Error())
			}
		})
	}
}

func TestFetchRetryAfterForms(t *testing.T) {
	cases := []struct {
		name     string
		header   func() string
		wantLow  time.Duration
		wantHigh time.Duration
	}{
		{"delta-seconds", func() string { return "120" }, 120 * time.Second, 120 * time.Second},
		{"http-date", func() string {
			return time.Now().Add(90 * time.Second).UTC().Format(http.TimeFormat)
		}, 60 * time.Second, 95 * time.Second},
		{"absent", func() string { return "" }, 0, 0},
		{"garbage", func() string { return "soon-ish" }, 0, 0},
		{"past-date", func() string {
			return time.Now().Add(-time.Hour).UTC().Format(http.TimeFormat)
		}, 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if v := tc.header(); v != "" {
					w.Header().Set("Retry-After", v)
				}
				w.WriteHeader(http.StatusTooManyRequests)
			}))
			defer srv.Close()

			_, err := newTestFetcher(testConfig(), nil).Fetch(context.Background(), FetchRequest{URL: srv.URL})
			fe := asFetchError(t, err)
			if fe.StatusCode != http.StatusTooManyRequests {
				t.Fatalf("StatusCode = %d", fe.StatusCode)
			}
			// Edge 3: a 429 is never retried inside the cycle.
			if fe.Temporary() {
				t.Error("Temporary() = true for 429, want false")
			}
			if fe.RetryAfter < tc.wantLow || fe.RetryAfter > tc.wantHigh {
				t.Errorf("RetryAfter = %v, want within [%v, %v]", fe.RetryAfter, tc.wantLow, tc.wantHigh)
			}
		})
	}
}

func TestFetchTemporaryDrivesRetry(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if hits.Add(1) == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = w.Write(fixture(t, "rss2.xml"))
	}))
	defer srv.Close()

	f := newTestFetcher(testConfig(), nil)
	var res *model.FetchResult
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		res, err = f.Fetch(context.Background(), FetchRequest{URL: srv.URL})
		if err == nil {
			break
		}
		if fe := asFetchError(t, err); !fe.Temporary() {
			t.Fatalf("attempt %d: %v is not temporary, retry would be skipped", attempt, fe)
		}
	}
	if err != nil {
		t.Fatalf("Fetch never succeeded: %v", err)
	}
	if hits.Load() != 2 {
		t.Errorf("server hits = %d, want 2 (500 then 200)", hits.Load())
	}
	if len(res.Feed.Items) != 11 {
		t.Errorf("items = %d, want 11", len(res.Feed.Items))
	}
}

func TestFetchBodyTooLarge(t *testing.T) {
	body := make([]byte, 4096)
	for i := range body {
		body[i] = 'x'
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	cfg := testConfig()
	cfg.MaxBodyBytes = 1024
	_, err := newTestFetcher(cfg, nil).Fetch(context.Background(), FetchRequest{URL: srv.URL})
	fe := asFetchError(t, err)
	if fe.Op != "toolarge" {
		t.Errorf("Op = %q, want toolarge", fe.Op)
	}
	if fe.Temporary() {
		t.Error("Temporary() = true for an oversized body, want false")
	}
}

func TestFetchBodyExactlyAtLimit(t *testing.T) {
	body := fixture(t, "rss2.xml")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	cfg := testConfig()
	cfg.MaxBodyBytes = int64(len(body))
	res, err := newTestFetcher(cfg, nil).Fetch(context.Background(), FetchRequest{URL: srv.URL})
	if err != nil {
		t.Fatalf("a body exactly at the limit must succeed: %v", err)
	}
	if len(res.Feed.Items) != 11 {
		t.Errorf("items = %d, want 11", len(res.Feed.Items))
	}
}

func TestFetchRedirectCap(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.Redirect(w, r, "/next", http.StatusFound)
	}))
	defer srv.Close()

	cfg := testConfig()
	cfg.MaxRedirects = 10
	_, err := newTestFetcher(cfg, nil).Fetch(context.Background(), FetchRequest{URL: srv.URL})
	fe := asFetchError(t, err)
	if !strings.Contains(fe.Error(), "stopped after 10 redirects") {
		t.Errorf("Error() = %q, want the redirect cap message", fe.Error())
	}
	// 10 requests are made; the 11th hop is refused.
	if hits.Load() != 10 {
		t.Errorf("server hits = %d, want 10", hits.Load())
	}
}

func TestFetchRedirectFollowedWithinCap(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/final", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(fixture(t, "rss2.xml"))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/final", http.StatusMovedPermanently)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	res, err := newTestFetcher(testConfig(), nil).Fetch(context.Background(), FetchRequest{URL: srv.URL})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if res.FinalURL != srv.URL+"/final" {
		t.Errorf("FinalURL = %q, want %q", res.FinalURL, srv.URL+"/final")
	}
}

func TestFetchSlowDripTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		w.WriteHeader(http.StatusOK)
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Error("ResponseWriter is not a Flusher")
			return
		}
		flusher.Flush()
		for i := 0; i < 100; i++ {
			select {
			case <-r.Context().Done():
				return
			case <-time.After(20 * time.Millisecond):
			}
			if _, err := w.Write([]byte("<")); err != nil {
				return
			}
			flusher.Flush()
		}
	}))
	defer srv.Close()

	cfg := testConfig()
	cfg.HTTPTimeout = 200 * time.Millisecond
	start := time.Now()
	_, err := newTestFetcher(cfg, nil).Fetch(context.Background(), FetchRequest{URL: srv.URL})
	elapsed := time.Since(start)

	fe := asFetchError(t, err)
	if fe.Op != "timeout" {
		t.Errorf("Op = %q, want timeout", fe.Op)
	}
	if !fe.Temporary() {
		t.Error("Temporary() = false for a timeout, want true")
	}
	// The client-level Timeout must cover the body read, not just the headers.
	if elapsed > 2*time.Second {
		t.Errorf("Fetch took %v, want the %v client timeout to fire", elapsed, cfg.HTTPTimeout)
	}
}

func TestFetchGzipTransparent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		w.Header().Set("Content-Encoding", "gzip")
		zw := gzip.NewWriter(w)
		_, _ = zw.Write(fixture(t, "rss2.xml"))
		_ = zw.Close()
	}))
	defer srv.Close()

	res, err := newTestFetcher(testConfig(), nil).Fetch(context.Background(), FetchRequest{URL: srv.URL})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(res.Feed.Items) != 11 {
		t.Fatalf("items = %d, want 11 from the gzipped body", len(res.Feed.Items))
	}
}

func TestFetchHTMLPageParseError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(fixture(t, "html_page.html"))
	}))
	defer srv.Close()

	_, err := newTestFetcher(testConfig(), nil).Fetch(context.Background(), FetchRequest{URL: srv.URL})
	var pe *ParseError
	if !errors.As(err, &pe) {
		t.Fatalf("err = %T (%v), want *ParseError", err, err)
	}
	const want = "not a valid RSS/Atom feed (content-type: text/html; charset=utf-8)"
	if pe.Error() != want {
		t.Errorf("Error() = %q, want %q", pe.Error(), want)
	}
	if pe.Snippet == "" || len(pe.Snippet) > snippetMax {
		t.Errorf("Snippet length = %d, want 1..%d", len(pe.Snippet), snippetMax)
	}
}

func TestFetchConnectionRefused(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	addr := srv.URL
	srv.Close() // nothing is listening any more

	_, err := newTestFetcher(testConfig(), nil).Fetch(context.Background(), FetchRequest{URL: addr})
	fe := asFetchError(t, err)
	if !fe.Temporary() {
		t.Errorf("Temporary() = false for %v, want true", fe)
	}
	if fe.Op != "connect" && fe.Op != "dns" && fe.Op != "timeout" {
		t.Errorf("Op = %q", fe.Op)
	}
}

func TestFetchErrorNeverLeaksCredentials(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	srv.Close()
	u.User = url.UserPassword("alice", "hunter2")

	_, err = newTestFetcher(testConfig(), nil).Fetch(context.Background(), FetchRequest{URL: u.String()})
	fe := asFetchError(t, err)
	if strings.Contains(fe.Error(), "hunter2") || strings.Contains(fe.Error(), "alice") {
		t.Errorf("Error() leaked credentials: %q", fe.Error())
	}
}

func TestFetchContextCancelled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := newTestFetcher(testConfig(), nil).Fetch(ctx, FetchRequest{URL: srv.URL})
	if err == nil {
		t.Fatal("Fetch returned nil error for a cancelled context")
	}
	asFetchError(t, err)
}

// TestFetchRefusesNonPublicAddress pins the SSRF guard: a feed URL (or a
// redirect hop) that resolves to a loopback / private / link-local address is
// refused at dial time, after DNS resolution, so a rebind cannot slip past.
func TestFetchRefusesNonPublicAddress(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = w.Write([]byte(`<rss version="2.0"><channel><title>t</title></channel></rss>`))
	}))
	defer srv.Close()

	allowLoopbackDial = false
	defer func() { allowLoopbackDial = true }()

	_, err := newTestFetcher(testConfig(), nil).Fetch(context.Background(), FetchRequest{URL: srv.URL})
	if err == nil {
		t.Fatal("Fetch reached a loopback address, want a refusal")
	}
	fe := asFetchError(t, err)
	if !strings.Contains(fe.Error(), "non-public address") {
		t.Errorf("Error() = %q, want it to name the non-public address refusal", fe.Error())
	}
}
