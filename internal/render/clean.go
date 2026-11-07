package render

import (
	"html"
	"regexp"
	"strings"
)

var (
	// <script>…</script> / <style>…</style> including their contents.
	reScriptStyle = regexp.MustCompile(`(?is)<\s*(script|style)\b[^>]*>.*?<\s*/\s*(script|style)\s*>`)
	// Block-level boundaries that must become whitespace so words do not run
	// together once the surrounding tags are stripped.
	reBlockTags = regexp.MustCompile(`(?i)<\s*/?\s*(br|p|div|li|tr|td|h[1-6])\b[^>]*>`)
	// Any remaining tag.
	reAnyTag = regexp.MustCompile(`(?s)<[^>]*>`)
)

// CleanText normalizes feed-supplied HTML into plain text. The step order is
// load-bearing:
//
//  1. drop <script>/<style> and their contents;
//  2. turn block-level boundaries into a single space;
//  3. strip every remaining tag;
//  4. decode HTML entities exactly once — never twice, so "&amp;lt;" stays
//     the literal text "&lt;" and cannot reopen the injection path;
//  5. collapse whitespace runs to one ASCII space and trim.
//
// Steps 3 and 4 are in this order deliberately: unescaping first would turn
// "&lt;b&gt;" into "<b>", which step 3 would then delete as a tag.
func CleanText(s string) string {
	if s == "" {
		return ""
	}
	s = reScriptStyle.ReplaceAllString(s, " ")
	s = reBlockTags.ReplaceAllString(s, " ")
	s = reAnyTag.ReplaceAllString(s, "")
	s = html.UnescapeString(s)
	return strings.Join(strings.Fields(s), " ")
}
