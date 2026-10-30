package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Victoroide/quarry-rag/internal/metrics"
	"github.com/stretchr/testify/require"
)

func TestRequestsAreCountedByPattern(t *testing.T) {
	m := metrics.NewService(nil)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /documents/{id}", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })
	mux.HandleFunc("GET /panic", func(http.ResponseWriter, *http.Request) { panic("failure") })
	h := RequestID(Metrics(Logging(Recovery(mux)), m))
	for _, path := range []string{"/documents/one", "/documents/two", "/unknown/one", "/unknown/two", "/panic"} {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, http.NoBody))
	}
	var b strings.Builder
	require.NoError(t, m.Registry.WriteText(&b))
	require.Contains(t, b.String(), `quarry_http_requests_total{route="GET /documents/{id}",code="204"} 2`)
	require.Contains(t, b.String(), `quarry_http_requests_total{route="unmatched",code="404"} 2`)
	require.Contains(t, b.String(), `quarry_http_requests_total{route="GET /panic",code="500"} 1`)
	require.Contains(t, b.String(), `quarry_http_request_duration_seconds_count{route="unmatched"} 2`)
	require.NotContains(t, b.String(), "/unknown/")
	require.NotContains(t, b.String(), "/documents/one")
}
