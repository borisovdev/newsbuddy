// Package feedtest provides a scripted feed.Fetcher for tests, so no test
// ever touches the network.
package feedtest

import (
	"context"
	"fmt"
	"os"
	"sync"

	"github.com/borisovdev/newsbuddy/internal/feed"
	"github.com/borisovdev/newsbuddy/internal/model"
)

// Response is one scripted outcome of a Fetch call.
type Response struct {
	Result *model.FetchResult
	Err    error
}

// Call records the conditional-GET validators a caller asked with, so conditional GET
// is assertable.
type Call struct {
	URL          string
	ETag         string
	LastModified string
}

// script is one URL's ordered response queue. An ordered slice, deliberately
// not a map.
type script struct {
	url       string
	responses []Response
}

// FakeFetcher is a feed.Fetcher scripted per URL, consuming one response per
// Fetch call for that URL.
type FakeFetcher struct {
	mu      sync.Mutex
	scripts []script
	calls   []Call
}

// NewFakeFetcher returns an empty fake with no scripted responses.
func NewFakeFetcher() *FakeFetcher { return &FakeFetcher{} }

// AddResponse appends one response to the queue for url.
func (f *FakeFetcher) AddResponse(url string, r Response) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.scripts {
		if f.scripts[i].url == url {
			f.scripts[i].responses = append(f.scripts[i].responses, r)
			return
		}
	}
	f.scripts = append(f.scripts, script{url: url, responses: []Response{r}})
}

// Fetch implements feed.Fetcher.
func (f *FakeFetcher) Fetch(_ context.Context, req feed.FetchRequest) (*model.FetchResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.calls = append(f.calls, Call{URL: req.URL, ETag: req.ETag, LastModified: req.LastModified})

	for i := range f.scripts {
		if f.scripts[i].url != req.URL {
			continue
		}
		if len(f.scripts[i].responses) == 0 {
			break
		}
		r := f.scripts[i].responses[0]
		f.scripts[i].responses = f.scripts[i].responses[1:]
		return r.Result, r.Err
	}
	return nil, fmt.Errorf("feedtest: no scripted response for %q (call %d)", req.URL, len(f.calls))
}

// Calls returns every Fetch call in order.
func (f *FakeFetcher) Calls() []Call {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Call(nil), f.calls...)
}

// CallCount returns how many times Fetch was called.
func (f *FakeFetcher) CallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// LoadFixture reads a testdata feed file, parses it with a real
// feed.GofeedParser, and wraps it as a successful *model.FetchResult.
func LoadFixture(path, baseURL string) (*model.FetchResult, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	parsed, err := feed.NewGofeedParser().Parse(data, baseURL)
	if err != nil {
		return nil, fmt.Errorf("feedtest: parsing %s: %w", path, err)
	}
	return &model.FetchResult{StatusCode: 200, Feed: parsed, FinalURL: baseURL}, nil
}
