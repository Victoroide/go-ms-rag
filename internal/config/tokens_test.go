package config

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"slices"
	"strings"
	"testing"
)

func TestConfiguredTokenWhitespaceIsTrimmed(t *testing.T) {
	for _, token := range []string{" secret\n", "\tsecret\r\n", "\u00a0secret\u2003"} {
		cfg := baseValidConfig()
		cfg.ServiceToken = token
		if err := cfg.validate(); err != nil {
			t.Fatal(err)
		}
		if cfg.ServiceToken != "secret" {
			t.Errorf("configured token retains surrounding whitespace: %q", cfg.ServiceToken)
		}
	}
}

func TestServiceTokensAreMerged(t *testing.T) {
	t.Setenv("GO_RAG_FAKE_MODE", "true")
	t.Setenv("DATABASE_URL", "host=localhost dbname=rag")
	t.Setenv("SERVICE_TOKEN", " old-secret\n")
	t.Setenv("SERVICE_TOKENS", "new-secret, , old-secret,\tnew-secret\r\n, third-secret,")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ServiceToken != "old-secret" || !slices.Equal(cfg.ServiceTokens, []string{"old-secret", "new-secret", "third-secret"}) {
		t.Fatalf("tokens were not trimmed, merged and deduplicated: %#v", cfg.ServiceTokens)
	}
}

func TestServiceTokensRejectWhitespaceAndControls(t *testing.T) {
	for _, token := range []string{"private secret", "private\tsecret", "private\nsecret", "private\u2003secret", "private\x00secret", "private\x7fsecret"} {
		t.Run(token, func(t *testing.T) {
			t.Setenv("GO_RAG_FAKE_MODE", "true")
			t.Setenv("DATABASE_URL", "host=localhost dbname=rag")
			t.Setenv("SERVICE_TOKEN", "valid-secret")
			t.Setenv("SERVICE_TOKENS", "other-secret")
			cfg, err := Load()
			if err != nil {
				t.Fatal(err)
			}
			cfg.ServiceTokens = append(cfg.ServiceTokens, token)
			err = cfg.validate()
			if err == nil {
				t.Fatal("token list with whitespace or control characters was accepted")
			}
			if strings.Contains(err.Error(), "private") {
				t.Fatal("configuration error reveals token contents")
			}
		})
	}
}

func TestServiceTokenListSatisfiesRealMode(t *testing.T) {
	cfg := baseValidConfig()
	cfg.ServiceToken = ""
	cfg.ServiceTokens = []string{" list-secret\n"}
	if err := cfg.validate(); err != nil {
		t.Fatalf("token list was rejected in real mode: %v", err)
	}
	if !slices.Equal(cfg.ServiceTokens, []string{"list-secret"}) {
		t.Fatal("token list retains surrounding whitespace")
	}
}

func TestEmptyServiceTokensRequireFakeMode(t *testing.T) {
	for _, fake := range []bool{false, true} {
		cfg := baseValidConfig()
		cfg.FakeMode = fake
		cfg.ServiceToken = "\t\n "
		cfg.ServiceTokens = []string{"", " \r\n"}
		err := cfg.validate()
		if fake && (err != nil || len(cfg.ServiceTokens) != 0) {
			t.Fatalf("empty token list in fake mode: tokens %q, error %v", cfg.ServiceTokens, err)
		}
		if !fake && err == nil {
			t.Fatal("real mode accepted an empty token list")
		}
	}
}

func TestShortServiceTokenWarningContainsOnlyCount(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	cfg := baseValidConfig()
	cfg.ServiceToken = "private-first"
	cfg.ServiceTokens = []string{"private-first", "private-second", strings.Repeat("x", 32), strings.Repeat("é", 16)}
	if err := cfg.validate(); err != nil {
		t.Fatal(err)
	}
	var record map[string]any
	if err := json.Unmarshal(logs.Bytes(), &record); err != nil {
		t.Fatalf("expected one warning: %v", err)
	}
	if record["level"] != "WARN" || record["count"] != float64(2) {
		t.Fatalf("unexpected short-token warning: %s", logs.Bytes())
	}
	if len(record) != 4 || strings.Contains(logs.String(), "private") || strings.Contains(logs.String(), strings.Repeat("x", 32)) {
		t.Fatalf("warning reveals information beyond the token count: %s", logs.Bytes())
	}
}

func TestTokenWithInnerWhitespaceIsRejected(t *testing.T) {
	for _, token := range []string{"private secret", "private\tsecret", "private\nsecret", "private\rsecret", "private\u00a0secret", "private\x00secret", "private\x7fsecret"} {
		t.Run(token, func(t *testing.T) {
			cfg := baseValidConfig()
			cfg.ServiceToken = token
			err := cfg.validate()
			if err == nil {
				t.Fatal("configured token with whitespace or control characters was accepted")
			}
			if strings.Contains(err.Error(), "private") {
				t.Fatal("configuration error reveals token contents")
			}
		})
	}
}
