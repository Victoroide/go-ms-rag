package config

import (
	"strings"
	"testing"
	"time"
)

func TestProbeConfiguration(t *testing.T) {
	t.Setenv("DATABASE_URL", "host=localhost dbname=rag")
	t.Setenv("GO_RAG_FAKE_MODE", "true")
	for _, tt := range []struct {
		name, drain, ttl, invalid string
		wantDrain, wantTTL        time.Duration
	}{
		{name: "defaults", wantDrain: 5 * time.Second, wantTTL: 10 * time.Second},
		{name: "minimum", drain: "0s", ttl: "1s", wantTTL: time.Second},
		{name: "maximum", drain: "60s", ttl: "5m", wantDrain: time.Minute, wantTTL: 5 * time.Minute},
		{name: "negative drain", drain: "-1ns", invalid: "SHUTDOWN_DRAIN_DELAY"},
		{name: "long drain", drain: "61s", invalid: "SHUTDOWN_DRAIN_DELAY"},
		{name: "malformed drain", drain: "bad", invalid: "SHUTDOWN_DRAIN_DELAY"},
		{name: "zero ttl", ttl: "0s", invalid: "HEALTH_CACHE_TTL"},
		{name: "short ttl", ttl: "999ms", invalid: "HEALTH_CACHE_TTL"},
		{name: "long ttl", ttl: "301s", invalid: "HEALTH_CACHE_TTL"},
		{name: "malformed ttl", ttl: "bad", invalid: "HEALTH_CACHE_TTL"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("SHUTDOWN_DRAIN_DELAY", tt.drain)
			t.Setenv("HEALTH_CACHE_TTL", tt.ttl)
			cfg, err := Load()
			if tt.invalid != "" {
				if err == nil || !strings.Contains(err.Error(), tt.invalid) {
					t.Fatalf("expected %s error, got %v", tt.invalid, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if cfg.ShutdownDrainDelay != tt.wantDrain || cfg.HealthCacheTTL != tt.wantTTL {
				t.Fatalf("probe configuration = %s, %s", cfg.ShutdownDrainDelay, cfg.HealthCacheTTL)
			}
		})
	}
}
