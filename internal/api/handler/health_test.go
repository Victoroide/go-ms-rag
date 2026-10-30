package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Victoroide/quarry-rag/internal/config"
	"github.com/Victoroide/quarry-rag/internal/domain"
	s3store "github.com/Victoroide/quarry-rag/internal/store/s3"
)

type healthVectorStore struct {
	domain.VectorStore
	calls atomic.Int32
	check func(context.Context) error
}

func (s *healthVectorStore) Ping(ctx context.Context) error {
	s.calls.Add(1)
	if s.check != nil {
		return s.check(ctx)
	}
	return nil
}

type healthChunkStore struct {
	domain.ChunkStore
	calls atomic.Int32
	check func(context.Context) error
}

func (s *healthChunkStore) Ping(ctx context.Context) error {
	s.calls.Add(1)
	if s.check != nil {
		return s.check(ctx)
	}
	return nil
}

func TestHealthIsCachedAcrossConcurrentRequests(t *testing.T) {
	vs, cs := &healthVectorStore{}, &healthChunkStore{}
	var objectCalls atomic.Int32
	objects := healthObjectStore(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		objectCalls.Add(1)
		if r.Method != http.MethodHead {
			t.Errorf("storage request = %s", r.Method)
		}
		w.WriteHeader(http.StatusOK)
	}))
	h := HealthHandler(NewProbes(vs, cs, objects, time.Minute))
	var wg sync.WaitGroup
	for range 100 {
		wg.Go(func() {
			rec := httptest.NewRecorder()
			h(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/health", http.NoBody))
			if rec.Code != http.StatusOK {
				t.Errorf("health status = %d", rec.Code)
			}
		})
	}
	wg.Wait()
	if vs.calls.Load() != 1 || cs.calls.Load() != 1 || objectCalls.Load() != 1 {
		t.Fatalf("dependency calls = %d, %d, %d; want one each", vs.calls.Load(), cs.calls.Load(), objectCalls.Load())
	}
}

