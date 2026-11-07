package bot_test

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/borisovdev/newsbuddy/internal/bot"
	"github.com/borisovdev/newsbuddy/internal/feed"
	"github.com/borisovdev/newsbuddy/internal/feed/feedtest"
	"github.com/borisovdev/newsbuddy/internal/logging"
	"github.com/borisovdev/newsbuddy/internal/render"
)

const feedURL = "https://example.com/feed"

// wantEmptyList is spelled out here rather than imported so a change to the
// empty-list wording fails this test instead of silently agreeing with itself.
const wantEmptyList = "No sources yet. Add one with /add <feed_url>"

var allSyntax = []string{
	bot.SyntaxStart, bot.SyntaxHelp, bot.SyntaxAdd,
	bot.SyntaxList, bot.SyntaxRemove, bot.SyntaxStatus,
}

func fixture(t *testing.T, url string) *feedtest.Response {
	t.Helper()
	res, err := feedtest.LoadFixture(filepath.Join("..", "..", "testdata", "rss2.xml"), url)
	if err != nil {
		t.Fatalf("LoadFixture: %v", err)
	}
	return &feedtest.Response{Result: res}
}

// scriptOK queues one successful fetch of the golden feed for url.
func (h *harness) scriptOK(url string) string {
	h.t.Helper()
	norm, err := feed.NormalizeURL(url)
	if err != nil {
		h.t.Fatalf("NormalizeURL(%q): %v", url, err)
	}
	h.fetch.AddResponse(norm, *fixture(h.t, norm))
	return norm
}

// ---- /add ------------------------------------------------------------

// /add with a valid feed replies with the assigned id and the resolved
// title, and /list then shows the source.
func TestAdd_Success(t *testing.T) {
	h := newHarness(t)
	h.scriptOK(feedURL)

	reply := h.only(h.send("/add " + feedURL))

	if !strings.Contains(reply, "#1") {
		t.Errorf("reply = %q, want the assigned id", reply)
	}
	if !strings.Contains(reply, "Golden Test Feed") {
		t.Errorf("reply = %q, want the resolved title", reply)
	}
	if !strings.Contains(reply, "Seeded 11") {
		t.Errorf("reply = %q, want the seeded count", reply)
	}

	listed := h.only(h.send("/list"))
	if !strings.Contains(listed, "Golden Test Feed") || !strings.Contains(listed, feedURL) {
		t.Errorf("/list = %q, want the new source", listed)
	}
}

func TestAdd_DisplayNameOverride(t *testing.T) {
	h := newHarness(t)
	h.scriptOK(feedURL)

	// The remainder after the first whitespace run is the display name, so a
	// name with spaces needs no quoting. The service collapses runs of
	// whitespace inside it.
	reply := h.only(h.send("/add " + feedURL + "   My Custom  Name"))

	if !strings.Contains(reply, "My Custom Name") {
		t.Errorf("reply = %q, want the display name taken from the remainder", reply)
	}
}

// /add with no argument is a usage hint, and the service is
// never called.
func TestAdd_NoArgument(t *testing.T) {
	h := newHarness(t)

	reply := h.only(h.send("/add"))

	if !strings.Contains(reply, bot.SyntaxAdd) {
		t.Errorf("reply = %q, want the /add usage hint", reply)
	}
	if h.fetch.CallCount() != 0 {
		t.Errorf("fetcher called %d times, want 0", h.fetch.CallCount())
	}
}

// Adding a URL that normalizes to an existing source is rejected with
// a message naming that source.
func TestAdd_Duplicate(t *testing.T) {
	h := newHarness(t)
	h.scriptOK(feedURL)

	if reply := h.only(h.send("/add " + feedURL)); !strings.Contains(reply, "#1") {
		t.Fatalf("first add: %q", reply)
	}

	reply := h.only(h.send("/add HTTPS://Example.COM/feed"))

	if reply != "Already added as #1: Golden Test Feed" {
		t.Errorf("reply = %q, want the duplicate rejection naming source 1", reply)
	}
	if h.fetch.CallCount() != 1 {
		t.Errorf("fetcher called %d times, want 1 (duplicate is rejected before fetching)", h.fetch.CallCount())
	}
}

