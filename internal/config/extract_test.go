package config

import (
	"strings"
	"testing"
	"time"
)

func TestLoadExtractionSettings(t *testing.T) {
	t.Setenv("GO_RAG_FAKE_MODE", "true")
	t.Setenv("DATABASE_URL", "host=localhost dbname=rag")
	for _, key := range []string{"EXTRACT_ISOLATION", "EXTRACT_TIMEOUT", "EXTRACT_CONCURRENCY", "EXTRACT_MEMORY_LIMIT_MB", "EXTRACT_QUEUE_TIMEOUT"} {
		t.Setenv(key, "")
	}
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.ExtractIsolation || cfg.ExtractTimeout != 30*time.Second || cfg.ExtractConcurrency != 2 || cfg.ExtractMemoryLimitMB != 512 || cfg.ExtractQueueTimeout != 5*time.Second {
		t.Fatalf("incorrect extraction defaults: %+v", cfg)
	}
	for _, tt := range []struct{ key, value string }{
		{"EXTRACT_ISOLATION", "invalid"}, {"EXTRACT_TIMEOUT", "invalid"}, {"EXTRACT_TIMEOUT", "999ms"}, {"EXTRACT_TIMEOUT", "11m"},
		{"EXTRACT_CONCURRENCY", "0"}, {"EXTRACT_CONCURRENCY", "65"}, {"EXTRACT_CONCURRENCY", "invalid"},
		{"EXTRACT_MEMORY_LIMIT_MB", "63"}, {"EXTRACT_MEMORY_LIMIT_MB", "8193"},
		{"EXTRACT_QUEUE_TIMEOUT", "-1s"}, {"EXTRACT_QUEUE_TIMEOUT", "61s"}, {"EXTRACT_QUEUE_TIMEOUT", "invalid"},
	} {
		t.Run(tt.key+"/"+tt.value, func(t *testing.T) {
			t.Setenv(tt.key, tt.value)
			if _, loadErr := Load(); loadErr == nil || !strings.Contains(loadErr.Error(), tt.key) {
				t.Fatalf("invalid %s accepted: %v", tt.key, loadErr)
			}
		})
	}
	t.Setenv("EXTRACT_ISOLATION", "false")
	t.Setenv("EXTRACT_TIMEOUT", "1m")
	t.Setenv("EXTRACT_CONCURRENCY", "4")
	t.Setenv("EXTRACT_MEMORY_LIMIT_MB", "1024")
	t.Setenv("EXTRACT_QUEUE_TIMEOUT", "0s")
	cfg, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ExtractIsolation || cfg.ExtractTimeout != time.Minute || cfg.ExtractConcurrency != 4 || cfg.ExtractMemoryLimitMB != 1024 || cfg.ExtractQueueTimeout != 0 {
		t.Fatalf("incorrect extraction settings: %+v", cfg)
	}
}
