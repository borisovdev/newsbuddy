package render

import "testing"

func TestCleanText(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"plain", "  hello   world \n", "hello world"},
		{"strips tags", "<p>hello <em>there</em></p>", "hello there"},
		{"strips script and contents", `a<script>var x = "<b>";</script>b`, "a b"},
		{"strips style and contents", "a<style>p{color:red}</style>b", "a b"},
		{"nbsp collapses", "a&nbsp;&nbsp;b", "a b"},
		{"attributes on stripped tags", `<a href="http://x/">link</a>`, "link"},
		{
			// Steps 3 and 4 in order: tags are stripped before the single
			// unescape, so escaped markup survives as literal text.
			name: "escaped markup survives as text",
			in:   "&lt;b&gt;bold&lt;/b&gt;",
			want: "<b>bold</b>",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := CleanText(tc.in); got != tc.want {
				t.Fatalf("CleanText(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// Edge 16: entities are decoded exactly once. "&amp;lt;" must stay the literal
// text "&lt;" and must never become "<".
func TestCleanText_Edge16_SingleDecode(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"double-escaped lt", "&amp;lt;script&amp;gt;", "&lt;script&gt;"},
		{"double-escaped amp", "a &amp;amp; b", "a &amp; b"},
		{"single decode of ampersand", "Fish &amp; Chips", "Fish & Chips"},
		{"numeric apostrophe", "it&#39;s", "it's"},
		{"numeric right quote", "don&#8217;t", "don’t"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := CleanText(tc.in)
			if got != tc.want {
				t.Fatalf("CleanText(%q) = %q, want %q", tc.in, got, tc.want)
			}
			// Re-running must be idempotent in the sense that our own single
			// pass never produced a raw tag opener from a double-escaped one.
			if tc.name == "double-escaped lt" && got == "<script>" {
				t.Fatal("double decode: &amp;lt; became a live tag opener")
			}
		})
	}
}

// Edge 17: CDATA-wrapped full HTML — block boundaries become whitespace so
// words do not run together.
func TestCleanText_Edge17_BlockTagsBecomeSpaces(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"br", "one<br>two", "one two"},
		{"br self closing", "one<br/>two", "one two"},
		{"br spaced self closing", "one<br />two", "one two"},
		{"paragraphs", "<p>one</p><p>two</p>", "one two"},
		{"divs", "<div>one</div><div>two</div>", "one two"},
		{"list items", "<ul><li>one</li><li>two</li></ul>", "one two"},
		{"table cells", "<tr><td>one</td><td>two</td></tr>", "one two"},
		{"headings", "<h1>one</h1><h2>two</h2>", "one two"},
		{"block tag with attributes", `<p class="lead">one</p><p>two</p>`, "one two"},
		{"inline tags still join", "one<em>two</em>", "onetwo"},
		{
			name: "full article",
			in:   "<div><h1>Title</h1><p>First para.</p><p>Second <b>para</b>.<br>Tail</p></div>",
			want: "Title First para. Second para. Tail",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := CleanText(tc.in); got != tc.want {
				t.Fatalf("CleanText(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
