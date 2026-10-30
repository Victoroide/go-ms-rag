package config

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestLoadIngestionSettings(t *testing.T) {
	t.Setenv("GO_RAG_FAKE_MODE", "true")
	t.Setenv("DATABASE_URL", "host=localhost dbname=rag")
	for _, key := range []string{"INGEST_WORKERS", "INGEST_MAX_ATTEMPTS", "INGEST_LEASE", "INGEST_JOB_TIMEOUT", "INGEST_POLL_INTERVAL", "WORKER_QUEUE_SIZE"} {
		t.Setenv(key, "")
	}
	c, err := Load()
	require.NoError(t, err)
	require.Equal(t, 2, c.IngestWorkers)
	require.Equal(t, 5, c.IngestMaxAttempts)
	require.Equal(t, 2*time.Minute, c.IngestLease)
	require.Equal(t, 15*time.Minute, c.IngestJobTimeout)
	require.Equal(t, time.Second, c.IngestPollInterval)
	require.Equal(t, 1000, c.WorkerQueueSize)
	for _, tt := range []struct{ key, value string }{{"INGEST_WORKERS", "0"}, {"INGEST_WORKERS", "33"}, {"INGEST_MAX_ATTEMPTS", "21"}, {"INGEST_MAX_ATTEMPTS", "bad"}, {"INGEST_LEASE", "29s"}, {"INGEST_LEASE", "31m"}, {"INGEST_JOB_TIMEOUT", "30s"}, {"INGEST_JOB_TIMEOUT", "2m"}, {"INGEST_JOB_TIMEOUT", "121m"}, {"INGEST_POLL_INTERVAL", "99ms"}, {"INGEST_POLL_INTERVAL", "61s"}, {"WORKER_QUEUE_SIZE", "100001"}} {
		t.Run(tt.key+tt.value, func(t *testing.T) {
			t.Setenv(tt.key, tt.value)
			_, loadErr := Load()
			require.Error(t, loadErr)
			require.True(t, strings.Contains(loadErr.Error(), tt.key), loadErr.Error())
		})
	}
	t.Setenv("WORKER_QUEUE_SIZE", "100000")
	t.Setenv("INGEST_WORKERS", "32")
	t.Setenv("INGEST_MAX_ATTEMPTS", "20")
	t.Setenv("INGEST_LEASE", "30m")
	t.Setenv("INGEST_JOB_TIMEOUT", "2h")
	t.Setenv("INGEST_POLL_INTERVAL", "100ms")
	_, err = Load()
	require.NoError(t, err)
}
