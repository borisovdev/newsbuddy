package feed

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"
)

// fixture reads a golden file from the repo-root testdata/ directory. go test
// runs with the package directory as its working directory, so internal/feed
// reaches testdata/ via ../../ .
func fixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", name))
	if err != nil {
		t.Fatalf("reading fixture %s: %v", name, err)
	}
	return data
}

const parseBase = "https://fetched.example.com/base/feed.xml"

func TestParseRSS2(t *testing.T) {
	f, err := NewGofeedParser().Parse(fixture(t, "rss2.xml"), parseBase)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if f.Title != "Golden Test Feed" {
		t.Errorf("Title = %q", f.Title)
	}
	if len(f.Items) != 11 {
		t.Fatalf("items = %d, want 11", len(f.Items))
	}
	first := f.Items[0]
	if first.Key != "https://example.com/articles/1" {
		t.Errorf("first key = %q", first.Key)
	}
	if first.Link != "https://example.com/articles/1" {
		t.Errorf("first link = %q", first.Link)
	}
	if first.Excerpt != "Description for item one." {
		t.Errorf("first excerpt = %q", first.Excerpt)
	}
	if first.Published == nil {
		t.Error("first published = nil, want parsed pubDate")
	}
	for i, it := range f.Items {
		if it.Order != i {
			t.Errorf("item %d Order = %d", i, it.Order)
		}
	}
}

func TestParseAtom(t *testing.T) {
	f, err := NewGofeedParser().Parse(fixture(t, "atom.xml"), parseBase)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(f.Items) != 11 {
		t.Fatalf("items = %d, want 11", len(f.Items))
	}
	// Atom <id> becomes the GUID and therefore the key.
	if f.Items[0].Key != "https://example.com/atom/1" {
		t.Errorf("first key = %q", f.Items[0].Key)
	}
	// Atom <summary> lands in Description, which wins over content:encoded.
	if f.Items[0].Excerpt != "Summary for atom item one." {
		t.Errorf("first excerpt = %q", f.Items[0].Excerpt)
	}
}

func TestParseRDF(t *testing.T) {
	f, err := NewGofeedParser().Parse(fixture(t, "rss1_rdf.xml"), parseBase)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(f.Items) != 2 {
		t.Fatalf("items = %d, want 2", len(f.Items))
	}
	// RSS 1.0 items have no <guid>, so the key falls back to the link.
	if f.Items[0].Key != "https://example.com/rdf/1" {
		t.Errorf("first key = %q", f.Items[0].Key)
	}
	if f.Items[1].Published == nil {
		t.Error("dc:date was not parsed into Published")
	}
}

func TestParseNoGUIDFallsBackToLink(t *testing.T) {
	f, err := NewGofeedParser().Parse(fixture(t, "no_guid.xml"), parseBase)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	want := []string{"https://example.com/no-guid/1", "https://example.com/no-guid/2"}
	if len(f.Items) != len(want) {
		t.Fatalf("items = %d, want %d", len(f.Items), len(want))
	}
	for i, w := range want {
		if f.Items[i].Key != w {
			t.Errorf("item %d key = %q, want %q", i, f.Items[i].Key, w)
		}
	}
}

func TestParseSkipsUnusableItem(t *testing.T) {
	f, skipped, err := NewGofeedParser().ParseReport(fixture(t, "no_guid_no_link.xml"), parseBase)
	if err != nil {
		t.Fatalf("ParseReport: %v", err)
	}
	if len(skipped) != 1 || skipped[0] != 0 {
		t.Fatalf("skipped = %v, want [0]", skipped)
	}
	if len(f.Items) != 1 {
		t.Fatalf("items = %d, want 1", len(f.Items))
	}
	if f.Items[0].Key != "https://example.com/unusable-feed/usable" {
		t.Errorf("key = %q", f.Items[0].Key)
	}
	// Order stays the document-order index, not the post-skip index.
	if f.Items[0].Order != 1 {
		t.Errorf("Order = %d, want 1", f.Items[0].Order)
	}
}

func TestParseDuplicateGUIDsFirstWins(t *testing.T) {
	f, err := NewGofeedParser().Parse(fixture(t, "duplicate_guids.xml"), parseBase)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(f.Items) != 1 {
		t.Fatalf("items = %d, want 1 (first occurrence only)", len(f.Items))
	}
	if f.Items[0].Title != "Duplicate GUID - First Occurrence" {
		t.Errorf("kept %q, want the first occurrence", f.Items[0].Title)
	}
}

func TestParseEmptyItemsIsSuccess(t *testing.T) {
	f, err := NewGofeedParser().Parse(fixture(t, "empty_items.xml"), parseBase)
	if err != nil {
		t.Fatalf("Parse: %v, want success with zero items", err)
	}
	if len(f.Items) != 0 {
		t.Fatalf("items = %d, want 0", len(f.Items))
	}
	if f.Title != "Empty Feed" {
		t.Errorf("Title = %q", f.Title)
	}
}

