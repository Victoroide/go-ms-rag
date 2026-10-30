package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Victoroide/quarry-rag/internal/api/handler"
	"github.com/Victoroide/quarry-rag/internal/config"
	"github.com/Victoroide/quarry-rag/internal/store/memory"
)

func TestProbeRoutesArePublic(t *testing.T) {
	router := NewRouter(&RouterDeps{Config: &config.Config{ServiceToken: "secret"}, ChunkStore: probeStore{}, VectorStore: memory.NewFakeVectorStore()})
	for _, path := range []string{"/livez", "/readyz", "/health"} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, http.NoBody))
		if rec.Code != http.StatusOK {
			t.Errorf("%s status = %d, want 200", path, rec.Code)
		}
	}
}

func TestRouterUsesSharedProbeDrainState(t *testing.T) {
	p := handler.NewProbes(memory.NewFakeVectorStore(), probeStore{}, nil, time.Minute)
	p.SetDraining(true)
	router := NewRouter(&RouterDeps{Config: &config.Config{ServiceToken: "secret"}, Probes: p})
	for _, tt := range []struct {
		path string
		code int
	}{{"/livez", http.StatusOK}, {"/readyz", http.StatusServiceUnavailable}} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, tt.path, http.NoBody))
		if rec.Code != tt.code {
			t.Errorf("%s status = %d, want %d", tt.path, rec.Code, tt.code)
		}
	}
}
