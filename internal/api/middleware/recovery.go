package middleware

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"runtime/debug"
)

// Recovery wraps an http.Handler and recovers from panics,
// returning a 500 Internal Server Error instead of crashing the process.
func Recovery(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rw, ok := w.(*responseWriter)
		if !ok {
			rw = &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}
		}
		defer func(ctx context.Context) {
			if rec := recover(); rec != nil {
				if err, ok := rec.(error); ok && errors.Is(err, http.ErrAbortHandler) {
					panic(rec)
				}
				slog.ErrorContext(ctx, "panic recovered", "panic", rec, "stack", string(debug.Stack()))
				if !rw.Written() {
					writeJSONErr(rw, http.StatusInternalServerError, "internal", "internal server error")
				}
			}
		}(r.Context())
		next.ServeHTTP(rw, r)
	})
}