func TestParseMalformedNeverPanics(t *testing.T) {
	// The poll goroutine must survive a malformed document.
	// gofeed's XML pull parser is deliberately non-strict, so this particular
	// fixture (an unclosed <title>) is recovered rather than rejected; what
	// matters is that it neither panics nor escapes as a non-ParseError.
	f, err := NewGofeedParser().Parse(fixture(t, "malformed.xml"), parseBase)
	if err != nil {
		var pe *ParseError
		if !errors.As(err, &pe) {
			t.Fatalf("err = %T (%v), want *ParseError", err, err)
		}
		if pe.Snippet == "" {
			t.Error("ParseError.Snippet is empty; debug logging needs a snippet")
		}
		return
	}
	if f == nil {
		t.Fatal("nil feed with nil error")
	}
}

func TestParseGarbageBytesReturnsParseError(t *testing.T) {
	var pe *ParseError
	_, err := NewGofeedParser().Parse([]byte{0x00, 0x01, 0x02, 0xff, 0xfe, 'n', 'o', 't', 'x', 'm', 'l'}, parseBase)
	if !errors.As(err, &pe) {
		t.Fatalf("err = %T (%v), want *ParseError", err, err)
	}
	if pe.Error() != "not a valid RSS/Atom feed" {
		t.Errorf("Error() = %q", pe.Error())
	}
	if !utf8.ValidString(pe.Snippet) {
		t.Error("Snippet is not valid UTF-8")
	}
}

func TestParseHTMLPageIsNotAFeed(t *testing.T) {
	var pe *ParseError
	_, err := NewGofeedParser().Parse(fixture(t, "html_page.html"), parseBase)
	if !errors.As(err, &pe) {
		t.Fatalf("err = %T (%v), want *ParseError", err, err)
	}
	if pe.Snippet == "" {
		t.Error("Snippet is empty")
	}
}

func TestParseWindows1251DecodesCyrillic(t *testing.T) {
	raw := fixture(t, "windows1251.xml")
	if utf8.Valid(raw) && strings.Contains(string(raw), "Новости") {
		t.Fatal("fixture is UTF-8; it must be byte-encoded windows-1251 to test charset decoding")
	}
	f, err := NewGofeedParser().Parse(raw, parseBase)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if f.Title != "Новости на русском" {
		t.Errorf("feed title = %q, want decoded Cyrillic", f.Title)
	}
	if len(f.Items) != 1 {
		t.Fatalf("items = %d, want 1", len(f.Items))
	}
	it := f.Items[0]
	if it.Title != "Первая новость на кириллице" {
		t.Errorf("item title = %q, want decoded Cyrillic", it.Title)
	}
	if !utf8.ValidString(it.Title) || !utf8.ValidString(it.Excerpt) {
		t.Error("decoded text is not valid UTF-8")
	}
	// "Ð" is the tell-tale first byte of UTF-8 Cyrillic reinterpreted as
	// latin-1 mojibake.
	if strings.ContainsAny(it.Title+it.Excerpt, "ÐÑ") {
		t.Errorf("mojibake in decoded text: %q / %q", it.Title, it.Excerpt)
	}
	if !strings.Contains(it.Excerpt, "съешь ещё этих мягких французских булок") {
		t.Errorf("excerpt = %q", it.Excerpt)
	}
}

func TestParseCDATAFullHTML(t *testing.T) {
	f, err := NewGofeedParser().Parse(fixture(t, "cdata_full_html.xml"), parseBase)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(f.Items) != 1 {
		t.Fatalf("items = %d, want 1", len(f.Items))
	}
	// Excerpt order is description first, then content:encoded; this fixture has
	// both, so the short <description> wins. Cleaning/trimming is render's job.
	if f.Items[0].Excerpt != "Short RSS description; the real payload is in content:encoded." {
		t.Errorf("excerpt = %q", f.Items[0].Excerpt)
	}
}

func TestParseCDATAContentUsedWhenDescriptionEmpty(t *testing.T) {
	// Same shape as cdata_full_html.xml but with no <description>, proving the
	// content:encoded fallback half of the excerpt order.
	doc := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0" xmlns:content="http://purl.org/rss/1.0/modules/content/">
  <channel><title>C</title><link>https://example.com/c</link>
    <item>
      <title>Only content</title>
      <link>https://example.com/c/1</link>
      <guid>https://example.com/c/1</guid>
      <content:encoded><![CDATA[<p>Body from content:encoded.</p>]]></content:encoded>
    </item>
  </channel>
</rss>`)
	f, err := NewGofeedParser().Parse(doc, parseBase)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := f.Items[0].Excerpt; got != "<p>Body from content:encoded.</p>" {
		t.Errorf("excerpt = %q", got)
	}
}

func TestParseRelativeLinksResolved(t *testing.T) {
	f, err := NewGofeedParser().Parse(fixture(t, "relative_links.xml"), parseBase)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	want := []string{
		"https://example.com/articles/123",      // root-relative against <link>
		"https://example.com/base/articles/456", // document-relative against <link>
	}
	if len(f.Items) != len(want) {
		t.Fatalf("items = %d, want %d", len(f.Items), len(want))
	}
	for i, w := range want {
		if f.Items[i].Link != w {
			t.Errorf("item %d link = %q, want %q", i, f.Items[i].Link, w)
		}
	}
}

func TestParseRelativeLinksFallBackToBaseURL(t *testing.T) {
	// No <link> in the channel, so the passed-in (post-redirect) base URL is
	// the resolution base (third precedence step).
	doc := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0"><channel><title>NoLink</title>
  <item><title>Rel</title><link>/x/1</link></item>
</channel></rss>`)
	f, err := NewGofeedParser().Parse(doc, "https://fallback.example.com/feeds/main.xml")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := f.Items[0].Link; got != "https://fallback.example.com/x/1" {
		t.Errorf("link = %q", got)
	}
}

