package render

import (
	"strings"
	"testing"

	"github.com/borisovdev/newsbuddy/internal/model"
)

func defaultOpts() Options {
	return Options{TitleMax: 200, ExcerptMax: 300, HardLimit: 4000}
}

func TestMessage_Layout(t *testing.T) {
	tests := []struct {
		name         string
		item         model.FeedItem
		source       string
		opt          Options
		want         string
		wantUnlinked bool
	}{
		{
			name:   "link and excerpt",
			item:   model.FeedItem{Title: "Hello", Link: "https://example.com/a", Excerpt: "<p>Body text.</p>"},
			source: "Example News",
			opt:    defaultOpts(),
			want:   "<b><a href=\"https://example.com/a\">Hello</a></b>\n\nBody text.\n\n<i>Example News</i>",
		},
		{
			name:   "link, no excerpt",
			item:   model.FeedItem{Title: "Hello", Link: "https://example.com/a"},
			source: "Example News",
			opt:    defaultOpts(),
			want:   "<b><a href=\"https://example.com/a\">Hello</a></b>\n\n<i>Example News</i>",
		},
		{
			name:         "no link",
			item:         model.FeedItem{Title: "Hello", Excerpt: "Body."},
			source:       "Example News",
			opt:          defaultOpts(),
			want:         "<b>Hello</b>\n\nBody.\n\n<i>Example News</i>",
			wantUnlinked: true,
		},
		{
			name:   "empty title falls back",
			item:   model.FeedItem{Title: "  <p></p>  ", Link: "https://example.com/a"},
			source: "Example News",
			opt:    defaultOpts(),
			want:   "<b><a href=\"https://example.com/a\">(untitled)</a></b>\n\n<i>Example News</i>",
		},
		{
			name:   "title and excerpt truncated at word boundaries",
			item:   model.FeedItem{Title: "alpha beta gamma", Link: "https://example.com/a", Excerpt: "one two three"},
			source: "Example News",
			opt:    Options{TitleMax: 12, ExcerptMax: 9, HardLimit: 4000},
			want:   "<b><a href=\"https://example.com/a\">alpha beta" + ellipsis + "</a></b>\n\none two" + ellipsis + "\n\n<i>Example News</i>",
		},
		{
			name:   "source title truncated to 64 runes",
			item:   model.FeedItem{Title: "T", Link: "https://example.com/a"},
			source: strings.Repeat("s", 100),
			opt:    defaultOpts(),
			want:   "<b><a href=\"https://example.com/a\">T</a></b>\n\n<i>" + strings.Repeat("s", 63) + ellipsis + "</i>",
		},
		{
			name:   "href is escaped",
			item:   model.FeedItem{Title: "T", Link: "https://example.com/a?x=1&y=2"},
			source: "S",
			opt:    defaultOpts(),
			want:   "<b><a href=\"https://example.com/a?x=1&amp;y=2\">T</a></b>\n\n<i>S</i>",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, unlinked := Message(tc.item, tc.source, tc.opt)
			if got != tc.want {
				t.Fatalf("Message() =\n%q\nwant\n%q", got, tc.want)
			}
			if unlinked != tc.wantUnlinked {
				t.Fatalf("unlinked = %v, want %v", unlinked, tc.wantUnlinked)
			}
		})
	}
}

