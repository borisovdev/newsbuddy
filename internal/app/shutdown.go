package app

import (
	"context"
	"os"
	"sync"
	"time"

	"github.com/borisovdev/newsbuddy/internal/logging"
)

// Run starts the poller and the bot, blocks until ctx is cancelled, then
// performs the shutdown sequence and returns the process exit code.
// It always returns 0: SIGTERM must end in exit 0, and a
// shutdown that overruns cfg.ShutdownBudget is forced to exit 0 by the
// watchdog rather than hanging.
func (a *App) Run(ctx context.Context) int {
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		a.poller.Run(ctx) // returns on ctx.Done
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		a.bot.Start(ctx) // blocks on long polling until ctx.Done
	}()

	<-ctx.Done() // SIGINT/SIGTERM

	t0 := time.Now()
	a.log.Info(logging.EvShutdown, "reason", "signal", "phase", "begin")

	budget := a.cfg.ShutdownBudget
	if budget <= 0 {
		budget = 10 * time.Second
	}

	// Hard-exit backstop ("budget 10s, then hard exit"). If a
	// goroutine wedges, the process still leaves with code 0.
	watchdog := time.AfterFunc(budget, func() {
		a.log.Warn(logging.EvShutdown, "reason", "signal", "forced", true,
			"duration_ms", budget.Milliseconds())
		os.Exit(0)
	})

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	forced := false
	select {
	case <-done:
	case <-time.After(budget):
		forced = true
	}
	watchdog.Stop()

	// Closed only after both goroutines returned (or the budget expired),
	// so no goroutine can write to a closed handle.
	a.store.Close()
	a.cache.Close()

	a.log.Info(logging.EvShutdown, "reason", "signal", "phase", "end",
		"forced", forced, "duration_ms", time.Since(t0).Milliseconds())
	return 0
}
