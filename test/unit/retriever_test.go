package unit

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Victoroide/quarry-rag/internal/domain"
	"github.com/Victoroide/quarry-rag/internal/retriever"
	"github.com/stretchr/testify/assert"
)

// mockVectorStore implements domain.VectorStore for testing.
type mockVectorStore struct {
	queryResult    []domain.ScoredChunk
	queryAllResult []domain.ScoredChunk
	queryAllCalls  int
}

func (m *mockVectorStore) Upsert(_ context.Context, _ string, _ []domain.Chunk) error { return nil }
func (m *mockVectorStore) Query(_ context.Context, _ string, _ []float32, _ int, _ string) ([]domain.ScoredChunk, error) {
	return m.queryResult, nil
}
func (m *mockVectorStore) QueryAllNamespaces(_ context.Context, _ []float32, _ int, _ string) ([]domain.ScoredChunk, error) {
	m.queryAllCalls++
	return m.queryAllResult, nil
}
func (m *mockVectorStore) Delete(_ context.Context, _ string, _ []string) error { return nil }
func (m *mockVectorStore) Ping(_ context.Context) error                         { return nil }

// mockEmbedder implements domain.EmbeddingProvider for testing.
type mockEmbedder struct{}

func (m *mockEmbedder) Embed(_ context.Context, _ string) ([]float32, error) {
	return make([]float32, 512), nil
}

func TestLocalModeThreshold(t *testing.T) {
	ret := retriever.NewRetriever(nil, nil, 0.70, 10)

	// Single sub-query + high similarity -> LOCAL.
	mode := ret.DetermineMode(1, 0.85)
	assert.Equal(t, retriever.RetrievalModeLocal, mode,
		"single sub-query with high similarity should route to LOCAL")

	// Single sub-query + exact threshold -> LOCAL.
	mode2 := ret.DetermineMode(1, 0.70)
	assert.Equal(t, retriever.RetrievalModeLocal, mode2,
		"single sub-query at threshold should route to LOCAL")
}

func TestGlobalModeFallback(t *testing.T) {
	vs := &mockVectorStore{
		queryAllResult: []domain.ScoredChunk{}, // Returns 0 chunks.
	}
	emb := &mockEmbedder{}

	ret := retriever.NewRetriever(vs, emb, 0.70, 10)

	// Multiple sub-queries -> GLOBAL.
	mode := ret.DetermineMode(3, 0.50)
	assert.Equal(t, retriever.RetrievalModeGlobal, mode,
		"multiple sub-queries should route to GLOBAL")

	// Low similarity with single sub-query -> GLOBAL.
	mode2 := ret.DetermineMode(1, 0.50)
	assert.Equal(t, retriever.RetrievalModeGlobal, mode2,
		"low similarity should route to GLOBAL")

	// Sparse global results require only one vector query.
	result, err := ret.Retrieve(context.Background(), "test query", retriever.RetrievalModeGlobal, 10, domain.VectorFilter{})
	assert.NoError(t, err)
	assert.NotNil(t, result)
	require.Equal(t, 1, vs.queryAllCalls)
	assert.Equal(t, retriever.RetrievalModeGlobal, result.Mode)
}

type transientProbeStore struct {
	mockVectorStore
	calls int
}

func (s *transientProbeStore) QueryAllNamespaces(context.Context, []float32, int, string) ([]domain.ScoredChunk, error) {
	s.calls++
	if s.calls == 1 {
		return nil, errors.New("temporary probe failure")
	}
	return []domain.ScoredChunk{{Chunk: domain.Chunk{ID: "evidence", Text: "Archive operating hours."}, Score: 0.8}}, nil
}
func TestRoutingProbeFailureFallsBackToGlobal(t *testing.T) {
	store := &transientProbeStore{}
	ret := retriever.NewRetriever(store, &mockEmbedder{}, 0.7, 10)
	embedding, score, failed, err := ret.EmbedQuery(t.Context(), "archive", domain.VectorFilter{}, 5)
	require.NoError(t, err)
	require.True(t, failed)
	require.Zero(t, score)
	result, err := ret.RetrieveWithEmbedding(t.Context(), embedding, retriever.RetrievalModeGlobal, "", 10, domain.VectorFilter{})
	require.NoError(t, err)
	require.Len(t, result.Chunks, 1)
}
