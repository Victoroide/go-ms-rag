package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func queryConfigEnvironment(t *testing.T) {
	t.Helper()
	t.Setenv("GO_RAG_FAKE_MODE", "true")
	t.Setenv("DATABASE_URL", "host=localhost dbname=rag")
	for _, key := range []string{"QUERY_TIMEOUT", "SYNTHESIS_TIMEOUT", "QUERY_CONCURRENCY", "QUERY_QUEUE_TIMEOUT", "MIN_RELEVANCE_SCORE", "HTTP_READ_TIMEOUT", "HTTP_WRITE_TIMEOUT", "HTTP_IDLE_TIMEOUT"} {
		t.Setenv(key, "")
	}
}
func TestWriteTimeoutMustExceedQueryTimeout(t *testing.T) {
	for _, v := range []string{"59s", "60s"} {
		t.Run(v, func(t *testing.T) {
			queryConfigEnvironment(t)
			t.Setenv("HTTP_WRITE_TIMEOUT", v)
			_, err := Load()
			require.Error(t, err)
			require.Contains(t, err.Error(), "HTTP_WRITE_TIMEOUT")
		})
	}
}
func TestQueryConfigDefaults(t *testing.T) {
	queryConfigEnvironment(t)
	cfg, err := Load()
	require.NoError(t, err)
	require.Equal(t, time.Minute, cfg.QueryTimeout)
	require.Equal(t, 45*time.Second, cfg.SynthesisTimeout)
	require.Equal(t, 32, cfg.QueryConcurrency)
	require.Equal(t, 2*time.Second, cfg.QueryQueueTimeout)
	require.Zero(t, cfg.MinRelevanceScore)
	require.Equal(t, time.Minute, cfg.HTTPReadTimeout)
	require.Equal(t, 75*time.Second, cfg.HTTPWriteTimeout)
	require.Equal(t, time.Minute, cfg.HTTPIdleTimeout)
	t.Setenv("QUERY_TIMEOUT", "2m")
	cfg, err = Load()
	require.NoError(t, err)
	require.Equal(t, 135*time.Second, cfg.HTTPWriteTimeout)
}
func TestQueryConfigRejectsInvalidBounds(t *testing.T) {
	for _, tc := range []struct{ key, value string }{
		{"QUERY_TIMEOUT", "4s"}, {"QUERY_TIMEOUT", "601s"}, {"QUERY_TIMEOUT", "bad"}, {"SYNTHESIS_TIMEOUT", "0s"}, {"SYNTHESIS_TIMEOUT", "60s"}, {"SYNTHESIS_TIMEOUT", "bad"},
		{"QUERY_CONCURRENCY", "0"}, {"QUERY_CONCURRENCY", "1025"}, {"QUERY_CONCURRENCY", "bad"}, {"QUERY_QUEUE_TIMEOUT", "-1s"}, {"QUERY_QUEUE_TIMEOUT", "61s"}, {"QUERY_QUEUE_TIMEOUT", "bad"},
		{"MIN_RELEVANCE_SCORE", "-0.1"}, {"MIN_RELEVANCE_SCORE", "1.1"}, {"MIN_RELEVANCE_SCORE", "NaN"}, {"MIN_RELEVANCE_SCORE", "Inf"}, {"MIN_RELEVANCE_SCORE", "bad"},
		{"HTTP_READ_TIMEOUT", "4s"}, {"HTTP_READ_TIMEOUT", "31m"}, {"HTTP_READ_TIMEOUT", "bad"}, {"HTTP_WRITE_TIMEOUT", "bad"}, {"HTTP_IDLE_TIMEOUT", "0s"}, {"HTTP_IDLE_TIMEOUT", "-1s"}, {"HTTP_IDLE_TIMEOUT", "bad"},
	} {
		t.Run(tc.key+"/"+tc.value, func(t *testing.T) {
			queryConfigEnvironment(t)
			t.Setenv(tc.key, tc.value)
			_, err := Load()
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.key)
		})
	}
}

func TestQueryConfigAcceptsBoundaries(t *testing.T) {
	for _, tc := range []struct{ query, synthesis, concurrency, queue, score, read string }{
		{"5s", "1s", "1", "0s", "0", "5s"},
		{"10m", "9m", "1024", "60s", "1", "30m"},
	} {
		t.Run(tc.query, func(t *testing.T) {
			queryConfigEnvironment(t)
			t.Setenv("QUERY_TIMEOUT", tc.query)
			t.Setenv("SYNTHESIS_TIMEOUT", tc.synthesis)
			t.Setenv("QUERY_CONCURRENCY", tc.concurrency)
			t.Setenv("QUERY_QUEUE_TIMEOUT", tc.queue)
			t.Setenv("MIN_RELEVANCE_SCORE", tc.score)
			t.Setenv("HTTP_READ_TIMEOUT", tc.read)
			t.Setenv("HTTP_WRITE_TIMEOUT", "11m")
			t.Setenv("HTTP_IDLE_TIMEOUT", "30s")
			_, err := Load()
			require.NoError(t, err)
		})
	}
}
