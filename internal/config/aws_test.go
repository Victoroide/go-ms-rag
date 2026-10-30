package config

import (
	"strings"
	"testing"
	"time"
)

func awsConfigEnvironment(t *testing.T) {
	t.Helper()
	t.Setenv("GO_RAG_FAKE_MODE", "false")
	t.Setenv("DATABASE_URL", "host=localhost dbname=rag")
	t.Setenv("AWS_REGION", "us-east-1")
	t.Setenv("AWS_ACCESS_KEY_ID", "")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "")
	t.Setenv("AWS_SESSION_TOKEN", "")
	t.Setenv("AWS_MAX_ATTEMPTS", "")
	t.Setenv("EMBED_TIMEOUT", "")
	t.Setenv("PINECONE_API_KEY", "test-key")
	t.Setenv("PINECONE_INDEX_NAME", "quarry-rag")
	t.Setenv("SERVICE_TOKEN", "test-token")
}

func TestAWSKeysAreOptional(t *testing.T) {
	awsConfigEnvironment(t)
	if _, err := Load(); err != nil {
		t.Fatalf("configuration without static AWS keys rejected: %v", err)
	}
}

func TestAWSKeysMustBePaired(t *testing.T) {
	for _, fake := range []string{"false", "true"} {
		for _, key := range []string{"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY"} {
			t.Run(fake+"/"+key, func(t *testing.T) {
				awsConfigEnvironment(t)
				t.Setenv("GO_RAG_FAKE_MODE", fake)
				t.Setenv(key, "test-value")
				if _, err := Load(); err == nil || !strings.Contains(err.Error(), "AWS_ACCESS_KEY_ID") || !strings.Contains(err.Error(), "AWS_SECRET_ACCESS_KEY") {
					t.Fatalf("expected paired-key validation error, got %v", err)
				}
			})
		}
	}
}

func TestAWSConfigRejectsInvalidBounds(t *testing.T) {
	for _, tt := range []struct{ key, value string }{
		{"AWS_MAX_ATTEMPTS", "0"}, {"AWS_MAX_ATTEMPTS", "11"}, {"AWS_MAX_ATTEMPTS", "bad"},
		{"EMBED_TIMEOUT", "999ms"}, {"EMBED_TIMEOUT", "121s"}, {"EMBED_TIMEOUT", "bad"},
	} {
		t.Run(tt.key+"/"+tt.value, func(t *testing.T) {
			awsConfigEnvironment(t)
			t.Setenv("AWS_ACCESS_KEY_ID", "test-key")
			t.Setenv("AWS_SECRET_ACCESS_KEY", "test-secret")
			t.Setenv(tt.key, tt.value)
			if _, err := Load(); err == nil || !strings.Contains(err.Error(), tt.key) {
				t.Fatalf("expected %s error, got %v", tt.key, err)
			}
		})
	}
}

func TestAWSConfigDefaultsAndOverrides(t *testing.T) {
	awsConfigEnvironment(t)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AWSMaxAttempts != 4 || cfg.EmbedTimeout != 15*time.Second || cfg.AWSSessionToken != "" {
		t.Fatalf("unexpected defaults: attempts=%d timeout=%s", cfg.AWSMaxAttempts, cfg.EmbedTimeout)
	}
	for _, tt := range []struct {
		attempts     string
		timeout      string
		wantAttempts int
		wantTimeout  time.Duration
	}{
		{"1", "1s", 1, time.Second}, {"10", "2m", 10, 2 * time.Minute},
	} {
		t.Run(tt.attempts+"/"+tt.timeout, func(t *testing.T) {
			t.Setenv("AWS_MAX_ATTEMPTS", tt.attempts)
			t.Setenv("EMBED_TIMEOUT", tt.timeout)
			t.Setenv("AWS_SESSION_TOKEN", "test-session")
			cfg, err := Load()
			if err != nil {
				t.Fatal(err)
			}
			if cfg.AWSMaxAttempts != tt.wantAttempts || cfg.EmbedTimeout != tt.wantTimeout || cfg.AWSSessionToken != "test-session" {
				t.Fatalf("configuration overrides not applied: attempts=%d timeout=%s", cfg.AWSMaxAttempts, cfg.EmbedTimeout)
			}
		})
	}
}
