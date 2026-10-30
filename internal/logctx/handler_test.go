package logctx

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"testing"
)

func TestHandlerAddsRequestID(t *testing.T) {
	for _, grouped := range []bool{false, true} {
		for _, id := range []string{"", "request-123"} {
			var output bytes.Buffer
			logger := slog.New(NewHandler(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: slog.LevelWarn})))
			if grouped {
				logger = logger.WithGroup("http")
			}
			logger = logger.With("component", "api")
			ctx := WithRequestID(t.Context(), id)
			logger.InfoContext(ctx, "suppressed")
			if output.Len() != 0 {
				t.Fatal("handler ignored log level")
			}
			logger.WarnContext(ctx, "request", "status", 429)
			var entry map[string]any
			if err := json.Unmarshal(output.Bytes(), &entry); err != nil {
				t.Fatal(err)
			}
			if grouped {
				entry = entry["http"].(map[string]any)
			}
			if entry["component"] != "api" || entry["status"] != float64(429) {
				t.Fatalf("attributes lost: %v", entry)
			}
			if id == "" {
				if _, exists := entry["request_id"]; exists {
					t.Fatalf("unexpected request ID: %v", entry)
				}
			} else if entry["request_id"] != id {
				t.Fatalf("missing request ID: %v", entry)
			}
		}
	}
}

func TestRequestIDContextIsolation(t *testing.T) {
	parent := t.Context()
	first := WithRequestID(parent, "first")
	second := WithRequestID(first, "second")
	if RequestID(parent) != "" || RequestID(first) != "first" || RequestID(second) != "second" {
		t.Fatal("request IDs leaked between contexts")
	}
}
