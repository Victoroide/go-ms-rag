package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/Victoroide/quarry-rag/internal/api/handler"
	"github.com/Victoroide/quarry-rag/internal/metrics"
	"github.com/stretchr/testify/require"
)

func TestMetricsExposition(t *testing.T) {
	m := metrics.NewService(func() float64 { return 7 })
	srv := metricsServer("127.0.0.1:0", m.Registry)
	require.Positive(t, srv.ReadHeaderTimeout)
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", srv.Addr)
	require.NoError(t, err)
	mainListener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	mainServer := &http.Server{ReadHeaderTimeout: time.Second, Handler: http.NotFoundHandler()}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- serve(ctx, mainServer, mainListener, handler.NewProbes(nil, nil, nil, time.Minute), 0, httpEndpoint{srv, listener})
	}()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+listener.Addr().String()+"/metrics", http.NoBody)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, resp.Body.Close())
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "text/plain; version=0.0.4; charset=utf-8", resp.Header.Get("Content-Type"))
	require.Contains(t, string(body), "quarry_ingest_jobs_pending 7\n")
	cancel()
	require.NoError(t, <-done)
	for _, addr := range []string{listener.Addr().String(), mainListener.Addr().String()} {
		conn, dialErr := (&net.Dialer{Timeout: time.Second}).DialContext(t.Context(), "tcp", addr)
		if conn != nil {
			require.NoError(t, conn.Close())
		}
		require.Error(t, dialErr)
	}
}