// A 404 is rejected with the reason and adds nothing.
func TestAdd_FetchError(t *testing.T) {
	h := newHarness(t)
	norm, err := feed.NormalizeURL(feedURL)
	if err != nil {
		t.Fatalf("NormalizeURL: %v", err)
	}
	h.fetch.AddResponse(norm, feedtest.Response{
		Err: &feed.FetchError{Op: "status", StatusCode: 404},
	})

	reply := h.only(h.send("/add " + feedURL))

	if !strings.HasPrefix(reply, "Could not add:") || !strings.Contains(reply, "404") {
		t.Errorf("reply = %q, want a rejection naming the 404", reply)
	}
	if got := h.only(h.send("/list")); got != wantEmptyList {
		t.Errorf("/list = %q, want the empty state (nothing persisted)", got)
	}
}

// An unparseable body is rejected with the parse reason.
func TestAdd_ParseError(t *testing.T) {
	h := newHarness(t)
	norm, err := feed.NormalizeURL(feedURL)
	if err != nil {
		t.Fatalf("NormalizeURL: %v", err)
	}
	h.fetch.AddResponse(norm, feedtest.Response{
		Err: &feed.ParseError{ContentType: "text/html", Err: errors.New("expected element type <rss>")},
	})

	reply := h.only(h.send("/add " + feedURL))

	if !strings.HasPrefix(reply, "Could not add:") || !strings.Contains(reply, "not a valid RSS/Atom feed") {
		t.Errorf("reply = %q, want a rejection naming the parse reason", reply)
	}
	if got := h.only(h.send("/list")); got != wantEmptyList {
		t.Errorf("/list = %q, want the empty state (nothing persisted)", got)
	}
}

func TestAdd_BadURL(t *testing.T) {
	h := newHarness(t)

	reply := h.only(h.send("/add not-a-url"))

	if !strings.HasPrefix(reply, "Could not add:") {
		t.Errorf("reply = %q, want a rejection", reply)
	}
	if !strings.Contains(reply, bot.SyntaxAdd) {
		t.Errorf("reply = %q, want the usage hint appended", reply)
	}
}

// ---- /list -----------------------------------------------------------

// /list with zero sources is an explicit empty state, verbatim.
func TestList_EmptyState(t *testing.T) {
	h := newHarness(t)

	if reply := h.only(h.send("/list")); reply != wantEmptyList {
		t.Errorf("reply = %q, want %q", reply, wantEmptyList)
	}
}

func TestList_ShowsIDTitleURLAndLastSuccess(t *testing.T) {
	h := newHarness(t)
	h.scriptOK(feedURL)
	h.send("/add " + feedURL)

	reply := h.only(h.send("/list"))

	if !strings.HasPrefix(reply, "1. Golden Test Feed — "+feedURL) {
		t.Errorf("reply = %q, want the id/title/url line", reply)
	}
	if !strings.Contains(reply, "last ok: ") || strings.Contains(reply, "last ok: never") {
		t.Errorf("reply = %q, want a last-success timestamp", reply)
	}
}

// A long list is split into several messages, each within the 4000 UTF-16
// unit reply budget.
func TestList_ChunksLongReplies(t *testing.T) {
	h := newHarness(t)

	const sources = 10
	longPath := strings.Repeat("a", 500)
	for i := 0; i < sources; i++ {
		h.scriptOK(fmt.Sprintf("https://example.com/%s/%d", longPath, i))
	}
	for i := 0; i < sources; i++ {
		h.send(fmt.Sprintf("/add https://example.com/%s/%d", longPath, i))
	}

	replies := h.send("/list")

	if len(replies) < 2 {
		t.Fatalf("got %d replies, want the list split across several messages", len(replies))
	}
	for i, r := range replies {
		if n := render.UTF16Len(r); n > 4000 {
			t.Errorf("chunk %d is %d UTF-16 units, want <= 4000", i, n)
		}
	}
	joined := strings.Join(replies, "\n")
	for i := 1; i <= sources; i++ {
		if !strings.Contains(joined, fmt.Sprintf("\n%d. ", i)) && !strings.HasPrefix(joined, fmt.Sprintf("%d. ", i)) {
			t.Errorf("source %d missing from the chunked list", i)
		}
	}
}

