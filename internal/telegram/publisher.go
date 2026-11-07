package telegram

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/borisovdev/newsbuddy/internal/config"
	"github.com/borisovdev/newsbuddy/internal/logging"
)

// Publisher posts pre-rendered HTML to the destination channel.
type Publisher interface {
	Send(ctx context.Context, html string) (messageID int64, err error)
}

// ErrForbidden indicates the bot no longer has permission to post to the
// destination channel (removed, kicked, etc). Callers must not mark the
// item posted when they see this error.
var ErrForbidden = errors.New("telegram: forbidden (bot removed from channel?)")

// BotPublisher implements Publisher using an injected *tgbot.Bot. The bot
// itself is constructed elsewhere (internal/app, T17) with its command
// handlers already attached at construction time; this package never calls
// tgbot.New.
type BotPublisher struct {
	b      *tgbot.Bot
	chatID any
	pacer  *Pacer
	cfg    config.Config
	log    *slog.Logger
}

// NewBotPublisher returns a BotPublisher that posts to cfg.TelegramChannelID,
// bound once at construction and never passed per call.
func NewBotPublisher(b *tgbot.Bot, cfg config.Config, log *slog.Logger) *BotPublisher {
	return &BotPublisher{
		b:      b,
		chatID: cfg.TelegramChannelID,
		pacer:  NewPacer(cfg.PostInterval),
		cfg:    cfg,
		log:    log,
	}
}

// Send posts html to the bound channel with ParseMode HTML and link
// previews disabled, honoring outbound pacing and retrying 429s.
// It does not attempt a plain-text fallback on a "can't parse
// entities" error; use SendWithFallback for that.
func (p *BotPublisher) Send(ctx context.Context, html string) (int64, error) {
	return p.send(ctx, html, models.ParseModeHTML)
}

// SendWithFallback tries Send(ctx, html) first. If it fails with a
// Telegram "can't parse entities" style 400 error and plain is non-empty,
// it retries once with plain and no ParseMode set. Any other
// error, or an empty plain, is returned as-is.
func (p *BotPublisher) SendWithFallback(ctx context.Context, html, plain string) (int64, error) {
	id, err := p.Send(ctx, html)
	if err == nil || plain == "" || !IsParseEntitiesError(err) {
		return id, err
	}
	p.log.Error("post.failed", "error", err, "payload", html, "retry_decision", "plain_fallback")
	return p.send(ctx, plain, "")
}

func (p *BotPublisher) send(ctx context.Context, text string, parseMode models.ParseMode) (int64, error) {
	// PostRetries is the TOTAL number of send attempts, never fewer than one:
	// a misconfigured 0 must still send once, not report a fake success with
	// no message id (which would permanently dedupe an unposted item).
	maxAttempts := max(1, p.cfg.PostRetries)
	disabled := true

	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if err := p.pacer.Wait(ctx); err != nil {
			return 0, err
		}

		m, err := p.b.SendMessage(ctx, &tgbot.SendMessageParams{
			ChatID:             p.chatID,
			Text:               text,
			ParseMode:          parseMode,
			LinkPreviewOptions: &models.LinkPreviewOptions{IsDisabled: &disabled},
		})
		if err == nil {
			return int64(m.ID), nil
		}
		lastErr = err

		if rlErr, ok := asTooManyRequests(err); ok {
			p.log.Warn(logging.EvPostRateLimited, "retry_after", rlErr.RetryAfter)
			if attempt == maxAttempts {
				break
			}
			sleep := time.Duration(rlErr.RetryAfter)*time.Second + time.Second
			timer := time.NewTimer(sleep)
			select {
			case <-timer.C:
			case <-ctx.Done():
				timer.Stop()
				return 0, ctx.Err()
			}
			continue
		}

		if errors.Is(err, tgbot.ErrorForbidden) {
			p.log.Error(logging.EvPostFailed, "error", err, "retry_decision", "forbidden")
			return 0, ErrForbidden
		}

		p.log.Error(logging.EvPostFailed, "error", err, "retry_decision", "none")
		return 0, err
	}

	return 0, lastErr
}

// asTooManyRequests reports whether err is a *tgbot.TooManyRequestsError
// and returns it for RetryAfter inspection.
func asTooManyRequests(err error) (*tgbot.TooManyRequestsError, bool) {
	var rlErr *tgbot.TooManyRequestsError
	if errors.As(err, &rlErr) {
		return rlErr, true
	}
	return nil, false
}

// IsParseEntitiesError reports whether err is Telegram's 400 "can't parse
// entities" response, the signal that the rendered HTML is malformed
// . It is exported so the test double in tgtest branches on
// this exact predicate instead of a look-alike of its own.
func IsParseEntitiesError(err error) bool {
	return errors.Is(err, tgbot.ErrorBadRequest) && strings.Contains(err.Error(), "can't parse entities")
}
