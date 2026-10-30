package config

import (
	"strings"
	"testing"
)

func TestS3BucketHasNoDefault(t *testing.T) {
	t.Setenv("GO_RAG_FAKE_MODE", "true")
	t.Setenv("DATABASE_URL", "host=localhost dbname=rag")
	t.Setenv("S3_BUCKET", "")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.S3Bucket != "" {
		t.Fatalf("unconfigured bucket = %q, want empty", cfg.S3Bucket)
	}
}

func TestExpectedBucketOwnerValidation(t *testing.T) {
	t.Setenv("GO_RAG_FAKE_MODE", "true")
	t.Setenv("DATABASE_URL", "host=localhost dbname=rag")
	for _, owner := range []string{"", "123456789012", "000000000000", "12345678901", "1234567890123", "12345678901x", " 123456789012", "123456789012\n", "１２３４５６７８９０１２"} {
		t.Run(owner, func(t *testing.T) {
			t.Setenv("S3_EXPECTED_BUCKET_OWNER", owner)
			cfg, err := Load()
			valid := owner == "" || owner == "123456789012" || owner == "000000000000"
			if valid && err != nil {
				t.Fatalf("valid owner rejected: %v", err)
			}
			if valid && cfg.S3ExpectedBucketOwner != owner {
				t.Fatalf("configured owner = %q, want %q", cfg.S3ExpectedBucketOwner, owner)
			}
			if !valid && (err == nil || !strings.Contains(err.Error(), "S3_EXPECTED_BUCKET_OWNER")) {
				t.Fatalf("invalid owner accepted or wrong error: %v", err)
			}
		})
	}
}
