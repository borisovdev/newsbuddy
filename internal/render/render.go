// Package render turns a parsed feed item into a Telegram message body.
//
// It is a pure package: no logging, no I/O, no clock, no configuration —
// a function of (item, source title, options) -> string.
package render

import (
	"html"
	"strings"

	"github.com/borisovdev/newsbuddy/internal/model"
)

// Options carries the rendering limits.
type Options struct {
	TitleMax   int // 200 runes
	ExcerptMax int // cfg.ExcerptLen, default 300 runes
	HardLimit  int // 4000 UTF-16 code units
}

const (
	untitled  = "(untitled)"
	sourceMax = 64
)

// Message renders it as a Telegram HTML message body (parse_mode=HTML):
//
//	<b><a href="{link}">{title}</a></b>
//
//	{excerpt}
//
//	<i>{source}</i>
//
// The excerpt block and its blank line are omitted when the cleaned excerpt is
// empty. When it.Link is empty the <a> wrapper is dropped, the
// item is still rendered, and unlinked is true so the caller can log a warn
// . All text is cleaned first and HTML-escaped afterwards, and
// the result is guaranteed to be at most opt.HardLimit UTF-16 code units
// — never split into multiple messages.
func Message(it model.FeedItem, sourceTitle string, opt Options) (string, bool) {
	title, excerpt, source := parts(it, sourceTitle, opt)
	href := it.Link
	build := func(t, e string) string { return assembleHTML(t, e, source, href) }
	return fit(build, []rune(title), []rune(excerpt), opt.HardLimit), href == ""
}

// PlainMessage renders the same content with no HTML tags and no escaping, for
// the parse_mode-less retry after Telegram's "can't parse entities" 400.
func PlainMessage(it model.FeedItem, sourceTitle string, opt Options) string {
	title, excerpt, source := parts(it, sourceTitle, opt)
	href := it.Link
	build := func(t, e string) string { return assemblePlain(t, e, source, href) }
	return fit(build, []rune(title), []rune(excerpt), opt.HardLimit)
}

// parts cleans and truncates the three text fields.
func parts(it model.FeedItem, sourceTitle string, opt Options) (title, excerpt, source string) {
	title = truncateWords(CleanText(it.Title), opt.TitleMax)
	if title == "" {
		title = untitled
	}
	excerpt = truncateWords(CleanText(it.Excerpt), opt.ExcerptMax)
	source = truncateWords(CleanText(sourceTitle), sourceMax)
	return title, excerpt, source
}

// assembleHTML lays out the escaped HTML message. Escaping happens here, on
// every field including the href, so length is always measured on the final
// wire form.
func assembleHTML(title, excerpt, source, href string) string {
	var b strings.Builder
	b.WriteString("<b>")
	if href != "" {
		b.WriteString(`<a href="`)
		b.WriteString(html.EscapeString(href))
		b.WriteString(`">`)
	}
	b.WriteString(html.EscapeString(title))
	if href != "" {
		b.WriteString("</a>")
	}
	b.WriteString("</b>")
	if excerpt != "" {
		b.WriteString("\n\n")
		b.WriteString(html.EscapeString(excerpt))
	}
	if source != "" {
		b.WriteString("\n\n<i>")
		b.WriteString(html.EscapeString(source))
		b.WriteString("</i>")
	}
	return b.String()
}

// assemblePlain lays out the same content without markup. The bare URL keeps
// the article reachable in the fallback message.
func assemblePlain(title, excerpt, source, href string) string {
	var b strings.Builder
	b.WriteString(title)
	if href != "" {
		b.WriteString("\n")
		b.WriteString(href)
	}
	if excerpt != "" {
		b.WriteString("\n\n")
		b.WriteString(excerpt)
	}
	if source != "" {
		b.WriteString("\n\n")
		b.WriteString(source)
	}
	return b.String()
}

// fit applies the hard length guard: trim the excerpt
// until the fully assembled, fully escaped message fits; if even an empty
// excerpt does not fit, drop the excerpt and shrink the title. The title+link
// line is never dropped and the href is never truncated.
func fit(build func(title, excerpt string) string, titleRunes, excerptRunes []rune, limit int) string {
	full := build(string(titleRunes), string(excerptRunes))
	if limit <= 0 || utf16Len(full) <= limit {
		return full
	}
	if s, ok := searchFit(func(n int) string {
		return build(string(titleRunes), prefixOf(excerptRunes, n))
	}, len(excerptRunes), limit); ok {
		return s
	}
	if s, ok := searchFit(func(n int) string {
		return build(prefixOf(titleRunes, n), "")
	}, len(titleRunes), limit); ok {
		return s
	}
	return build(prefixOf(titleRunes, 1), "")
}

// searchFit binary-searches the largest n in [0, hi] whose assembled message
// is within limit UTF-16 code units, returning that message.
func searchFit(build func(n int) string, hi, limit int) (string, bool) {
	lo, found := 0, false
	var best string
	for lo <= hi {
		mid := lo + (hi-lo)/2
		s := build(mid)
		if utf16Len(s) <= limit {
			best, found = s, true
			lo = mid + 1
		} else {
			hi = mid - 1
		}
	}
	return best, found
}
