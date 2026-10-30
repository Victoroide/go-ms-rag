package pipeline

import (
	"context"
	"testing"

	"github.com/Victoroide/quarry-rag/internal/domain"
	"github.com/stretchr/testify/require"
)

type persistVectorWrite struct {
	namespace string
	chunks    []domain.Chunk
}

type persistVectorRecorder struct {
	domain.VectorStore
	writes []persistVectorWrite
}

func (s *persistVectorRecorder) Upsert(_ context.Context, namespace string, chunks []domain.Chunk) error {
	s.writes = append(s.writes, persistVectorWrite{namespace: namespace, chunks: chunks})
	return nil
}

type persistChunkRecorder struct {
	domain.ChunkStore
	document domain.Document
	chunks   []domain.Chunk
}

func (s *persistChunkRecorder) InsertDocument(_ context.Context, doc domain.Document) error {
	s.document = doc
	return nil
}

func (s *persistChunkRecorder) InsertChunks(_ context.Context, chunks []domain.Chunk) error {
	s.chunks = chunks
	return nil
}

func (*persistChunkRecorder) StoreFingerprint(context.Context, string, string) error {
	return nil
}

func TestPersistWritesOnlyDefaultNamespace(t *testing.T) {
	vs := &persistVectorRecorder{}
	cs := &persistChunkRecorder{}
	ic := NewIngestContext("document-a", "First paragraph. Second paragraph.", nil)
	ic.DocType = "reference"
	ic.Chunks = []domain.Chunk{
		{ID: "chunk-a", DocumentID: "document-a", Text: "First paragraph.", Embedding: []float32{1, 0}},
		{ID: "chunk-b", DocumentID: "document-a", Index: 1, Text: "Second paragraph.", Embedding: []float32{0, 1}},
	}

	require.NoError(t, NewPersistStage(vs, cs).Run(t.Context(), ic))

	require.Len(t, vs.writes, 1, "each chunk batch must be written once")
	require.Empty(t, vs.writes[0].namespace, "vectors must use the default namespace")
	want := []domain.Chunk{
		{ID: "chunk-a", DocumentID: "document-a", Text: "First paragraph.", Embedding: []float32{1, 0}, Metadata: map[string]any{"doc_type": "reference"}},
		{ID: "chunk-b", DocumentID: "document-a", Index: 1, Text: "Second paragraph.", Embedding: []float32{0, 1}, Metadata: map[string]any{"doc_type": "reference"}},
	}
	require.Equal(t, want, vs.writes[0].chunks)
	require.Equal(t, want, cs.chunks)
	require.Equal(t, domain.DocumentID("document-a"), cs.document.ID)
	require.Equal(t, "reference", cs.document.Metadata["doc_type"])
}
