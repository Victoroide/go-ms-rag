package config

import (
	"strings"
	"testing"
	"time"
)

// baseValidConfig returns a Config that passes validate() in real (non-fake)
// mode, so each test can flip exactly one field and assert the guard it targets.
func baseValidConfig() *Config {
	return &Config{
		ServerPort: "8081", ChunkTokenSize: 512, ChunkOverlap: 64, LocalRetrievalThreshold: 0.7, MaxChunksPerQuery: 10, MaxUploadBytes: 20 << 20, EmbeddingDimensions: 1024, SynthesisModel: "fallback",
		DatabaseURL:     "postgres://localhost:5432/db?sslmode=disable",
		AWSRegion:       "us-east-1",
		AWSAccessKeyID:  "AKIA_TEST",
		AWSSecretKey:    "secret_test",
		PineconeAPIKey:  "pc_test",
		WorkerQueueSize: 1000,
		IngestWorkers:   2, IngestMaxAttempts: 5, IngestLease: 2 * time.Minute, IngestJobTimeout: 15 * time.Minute, IngestPollInterval: time.Second,
		EmbedConcurrency:        10,
		ServiceToken:            "a-strong-service-token",
		FakeMode:                false,
		PineconeUpsertBatchSize: 100,
		PineconeTimeout:         10 * time.Second,
		PineconeMaxAttempts:     5,
		ExtractIsolation:        true, ExtractTimeout: 30 * time.Second, ExtractConcurrency: 2, ExtractMemoryLimitMB: 512, ExtractQueueTimeout: 5 * time.Second,
		MaxAllowedDocumentIDs: 25000,
		AWSMaxAttempts:        4, EmbedTimeout: 15 * time.Second,
	}
}

func TestValidate_NonFakeRequiresServiceToken(t *testing.T) {
	cfg := baseValidConfig()
	cfg.ServiceToken = ""
	if err := cfg.validate(); err == nil {
		t.Fatal("expected validate() to fail when SERVICE_TOKEN is empty in non-fake mode")
	}

	cfg.ServiceToken = "   " // whitespace must not count as a token
	if err := cfg.validate(); err == nil {
		t.Fatal("expected validate() to fail when SERVICE_TOKEN is only whitespace in non-fake mode")
	}
}

func TestValidate_NonFakeWithServiceTokenOK(t *testing.T) {
	cfg := baseValidConfig()
	if err := cfg.validate(); err != nil {
		t.Fatalf("expected valid non-fake config to pass, got %v", err)
	}
}

func TestValidate_FakeModeAllowsEmptyServiceToken(t *testing.T) {
	cfg := baseValidConfig()
	cfg.FakeMode = true
	cfg.ServiceToken = ""
	// Fake mode also runs without cloud creds.
	cfg.AWSRegion, cfg.AWSAccessKeyID, cfg.AWSSecretKey, cfg.PineconeAPIKey = "", "", "", ""
	if err := cfg.validate(); err != nil {
		t.Fatalf("expected fake-mode config with empty SERVICE_TOKEN to pass, got %v", err)
	}
}

func TestValidateOperationalBounds(t *testing.T) {
	for _, tt := range []struct {
		name   string
		mutate func(*Config)
	}{
		{"port", func(c *Config) { c.ServerPort = "70000" }},
		{"chunk size", func(c *Config) { c.ChunkTokenSize = 0 }},
		{"overlap", func(c *Config) { c.ChunkOverlap = c.ChunkTokenSize }},
		{"threshold", func(c *Config) { c.LocalRetrievalThreshold = 2 }},
		{"chunks", func(c *Config) { c.MaxChunksPerQuery = -1 }},
		{"upload", func(c *Config) { c.MaxUploadBytes = 0 }},
		{"dimensions", func(c *Config) { c.EmbeddingDimensions = 3 }},
		{"synthesis", func(c *Config) { c.SynthesisModel = "unknown" }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c := baseValidConfig()
			tt.mutate(c)
			if c.validate() == nil {
				t.Fatal("invalid configuration accepted")
			}
		})
	}
}

func TestLoadRejectsMalformedNumericEnvironment(t *testing.T) {
	t.Setenv("GO_RAG_FAKE_MODE", "true")
	t.Setenv("DATABASE_URL", "postgres://localhost/rag")
	t.Setenv("WORKER_QUEUE_SIZE", "not-a-number")
	if _, err := Load(); err == nil {
		t.Fatal("malformed numeric configuration accepted")
	}
}

func TestLoadParsesEnvironmentOnceAndRejectsMalformedValues(t *testing.T) {
	for _, setting := range []struct{ key, value string }{{"MAX_UPLOAD_BYTES", "bad"}, {"LOCAL_RETRIEVAL_THRESHOLD", "bad"}, {"GO_RAG_FAKE_MODE", "bad"}, {"EMBEDDING_DIMENSIONS", "9999999999999999999999999999"}} {
		t.Run(setting.key, func(t *testing.T) {
			t.Setenv("DATABASE_URL", "host=localhost dbname=rag")
			t.Setenv("GO_RAG_FAKE_MODE", "true")
			t.Setenv(setting.key, setting.value)
			if _, err := Load(); err == nil || !strings.Contains(err.Error(), setting.key) {
				t.Fatalf("expected %s parse failure: %v", setting.key, err)
			}
		})
	}
}
