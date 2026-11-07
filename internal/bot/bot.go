// Package bot implements the owner-only Telegram command surface. It deliberately does NOT construct the
// *tgbot.Bot: middlewares and handlers are construction-time-only options in
// github.com/go-telegram/bot, so the handlers must exist before the single
// shared instance is built. This package therefore exports the option list
// and lets the composition root call tgbot.New and Start.
package bot

import (
	"context"
	"log/slog"
	"strings"
	"time"
	"unicode"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/borisovdev/newsbuddy/internal/config"
	"github.com/borisovdev/newsbuddy/internal/logging"
	"github.com/borisovdev/newsbuddy/internal/poller"
	"github.com/borisovdev/newsbuddy/internal/service"
)

// Handlers owns the command implementations and their collaborators.
type Handlers struct {
	svc *service.SourceService
	p   *poller.Poller
	cfg config.Config
	log *slog.Logger

	// startedAt is stamped at construction time and is what /status reports
	// as the process uptime origin.
	startedAt time.Time
}

// NewHandlers returns the command handlers. The uptime shown by /status is
// measured from this call, so the composition root should build the handlers
// as part of startup rather than lazily.
func NewHandlers(svc *service.SourceService, p *poller.Poller, cfg config.Config, log *slog.Logger) *Handlers {
	if log == nil {
		log = slog.Default()
	}
	return &Handlers{svc: svc, p: p, cfg: cfg, log: log, startedAt: time.Now()}
}

// Options returns the full construction-time option list for tgbot.New: the
// owner-only middleware, one message-text handler per command, the
// default handler, and an errors handler for library-level failures.
func Options(h *Handlers) []tgbot.Option {
	return []tgbot.Option{
		tgbot.WithMiddlewares(h.ownerOnly()),
		tgbot.WithDefaultHandler(h.unknown),
		// MatchTypeCommand compares the entity text with the leading slash
		// already stripped, so the patterns here carry no slash.
		tgbot.WithMessageTextHandler("start", tgbot.MatchTypeCommand, h.start),
		tgbot.WithMessageTextHandler("help", tgbot.MatchTypeCommand, h.help),
		tgbot.WithMessageTextHandler("add", tgbot.MatchTypeCommand, h.add),
		tgbot.WithMessageTextHandler("list", tgbot.MatchTypeCommand, h.list),
		tgbot.WithMessageTextHandler("remove", tgbot.MatchTypeCommand, h.remove),
		tgbot.WithMessageTextHandler("status", tgbot.MatchTypeCommand, h.status),
		tgbot.WithErrorsHandler(func(err error) {
			h.log.Error(logging.EvBotError, "error", err.Error())
		}),
	}
}

// ownerOnly is the single gate in front of every handler, rather than a
// check repeated in six of them.
func (h *Handlers) ownerOnly() tgbot.Middleware {
	return func(next tgbot.HandlerFunc) tgbot.HandlerFunc {
		return func(ctx context.Context, b *tgbot.Bot, update *models.Update) {
			// Channel posts, edited messages and callbacks carry no
			// Message: drop them silently.
			if update == nil || update.Message == nil {
				return
			}
			msg := update.Message

			// Commands are accepted in the bot's private chat only.
			if msg.Chat.Type != models.ChatTypePrivate {
				return
			}

			if msg.From == nil || msg.From.ID != h.cfg.TelegramOwnerID {
				var uid int64
				if msg.From != nil {
					uid = msg.From.ID
				}
				// Exactly one warn line, no reply, no next.
				h.log.WarnContext(ctx, logging.EvCommandRejected,
					"reason", "not_owner", "user_id", uid)
				return
			}

			h.log.InfoContext(ctx, logging.EvCommandReceived,
				"command", firstToken(msg.Text), "user_id", msg.From.ID)
			next(ctx, b, update)
		}
	}
}

// firstToken returns the leading whitespace-delimited token of s.
func firstToken(s string) string {
	t := strings.TrimSpace(s)
	if i := strings.IndexFunc(t, unicode.IsSpace); i >= 0 {
		return t[:i]
	}
	return t
}

// argument returns everything after the first whitespace run in s, trimmed.
// Applied to a message text it yields the command's arguments; applied again
// to those arguments it yields everything after the first one.
func argument(s string) string {
	t := strings.TrimSpace(s)
	i := strings.IndexFunc(t, unicode.IsSpace)
	if i < 0 {
		return ""
	}
	return strings.TrimSpace(t[i:])
}
