// Package middleware provides HTTP middleware for logging and recovery.
package middleware

import (
	"log/slog"
	"net/http"
	"time"
)

// Logging wraps an http.Handler and logs each request's method, path,
// status code, and duration.
func Logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}
		next.ServeHTTP(rw, r)
		slog.InfoContext(r.Context(), "http request", "method", r.Method, "path", r.URL.Path, "status", rw.statusCode, "duration", time.Since(start))
	})
}

type responseWriter struct {
	http.ResponseWriter
	statusCode int
	written    bool
}

func (rw *responseWriter) WriteHeader(code int) {
	if rw.written {
		return
	}
	rw.written = true
	rw.statusCode = code
	rw.ResponseWriter.WriteHeader(code)
}

func (rw *responseWriter) Write(p []byte) (int, error) {
	if !rw.written {
		rw.WriteHeader(http.StatusOK)
	}
	return rw.ResponseWriter.Write(p)
}

func (rw *responseWriter) Unwrap() http.ResponseWriter { return rw.ResponseWriter }
