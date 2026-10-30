package config

import (
	"strings"
	"testing"
	"time"
)

func pineconeLoadConfig(t *testing.T) *Config {
	t.Helper()
	t.Setenv("GO_RAG_FAKE_MODE", "true")
	t.Setenv("DATABASE_URL", "host=localhost dbname=rag")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestPineconeConfigDefaults(t *testing.T) {
	t.Setenv("PINECONE_UPSERT_BATCH_SIZE", "")
	t.Setenv("PINECONE_TIMEOUT", "")
	t.Setenv("PINECONE_MAX_ATTEMPTS", "")
	cfg := pineconeLoadConfig(t)
	if cfg.PineconeUpsertBatchSize != 100 || cfg.PineconeTimeout != 10*time.Second || cfg.PineconeMaxAttempts != 5 {
		t.Fatalf("unexpected settings: batch=%d timeout=%s attempts=%d", cfg.PineconeUpsertBatchSize, cfg.PineconeTimeout, cfg.PineconeMaxAttempts)
	}
}

func TestPineconeConfigBounds(t *testing.T) {
	for _, tt := range []struct{ key, value string }{
		{"PINECONE_UPSERT_BATCH_SIZE", "0"}, {"PINECONE_UPSERT_BATCH_SIZE", "1001"}, {"PINECONE_UPSERT_BATCH_SIZE", "bad"},
		{"PINECONE_TIMEOUT", "999ms"}, {"PINECONE_TIMEOUT", "121s"}, {"PINECONE_TIMEOUT", "bad"},
		{"PINECONE_MAX_ATTEMPTS", "0"}, {"PINECONE_MAX_ATTEMPTS", "11"}, {"PINECONE_MAX_ATTEMPTS", "bad"},
	} {
		t.Run(tt.key+"/"+tt.value, func(t *testing.T) {
			t.Setenv("GO_RAG_FAKE_MODE", "true")
			t.Setenv("DATABASE_URL", "host=localhost dbname=rag")
			t.Setenv(tt.key, tt.value)
			if _, err := Load(); err == nil || !strings.Contains(err.Error(), tt.key) {
				t.Fatalf("expected %s error, got %v", tt.key, err)
			}
		})
	}
}

func TestPineconeConfigAcceptsBoundaries(t *testing.T) {
	for _, values := range [][3]string{{"1", "1s", "1"}, {"1000", "2m", "10"}} {
		t.Run(strings.Join(values[:], "/"), func(t *testing.T) {
			t.Setenv("PINECONE_UPSERT_BATCH_SIZE", values[0])
			t.Setenv("PINECONE_TIMEOUT", values[1])
			t.Setenv("PINECONE_MAX_ATTEMPTS", values[2])
			pineconeLoadConfig(t)
		})
	}
}