func TestParseNonHTTPLinkBecomesEmpty(t *testing.T) {
	doc := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0"><channel><title>Schemes</title><link>https://example.com/s</link>
  <item><title>Bad scheme</title><link>javascript:alert(1)</link></item>
  <item><title>Ftp</title><link>ftp://example.com/f</link></item>
</channel></rss>`)
	f, err := NewGofeedParser().Parse(doc, parseBase)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(f.Items) != 2 {
		t.Fatalf("items = %d, want 2 (both keep their title-hash key)", len(f.Items))
	}
	for i, it := range f.Items {
		if it.Link != "" {
			t.Errorf("item %d link = %q, want empty (unlinked path)", i, it.Link)
		}
		if it.Key == "" {
			t.Errorf("item %d has no key", i)
		}
	}
}

func TestParseEntitiesAndEmojiAndFutureDates(t *testing.T) {
	// Content cleaning is render's job; here we only prove these parse.
	cases := []struct {
		file      string
		wantItems int
	}{
		{"entities.xml", 1},
		{"astral_emoji.xml", 1},
		{"future_dates.xml", 2},
	}
	p := NewGofeedParser()
	for _, tc := range cases {
		t.Run(tc.file, func(t *testing.T) {
			f, err := p.Parse(fixture(t, tc.file), parseBase)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if len(f.Items) != tc.wantItems {
				t.Fatalf("items = %d, want %d", len(f.Items), tc.wantItems)
			}
			if f.Items[0].Key == "" {
				t.Error("first item has no key")
			}
		})
	}
}

func TestParseEntitiesDecodedOnce(t *testing.T) {
	f, err := NewGofeedParser().Parse(fixture(t, "entities.xml"), parseBase)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	// A single XML unescape: &amp;#39; stays the literal text &#39;.
	const want = "Tom & Jerry: <script> and &#39;quotes&#39;"
	if got := f.Items[0].Title; got != want {
		t.Errorf("title = %q, want %q", got, want)
	}
}

func TestParsePublishedFallsBackToUpdated(t *testing.T) {
	doc := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<feed xmlns="http://www.w3.org/2005/Atom">
  <title>Updated Only</title>
  <id>https://example.com/u</id>
  <entry>
    <title>Only updated</title>
    <id>https://example.com/u/1</id>
    <updated>2024-03-04T05:06:07Z</updated>
  </entry>
  <entry>
    <title>No dates at all</title>
    <id>https://example.com/u/2</id>
  </entry>
</feed>`)
	f, err := NewGofeedParser().Parse(doc, parseBase)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if f.Items[0].Published == nil {
		t.Fatal("item 0 Published = nil, want the <updated> fallback")
	}
	if got := f.Items[0].Published.UTC().Format("2006-01-02T15:04:05Z"); got != "2024-03-04T05:06:07Z" {
		t.Errorf("Published = %s", got)
	}
	if f.Items[1].Published != nil {
		t.Errorf("item 1 Published = %v, want nil", f.Items[1].Published)
	}
}

func TestParseKeyUsesNormalizedLink(t *testing.T) {
	// Two guid-less items whose links differ only by trailing slash and host
	// case collapse to one key.
	doc := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0"><channel><title>Norm</title><link>https://example.com/n</link>
  <item><title>A</title><link>https://Example.com/a/</link></item>
  <item><title>B</title><link>https://example.com/a</link></item>
</channel></rss>`)
	f, err := NewGofeedParser().Parse(doc, parseBase)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(f.Items) != 1 {
		t.Fatalf("items = %d, want 1 after normalization-based dedup", len(f.Items))
	}
	if f.Items[0].Key != "https://example.com/a" {
		t.Errorf("key = %q, want the normalized link", f.Items[0].Key)
	}
}

func TestParseConcurrentIsRaceFree(t *testing.T) {
	// The poll workers share one parser (cfg.PollConcurrency), so gofeed's
	// lazily-created translators must already be assigned.
	p := NewGofeedParser()
	docs := [][]byte{fixture(t, "rss2.xml"), fixture(t, "atom.xml"), fixture(t, "rss1_rdf.xml")}
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(doc []byte) {
			defer wg.Done()
			if _, err := p.Parse(doc, parseBase); err != nil {
				t.Errorf("Parse: %v", err)
			}
		}(docs[i%len(docs)])
	}
	wg.Wait()
}
