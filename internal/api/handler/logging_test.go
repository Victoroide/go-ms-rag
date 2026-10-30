package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Victoroide/quarry-rag/internal/logctx"
	"github.com/stretchr/testify/require"
)

func captureLeveledLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var logs bytes.Buffer
	old, flags := slog.Default(), log.Flags()
	slog.SetDefault(slog.New(logctx.NewHandler(slog.NewJSONHandler(&logs, nil))))
	t.Cleanup(func() { slog.SetDefault(old); log.SetFlags(flags) })
	return &logs
}

func TestHealthProbeLogsWarning(t *testing.T) {
	logs := captureLeveledLogs(t)
	vs := &healthVectorStore{check: func(context.Context) error { return errors.New("connection unavailable") }}
	ctx := logctx.WithRequestID(t.Context(), "probe-request")
	status := NewProbes(vs, &healthChunkStore{}, nil, time.Minute).Snapshot(ctx)
	require.Equal(t, "degraded", status.Status)
	var record map[string]any
	require.NoError(t, json.Unmarshal(logs.Bytes(), &record))
	require.Equal(t, "WARN", record["level"])
	require.Equal(t, "pinecone", record["dependency"])
	require.Equal(t, "connection unavailable", record["error"])
	require.Equal(t, "probe-request", record["request_id"])
}

type leveledFailWriter struct{ httptest.ResponseRecorder }

func (*leveledFailWriter) Write([]byte) (int, error) { return 0, errors.New("connection closed") }

func TestHealthResponseLogsEncodingError(t *testing.T) {
	logs := captureLeveledLogs(t)
	ctx := logctx.WithRequestID(t.Context(), "health-request")
	req := httptest.NewRequestWithContext(ctx, http.MethodGet, "/health", http.NoBody)
	writer := &leveledFailWriter{*httptest.NewRecorder()}
	HealthHandler(NewProbes(&healthVectorStore{}, &healthChunkStore{}, nil, time.Minute))(writer, req)
	var record map[string]any
	require.NoError(t, json.Unmarshal(logs.Bytes(), &record))
	require.Equal(t, "ERROR", record["level"])
	require.Equal(t, "connection closed", record["error"])
	require.Equal(t, "health-request", record["request_id"])
}
