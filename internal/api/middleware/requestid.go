package middleware

import (
	"log/slog"
	"net/http"
	"regexp"

	"github.com/Victoroide/quarry-rag/internal/logctx"
	"github.com/google/uuid"
)

var requestIDPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

// RequestID accepts or generates a request ID and echoes it in the response.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-ID")
		if !requestIDPattern.MatchString(id) {
			generated, err := uuid.NewV7()
			if err != nil {
				slog.ErrorContext(r.Context(), "generate request ID", "error", err)
				writeJSONErr(w, http.StatusInternalServerError, "internal", "internal server error")
				return
			}
			id = generated.String()
		}
		w.Header().Set("X-Request-ID", id)
		next.ServeHTTP(w, r.WithContext(logctx.WithRequestID(r.Context(), id)))
	})
}