func TestHealthReportsPostgresAndNeon(t *testing.T) {
	rec := httptest.NewRecorder()
	HealthHandler(NewProbes(&healthVectorStore{}, &healthChunkStore{}, nil, time.Minute))(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/health", http.NoBody))
	var status map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status["postgres"] != "ok" || status["neon"] != status["postgres"] {
		t.Fatalf("database fields = %v", status)
	}
}

func healthObjectStore(t *testing.T, h http.Handler) *s3store.Client {
	t.Helper()
	server := httptest.NewServer(h)
	t.Cleanup(server.Close)
	t.Setenv("AWS_ENDPOINT_URL_S3", server.URL)
	client, err := s3store.New(t.Context(), &config.Config{AWSRegion: "us-east-1", AWSAccessKeyID: "test-key", AWSSecretKey: "test-secret", S3Bucket: "health-bucket"})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestLivenessDoesNotCallDependencies(t *testing.T) {
	vs, cs := &healthVectorStore{}, &healthChunkStore{}
	p := NewProbes(vs, cs, nil, time.Minute)
	p.SetDraining(true)
	rec := httptest.NewRecorder()
	LivenessHandler()(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/livez", http.NoBody))
	if rec.Code != http.StatusOK || rec.Body.String() != "{\"status\":\"ok\"}\n" {
		t.Fatalf("liveness = %d %s", rec.Code, rec.Body.String())
	}
	if vs.calls.Load() != 0 || cs.calls.Load() != 0 {
		t.Fatal("liveness called dependencies")
	}
}

func TestReadinessFailsWhileDraining(t *testing.T) {
	vs, cs := &healthVectorStore{}, &healthChunkStore{}
	p := NewProbes(vs, cs, nil, time.Minute)
	p.SetDraining(true)
	rec := httptest.NewRecorder()
	ReadinessHandler(p)(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/readyz", http.NoBody))
	if rec.Code != http.StatusServiceUnavailable || rec.Body.String() != "{\"status\":\"draining\"}\n" {
		t.Fatalf("readiness = %d %s", rec.Code, rec.Body.String())
	}
	if vs.calls.Load() != 0 || cs.calls.Load() != 0 {
		t.Fatal("draining readiness called dependencies")
	}
}

func TestReadinessFailsWhenPostgresDown(t *testing.T) {
	cs := &healthChunkStore{check: func(context.Context) error { return errors.New("database unavailable") }}
	p := NewProbes(&healthVectorStore{}, cs, nil, time.Minute)
	rec := httptest.NewRecorder()
	ReadinessHandler(p)(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/readyz", http.NoBody))
	if rec.Code != http.StatusServiceUnavailable || rec.Body.String() != "{\"status\":\"unavailable\"}\n" {
		t.Fatalf("readiness = %d %s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	HealthHandler(p)(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/health", http.NoBody))
	var status HealthStatus
	if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusServiceUnavailable || status.Status != "degraded" || status.Postgres != "unreachable" || status.Neon != status.Postgres || cs.calls.Load() != 1 {
		t.Fatalf("health = %d %+v, database calls = %d", rec.Code, status, cs.calls.Load())
	}
}

func TestReadinessIgnoresVectorAndStorageFailures(t *testing.T) {
	vs := &healthVectorStore{check: func(context.Context) error { return errors.New("index unavailable") }}
	objects := healthObjectStore(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusForbidden) }))
	p := NewProbes(vs, &healthChunkStore{}, objects, time.Minute)
	rec := httptest.NewRecorder()
	ReadinessHandler(p)(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/readyz", http.NoBody))
	if rec.Code != http.StatusOK || rec.Body.String() != "{\"status\":\"ready\"}\n" {
		t.Fatalf("readiness = %d %s", rec.Code, rec.Body.String())
	}
	status := p.Snapshot(t.Context())
	if status.Status != "degraded" || status.S3 != "unreachable" || status.Pinecone != "unreachable" {
		t.Fatalf("health = %+v", status)
	}
}

func TestHealthCacheExpires(t *testing.T) {
	vs, cs := &healthVectorStore{}, &healthChunkStore{}
	p := NewProbes(vs, cs, nil, 10*time.Millisecond)
	p.Snapshot(t.Context())
	time.Sleep(20 * time.Millisecond)
	p.Snapshot(t.Context())
	if vs.calls.Load() != 2 || cs.calls.Load() != 2 {
		t.Fatalf("dependency calls after expiry = %d, %d", vs.calls.Load(), cs.calls.Load())
	}
}

func TestHealthChecksRunConcurrentlyWithDeadlines(t *testing.T) {
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	check := func(ctx context.Context) error {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 5*time.Second || time.Until(deadline) <= 0 {
			t.Error("probe has no bounded deadline")
		}
		started <- struct{}{}
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	p := NewProbes(&healthVectorStore{check: check}, &healthChunkStore{check: check}, nil, time.Minute)
	finished := make(chan struct{})
	go func() { p.Snapshot(t.Context()); close(finished) }()
	defer func() { close(release); <-finished }()
	for range 2 {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("dependency checks did not start concurrently")
		}
	}
}

func TestHealthRefreshSurvivesCallerCancellation(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	cs := &healthChunkStore{check: func(ctx context.Context) error {
		close(started)
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}}
	p := NewProbes(&healthVectorStore{}, cs, nil, time.Minute)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	first := make(chan HealthStatus, 1)
	go func() { first <- p.Snapshot(ctx) }()
	<-started
	cancel()
	select {
	case <-first:
	case <-time.After(time.Second):
		t.Error("canceled caller did not return promptly")
	}
	close(release)
	status := p.Snapshot(t.Context())
	if status.Postgres != "ok" || cs.calls.Load() != 1 {
		t.Fatalf("shared refresh was canceled: %+v, calls = %d", status, cs.calls.Load())
	}
}

func TestReadinessDrainingDuringRefresh(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	cs := &healthChunkStore{check: func(context.Context) error { close(started); <-release; return nil }}
	p := NewProbes(&healthVectorStore{}, cs, nil, time.Minute)
	rec := httptest.NewRecorder()
	finished := make(chan struct{})
	go func() {
		ReadinessHandler(p)(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/readyz", http.NoBody))
		close(finished)
	}()
	<-started
	p.SetDraining(true)
	close(release)
	<-finished
	if rec.Code != http.StatusServiceUnavailable || rec.Body.String() != "{\"status\":\"draining\"}\n" {
		t.Fatalf("readiness after drain started = %d %s", rec.Code, rec.Body.String())
	}
}
