// Command news-buddy polls RSS/Atom feeds and posts new items to a
// Telegram channel.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/borisovdev/newsbuddy/internal/app"
	"github.com/borisovdev/newsbuddy/internal/config"
	"github.com/borisovdev/newsbuddy/internal/logging"
)

func main() { os.Exit(run()) }

func run() int {
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		// Plain stderr, not a log line: the logger is configured from cfg.
		fmt.Fprintf(os.Stderr, "configuration error:\n%v\n", err)
		return 1
	}
	log := logging.New(cfg.LogFormat, cfg.LogLevel, os.Stdout)
	slog.SetDefault(log)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	a, err := app.New(ctx, cfg, log)
	if err != nil {
		log.Error("startup.failed", "error", err)
		return 1
	}
	return a.Run(ctx)
}