// ---- /remove ---------------------------------------------------------

// /remove by id removes the source and /list no longer shows it.
func TestRemove_ByID(t *testing.T) {
	h := newHarness(t)
	h.scriptOK(feedURL)
	h.send("/add " + feedURL)

	reply := h.only(h.send("/remove 1"))

	if !strings.Contains(reply, "#1") || !strings.Contains(reply, "Golden Test Feed") {
		t.Errorf("reply = %q, want the removed id and title", reply)
	}
	if got := h.only(h.send("/list")); got != wantEmptyList {
		t.Errorf("/list = %q, want the empty state after removal", got)
	}
}

func TestRemove_ByURL(t *testing.T) {
	h := newHarness(t)
	h.scriptOK(feedURL)
	h.send("/add " + feedURL)

	reply := h.only(h.send("/remove " + feedURL))

	if !strings.Contains(reply, "#1") {
		t.Errorf("reply = %q, want the removed id", reply)
	}
	if got := h.only(h.send("/list")); got != wantEmptyList {
		t.Errorf("/list = %q, want the empty state after removal", got)
	}
}

// Removing an unknown id replies with exactly this text.
func TestRemove_UnknownID(t *testing.T) {
	h := newHarness(t)

	if reply := h.only(h.send("/remove 9999")); reply != "No source with id 9999" {
		t.Errorf("reply = %q, want %q", reply, "No source with id 9999")
	}
}

func TestRemove_UnknownURL(t *testing.T) {
	h := newHarness(t)

	reply := h.only(h.send("/remove https://nope.example.com/feed"))

	if !strings.HasPrefix(reply, "No source with feed URL") {
		t.Errorf("reply = %q, want a not-found reply", reply)
	}
}

func TestRemove_NoArgument(t *testing.T) {
	h := newHarness(t)

	if reply := h.only(h.send("/remove")); !strings.Contains(reply, bot.SyntaxRemove) {
		t.Errorf("reply = %q, want the /remove usage hint", reply)
	}
}

// ---- /status ---------------------------------------------------------

func TestStatus_ReportsRuntimeFactsAndNoConfig(t *testing.T) {
	h := newHarness(t)
	h.scriptOK(feedURL)
	h.send("/add " + feedURL)

	reply := h.only(h.send("/status"))

	for _, want := range []string{"Uptime: ", "Sources: 1", "Posted in last 24h: 0", "Last poll cycle: never"} {
		if !strings.Contains(reply, want) {
			t.Errorf("reply = %q, missing %q", reply, want)
		}
	}
	// No configuration value, and above all no token, ever reaches a
	// reply.
	if strings.Contains(reply, secretToken) || strings.Contains(reply, "@secret-channel") {
		t.Errorf("reply leaked configuration: %q", reply)
	}
}

// ---- /start and /help ------------------------------------------------

func TestHelp_ListsEveryCommand(t *testing.T) {
	h := newHarness(t)

	reply := h.only(h.send("/help"))

	for _, syntax := range allSyntax {
		if !strings.Contains(reply, syntax) {
			t.Errorf("/help = %q, missing %q", reply, syntax)
		}
	}
}

func TestStart_GreetsAndRepeatsHelp(t *testing.T) {
	h := newHarness(t)

	start := h.only(h.send("/start"))
	help := h.only(h.send("/help"))

	if !strings.HasSuffix(start, help) {
		t.Errorf("/start = %q, want it to end with the /help body %q", start, help)
	}
	if start == help {
		t.Error("/start is missing its greeting")
	}
}

// ---- logging ---------------------------------------------------------

// Source.added and source.removed are logged for each management
// action.
func TestManagementActionsAreLogged(t *testing.T) {
	h := newHarness(t)
	h.scriptOK(feedURL)

	h.send("/add " + feedURL)
	h.send("/remove 1")

	if n := h.logs.countEvent(logging.EvSourceAdded); n != 1 {
		t.Errorf("%s lines = %d, want 1", logging.EvSourceAdded, n)
	}
	if n := h.logs.countEvent(logging.EvSourceRemoved); n != 1 {
		t.Errorf("%s lines = %d, want 1", logging.EvSourceRemoved, n)
	}
}
