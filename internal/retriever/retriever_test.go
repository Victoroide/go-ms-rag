package retriever

import (
	"context"
	"errors"
	"testing"

	"github.com/Victoroide/quarry-rag/internal/domain"
	"github.com/Victoroide/quarry-rag/internal/store/memory"
	"github.com/stretchr/testify/require"
)

type localScopedStore struct {
	*memory.FakeVectorStore
	filters []domain.VectorFilter
}

func (s *localScopedStore) QueryFiltered(ctx context.Context, embedding []float32, topK int, filter domain.VectorFilter) ([]domain.ScoredChunk, error) {
	s.filters = append(s.filters, filter)
	return s.FakeVectorStore.QueryFiltered(ctx, embedding, topK, filter)
}

func TestLocalRetrievalStaysWithinAllowlist(t *testing.T) {
	for _, tt := range []struct {
		name, target string
		allowed      []string
		calls        int
	}{
		{name: "authorized target", target: "allowed", allowed: []string{"allowed", "other"}, calls: 2},
		{name: "unauthorized target", target: "private", allowed: []string{"allowed", "other"}, calls: 1},
		{name: "empty allowlist", target: "private", allowed: []string{}, calls: 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			store := &localScopedStore{FakeVectorStore: memory.NewFakeVectorStore()}
			require.NoError(t, store.Upsert(t.Context(), "", []domain.Chunk{
				{ID: "allowed", DocumentID: "allowed", Embedding: []float32{.8, .6}, Metadata: map[string]any{"doc_type": "report"}},
				{ID: "other", DocumentID: "other", Embedding: []float32{.9, .1}, Metadata: map[string]any{"doc_type": "report"}},
				{ID: "private", DocumentID: "private", Embedding: []float32{1, 0}, Metadata: map[string]any{"doc_type": "report"}},
				{ID: "wrong-type", DocumentID: "allowed", Embedding: []float32{1, 0}, Metadata: map[string]any{"doc_type": "note"}},
			}))
			filter := domain.VectorFilter{DocType: "report", DocumentIDs: tt.allowed}
			result, err := NewRetriever(store, nil, .7, 10).RetrieveWithEmbedding(t.Context(), []float32{1, 0}, RetrievalModeLocal, tt.target, 10, filter)
			require.NoError(t, err)
			require.Len(t, store.filters, tt.calls)
			if tt.calls == 0 {
				require.Empty(t, result.Chunks)
				return
			}
			require.Len(t, result.Chunks, 2)
			for _, sc := range result.Chunks {
				require.Contains(t, tt.allowed, string(sc.Chunk.DocumentID))
				require.Equal(t, "report", sc.Chunk.Metadata["doc_type"])
			}
			require.Equal(t, filter, store.filters[len(store.filters)-1])
			require.Equal(t, []string{"allowed", "other"}, tt.allowed)
		})
	}
}

type localSequenceStore struct {
	domain.VectorStore
	results [][]domain.ScoredChunk
	filters []domain.VectorFilter
	err     error
}

func (s *localSequenceStore) QueryFiltered(_ context.Context, _ []float32, _ int, filter domain.VectorFilter) ([]domain.ScoredChunk, error) {
	s.filters = append(s.filters, filter)
	if len(s.filters) == 2 && s.err != nil {
		return nil, s.err
	}
	return s.results[len(s.filters)-1], nil
}

func TestLocalTopUpSortsDeduplicatesAndTrims(t *testing.T) {
	chunk := func(id string, score float64) domain.ScoredChunk {
		return domain.ScoredChunk{Chunk: domain.Chunk{ID: domain.ChunkID(id)}, Score: score}
	}
	store := &localSequenceStore{results: [][]domain.ScoredChunk{
		{chunk("local", .6)},
		{chunk("last", .3), chunk("local", .8), chunk("first", 1), chunk("second", .9)},
	}}
	result, err := NewRetriever(store, nil, .7, 3).RetrieveWithEmbedding(t.Context(), []float32{1, 0}, RetrievalModeLocal, "target", 0, domain.VectorFilter{})
	require.NoError(t, err)
	require.Len(t, result.Chunks, 3)
	require.Equal(t, []domain.ScoredChunk{chunk("first", 1), chunk("second", .9), chunk("local", .8)}, result.Chunks)
	require.Equal(t, 1.0, result.TopScore)
	require.Equal(t, RetrievalModeLocal, result.Mode)
}

func TestLocalTopUpFailureIsReturned(t *testing.T) {
	want := errors.New("search unavailable")
	store := &localSequenceStore{results: [][]domain.ScoredChunk{nil}, err: want}
	_, err := NewRetriever(store, nil, .7, 10).RetrieveWithEmbedding(t.Context(), []float32{1, 0}, RetrievalModeLocal, "target", 10, domain.VectorFilter{})
	require.ErrorIs(t, err, want)
	require.Len(t, store.filters, 2)
}

func TestLocalWithoutTargetQueriesScopeOnce(t *testing.T) {
	store := &localSequenceStore{results: [][]domain.ScoredChunk{nil}}
	result, err := NewRetriever(store, nil, .7, 10).RetrieveWithEmbedding(t.Context(), []float32{1, 0}, RetrievalModeLocal, "", 10, domain.VectorFilter{})
	require.NoError(t, err)
	require.Equal(t, RetrievalModeGlobal, result.Mode)
	require.Len(t, store.filters, 1)
}
