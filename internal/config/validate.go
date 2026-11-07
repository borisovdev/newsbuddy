package config

import (
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// tokenRe validates the TELEGRAM_BOT_TOKEN shape: numeric bot id, colon,
// then a base64url-ish secret of at least 30 characters.
var tokenRe = regexp.MustCompile(`^\d+:[A-Za-z0-9_-]{30,}$`)

// applyEnv overlays the ten environment variables from the README table
// onto cfg (already populated by Defaults()). An unset or empty variable
// keeps the default; a set-but-unparseable or out-of-range value is always
// an error, never a silent fallback. All failures are collected and
// returned joined via errors.Join.
func applyEnv(cfg Config, getenv func(string) string) (Config, error) {
	var errs []error

	if v := strings.TrimSpace(getenv("TELEGRAM_BOT_TOKEN")); v == "" {
		errs = append(errs, errors.New("TELEGRAM_BOT_TOKEN is required"))
	} else if !tokenRe.MatchString(v) {
		errs = append(errs, fmt.Errorf("TELEGRAM_BOT_TOKEN is invalid: must match ^\\d+:[A-Za-z0-9_-]{30,}$"))
	} else {
		cfg.TelegramBotToken = v
	}

	if v := strings.TrimSpace(getenv("TELEGRAM_CHANNEL_ID")); v == "" {
		errs = append(errs, errors.New("TELEGRAM_CHANNEL_ID is required"))
	} else if !validChannelID(v) {
		errs = append(errs, fmt.Errorf("TELEGRAM_CHANNEL_ID is invalid: must be @name or a numeric id, got %q", v))
	} else {
		cfg.TelegramChannelID = v
	}

	if v := strings.TrimSpace(getenv("TELEGRAM_OWNER_ID")); v == "" {
		errs = append(errs, errors.New("TELEGRAM_OWNER_ID is required"))
	} else if id, err := strconv.ParseInt(v, 10, 64); err != nil || id <= 0 {
		errs = append(errs, fmt.Errorf("TELEGRAM_OWNER_ID is invalid: must be a positive integer, got %q", v))
	} else {
		cfg.TelegramOwnerID = id
	}

	if v := getenv("DB_PATH"); v != "" {
		cfg.DBPath = v
	}

	if v := getenv("POLL_INTERVAL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			errs = append(errs, fmt.Errorf("POLL_INTERVAL is invalid: %w", err))
		} else if d < time.Minute || d > 24*time.Hour {
			errs = append(errs, fmt.Errorf("POLL_INTERVAL must be between 1m and 24h, got %s", d))
		} else {
			cfg.PollInterval = d
		}
	}

	if v := getenv("HTTP_TIMEOUT"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			errs = append(errs, fmt.Errorf("HTTP_TIMEOUT is invalid: %w", err))
		} else if d < time.Second || d > 5*time.Minute {
			errs = append(errs, fmt.Errorf("HTTP_TIMEOUT must be between 1s and 5m, got %s", d))
		} else {
			cfg.HTTPTimeout = d
		}
	}

	if v := getenv("EXCERPT_LEN"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			errs = append(errs, fmt.Errorf("EXCERPT_LEN is invalid: %w", err))
		} else if n < 1 || n > 2000 {
			errs = append(errs, fmt.Errorf("EXCERPT_LEN must be between 1 and 2000, got %d", n))
		} else {
			cfg.ExcerptLen = n
		}
	}

	if v := getenv("MAX_ITEMS_PER_POLL"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			errs = append(errs, fmt.Errorf("MAX_ITEMS_PER_POLL is invalid: %w", err))
		} else if n < 1 || n > 100 {
			errs = append(errs, fmt.Errorf("MAX_ITEMS_PER_POLL must be between 1 and 100, got %d", n))
		} else {
			cfg.MaxItemsPerPoll = n
		}
	}

	if v := getenv("LOG_LEVEL"); v != "" {
		lvl, err := parseLogLevel(v)
		if err != nil {
			errs = append(errs, err)
		} else {
			cfg.LogLevel = lvl
		}
	}

	if v := getenv("LOG_FORMAT"); v != "" {
		f := strings.ToLower(v)
		if f != "json" && f != "text" {
			errs = append(errs, fmt.Errorf("LOG_FORMAT must be one of json, text, got %q", v))
		} else {
			cfg.LogFormat = f
		}
	}

	if len(errs) > 0 {
		return cfg, errors.Join(errs...)
	}
	return cfg, nil
}

// validChannelID reports whether v is a valid TELEGRAM_CHANNEL_ID: either
// an "@name" handle or a value parseable as an int64 (e.g. "-100123...").
func validChannelID(v string) bool {
	if strings.HasPrefix(v, "@") {
		return len(v) > 1
	}
	_, err := strconv.ParseInt(v, 10, 64)
	return err == nil
}

// parseLogLevel parses the LOG_LEVEL enum, case-insensitively.
func parseLogLevel(v string) (slog.Level, error) {
	switch strings.ToLower(v) {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("LOG_LEVEL must be one of debug, info, warn, error, got %q", v)
	}
}
