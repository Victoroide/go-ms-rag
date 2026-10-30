package middleware

import (
	"net/http"
	"strconv"
	"time"

	"github.com/Victoroide/quarry-rag/internal/metrics"
)

// Metrics records requests by their matched route pattern after dispatch.
func Metrics(next http.Handler, service *metrics.Service) http.Handler {
	if service == nil {
		service = metrics.Default
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}
		r = r.WithContext(metrics.WithService(r.Context(), service))
		next.ServeHTTP(rw, r)
		route := r.Pattern
		if route == "" {
			route = "unmatched"
		}
		service.HTTPRequests.WithLabelValues(route, strconv.Itoa(rw.statusCode)).Inc()
		service.HTTPDuration.WithLabelValues(route).Observe(time.Since(start).Seconds())
	})
}
