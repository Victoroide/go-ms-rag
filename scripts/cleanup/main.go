// Package main deletes the explicitly supplied document identifiers.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/Victoroide/go-ms-rag/internal/cleanup"
	"github.com/Victoroide/go-ms-rag/internal/config"
	"github.com/google/uuid"
)

func main() {
	if err := run(); err != nil {
		slog.Error("delete documents", "error", err)
		os.Exit(1)
	}
}
func run() error {
	ids := os.Args[1:]
	if len(ids) == 0 {
		return fmt.Errorf("provide at least one document UUID")
	}
	for _, id := range ids {
		if _, err := uuid.Parse(id); err != nil {
			return fmt.Errorf("invalid document id: %w", err)
		}
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cleaner, err := cleanup.New(ctx, cfg)
	if err != nil {
		return err
	}
	defer func() {
		if err := cleaner.Close(ctx); err != nil {
			slog.Error("close document stores", "error", err)
		}
	}()
	for _, id := range ids {
		if err := cleaner.Delete(ctx, id); err != nil {
			return fmt.Errorf("delete %s: %w", id, err)
		}
	}
	return nil
}
