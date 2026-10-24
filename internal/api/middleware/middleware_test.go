package middleware

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRecoveryReturnsJSON(t *testing.T) {
	rec := httptest.NewRecorder()
	Recovery(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("test") })).ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", http.NoBody))
	if rec.Code != 500 || rec.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("invalid recovery response: %d %s", rec.Code, rec.Header())
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body["error"] != "internal" {
		t.Fatalf("invalid error body: %s", rec.Body)
	}
}
func TestResponseWriterKeepsFirstStatus(t *testing.T) {
	rec := httptest.NewRecorder()
	rw := &responseWriter{ResponseWriter: rec, statusCode: 200}
	rw.WriteHeader(201)
	rw.WriteHeader(500)
	if rw.statusCode != 201 {
		t.Fatalf("logged status %d differs from sent status %d", rw.statusCode, rec.Code)
	}
}
