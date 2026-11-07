package bot

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/borisovdev/newsbuddy/internal/logging"
	"github.com/borisovdev/newsbuddy/internal/model"
	"github.com/borisovdev/newsbuddy/internal/render"
	"github.com/borisovdev/newsbuddy/internal/service"
	// Imported for the ErrNotFound sentinel only, so /remove can tell "no
	// such source" apart from a real storage failure.
	"github.com/borisovdev/newsbuddy/internal/storage"
)

// lastErrorMax is the /list truncation budget for a source's last error.
const lastErrorMax = 200

// ---- handlers --------------------------------------------------------

func (h *Handlers) start(ctx context.Context, b *tgbot.Bot, update *models.Update) {
	h.reply(ctx, b, update, greeting+"\n\n"+helpBody())
}

func (h *Handlers) help(ctx context.Context, b *tgbot.Bot, update *models.Update) {
	h.reply(ctx, b, update, helpBody())
}

// unknown is the default handler: an unrecognized command is answered
// with a usage hint, never with silence.
func (h *Handlers) unknown(ctx context.Context, b *tgbot.Bot, update *models.Update) {
	h.reply(ctx, b, update, unknownReply+"\n\n"+helpBody())
}

// add implements /add <feed_url> [display name].
func (h *Handlers) add(ctx context.Context, b *tgbot.Bot, update *models.Update) {
	arg := argument(update.Message.Text)
	if arg == "" {
		h.reply(ctx, b, update, usageHint(SyntaxAdd))
		return
	}
	// Token 1 is the URL, the trimmed remainder is the optional display
	// name, so a name with spaces needs no quoting.
	feedURL := firstToken(arg)
	displayName := argument(arg)

	src, seeded, err := h.svc.Add(ctx, feedURL, displayName)
	if err != nil {
		var badURL *service.ErrBadURL
		var dup *service.ErrDuplicateSource
		switch {
		case errors.As(err, &badURL):
			h.reply(ctx, b, update, "Could not add: "+badURL.Error()+"\n"+usageHint(SyntaxAdd))
		case errors.As(err, &dup):
			h.reply(ctx, b, update, fmt.Sprintf("Already added as #%d: %s", dup.ExistingID, dup.ExistingTitle))
		default:
			// A feed.FetchError or feed.ParseError carries its own reason.
			h.reply(ctx, b, update, "Could not add: "+err.Error())
		}
		return
	}

	h.reply(ctx, b, update, fmt.Sprintf(
		"Added #%d: %s\nSeeded %d existing items (they will not be posted).",
		src.ID, src.Title, seeded))
}

// list implements /list, including the explicit empty state and the
// multi-message split for long lists.
func (h *Handlers) list(ctx context.Context, b *tgbot.Bot, update *models.Update) {
	sources, err := h.svc.List(ctx)
	if err != nil {
		h.reply(ctx, b, update, "Could not list sources: "+err.Error())
		return
	}
	if len(sources) == 0 {
		h.reply(ctx, b, update, emptyListReply)
		return
	}

	entries := make([]string, 0, len(sources))
	for _, s := range sources {
		entries = append(entries, formatSource(s))
	}
	for _, chunk := range chunkEntries(entries, maxReplyUTF16) {
		h.reply(ctx, b, update, chunk)
	}
}

// remove implements /remove <id|url>.
func (h *Handlers) remove(ctx context.Context, b *tgbot.Bot, update *models.Update) {
	arg := argument(update.Message.Text)
	if arg == "" {
		h.reply(ctx, b, update, usageHint(SyntaxRemove))
		return
	}

	src, err := h.svc.Remove(ctx, arg)
	if err != nil {
		var badURL *service.ErrBadURL
		switch {
		case errors.Is(err, storage.ErrNotFound):
			if id, perr := strconv.ParseInt(arg, 10, 64); perr == nil {
				h.reply(ctx, b, update, fmt.Sprintf("No source with id %d", id))
			} else {
				h.reply(ctx, b, update, "No source with feed URL "+arg)
			}
		case errors.As(err, &badURL):
			h.reply(ctx, b, update, "Could not remove: "+badURL.Error()+"\n"+usageHint(SyntaxRemove))
		default:
			h.reply(ctx, b, update, "Could not remove: "+err.Error())
		}
		return
	}

	h.reply(ctx, b, update, fmt.Sprintf("Removed #%d: %s", src.ID, src.Title))
}

