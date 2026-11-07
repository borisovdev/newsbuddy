package apptest_test

import (
	"encoding/json"
	"log/slog"
	"strconv"
	"strings"
	"testing"

	"github.com/borisovdev/newsbuddy/internal/app/apptest"
	"github.com/borisovdev/newsbuddy/internal/logging"
)

// runFullScenario drives an /add, three poll cycles (one of which posts) and
// a /remove, so the buffer afterwards contains every log class the logging tests check.
func runFullScenario(t *testing.T, h *apptest.Harness) {
	t.Helper()

	res := apptest.Fixture(t, "rss2.xml", urlA)
	grown := apptest.AppendItem(res, "Twelfth Article", "https://example.com/articles/12")

	h.Script(urlA, res, 1)
	src := h.Add(urlA, "Observability Source")

	h.Script(urlA, res, 1)
	h.Cycle()
	h.Script(urlA, grown, 1)
	h.Cycle()
	h.Script(urlA, grown, 1)
	h.Cycle()

	if _, err := h.Svc.Remove(h.Ctx(), strconv.FormatInt(src.ID, 10)); err != nil {
		t.Fatalf("service.Remove(%d): %v", src.ID, err)
	}
}

// TestEveryLineIsJSONWithCycleTelemetry: JSON on every
// line, cycle start/end with a numeric duration_ms, and per-source start and
// result lines.
func TestEveryLineIsJSONWithCycleTelemetry(t *testing.T) {
	h := apptest.New(t)
	runFullScenario(t, h)

	lines := h.Logs.Lines()
	if len(lines) == 0 {
		t.Fatal("no log lines were buffered")
	}

	// Every line is valid JSON, and carries the standard envelope.
	for i, ln := range lines {
		var fields map[string]any
		if err := json.Unmarshal([]byte(ln), &fields); err != nil {
			t.Fatalf("line %d is not valid JSON: %v\nline: %s", i, err, ln)
		}
		for _, required := range []string{"time", "level", "msg", "service"} {
			if _, ok := fields[required]; !ok {
				t.Errorf("line %d is missing %q: %s", i, required, ln)
			}
		}
	}

	// Cycle start lines.
	starts := h.Logs.LinesFor(logging.EvCycleStart)
	if len(starts) != 3 {
		t.Errorf("%s lines = %d, want 3", logging.EvCycleStart, len(starts))
	}
	for _, ln := range starts {
		if _, ok := decodeLine(t, ln)["sources"].(float64); !ok {
			t.Errorf("%s has no numeric sources: %s", logging.EvCycleStart, ln)
		}
	}

	// Cycle end lines with a numeric duration_ms.
	ends := h.Logs.LinesFor(logging.EvCycleEnd)
	if len(ends) != 3 {
		t.Fatalf("%s lines = %d, want 3", logging.EvCycleEnd, len(ends))
	}
	for _, ln := range ends {
		fields := decodeLine(t, ln)
		if _, ok := fields["duration_ms"].(float64); !ok {
			t.Errorf("%s duration_ms = %v (%T), want a number",
				logging.EvCycleEnd, fields["duration_ms"], fields["duration_ms"])
		}
		for _, required := range []string{"sources", "new_items", "posted", "errors"} {
			if _, ok := fields[required].(float64); !ok {
				t.Errorf("%s %s = %v, want a number", logging.EvCycleEnd, required, fields[required])
			}
		}
	}

	// Per-source start lines (debug) and result lines.
	if n := h.Logs.Count(logging.EvSourceStart); n != 3 {
		t.Errorf("%s lines = %d, want 3", logging.EvSourceStart, n)
	}
	oks := h.Logs.LinesFor(logging.EvSourceOK)
	if len(oks) != 3 {
		t.Fatalf("%s lines = %d, want 3", logging.EvSourceOK, len(oks))
	}
	for _, ln := range oks {
		fields := decodeLine(t, ln)
		for _, required := range []string{"source_id", "items", "new", "posted", "duration_ms"} {
			if _, ok := fields[required].(float64); !ok {
				t.Errorf("%s %s = %v, want a number", logging.EvSourceOK, required, fields[required])
			}
		}
	}

	// The posting cycle produced a post.sent line.
	if n := h.Logs.Count(logging.EvPostSent); n != 1 {
		t.Errorf("%s lines = %d, want 1", logging.EvPostSent, n)
	}
}

