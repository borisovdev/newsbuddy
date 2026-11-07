package config

import (
	"bytes"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// envPair is a single (key, value) environment entry. Deliberately not a
// map[string]string: the getenv test fake below is a linear-scan closure
// over a slice.
type envPair struct {
	key, value string
}

// makeGetenv builds a getenv function backed by a slice of pairs, scanned
// linearly. Keys not present return "".
func makeGetenv(pairs []envPair) func(string) string {
	return func(key string) string {
		for _, p := range pairs {
			if p.key == key {
				return p.value
			}
		}
		return ""
	}
}

// validToken is a TELEGRAM_BOT_TOKEN that satisfies the token regex.
var validToken = "123456789:" + strings.Repeat("A", 35)

func requiredPairs() []envPair {
	return []envPair{
		{"TELEGRAM_BOT_TOKEN", validToken},
		{"TELEGRAM_CHANNEL_ID", "@testchannel"},
		{"TELEGRAM_OWNER_ID", "12345"},
	}
}

func TestLoad_EmptyEnv_ErrorsOnBotToken(t *testing.T) {
	_, err := Load(makeGetenv(nil))
	if err == nil {
		t.Fatal("expected error for empty environment, got nil")
	}
	if !strings.Contains(err.Error(), "TELEGRAM_BOT_TOKEN") {
		t.Errorf("expected error to mention TELEGRAM_BOT_TOKEN, got: %v", err)
	}
}

func TestLoad_RequiredVarsOnly_UsesDefaults(t *testing.T) {
	cfg, err := Load(makeGetenv(requiredPairs()))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.TelegramBotToken != validToken {
		t.Errorf("TelegramBotToken = %q, want %q", cfg.TelegramBotToken, validToken)
	}
	if cfg.TelegramChannelID != "@testchannel" {
		t.Errorf("TelegramChannelID = %q, want @testchannel", cfg.TelegramChannelID)
	}
	if cfg.TelegramOwnerID != 12345 {
		t.Errorf("TelegramOwnerID = %d, want 12345", cfg.TelegramOwnerID)
	}

	defaults := Defaults()
	if cfg.DBPath != defaults.DBPath {
		t.Errorf("DBPath = %q, want default %q", cfg.DBPath, defaults.DBPath)
	}
	if cfg.PollInterval != defaults.PollInterval {
		t.Errorf("PollInterval = %v, want default %v", cfg.PollInterval, defaults.PollInterval)
	}
	if cfg.HTTPTimeout != defaults.HTTPTimeout {
		t.Errorf("HTTPTimeout = %v, want default %v", cfg.HTTPTimeout, defaults.HTTPTimeout)
	}
	if cfg.ExcerptLen != defaults.ExcerptLen {
		t.Errorf("ExcerptLen = %d, want default %d", cfg.ExcerptLen, defaults.ExcerptLen)
	}
	if cfg.MaxItemsPerPoll != defaults.MaxItemsPerPoll {
		t.Errorf("MaxItemsPerPoll = %d, want default %d", cfg.MaxItemsPerPoll, defaults.MaxItemsPerPoll)
	}
	if cfg.LogLevel != defaults.LogLevel {
		t.Errorf("LogLevel = %v, want default %v", cfg.LogLevel, defaults.LogLevel)
	}
	if cfg.LogFormat != defaults.LogFormat {
		t.Errorf("LogFormat = %q, want default %q", cfg.LogFormat, defaults.LogFormat)
	}

	// Compile-time tunables must also come from Defaults().
	if cfg.PollConcurrency != defaults.PollConcurrency {
		t.Errorf("PollConcurrency = %d, want default %d", cfg.PollConcurrency, defaults.PollConcurrency)
	}
	if cfg.PostInterval != defaults.PostInterval {
		t.Errorf("PostInterval = %v, want default %v", cfg.PostInterval, defaults.PostInterval)
	}
}

func TestLoad_InvalidValues(t *testing.T) {
	base := requiredPairs()

	withOverride := func(key, value string) []envPair {
		out := make([]envPair, 0, len(base)+1)
		overridden := false
		for _, p := range base {
			if p.key == key {
				out = append(out, envPair{key, value})
				overridden = true
				continue
			}
			out = append(out, p)
		}
		if !overridden {
			out = append(out, envPair{key, value})
		}
		return out
	}

	tests := []struct {
		name       string
		key, value string
	}{
		{"bad token format", "TELEGRAM_BOT_TOKEN", "not-a-token"},
		{"token secret too short", "TELEGRAM_BOT_TOKEN", "123456:short"},
		{"bad channel id", "TELEGRAM_CHANNEL_ID", "not-a-handle-or-int"},
		{"non-numeric owner id", "TELEGRAM_OWNER_ID", "abc"},
		{"zero owner id", "TELEGRAM_OWNER_ID", "0"},
		{"negative owner id", "TELEGRAM_OWNER_ID", "-5"},
		{"unparseable poll interval", "POLL_INTERVAL", "5min"},
		{"poll interval too short", "POLL_INTERVAL", "30s"},
		{"poll interval too long", "POLL_INTERVAL", "25h"},
		{"unparseable http timeout", "HTTP_TIMEOUT", "banana"},
		{"http timeout too short", "HTTP_TIMEOUT", "500ms"},
		{"http timeout too long", "HTTP_TIMEOUT", "6m"},
		{"unparseable excerpt len", "EXCERPT_LEN", "big"},
		{"excerpt len too small", "EXCERPT_LEN", "0"},
		{"excerpt len too large", "EXCERPT_LEN", "2001"},
		{"unparseable max items per poll", "MAX_ITEMS_PER_POLL", "many"},
		{"max items per poll too small", "MAX_ITEMS_PER_POLL", "0"},
		{"max items per poll too large", "MAX_ITEMS_PER_POLL", "101"},
		{"invalid log level", "LOG_LEVEL", "verbose"},
		{"invalid log format", "LOG_FORMAT", "xml"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(makeGetenv(withOverride(tt.key, tt.value)))
			if err == nil {
				t.Fatalf("expected error for %s=%q, got nil", tt.key, tt.value)
			}
		})
	}
}

