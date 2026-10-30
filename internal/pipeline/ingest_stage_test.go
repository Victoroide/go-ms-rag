package pipeline

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Victoroide/quarry-rag/internal/domain"
	"github.com/stretchr/testify/require"
)

type concurrentIngestEmbedder struct{ active, peak atomic.Int32 }

func (e *concurrentIngestEmbedder) Embed(ctx context.Context, _ string) ([]float32, error) {
	n := e.active.Add(1)
	defer e.active.Add(-1)
	for old := e.peak.Load(); n > old; old = e.peak.Load() {
		if e.peak.CompareAndSwap(old, n) {
			break
		}
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(20 * time.Millisecond):
		return []float32{1}, nil
	}
}
func TestEmbedStageRunsConcurrently(t *testing.T) {
	e := &concurrentIngestEmbedder{}
	ic := NewIngestContext("doc", "", nil)
	ic.Chunks = make([]domain.Chunk, 8)
	require.NoError(t, NewEmbedStage(e, 3).Run(t.Context(), ic))
	require.Equal(t, int32(3), e.peak.Load())
	for _, ch := range ic.Chunks {
		require.Equal(t, []float32{1}, ch.Embedding)
	}
}
