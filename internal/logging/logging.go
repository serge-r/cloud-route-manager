// Package logging builds the application logger from the configuration.
package logging

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

	"github.com/serge-r/cloud-route-manager/internal/config"
)

// New returns a logger and, when the destination is a file, the closer for it.
// Everything goes to stdout unless general.log-file says otherwise.
func New(cfg config.General) (*slog.Logger, io.Closer, error) {
	var (
		w      io.Writer
		closer io.Closer
	)
	switch dest := strings.TrimSpace(cfg.LogFile); strings.ToLower(dest) {
	case "", "stdout", "-":
		w = os.Stdout
	case "stderr":
		w = os.Stderr
	default:
		f, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return nil, nil, fmt.Errorf("open log file: %w", err)
		}
		w, closer = f, f
	}

	opts := &slog.HandlerOptions{Level: Level(cfg.LogSeverity)}
	var h slog.Handler
	if strings.EqualFold(cfg.LogFormat, "json") {
		h = slog.NewJSONHandler(w, opts)
	} else {
		h = slog.NewTextHandler(w, opts)
	}
	return slog.New(h), closer, nil
}

// Level maps a configuration string to a slog level, defaulting to info.
func Level(name string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
