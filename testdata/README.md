# testdata/ — feed fixtures

Each file is minimal but *hostile in exactly one way*. Tests depend on their
exact contents, so add a new fixture rather than editing an existing one.

## Path convention

Consumers reference these files by relative path from their own package
directory:

- From `internal/feed`: `../../testdata/<file>`
- From `internal/app/apptest`: `../../../testdata/<file>`

## Fixtures

| File | What it exercises |
|---|---|
| `rss2.xml` | Well-formed RSS 2.0 feed with 11 items, so a test can seed 10+ items without posting. |
| `rss1_rdf.xml` | Valid RSS 1.0 / RDF feed: the parser handles RDF, not just RSS 2.0 and Atom. |
| `atom.xml` | Well-formed Atom feed with 11 entries: Atom parsing and seeding 10+ items without posting. |
| `no_guid.xml` | Items without `<guid>`, forcing the link-based key fallback. |
| `no_guid_no_link.xml` | One item with no guid, no link and no title, which is unusable and must be skipped with a `warn`, plus one normal item to prove the rest of the feed still parses. |
| `duplicate_guids.xml` | The same `<guid>` on 3 items in a known order: first occurrence wins. |
| `empty_items.xml` | Structurally valid feed with zero `<item>` elements: a successful outcome, not an error. |
| `malformed.xml` | Syntactically broken XML (unclosed `<title>`) that must never cause a panic. |
| `windows1251.xml` | Declares `encoding="windows-1251"` and is byte-encoded in real windows-1251 with Cyrillic content: charset-aware decoding. |
| `cdata_full_html.xml` | `<content:encoded>` with ~50 KB of HTML (`<p>`/`<br>`), large enough that the rendered message must be trimmed to at most 4000 UTF-16 units. |
| `relative_links.xml` | Relative item `<link>` values (`/articles/123`, `articles/456`) that need base-URL resolution. |
| `entities.xml` | A title whose raw XML contains `&amp;`, `&lt;`, `&gt;` and `&amp;#39;`. After a single XML unescape the title is `Tom & Jerry: <script> and &#39;quotes&#39;`: entities are decoded once, never twice. |
| `astral_emoji.xml` | Astral-plane emoji (`😀🎉`) in the title and near the ~200 character mark of the description: truncation never splits a surrogate pair. |
| `html_page.html` | An ordinary HTML page, not a feed: `/add` rejects it with a clear parse-failure reason. |
| `future_dates.xml` | Items with `pubDate` far in the future, which are still posted normally because dedup uses the seen set, not dates. |

## Verifying `windows1251.xml`

```sh
file testdata/windows1251.xml   # reports non-UTF-8 / ISO-8859-style single-byte text
iconv -f WINDOWS-1251 -t UTF-8 testdata/windows1251.xml   # renders correct Cyrillic
```
