// Package config loads and validates news-buddy's configuration from
// environment variables, and holds the compile-time tunables that are
// not environment-driven but must be overridable in tests.
package config

import (
	"log/slog"
	"time"
)

// Config holds all runtime configuration for news-buddy.
type Config struct {
	// ---- environment-driven (the README table) ----
	TelegramBotToken  string        // TELEGRAM_BOT_TOKEN   required
	TelegramChannelID string        // TELEGRAM_CHANNEL_ID  required, raw
	TelegramOwnerID   int64         // TELEGRAM_OWNER_ID    required
	DBPath            string        // DB_PATH              ./data/news-buddy.db
	PollInterval      time.Duration // POLL_INTERVAL        5m
	HTTPTimeout       time.Duration // HTTP_TIMEOUT         15s
	ExcerptLen        int           // EXCERPT_LEN          300
	MaxItemsPerPoll   int           // MAX_ITEMS_PER_POLL   20
	LogLevel          slog.Level    // LOG_LEVEL            info
	LogFormat         string        // LOG_FORMAT           json

	// ---- compile-time tunables (NOT env-driven; injectable for tests) ----
	PollConcurrency int           // 4
	MaxBodyBytes    int64         // 10<<20
	MaxRedirects    int           // 10
	FetchRetries    int           // 2
	PostInterval    time.Duration // 3s
	PostRetries     int           // 3
	TitleMaxLen     int           // 200
	SourceTitleMax  int           // 64
	MessageMaxUTF16 int           // 4000
	CacheEntries    int           // 1024
	CacheSeenPerSrc int           // 5000
	CacheSeenTTL    time.Duration // 1h
	CacheSourcesTTL time.Duration // 30s
	ShutdownBudget  time.Duration // 10s
}

// Defaults returns a Config with all compile-time tunables and all
// optional environment-driven fields set to their default values.
// Load starts from Defaults() and overlays environment variables.
func Defaults() Config {
	return Config{
		DBPath:          "./data/news-buddy.db",
		PollInterval:    5 * time.Minute,
		HTTPTimeout:     15 * time.Second,
		ExcerptLen:      300,
		MaxItemsPerPoll: 20,
		LogLevel:        slog.LevelInfo,
		LogFormat:       "json",

		PollConcurrency: 4,
		MaxBodyBytes:    10 << 20,
		MaxRedirects:    10,
		FetchRetries:    2,
		PostInterval:    3 * time.Second,
		PostRetries:     3,
		TitleMaxLen:     200,
		SourceTitleMax:  64,
		MessageMaxUTF16: 4000,
		CacheEntries:    1024,
		CacheSeenPerSrc: 5000,
		CacheSeenTTL:    time.Hour,
		CacheSourcesTTL: 30 * time.Second,
		ShutdownBudget:  10 * time.Second,
	}
}

// Load builds a Config starting from Defaults() and overlaying the ten
// environment variables from the README table, read via getenv. All
// validation failures are collected and returned joined via errors.Join;
// a nil error means cfg is fully valid. main.go passes os.Getenv.
func Load(getenv func(string) string) (Config, error) {
	cfg := Defaults()
	return applyEnv(cfg, getenv)
}

// LogValue implements slog.LogValuer so the token can never leak through
// an slog call, however carelessly cfg is logged. Only the token's length
// is exposed.
func (c Config) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("telegram_channel_id", c.TelegramChannelID),
		slog.Int64("telegram_owner_id", c.TelegramOwnerID),
		slog.String("db_path", c.DBPath),
		slog.Duration("poll_interval", c.PollInterval),
		slog.Duration("http_timeout", c.HTTPTimeout),
		slog.Int("excerpt_len", c.ExcerptLen),
		slog.Int("max_items_per_poll", c.MaxItemsPerPoll),
		slog.String("log_level", c.LogLevel.String()),
		slog.String("log_format", c.LogFormat),
		slog.Int("bot_token_len", len(c.TelegramBotToken)), // length only
	)
}

// String is overridden so that %v/%s in an ad-hoc fmt.Errorf cannot leak
// the token either.
func (c Config) String() string { return "config{redacted}" }
