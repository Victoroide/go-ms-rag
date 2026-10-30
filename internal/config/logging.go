package config

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
)

func envLogLevel() (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("LOG_LEVEL"))) {
	case "debug":
		return slog.LevelDebug, nil
	case "", "info":
		return slog.LevelInfo, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return slog.LevelInfo, fmt.Errorf("config: LOG_LEVEL must be debug, info, warn or error")
	}
}
