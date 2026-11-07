package feedtest

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/borisovdev/newsbuddy/internal/feed"
	"github.com/borisovdev/newsbuddy/internal/model"
)

func fixturePath(name string) string {
	return filepath.Join("..", "..", "..", "testdata", name)
}

func TestFakeFetcherConsumesResponsesInOrder(t *testing.T) {
	const url = "https://example.com/feed"
	boom := errors.New("boom")

	f := NewFakeFetcher()
	f.AddResponse(url, Response{Result: &model.FetchResult{StatusCode: 200}})
	f.AddResponse(url, Response{Result: &model.FetchResult{NotModified: true, StatusCode: 304}})
	f.AddResponse(url, Response{Err: boom})

	res, err := f.Fetch(context.Background(), feed.FetchRequest{URL: url})
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("call 1: res=%+v err=%v", res, err)
	}
	res, err = f.Fetch(context.Background(), feed.FetchRequest{URL: url, ETag: `"v1"`})
	if err != nil || !res.NotModified {
		t.Fatalf("call 2: res=%+v err=%v", res, err)
	}
	if _, err = f.Fetch(context.Background(), feed.FetchRequest{URL: url}); !errors.Is(err, boom) {
		t.Fatalf("call 3: err=%v, want boom", err)
	}
	if _, err = f.Fetch(context.Background(), feed.FetchRequest{URL: url}); err == nil {
		t.Fatal("call 4: want an exhausted-script error")
	}
	if _, err = f.Fetch(context.Background(), feed.FetchRequest{URL: "https://other.example.com"}); err == nil {
		t.Fatal("unscripted URL: want an error")
	}
}

func TestFakeFetcherRecordsValidators(t *testing.T) {
	const url = "https://example.com/feed"
	f := NewFakeFetcher()
	f.AddResponse(url, Response{Result: &model.FetchResult{StatusCode: 200}})
	f.AddResponse(url, Response{Result: &model.FetchResult{StatusCode: 200}})

	_, _ = f.Fetch(context.Background(), feed.FetchRequest{URL: url})
	_, _ = f.Fetch(context.Background(), feed.FetchRequest{
		URL:          url,
		ETag:         `"v1"`,
		LastModified: "Tue, 02 Jan 2024 00:00:00 GMT",
	})

	calls := f.Calls()
	if len(calls) != 2 || f.CallCount() != 2 {
		t.Fatalf("calls = %d", len(calls))
	}
	if calls[0].ETag != "" || calls[0].LastModified != "" {
		t.Errorf("call 0 = %+v, want no validators", calls[0])
	}
	if calls[1].ETag != `"v1"` || calls[1].LastModified != "Tue, 02 Jan 2024 00:00:00 GMT" {
		t.Errorf("call 1 = %+v", calls[1])
	}
}

func TestLoadFixture(t *testing.T) {
	res, err := LoadFixture(fixturePath("rss2.xml"), "https://example.com/feed")
	if err != nil {
		t.Fatalf("LoadFixture: %v", err)
	}
	if res.StatusCode != 200 || res.Feed == nil {
		t.Fatalf("res = %+v", res)
	}
	if len(res.Feed.Items) != 11 {
		t.Errorf("items = %d, want 11", len(res.Feed.Items))
	}

	if _, err := LoadFixture(fixturePath("html_page.html"), "https://example.com/page"); err == nil {
		t.Error("LoadFixture of a non-feed returned nil error")
	}
	if _, err := LoadFixture(fixturePath("does_not_exist.xml"), ""); err == nil {
		t.Error("LoadFixture of a missing file returned nil error")
	}
}
