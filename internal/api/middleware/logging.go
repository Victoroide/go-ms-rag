// Package middleware provides HTTP middleware for logging and recovery.
package middleware

import (
	"log/slog"
	"net/http"
	"time"
)

// Logging wraps an http.Handler and logs each request's method, path,
// status code, bytes written, duration, and remote address.
func Logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}
		next.ServeHTTP(rw, r)
		slog.InfoContext(r.Context(), "http request", "method", r.Method, "path", r.URL.Path, "status", rw.statusCode, "bytes", rw.bytes, "duration_ms", float64(time.Since(start))/float64(time.Millisecond), "remote_addr", r.RemoteAddr)
	})
}

type responseWriter struct {
	http.ResponseWriter
	statusCode int
	written    bool
	bytes      int
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
	n, err := rw.ResponseWriter.Write(p)
	rw.bytes += n
	return n, err
}

// Written reports whether response headers have been sent.
func (rw *responseWriter) Written() bool { return rw.written }

func (rw *responseWriter) Unwrap() http.ResponseWriter { return rw.ResponseWriter }
