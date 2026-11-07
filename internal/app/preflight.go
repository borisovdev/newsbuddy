package app

import (
	"context"
	"fmt"
	"log/slog"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/borisovdev/newsbuddy/internal/config"
	"github.com/borisovdev/newsbuddy/internal/logging"
)

// Preflight is the real startup probe and the default value of the
// preflight seam. It proves three things, all fatal:
//
//	getMe        — the token is valid.
//	getChat      — the configured channel exists and the bot can see it.
//	getChatMember— the bot is an administrator (or the creator) of that
//	               channel and, where the API exposes it, may post.
//
// It deliberately sends no probe message: a test post to the
// operator's channel on every restart is unacceptable noise, and the
// membership check answers the same question. The residual risk — posting
// rights revoked after boot — surfaces at runtime as a forbidden send and in
// /status.
func Preflight(ctx context.Context, b *tgbot.Bot, cfg config.Config, log *slog.Logger) error {
	me, err := b.GetMe(ctx)
	if err != nil {
		return fmt.Errorf("getMe failed (is TELEGRAM_BOT_TOKEN valid?): %w", err)
	}
	log.InfoContext(ctx, logging.EvStartup, "bot_username", me.Username, "bot_id", me.ID)

	if _, err := b.GetChat(ctx, &tgbot.GetChatParams{ChatID: cfg.TelegramChannelID}); err != nil {
		return fmt.Errorf("getChat %s failed (is the channel id correct and the bot a member?): %w",
			cfg.TelegramChannelID, err)
	}

	mem, err := b.GetChatMember(ctx, &tgbot.GetChatMemberParams{
		ChatID: cfg.TelegramChannelID,
		UserID: me.ID,
	})
	if err != nil {
		return fmt.Errorf("getChatMember %s failed: %w", cfg.TelegramChannelID, err)
	}

	switch mem.Type {
	case models.ChatMemberTypeOwner:
		// The creator always has every right.
		return nil
	case models.ChatMemberTypeAdministrator:
		// CanPostMessages is channel-only and omitempty, so it is the one
		// right that actually matters here.
		if mem.Administrator != nil && !mem.Administrator.CanPostMessages {
			return fmt.Errorf("bot is an administrator of %s but lacks the post-messages right (cannot post)",
				cfg.TelegramChannelID)
		}
		return nil
	default:
		return fmt.Errorf("bot is not an administrator of %s (cannot post)", cfg.TelegramChannelID)
	}
}
