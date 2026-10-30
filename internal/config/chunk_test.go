package config

import (
	"strings"
	"testing"
)

func TestChunkTokenSizeMinimum(t *testing.T) {
	cfg := baseValidConfig()
	cfg.ChunkOverlap = 0
	cfg.ChunkTokenSize = 1
	if err := cfg.validate(); err == nil || !strings.Contains(err.Error(), "CHUNK_TOKEN_SIZE") {
		t.Fatalf("one-token budget must be rejected: %v", err)
	}
	cfg.ChunkTokenSize = 2
	cfg.ChunkOverlap = 1
	if err := cfg.validate(); err != nil {
		t.Fatalf("two-token budget must be accepted: %v", err)
	}
}
