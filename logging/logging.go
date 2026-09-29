package logging

import (
	"fmt"
	"io"
	"log/slog"
	"os"
)

// New returns a logger that writes to [os.Stdout] with the given level and format.
func New(level, format string) (*slog.Logger, error) {
	return NewWithWriter(os.Stdout, level, format)
}

// NewWithWriter returns a logger that writes to w with the given level and format.
func NewWithWriter(w io.Writer, level, format string) (*slog.Logger, error) {
	lvl, err := parseLevel(level)
	if err != nil {
		return nil, err
	}
	opts := &slog.HandlerOptions{Level: lvl}
	switch format {
	case "text":
		return slog.New(slog.NewTextHandler(w, opts)), nil
	case "json":
		return slog.New(slog.NewJSONHandler(w, opts)), nil
	default:
		return nil, fmt.Errorf("logging: invalid format %q (want text, json)", format)
	}
}

func parseLevel(level string) (slog.Level, error) {
	switch level {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("logging: invalid level %q (want debug, info, warn, error)", level)
	}
}
