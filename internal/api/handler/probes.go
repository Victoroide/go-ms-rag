package handler

import (
	"context"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Victoroide/quarry-rag/internal/domain"
	s3store "github.com/Victoroide/quarry-rag/internal/store/s3"
	"golang.org/x/sync/singleflight"
)

// Probes shares cached dependency checks and shutdown readiness across handlers.
type Probes struct {
	vs       domain.VectorStore
	cs       domain.ChunkStore
	objects  *s3store.Client
	ttl      time.Duration
	mu       sync.RWMutex
	status   HealthStatus
	updated  time.Time
	refresh  singleflight.Group
	draining atomic.Bool
}

// NewProbes creates dependency probes with a shared cache lifetime.
func NewProbes(vs domain.VectorStore, cs domain.ChunkStore, objects *s3store.Client, ttl time.Duration) *Probes {
	if ttl <= 0 {
		ttl = 10 * time.Second
	}
	return &Probes{vs: vs, cs: cs, objects: objects, ttl: ttl}
}

// Snapshot returns cached health, sharing one refresh among concurrent callers.
func (p *Probes) Snapshot(ctx context.Context) HealthStatus {
	if status, fresh := p.cached(); fresh {
		return status
	}
	result := p.refresh.DoChan("health", func() (any, error) {
		if status, fresh := p.cached(); fresh {
			return status, nil
		}
		// A disconnected caller must not cancel the shared refresh or cache a failure.
		status := p.check(context.WithoutCancel(ctx))
		p.mu.Lock()
		p.status, p.updated = status, time.Now()
		p.mu.Unlock()
		return status, nil
	})
	select {
	case snapshot := <-result:
		return snapshot.Val.(HealthStatus)
	case <-ctx.Done():
		return HealthStatus{Status: "degraded", Pinecone: "unreachable", Postgres: "unreachable", Neon: "unreachable", S3: "unreachable"}
	}
}

func (p *Probes) cached() (HealthStatus, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.status, !p.updated.IsZero() && time.Since(p.updated) < p.ttl
}

func (p *Probes) check(ctx context.Context) HealthStatus {
	status := HealthStatus{Status: "ok", Pinecone: "ok", Postgres: "ok", Neon: "ok", S3: "ok"}
	var wg sync.WaitGroup
	probe := func(name string, ping func(context.Context) error, value *string) {
		wg.Go(func() {
			probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			if err := ping(probeCtx); err != nil {
				log.Printf("health: %s probe failed: %v", name, err)
				*value = "unreachable"
			}
		})
	}
	probe("pinecone", p.vs.Ping, &status.Pinecone)
	probe("postgres", p.cs.Ping, &status.Postgres)
	if p.objects != nil {
		probe("s3", p.objects.Ping, &status.S3)
	}
	wg.Wait()
	status.Neon = status.Postgres
	if status.Pinecone != "ok" || status.Postgres != "ok" || status.S3 != "ok" {
		status.Status = "degraded"
	}
	return status
}

// SetDraining controls whether the server is accepting new traffic.
func (p *Probes) SetDraining(draining bool) { p.draining.Store(draining) }

// Draining reports whether the server is waiting for traffic to drain.
func (p *Probes) Draining() bool { return p.draining.Load() }
