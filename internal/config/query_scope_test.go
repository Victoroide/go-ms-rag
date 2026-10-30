package config

import (
	"strings"
	"testing"
)

func TestQueryScopeConfiguration(t *testing.T) {
	t.Setenv("GO_RAG_FAKE_MODE", "true")
	t.Setenv("DATABASE_URL", "host=localhost dbname=rag")
	t.Setenv("MAX_ALLOWED_DOCUMENT_IDS", "")
	t.Setenv("REQUIRE_DOCUMENT_ALLOWLIST", "")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MaxAllowedDocumentIDs != 25000 || cfg.RequireDocumentAllowlist {
		t.Fatalf("incorrect defaults: %d %v", cfg.MaxAllowedDocumentIDs, cfg.RequireDocumentAllowlist)
	}
	for _, tt := range []struct {
		key, value string
		valid      bool
	}{
		{"MAX_ALLOWED_DOCUMENT_IDS", "1", true}, {"MAX_ALLOWED_DOCUMENT_IDS", "100000", true},
		{"MAX_ALLOWED_DOCUMENT_IDS", "0", false}, {"MAX_ALLOWED_DOCUMENT_IDS", "100001", false}, {"MAX_ALLOWED_DOCUMENT_IDS", "bad", false},
		{"REQUIRE_DOCUMENT_ALLOWLIST", "true", true}, {"REQUIRE_DOCUMENT_ALLOWLIST", "bad", false},
	} {
		t.Run(tt.key+tt.value, func(t *testing.T) {
			t.Setenv(tt.key, tt.value)
			got, err := Load()
			if tt.valid {
				if err != nil {
					t.Fatal(err)
				}
				if tt.key == "REQUIRE_DOCUMENT_ALLOWLIST" && !got.RequireDocumentAllowlist {
					t.Fatal("requirement not enabled")
				}
			} else if err == nil || !strings.Contains(err.Error(), tt.key) {
				t.Fatalf("expected %s error, got %v", tt.key, err)
			}
		})
	}
}
