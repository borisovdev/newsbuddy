package feed

import (
	"bytes"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/mmcdole/gofeed"

	"github.com/borisovdev/newsbuddy/internal/model"
)

// GofeedParser implements Parser on top of github.com/mmcdole/gofeed.
// Charset handling comes for free: gofeed decodes via
// golang.org/x/net/html/charset, honoring the declared XML prolog encoding.
type GofeedParser struct{ fp *gofeed.Parser }

// NewGofeedParser builds a parser safe for concurrent use by the poll
// workers. The translators are assigned eagerly because gofeed otherwise
// creates them lazily on first Parse, which would be a data race.
func NewGofeedParser() *GofeedParser {
	fp := gofeed.NewParser()
	fp.UserAgent = UserAgent()
	fp.RSSTranslator = &gofeed.DefaultRSSTranslator{}
	fp.AtomTranslator = &gofeed.DefaultAtomTranslator{}
	fp.JSONTranslator = &gofeed.DefaultJSONTranslator{}
	return &GofeedParser{fp: fp}
}

// Parse implements Parser. Zero items is a valid, successful result.
func (p *GofeedParser) Parse(data []byte, baseURL string) (*model.Feed, error) {
	parsed, _, err := p.ParseReport(data, baseURL)
	return parsed, err
}

// ParseReport is Parse plus the document-order indexes of items that were
// skipped as unusable, so the caller can log one warn each.
func (p *GofeedParser) ParseReport(data []byte, baseURL string) (*model.Feed, []int, error) {
	gf, err := p.parseSafe(data)
	if err != nil {
		return nil, nil, &ParseError{Snippet: snippet(data), Err: err}
	}
	if gf == nil {
		return nil, nil, &ParseError{Snippet: snippet(data), Err: errors.New("parser returned no feed")}
	}

	base := firstNonEmpty(gf.FeedLink, gf.Link, baseURL)

	var skipped []int
	items := make([]model.FeedItem, 0, len(gf.Items))
	// Intra-feed dedup: a []string plus a linear scan, deliberately
	// not a map.
	seen := make([]string, 0, len(gf.Items))

	for i, it := range gf.Items {
		if it == nil {
			skipped = append(skipped, i)
			continue
		}
		link := resolveLink(base, it.Link)
		key, ok := ItemKey(it.GUID, keyLink(link), it.Title, it.Published)
		if !ok {
			skipped = append(skipped, i)
			continue
		}
		if slices.Contains(seen, key) { // first occurrence wins
			continue
		}
		seen = append(seen, key)
		items = append(items, model.FeedItem{
			Key:       key,
			Title:     it.Title,
			Link:      link,
			Excerpt:   firstNonEmpty(it.Description, it.Content),
			Published: publishedAt(it),
			Order:     i,
		})
	}

	return &model.Feed{Title: gf.Title, Link: gf.Link, Items: items}, skipped, nil
}

// parseSafe wraps gofeed in a recover: a malformed feed must never take down
// the poll goroutine.
func (p *GofeedParser) parseSafe(data []byte) (gf *gofeed.Feed, err error) {
	defer func() {
		if r := recover(); r != nil {
			gf = nil
			err = fmt.Errorf("panic in feed parser: %v", r)
		}
	}()
	return p.fp.Parse(bytes.NewReader(data))
}

// resolveLink resolves a possibly relative item link against the feed base
// . Anything that fails to resolve, or is not http(s), becomes ""
// so the item renders unlinked rather than failing the whole feed.
func resolveLink(base, raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	resolved, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	if b, berr := url.Parse(strings.TrimSpace(base)); berr == nil && b.IsAbs() {
		resolved = b.ResolveReference(resolved)
	}
	switch strings.ToLower(resolved.Scheme) {
	case "http", "https":
		return resolved.String()
	default:
		return ""
	}
}

// keyLink normalizes the resolved link for key derivation.
// When normalization fails the trimmed link is used as-is: a stable imperfect
// key beats a hash over a possibly-unstable title.
func keyLink(link string) string {
	if link == "" {
		return ""
	}
	if normalized, err := NormalizeURL(link); err == nil {
		return normalized
	}
	return strings.TrimSpace(link)
}

// publishedAt prefers PublishedParsed, falls back to UpdatedParsed, else nil.
func publishedAt(it *gofeed.Item) *time.Time {
	if it.PublishedParsed != nil {
		return it.PublishedParsed
	}
	if it.UpdatedParsed != nil {
		return it.UpdatedParsed
	}
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
