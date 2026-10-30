package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Victoroide/quarry-rag/internal/config"
	"github.com/Victoroide/quarry-rag/internal/domain"
	"github.com/Victoroide/quarry-rag/internal/store/memory"
)

type probeStore struct{ domain.ChunkStore }

func (probeStore) Ping(context.Context) error { return nil }
func (probeStore) ListDocuments(context.Context) ([]domain.DocumentRef, error) {
	return []domain.DocumentRef{}, nil
}
func TestServiceRoutesRequireAuthentication(t *testing.T) {
	router := NewRouter(&RouterDeps{Config: &config.Config{ServiceToken: "secret"}, ChunkStore: probeStore{}, VectorStore: memory.NewFakeVectorStore()})
	for _, route := range []struct{ method, path string }{{http.MethodPost, "/ingest"}, {http.MethodPost, "/query"}, {http.MethodGet, "/documents"}, {http.MethodGet, "/documents/550e8400-e29b-41d4-a716-446655440000/file"}, {http.MethodDelete, "/documents/550e8400-e29b-41d4-a716-446655440000"}} {
		req := httptest.NewRequestWithContext(t.Context(), route.method, route.path, http.NoBody)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s: got %d", route.method, route.path, rec.Code)
		}
	}
	for _, route := range []struct{ path, token string }{{"/health", ""}, {"/documents", "secret"}} {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, route.path, http.NoBody)
		req.Header.Set("X-Service-Token", route.token)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Errorf("%s: got %d", route.path, rec.Code)
		}
	}
}

func TestServiceRoutesAcceptRotationTokens(t *testing.T) {
	router := NewRouter(&RouterDeps{Config: &config.Config{ServiceTokens: []string{"old-secret", "new-secret"}}, ChunkStore: probeStore{}, VectorStore: memory.NewFakeVectorStore()})
	for _, route := range []struct{ method, path string }{
		{http.MethodPost, "/ingest"},
		{http.MethodPost, "/query"},
		{http.MethodGet, "/documents"},
		{http.MethodGet, "/documents/not-a-uuid"},
		{http.MethodGet, "/documents/not-a-uuid/file"},
		{http.MethodDelete, "/documents/not-a-uuid"},
	} {
		for _, token := range []string{"", "wrong-secret", "old-secret", "new-secret"} {
			req := httptest.NewRequestWithContext(t.Context(), route.method, route.path, http.NoBody)
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Service-Token", token)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			want := http.StatusUnauthorized
			if token == "old-secret" || token == "new-secret" {
				want = http.StatusBadRequest
				if route.path == "/documents" {
					want = http.StatusOK
				}
			}
			if rec.Code != want {
				t.Errorf("%s %s with token %q: got %d, want %d", route.method, route.path, token, rec.Code, want)
			}
		}
	}
}