// Every posted message contains a clickable link to the article, the
// title, and (when the feed provides one) an excerpt.
func TestMessage_AC16_ContainsLinkTitleExcerpt(t *testing.T) {
	tests := []struct {
		name        string
		item        model.FeedItem
		wantExcerpt string
	}{
		{
			name:        "plain item",
			item:        model.FeedItem{Title: "Go 1.25 released", Link: "https://example.com/go125", Excerpt: "The Go team announced the release."},
			wantExcerpt: "The Go team announced the release.",
		},
		{
			name:        "html excerpt",
			item:        model.FeedItem{Title: "CDATA item", Link: "https://example.com/c", Excerpt: "<div><p>First.</p><p>Second.</p></div>"},
			wantExcerpt: "First. Second.",
		},
		{
			name: "no excerpt provided",
			item: model.FeedItem{Title: "Bare item", Link: "https://example.com/b"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, unlinked := Message(tc.item, "Example News", defaultOpts())
			if unlinked {
				t.Fatal("unlinked = true for an item with a link")
			}
			if want := `<a href="` + tc.item.Link + `">`; !strings.Contains(got, want) {
				t.Fatalf("message %q missing clickable link %q", got, want)
			}
			if !strings.Contains(got, tc.item.Title) {
				t.Fatalf("message %q missing title %q", got, tc.item.Title)
			}
			if tc.wantExcerpt != "" && !strings.Contains(got, tc.wantExcerpt) {
				t.Fatalf("message %q missing excerpt %q", got, tc.wantExcerpt)
			}
			if !strings.Contains(got, "<i>Example News</i>") {
				t.Fatalf("message %q missing source title", got)
			}
		})
	}
}

// A 50 KB+ description produces a message of at most 4000 UTF-16 code
// units, with the title link intact and the excerpt ending in "…".
func TestMessage_AC17_HardLimitTrimsExcerpt(t *testing.T) {
	const link = "https://example.com/long-article"
	longProse := strings.Repeat("Lorem ipsum dolor sit amet consectetur adipiscing elit. ", 1000)   // ~56 KB
	longEntities := strings.Repeat("R&D <tools> & \"quotes\" and 'apostrophes' everywhere. ", 1200) // escape-expanding
	longEmoji := strings.Repeat("emoji 😀 tail ", 5000)

	tests := []struct {
		name string
		opt  Options
		item model.FeedItem
	}{
		{
			name: "50KB prose with default excerpt cap",
			opt:  defaultOpts(),
			item: model.FeedItem{Title: "Long article", Link: link, Excerpt: "<div><p>" + longProse + "</p></div>"},
		},
		{
			name: "50KB prose with no excerpt cap",
			opt:  Options{TitleMax: 200, ExcerptMax: 1 << 20, HardLimit: 4000},
			item: model.FeedItem{Title: "Long article", Link: link, Excerpt: "<div><p>" + longProse + "</p></div>"},
		},
		{
			name: "entity dense excerpt measured after escaping",
			opt:  Options{TitleMax: 200, ExcerptMax: 1 << 20, HardLimit: 4000},
			item: model.FeedItem{Title: "R&D <report>", Link: link, Excerpt: longEntities},
		},
		{
			name: "astral runes near the limit",
			opt:  Options{TitleMax: 200, ExcerptMax: 1 << 20, HardLimit: 4000},
			item: model.FeedItem{Title: "Emoji storm", Link: link, Excerpt: longEmoji},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, unlinked := Message(tc.item, "Example News", tc.opt)
			if unlinked {
				t.Fatal("unlinked = true for an item with a link")
			}
			n := UTF16Len(got)
			if n > tc.opt.HardLimit {
				t.Fatalf("UTF16Len = %d, over hard limit %d", n, tc.opt.HardLimit)
			}
			// The binary search must find the largest excerpt that fits, not
			// merely some short one.
			if tc.opt.ExcerptMax > tc.opt.HardLimit && n < tc.opt.HardLimit-32 {
				t.Fatalf("UTF16Len = %d, over-trimmed well under the %d limit", n, tc.opt.HardLimit)
			}
			if want := `<b><a href="` + link + `">`; !strings.Contains(got, want) {
				t.Fatalf("title link line damaged: %q", firstRunes(got, 120))
			}
			if !strings.Contains(got, "</a></b>\n\n") {
				t.Fatalf("title line/excerpt separator damaged: %q", firstRunes(got, 120))
			}
			if !strings.HasSuffix(got, "\n\n<i>Example News</i>") {
				t.Fatalf("source line damaged: %q", got[max(0, len(got)-60):])
			}
			excerpt := strings.TrimSuffix(got, "\n\n<i>Example News</i>")
			excerpt = excerpt[strings.Index(excerpt, "</a></b>\n\n")+len("</a></b>\n\n"):]
			if excerpt == "" {
				t.Fatal("excerpt was dropped entirely; it should have been trimmed instead")
			}
			if !strings.HasSuffix(excerpt, ellipsis) {
				t.Fatalf("trimmed excerpt does not end in %q: %q", ellipsis, lastRunes(excerpt, 40))
			}
			assertNoSplitRune(t, got)
		})
	}
}

