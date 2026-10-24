package middleware

import (
	"log"
	"net/http"
	"runtime/debug"
)

// Recovery wraps an http.Handler and recovers from panics,
// returning a 500 Internal Server Error instead of crashing the process.
func Recovery(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("panic recovered: %v\n%s", rec, debug.Stack())
				writeJSONErr(w, http.StatusInternalServerError, "internal", "internal server error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}
