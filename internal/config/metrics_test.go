package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMetricsAddress(t *testing.T) {
	t.Setenv("GO_RAG_FAKE_MODE", "true")
	t.Setenv("DATABASE_URL", "host=localhost dbname=rag")
	for _, addr := range []string{"", "127.0.0.1:9090"} {
		t.Setenv("METRICS_ADDR", addr)
		cfg, err := Load()
		require.NoError(t, err)
		require.Equal(t, addr, cfg.MetricsAddr)
	}
}