// The binary-search guard must also cope with a title so long that no excerpt
// fits: the excerpt block is dropped and the title shrinks, but the link line
// survives.
func TestMessage_AC17_TitleShrinksWhenExcerptCannotFit(t *testing.T) {
	opt := Options{TitleMax: 1 << 20, ExcerptMax: 1 << 20, HardLimit: 200}
	it := model.FeedItem{
		Title:   strings.Repeat("title word ", 500),
		Link:    "https://example.com/a",
		Excerpt: strings.Repeat("excerpt word ", 500),
	}
	got, unlinked := Message(it, "Example News", opt)
	if unlinked {
		t.Fatal("unlinked = true for an item with a link")
	}
	if n := UTF16Len(got); n > opt.HardLimit {
		t.Fatalf("UTF16Len = %d, over hard limit %d", n, opt.HardLimit)
	}
	if !strings.HasPrefix(got, `<b><a href="https://example.com/a">`) {
		t.Fatalf("link line not intact: %q", got)
	}
	if strings.Contains(got, "excerpt") {
		t.Fatalf("excerpt should have been dropped: %q", got)
	}
	if strings.Count(got, "\n\n") != 1 {
		t.Fatalf("expected only the source separator to remain: %q", got)
	}
}

// A title containing &, <, > and &#39; renders with no visible entity
// codes and no double decode of "&amp;lt;".
func TestMessage_AC18_EntityEscaping(t *testing.T) {
	tests := []struct {
		name      string
		raw       string
		wantClean string
		wantHTML  string
	}{
		{
			name:      "ampersand and angle brackets",
			raw:       "Fish &amp; Chips &lt;b&gt;bold&lt;/b&gt;",
			wantClean: "Fish & Chips <b>bold</b>",
			wantHTML:  "Fish &amp; Chips &lt;b&gt;bold&lt;/b&gt;",
		},
		{
			name:      "numeric apostrophe entity",
			raw:       "It&#39;s a &quot;test&quot;",
			wantClean: `It's a "test"`,
			wantHTML:  "It&#39;s a &#34;test&#34;",
		},
		{
			name:      "double escaped stays literal",
			raw:       "Danger: &amp;lt;script&amp;gt;alert(1)&amp;lt;/script&amp;gt;",
			wantClean: "Danger: &lt;script&gt;alert(1)&lt;/script&gt;",
			wantHTML:  "Danger: &amp;lt;script&amp;gt;alert(1)&amp;lt;/script&amp;gt;",
		},
		{
			name:      "raw markup in the title is stripped",
			raw:       "<b>Real</b> tags removed",
			wantClean: "Real tags removed",
			wantHTML:  "Real tags removed",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := CleanText(tc.raw); got != tc.wantClean {
				t.Fatalf("CleanText(%q) = %q, want %q", tc.raw, got, tc.wantClean)
			}
			it := model.FeedItem{Title: tc.raw, Link: "https://example.com/a"}
			got, _ := Message(it, "Example News", defaultOpts())
			want := `<b><a href="https://example.com/a">` + tc.wantHTML + "</a></b>\n\n<i>Example News</i>"
			if got != want {
				t.Fatalf("Message() =\n%q\nwant\n%q", got, want)
			}
			// The only markup left must be ours: no stray tag opener from the
			// item text can survive into the payload.
			body := strings.TrimPrefix(got, `<b><a href="https://example.com/a">`)
			body = strings.TrimSuffix(body, "</a></b>\n\n<i>Example News</i>")
			if strings.ContainsAny(body, "<>") {
				t.Fatalf("unescaped angle bracket leaked into the payload: %q", body)
			}
		})
	}
}

