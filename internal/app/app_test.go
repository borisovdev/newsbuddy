package app_test

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	tgbot "github.com/go-telegram/bot"

	"github.com/borisovdev/newsbuddy/internal/app"
	"github.com/borisovdev/newsbuddy/internal/config"
)

// TestNewSmoke pins the testability seam T19's acceptance harness depends
// on: an App can be constructed with no network at all, by skipping the
// library's construction-time getMe and replacing preflight.
func TestNewSmoke(t *testing.T) {
	cfg := config.Defaults()
	cfg.TelegramBotToken = "123456:test-token"
	cfg.TelegramChannelID = "@testchannel"
	cfg.TelegramOwnerID = 42
	cfg.DBPath = filepath.Join(t.TempDir(), "nested", "news-buddy.db")

	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	preflightCalled := false
	a, err := app.New(context.Background(), cfg, log,
		app.WithBotOptions(tgbot.WithSkipGetMe()),
		app.WithPreflight(func(context.Context, *tgbot.Bot, config.Config, *slog.Logger) error {
			preflightCalled = true
			return nil
		}),
	)
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	if !preflightCalled {
		t.Error("injected preflight was not called")
	}

	// Run returns 0 once the context is done, and closes store and cache.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if code := a.Run(ctx); code != 0 {
		t.Errorf("Run() = %d, want 0", code)
	}
}
