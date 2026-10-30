package middleware

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"log/slog"
	"net/http"
)

// RequireServiceToken guards a handler with one token, or leaves it open when empty.
func RequireServiceToken(token string, next http.HandlerFunc) http.HandlerFunc {
	if token == "" {
		return RequireServiceTokens(nil, next)
	}
	return RequireServiceTokens([]string{token}, next)
}

// RequireServiceTokens accepts any configured token in X-Service-Token.
// An empty token list leaves the handler open for offline use.
func RequireServiceTokens(tokens []string, next http.HandlerFunc) http.HandlerFunc {
	digests := make([][sha256.Size]byte, len(tokens))
	for i, token := range tokens {
		digests[i] = sha256.Sum256([]byte(token))
	}
	return func(w http.ResponseWriter, r *http.Request) {
		presented := r.Header.Get("X-Service-Token")
		digest := sha256.Sum256([]byte(presented))
		matched := 0
		for _, configured := range digests {
			matched |= subtle.ConstantTimeCompare(digest[:], configured[:])
		}
		if len(digests) > 0 && (presented == "" || matched != 1) {
			writeJSONErr(w, http.StatusUnauthorized, "unauthorized", "invalid or missing X-Service-Token")
			return
		}
		next(w, r)
	}
}

// LogServiceAuthState reports only how many tokens protect service routes.
func LogServiceAuthState(tokens []string) {
	if len(tokens) == 0 {
		slog.Warn("service authentication disabled", "count", 0)
		return
	}
	slog.Info("service authentication enabled", "count", len(tokens))
}

// writeJSONErr mirrors handler.writeJSONError's {"error","message"} envelope.
// Shared by auth and recovery to avoid a middleware -> handler import edge.
func writeJSONErr(w http.ResponseWriter, code int, errType, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": errType, "message": message})
}