// An item with an empty description produces no stray blank line.
func TestMessage_AC19_EmptyExcerptNoBlankLine(t *testing.T) {
	tests := []struct {
		name    string
		excerpt string
	}{
		{"absent", ""},
		{"whitespace only", "   \n\t "},
		{"markup only", "<p></p><br/><div></div>"},
		{"nbsp only", "&nbsp;&nbsp;"},
		{"script only", "<script>var x = 1;</script>"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			it := model.FeedItem{Title: "Hello", Link: "https://example.com/a", Excerpt: tc.excerpt}
			got, _ := Message(it, "Example News", defaultOpts())
			want := "<b><a href=\"https://example.com/a\">Hello</a></b>\n\n<i>Example News</i>"
			if got != want {
				t.Fatalf("Message() =\n%q\nwant\n%q", got, want)
			}
			if strings.Contains(got, "\n\n\n") {
				t.Fatalf("stray blank line in %q", got)
			}
			if strings.Count(got, "\n\n") != 1 {
				t.Fatalf("expected exactly one blank line, got %q", got)
			}
		})
	}
}

// A missing link renders the title unlinked and still posts the item.
func TestMessage_FR24_MissingLink(t *testing.T) {
	tests := []struct {
		name string
		item model.FeedItem
	}{
		{"no link with excerpt", model.FeedItem{Title: "Orphan", Excerpt: "Body text."}},
		{"no link no excerpt", model.FeedItem{Title: "Orphan"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, unlinked := Message(tc.item, "Example News", defaultOpts())
			if !unlinked {
				t.Fatal("unlinked = false, want true for an item with no link")
			}
			if strings.Contains(got, "<a href=") {
				t.Fatalf("message must not contain an anchor: %q", got)
			}
			if !strings.HasPrefix(got, "<b>Orphan</b>") {
				t.Fatalf("title not rendered bold and unlinked: %q", got)
			}
		})
	}
}

func TestPlainMessage(t *testing.T) {
	tests := []struct {
		name string
		item model.FeedItem
		opt  Options
		want string
	}{
		{
			name: "link and excerpt",
			item: model.FeedItem{Title: "Fish &amp; Chips", Link: "https://example.com/a", Excerpt: "<p>Body &amp; more.</p>"},
			opt:  defaultOpts(),
			want: "Fish & Chips\nhttps://example.com/a\n\nBody & more.\n\nExample News",
		},
		{
			name: "no excerpt",
			item: model.FeedItem{Title: "Hello", Link: "https://example.com/a"},
			opt:  defaultOpts(),
			want: "Hello\nhttps://example.com/a\n\nExample News",
		},
		{
			name: "no link",
			item: model.FeedItem{Title: "Orphan", Excerpt: "Body."},
			opt:  defaultOpts(),
			want: "Orphan\n\nBody.\n\nExample News",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := PlainMessage(tc.item, "Example News", tc.opt)
			if got != tc.want {
				t.Fatalf("PlainMessage() =\n%q\nwant\n%q", got, tc.want)
			}
			if strings.ContainsAny(got, "<>") {
				t.Fatalf("plain fallback must carry no markup: %q", got)
			}
		})
	}
}

func TestPlainMessage_HardLimit(t *testing.T) {
	it := model.FeedItem{
		Title:   "Long article",
		Link:    "https://example.com/a",
		Excerpt: strings.Repeat("word ", 20000),
	}
	opt := Options{TitleMax: 200, ExcerptMax: 1 << 20, HardLimit: 4000}
	got := PlainMessage(it, "Example News", opt)
	if n := UTF16Len(got); n > opt.HardLimit {
		t.Fatalf("UTF16Len = %d, over hard limit %d", n, opt.HardLimit)
	}
	if !strings.HasPrefix(got, "Long article\nhttps://example.com/a\n\n") {
		t.Fatalf("title/link line damaged: %q", firstRunes(got, 80))
	}
}

func firstRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

func lastRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[len(r)-n:])
}