func TestLoad_ValidOptionalOverrides(t *testing.T) {
	pairs := append(requiredPairs(),
		envPair{"DB_PATH", "/tmp/nb.db"},
		envPair{"POLL_INTERVAL", "10m"},
		envPair{"HTTP_TIMEOUT", "30s"},
		envPair{"EXCERPT_LEN", "150"},
		envPair{"MAX_ITEMS_PER_POLL", "50"},
		envPair{"LOG_LEVEL", "DEBUG"},
		envPair{"LOG_FORMAT", "TEXT"},
	)

	cfg, err := Load(makeGetenv(pairs))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.DBPath != "/tmp/nb.db" {
		t.Errorf("DBPath = %q", cfg.DBPath)
	}
	if cfg.PollInterval != 10*time.Minute {
		t.Errorf("PollInterval = %v", cfg.PollInterval)
	}
	if cfg.HTTPTimeout != 30*time.Second {
		t.Errorf("HTTPTimeout = %v", cfg.HTTPTimeout)
	}
	if cfg.ExcerptLen != 150 {
		t.Errorf("ExcerptLen = %d", cfg.ExcerptLen)
	}
	if cfg.MaxItemsPerPoll != 50 {
		t.Errorf("MaxItemsPerPoll = %d", cfg.MaxItemsPerPoll)
	}
	if cfg.LogLevel != slog.LevelDebug {
		t.Errorf("LogLevel = %v", cfg.LogLevel)
	}
	if cfg.LogFormat != "text" {
		t.Errorf("LogFormat = %q", cfg.LogFormat)
	}
}

// TestRedaction: the token substring must never
// appear in fmt.Sprintf("%v", cfg) nor in the rendered output of
// cfg.LogValue() when logged through slog.
func TestRedaction(t *testing.T) {
	cfg, err := Load(makeGetenv(requiredPairs()))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if s := fmt.Sprintf("%v", cfg); strings.Contains(s, validToken) {
		t.Errorf("token leaked via fmt %%v: %s", s)
	}
	if s := fmt.Sprintf("%s", cfg); strings.Contains(s, validToken) {
		t.Errorf("token leaked via fmt %%s: %s", s)
	}

	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	logger.Info("startup", "config", cfg)

	if strings.Contains(buf.String(), validToken) {
		t.Errorf("token leaked via LogValue: %s", buf.String())
	}
	if !strings.Contains(buf.String(), "bot_token_len") {
		t.Errorf("expected bot_token_len in log output, got: %s", buf.String())
	}
}