// status implements /status. It reports runtime facts only and never prints
// any configuration value, so there is no path by which the token could
// reach a reply.
func (h *Handlers) status(ctx context.Context, b *tgbot.Bot, update *models.Update) {
	st, err := h.svc.Status(ctx, h.startedAt, h.p.Snapshot())
	if err != nil {
		h.reply(ctx, b, update, "Could not read status: "+err.Error())
		return
	}
	h.reply(ctx, b, update, formatStatus(st))
}

// ---- formatting ------------------------------------------------------

// formatSource renders one /list entry.
func formatSource(s model.Source) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d. %s — %s\n     last ok: %s", s.ID, s.Title, s.FeedURL, formatTime(s.LastSuccessAt))
	if s.ConsecutiveFailures > 0 {
		fmt.Fprintf(&b, "\n     ⚠ %d failures: %s",
			s.ConsecutiveFailures, truncateRunes(s.LastError, lastErrorMax))
	}
	return b.String()
}

// formatStatus renders the /status reply.
func formatStatus(s model.Status) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Uptime: %s\n", time.Since(s.StartedAt).Round(time.Second))
	fmt.Fprintf(&b, "Sources: %d\n", s.SourceCount)
	fmt.Fprintf(&b, "Posted in last 24h: %d\n", s.PostedLast24h)
	if s.LastCycleAt == nil {
		b.WriteString("Last poll cycle: never")
	} else {
		fmt.Fprintf(&b, "Last poll cycle: %s (%d ms, %d errors)",
			formatTime(s.LastCycleAt), s.LastCycleDuration.Milliseconds(), s.LastCycleErrors)
	}
	if s.LastPublishError != "" {
		fmt.Fprintf(&b, "\nLast publish error: %s", truncateRunes(s.LastPublishError, lastErrorMax))
	}
	return b.String()
}

func formatTime(t *time.Time) string {
	if t == nil || t.IsZero() {
		return "never"
	}
	return t.UTC().Format(time.RFC3339)
}

func truncateRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max])
}

// chunkEntries groups entries into newline-joined messages that each stay
// within limit UTF-16 units, the way Telegram counts message length. Bot
// replies carry no parse_mode, so a chunk boundary can never split markup.
func chunkEntries(entries []string, limit int) []string {
	var (
		out []string
		cur strings.Builder
	)
	for _, e := range entries {
		e = clampUTF16(e, limit)
		if cur.Len() > 0 && render.UTF16Len(cur.String())+1+render.UTF16Len(e) > limit {
			out = append(out, cur.String())
			cur.Reset()
		}
		if cur.Len() > 0 {
			cur.WriteString("\n")
		}
		cur.WriteString(e)
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

// clampUTF16 cuts s to at most limit UTF-16 units, always on a rune
// boundary so a surrogate pair is never split.
func clampUTF16(s string, limit int) string {
	if render.UTF16Len(s) <= limit {
		return s
	}
	n := 0
	for i, r := range s {
		w := 1
		if r > 0xFFFF {
			w = 2
		}
		if n+w > limit {
			return s[:i]
		}
		n += w
	}
	return s
}

// ---- sending ---------------------------------------------------------

// reply sends text back to the chat the update came from, as plain text with
// no parse_mode: a feed title containing "<" or "&" can therefore never
// break a reply the way it could break a parse_mode=HTML channel post.
func (h *Handlers) reply(ctx context.Context, b *tgbot.Bot, update *models.Update, text string) {
	if _, err := b.SendMessage(ctx, &tgbot.SendMessageParams{
		ChatID: update.Message.Chat.ID,
		Text:   text,
	}); err != nil {
		h.log.ErrorContext(ctx, logging.EvBotError, "error", err.Error())
	}
}
