package middleware

import (
	"crypto/subtle"
	"encoding/json"
	"log"
	"net/http"
)

// RequireServiceToken guards a single handler with a shared service token. When
// token is non-empty, requests must present a matching X-Service-Token header
// or receive 401. When token is empty, the handler is open (backward
// compatible) — callers should log that auth is disabled at startup.
//
// This is the service-to-service auth for the caller-to-service boundary: every route
// except GET /health is wrapped (POST /ingest, POST /query, GET /documents,
// GET /documents/{id}/file and DELETE /documents/{id}). The header comparison is
// constant-time so a matching token cannot be recovered via response timing.
func RequireServiceToken(token string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		presented := r.Header.Get("X-Service-Token")
		if token != "" && subtle.ConstantTimeCompare([]byte(presented), []byte(token)) != 1 {
			writeJSONErr(w, http.StatusUnauthorized, "unauthorized", "invalid or missing X-Service-Token")
			return
		}
		next(w, r)
	}
}

// LogServiceAuthState reports whether the shared token protects service routes.
func LogServiceAuthState(token string) {
	if token == "" {
		log.Printf("auth: SERVICE_TOKEN empty; service auth DISABLED (mutating routes are public)")
		return
	}
	log.Printf("auth: SERVICE_TOKEN set; X-Service-Token required on every route except GET /health")
}

// writeJSONErr mirrors handler.writeJSONError's {"error","message"} envelope.
// Shared by auth and recovery to avoid a middleware -> handler import edge.
func writeJSONErr(w http.ResponseWriter, code int, errType, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": errType, "message": message})
}
