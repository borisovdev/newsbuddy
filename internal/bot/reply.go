package bot

import "strings"

// Command syntax strings. Every usage hint and the /help body are built from
// these constants, so a hint can never drift from the documented syntax.
const (
	SyntaxStart  = "/start"
	SyntaxHelp   = "/help"
	SyntaxAdd    = "/add <feed_url> [display name]"
	SyntaxList   = "/list"
	SyntaxRemove = "/remove <id_or_url>"
	SyntaxStatus = "/status"
)

// Fixed reply texts whose exact wording is asserted by the acceptance
// criteria.
const (
	emptyListReply = "No sources yet. Add one with /add <feed_url>"
	unknownReply   = "Unknown command. Try /help"
	greeting       = "Hi! I relay RSS and Atom feeds into your channel."
)

// maxReplyUTF16 is the per-message budget for bot replies, counted in UTF-16
// units the way Telegram counts them. Replies longer than this are split
// across several messages; the single-message rule governs channel
// posts, not replies.
const maxReplyUTF16 = 4000

// command pairs one command's syntax with a one-line description.
type command struct {
	syntax string
	desc   string
}

// commands is the single source of truth for the /help body.
var commands = []command{
	{SyntaxStart, "greeting and this help"},
	{SyntaxHelp, "this help"},
	{SyntaxAdd, "subscribe to a feed; the display name is optional"},
	{SyntaxList, "show every source with its id and health"},
	{SyntaxRemove, "unsubscribe by the id from /list, or by feed URL"},
	{SyntaxStatus, "uptime, source count, and last poll cycle"},
}

// helpBody lists every command with its exact syntax.
func helpBody() string {
	var b strings.Builder
	b.WriteString("Commands:")
	for _, c := range commands {
		b.WriteString("\n")
		b.WriteString(c.syntax)
		b.WriteString(" — ")
		b.WriteString(c.desc)
	}
	return b.String()
}

// usageHint is the reply for a command called with missing or
// unparseable arguments.
func usageHint(syntax string) string { return "Usage: " + syntax }