// TestTokenNeverAppearsInLogs greps the whole buffer for the configured
// bot token after a full run that includes an App boot — the startup line
// logs the entire Config, so this is the assertion that proves
// Config.LogValue's redaction actually holds.
func TestTokenNeverAppearsInLogs(t *testing.T) {
	h := apptest.New(t)
	runFullScenario(t, h)

	// Boot and stop a real App writing into the SAME buffer, so the startup
	// line (which logs cfg) is included in the grep.
	h.Close()
	a := bootApp(t, h.Cfg, h.Log)
	stopApp(t, a)

	buf := h.Logs.String()
	if len(buf) == 0 {
		t.Fatal("no log output to grep")
	}
	if n := h.Logs.Count(logging.EvStartup); n < 1 {
		t.Fatalf("%s lines = %d; the grep would be vacuous", logging.EvStartup, n)
	}

	if strings.Contains(buf, apptest.FakeToken) {
		t.Errorf("the bot token appears verbatim in the log output")
	}
	// Also grep for the token's secret half on its own, in case something
	// logs a partial value.
	if secret := strings.SplitN(apptest.FakeToken, ":", 2)[1]; strings.Contains(buf, secret) {
		t.Errorf("the secret half of the bot token appears in the log output")
	}

	// Proof the redacted form is what got logged instead.
	fields := decodeLine(t, h.Logs.LinesFor(logging.EvStartup)[0])
	cfgAttr, ok := fields["config"].(map[string]any)
	if !ok {
		t.Fatalf("startup line has no config group: %v", fields["config"])
	}
	if _, ok := cfgAttr["bot_token_len"].(float64); !ok {
		t.Errorf("startup config has no bot_token_len: %v", cfgAttr)
	}
	if _, present := cfgAttr["telegram_bot_token"]; present {
		t.Errorf("startup config exposes telegram_bot_token")
	}
}

func TestSourceAddedAndRemovedAreLogged(t *testing.T) {
	h := apptest.New(t)
	runFullScenario(t, h)

	added := h.Logs.LinesFor(logging.EvSourceAdded)
	if len(added) != 1 {
		t.Fatalf("%s lines = %d, want 1", logging.EvSourceAdded, len(added))
	}
	addedFields := decodeLine(t, added[0])
	if addedFields["feed_url"] != urlA {
		t.Errorf("%s feed_url = %v, want %q", logging.EvSourceAdded, addedFields["feed_url"], urlA)
	}
	if _, ok := addedFields["source_id"].(float64); !ok {
		t.Errorf("%s has no numeric source_id: %v", logging.EvSourceAdded, addedFields)
	}

	if n := h.Logs.Count(logging.EvSourceSeeded); n != 1 {
		t.Errorf("%s lines = %d, want 1", logging.EvSourceSeeded, n)
	}

	removed := h.Logs.LinesFor(logging.EvSourceRemoved)
	if len(removed) != 1 {
		t.Fatalf("%s lines = %d, want 1", logging.EvSourceRemoved, len(removed))
	}
	if f := decodeLine(t, removed[0]); f["feed_url"] != urlA {
		t.Errorf("%s feed_url = %v, want %q", logging.EvSourceRemoved, f["feed_url"], urlA)
	}
}

// TestLogSinkLevelIsDebug guards the harness contract the cycle-telemetry assertions
// rely on: poll.source.start is a debug line, so a harness built at info
// level would silently stop covering it.
func TestLogSinkLevelIsDebug(t *testing.T) {
	h := apptest.New(t)
	if h.Cfg.LogLevel != slog.LevelDebug {
		t.Fatalf("harness LogLevel = %v, want debug", h.Cfg.LogLevel)
	}
}
