package memory

import (
	"testing"

	"github.com/Victoroide/quarry-rag/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestQueryFilteredHonorsIDsAndType(t *testing.T) {
	vs := NewFakeVectorStore()
	chunks := []domain.Chunk{
		{ID: "outside", DocumentID: "private", Embedding: []float32{1, 0}, Metadata: map[string]any{"doc_type": "report"}},
		{ID: "wrong-type", DocumentID: "allowed", Embedding: []float32{1, 0}, Metadata: map[string]any{"doc_type": "note"}},
		{ID: "wanted", DocumentID: "allowed", Embedding: []float32{0, 1}, Metadata: map[string]any{"doc_type": "report"}},
	}
	require.NoError(t, vs.Upsert(t.Context(), "", chunks))
	require.NoError(t, vs.Upsert(t.Context(), "legacy", []domain.Chunk{{ID: "legacy", DocumentID: "allowed", Embedding: []float32{1, 0}, Metadata: map[string]any{"doc_type": "report"}}}))
	got, err := vs.QueryFiltered(t.Context(), []float32{1, 0}, 1, domain.VectorFilter{DocType: "report", DocumentIDs: []string{"allowed"}})
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, domain.ChunkID("wanted"), got[0].Chunk.ID)
	got, err = vs.QueryFiltered(t.Context(), []float32{1, 0}, 10, domain.VectorFilter{DocumentIDs: []string{}})
	require.NoError(t, err)
	require.Empty(t, got)
	got, err = vs.QueryFiltered(t.Context(), []float32{1, 0}, 10, domain.VectorFilter{})
	require.NoError(t, err)
	require.Len(t, got, 3)
}
