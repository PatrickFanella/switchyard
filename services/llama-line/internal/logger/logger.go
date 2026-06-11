package logger

import (
	"log/slog"
	"os"
	"strings"
)

// New returns a JSON structured logger at the given level string.
// Unrecognised or empty level defaults to info.
func New(level string) *slog.Logger {
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: parseLevel(level)}))
}

// NewWithBroadcaster returns a JSON logger that writes to stdout AND fans every
// record out to the provided Broadcaster. If b is nil this is equivalent to New.
func NewWithBroadcaster(level string, b *Broadcaster) *slog.Logger {
	if b == nil {
		return New(level)
	}
	return slog.New(newTeeHandler(parseLevel(level), os.Stdout, b))
}

func parseLevel(level string) slog.Level {
	switch strings.ToLower(level) {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// NewFile returns a JSON structured logger that appends to the given file path.
// The file is created if it does not exist. The log level is always Info (audit
// logs capture every request regardless of the main log level).
func NewFile(path string) (*slog.Logger, error) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	return slog.New(slog.NewJSONHandler(f, &slog.HandlerOptions{Level: slog.LevelInfo})), nil
}
