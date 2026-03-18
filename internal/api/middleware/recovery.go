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
				http.Error(w, `{"error":"internal","message":"internal server error"}`,
					http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}
