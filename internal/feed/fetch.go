package feed

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/borisovdev/newsbuddy/internal/config"
	"github.com/borisovdev/newsbuddy/internal/logging"
	"github.com/borisovdev/newsbuddy/internal/model"
)

const acceptHeader = "application/rss+xml, application/atom+xml, application/xml;q=0.9, text/xml;q=0.9, */*;q=0.1"

// snippetMax bounds ParseError.Snippet.
const snippetMax = 512

// HTTPFetcher implements Fetcher over net/http.
type HTTPFetcher struct {
	client    *http.Client
	parser    Parser
	maxBody   int64
	userAgent string // "news-buddy/<version> (+https://github.com/borisovdev/newsbuddy)"
}

// allowLoopbackDial lets the in-package tests point the fetcher at an
// httptest.Server, which always binds a loopback address the SSRF guard would
// otherwise refuse. A package var purely so tests can flip it (the same
// pattern as poller.retryBackoffs); production never reassigns it, and it
// relaxes ONLY the loopback rule — private, link-local, and unspecified
// addresses stay refused everywhere.
var allowLoopbackDial = false

// safeDialControl is a net.Dialer.Control hook: it runs after DNS resolution,
// on the concrete address about to be connected, so a hostile feed cannot
// reach an internal service through a redirect or a DNS rebind (the guard sees
// the resolved IP, not the name). It applies to every hop, initial and
// redirected alike, because it lives in the transport.
func safeDialControl(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("refusing to connect to unresolvable address %q", address)
	}
	if ip.IsLoopback() && allowLoopbackDial {
		return nil
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsUnspecified() {
		return fmt.Errorf("refusing to connect to non-public address %s", ip)
	}
	return nil
}

// NewHTTPFetcher builds a fetcher whose client Timeout covers the WHOLE
// request including the body read and whose redirect chain is capped
// at cfg.MaxRedirects hops. The default transport is CLONED, not
// rebuilt, so gzip/deflate stay transparent — Accept-Encoding is never set by
// hand — with only DialContext replaced by the SSRF guard.
func NewHTTPFetcher(cfg config.Config, p Parser) *HTTPFetcher {
	maxRedirects := cfg.MaxRedirects

	tr := http.DefaultTransport.(*http.Transport).Clone()
	dialer := &net.Dialer{Timeout: cfg.HTTPTimeout, KeepAlive: 30 * time.Second, Control: safeDialControl}
	tr.DialContext = dialer.DialContext

	return &HTTPFetcher{
		client: &http.Client{
			Timeout:   cfg.HTTPTimeout,
			Transport: tr,
			CheckRedirect: func(_ *http.Request, via []*http.Request) error {
				if len(via) >= maxRedirects {
					return fmt.Errorf("stopped after %d redirects", maxRedirects)
				}
				return nil
			},
		},
		parser:    p,
		maxBody:   cfg.MaxBodyBytes,
		userAgent: UserAgent(),
	}
}

// UserAgent is the User-Agent sent with every feed request.
func UserAgent() string {
	return "news-buddy/" + logging.Version + " (+https://github.com/borisovdev/newsbuddy)"
}

// Fetch implements Fetcher in the nine steps below.
func (f *HTTPFetcher) Fetch(ctx context.Context, req FetchRequest) (*model.FetchResult, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, req.URL, nil)
	if err != nil {
		return nil, &FetchError{Op: "connect", Err: err}
	}
	httpReq.Header.Set("User-Agent", f.userAgent)
	httpReq.Header.Set("Accept", acceptHeader)
	if req.ETag != "" {
		httpReq.Header.Set("If-None-Match", req.ETag)
	}
	if req.LastModified != "" {
		httpReq.Header.Set("If-Modified-Since", req.LastModified)
	}

	resp, err := f.client.Do(httpReq)
	if err != nil {
		return nil, &FetchError{Op: classify(err, "connect"), Err: err}
	}
	defer resp.Body.Close() //nolint:errcheck // response body close

	// Step 4: 304 short-circuit. The body is drained and closed, never parsed.
	if resp.StatusCode == http.StatusNotModified {
		_, _ = io.Copy(io.Discard, resp.Body)
		return &model.FetchResult{
			NotModified:  true,
			StatusCode:   http.StatusNotModified,
			ETag:         req.ETag,
			LastModified: req.LastModified,
		}, nil
	}

	// Step 5: 429 carries Retry-After but is not retried inside the cycle.
	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, &FetchError{
			Op:         "status",
			StatusCode: resp.StatusCode,
			RetryAfter: parseRetryAfter(resp.Header, time.Now()),
		}
	}

	// Step 6: any other non-2xx.
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, &FetchError{Op: "status", StatusCode: resp.StatusCode}
	}

	// Step 7: bounded body read.
	body, err := io.ReadAll(io.LimitReader(resp.Body, f.maxBody+1))
	if err != nil {
		return nil, &FetchError{Op: classify(err, "read"), Err: err}
	}
	if int64(len(body)) > f.maxBody {
		return nil, &FetchError{Op: "toolarge"}
	}

	// Step 8: parse, tagging failures with the response Content-Type.
	finalURL := resp.Request.URL.String()
	parsed, err := f.parser.Parse(body, finalURL)
	if err != nil {
		contentType := resp.Header.Get("Content-Type")
		var pe *ParseError
		if errors.As(err, &pe) {
			pe.ContentType = contentType
			return nil, pe
		}
		return nil, &ParseError{ContentType: contentType, Snippet: snippet(body), Err: err}
	}

	// Step 9.
	return &model.FetchResult{
		StatusCode:   resp.StatusCode,
		Feed:         parsed,
		ETag:         resp.Header.Get("ETag"),
		LastModified: resp.Header.Get("Last-Modified"),
		FinalURL:     finalURL,
	}, nil
}

// classify maps a transport-level error onto a FetchError Op, falling back to
// the caller's guess when the error is neither a DNS failure nor a timeout.
func classify(err error, fallback string) string {
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return "dns"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return "timeout"
	}
	return fallback
}

// parseRetryAfter understands both RFC 9110 forms of Retry-After:
// delta-seconds and an HTTP-date.
func parseRetryAfter(h http.Header, now time.Time) time.Duration {
	raw := strings.TrimSpace(h.Get("Retry-After"))
	if raw == "" {
		return 0
	}
	if secs, err := strconv.Atoi(raw); err == nil {
		if secs <= 0 {
			return 0
		}
		return time.Duration(secs) * time.Second
	}
	if when, err := http.ParseTime(raw); err == nil {
		if d := when.Sub(now); d > 0 {
			return d
		}
	}
	return 0
}

// snippet returns at most snippetMax bytes of b as a valid UTF-8 string.
func snippet(b []byte) string {
	if int64(len(b)) > snippetMax {
		b = b[:snippetMax]
	}
	return strings.ToValidUTF8(string(b), "")
}
