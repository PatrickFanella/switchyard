package logger_test

import (
	"log/slog"
	"testing"

	"git.subcult.tv/PatrickFanella/llama-line/internal/logger"
)

func TestNew_DebugLevel(t *testing.T) {
	l := logger.New("debug")
	if !l.Handler().Enabled(nil, slog.LevelDebug) {
		t.Error("expected debug level to be enabled")
	}
}

func TestNew_InfoLevel(t *testing.T) {
	l := logger.New("info")
	if !l.Handler().Enabled(nil, slog.LevelInfo) {
		t.Error("expected info level to be enabled")
	}
	if l.Handler().Enabled(nil, slog.LevelDebug) {
		t.Error("expected debug level to be disabled at info")
	}
}

func TestNew_EmptyDefaultsToInfo(t *testing.T) {
	l := logger.New("")
	if !l.Handler().Enabled(nil, slog.LevelInfo) {
		t.Error("expected info level to be enabled")
	}
	if l.Handler().Enabled(nil, slog.LevelDebug) {
		t.Error("expected debug level to be disabled at default info")
	}
}

func TestNew_WarnLevel(t *testing.T) {
	l := logger.New("warn")
	if !l.Handler().Enabled(nil, slog.LevelWarn) {
		t.Error("expected warn level to be enabled")
	}
	if l.Handler().Enabled(nil, slog.LevelInfo) {
		t.Error("expected info level to be disabled at warn")
	}
}

func TestNew_ErrorLevel(t *testing.T) {
	l := logger.New("error")
	if !l.Handler().Enabled(nil, slog.LevelError) {
		t.Error("expected error level to be enabled")
	}
	if l.Handler().Enabled(nil, slog.LevelWarn) {
		t.Error("expected warn level to be disabled at error")
	}
}
