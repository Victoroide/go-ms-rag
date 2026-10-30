package config

import (
	"log/slog"
	"strings"
	"testing"
)

func TestLogLevelConfiguration(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  slog.Level
	}{
		{"", slog.LevelInfo}, {"debug", slog.LevelDebug}, {"info", slog.LevelInfo},
		{"warn", slog.LevelWarn}, {"error", slog.LevelError},
	} {
		t.Run(tc.value, func(t *testing.T) {
			t.Setenv("GO_RAG_FAKE_MODE", "true")
			t.Setenv("DATABASE_URL", "host=localhost dbname=rag")
			t.Setenv("LOG_LEVEL", tc.value)
			cfg, err := Load()
			if err != nil {
				t.Fatal(err)
			}
			if cfg.LogLevel != tc.want {
				t.Fatalf("log level = %v, want %v", cfg.LogLevel, tc.want)
			}
		})
	}
}

func TestLogLevelRejectsInvalidConfiguration(t *testing.T) {
	for _, value := range []string{"trace", "4", "info+2"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("GO_RAG_FAKE_MODE", "true")
			t.Setenv("DATABASE_URL", "host=localhost dbname=rag")
			t.Setenv("LOG_LEVEL", value)
			if _, err := Load(); err == nil || !strings.Contains(err.Error(), "LOG_LEVEL") {
				t.Fatalf("expected LOG_LEVEL error, got %v", err)
			}
		})
	}
}
