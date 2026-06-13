package middleware

import (
	"encoding/json"
	"log"
	"net/http"
)

// RequireServiceToken guards a single handler with a shared service token. When
// token is non-empty, requests must present a matching X-Service-Token header
// or receive 401. When token is empty, the handler is open (backward
// compatible) — callers should log that auth is disabled at startup.
//
// This is the service-to-service auth for the Django -> Go seam: only the
// mutating/expensive routes (POST /ingest, POST /query, DELETE /documents/{id})
// are wrapped; GET /health and the presigned file GET stay public.
func RequireServiceToken(token string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if token != "" && r.Header.Get("X-Service-Token") != token {
			writeJSONErr(w, http.StatusUnauthorized, "unauthorized", "invalid or missing X-Service-Token")
			return
		}
		next(w, r)
	}
}

// LogServiceAuthState logs once whether service auth is enforced, without
// leaking the token value.
func LogServiceAuthState(token string) {
	if token == "" {
		log.Printf("auth: SERVICE_TOKEN empty; service auth DISABLED (mutating routes are public)")
		return
	}
	log.Printf("auth: SERVICE_TOKEN set; X-Service-Token required on /ingest, /query, DELETE /documents/{id}")
}

// writeJSONErr mirrors handler.writeJSONError's {"error","message"} envelope.
// Inlined here (as the Recovery middleware already does for its 500 body) to
// avoid a middleware -> handler import edge.
func writeJSONErr(w http.ResponseWriter, code int, errType, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": errType, "message": message})
}
