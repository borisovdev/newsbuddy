// Package logging provides structured logging via log/slog, writing JSON
// (or text, for local runs) to a single writer.
package logging

import (
	"io"
	"log/slog"
)

// Version is the build version, overridden at build time via
// -ldflags "-X github.com/borisovdev/newsbuddy/internal/logging.Version=$(VERSION)".
var Version = "dev"

// New builds a slog.Logger using a JSON handler unless format == "text", in
// which case a text handler is used. The returned logger always carries
// "service" and "version" attributes.
func New(format string, level slog.Level, w io.Writer) *slog.Logger {
	opts := &slog.HandlerOptions{Level: level}
	var h slog.Handler
	if format == "text" {
		h = slog.NewTextHandler(w, opts)
	} else {
		h = slog.NewJSONHandler(w, opts)
	}
	return slog.New(h).With("service", "news-buddy", "version", Version)
}
