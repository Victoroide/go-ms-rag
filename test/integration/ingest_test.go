package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Victoroide/go-ms-rag/internal/api/handler"
	"github.com/Victoroide/go-ms-rag/internal/chunker"
	"github.com/Victoroide/go-ms-rag/internal/domain"
	"github.com/Victoroide/go-ms-rag/internal/pipeline"
	"github.com/Victoroide/go-ms-rag/internal/worker"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockChunkStore implements domain.ChunkStore for testing.
type mockChunkStore struct {
	documents    map[string]domain.Document
	chunks       map[string]domain.Chunk
	fingerprints map[string]string
}

func newMockChunkStore() *mockChunkStore {
	return &mockChunkStore{
		documents:    make(map[string]domain.Document),
		chunks:       make(map[string]domain.Chunk),
		fingerprints: make(map[string]string),
	}
}

func (m *mockChunkStore) InsertDocument(_ context.Context, doc domain.Document) error {
	m.documents[string(doc.ID)] = doc
	return nil
}

func (m *mockChunkStore) InsertChunks(_ context.Context, chunks []domain.Chunk) error {
	for _, c := range chunks {
		m.chunks[string(c.ID)] = c
	}
	return nil
}

func (m *mockChunkStore) GetDocument(_ context.Context, id domain.DocumentID) (*domain.Document, error) {
	doc, ok := m.documents[string(id)]
	if !ok {
		return nil, nil
	}
	return &doc, nil
}

func (m *mockChunkStore) CheckFingerprint(_ context.Context, fp string) (domain.DocumentID, bool, error) {
	docID, ok := m.fingerprints[fp]
	if !ok {
		return "", false, nil
	}
	return domain.DocumentID(docID), true, nil
}

func (m *mockChunkStore) StoreFingerprint(_ context.Context, fp, docID string) error {
	m.fingerprints[fp] = docID
	return nil
}

func (m *mockChunkStore) Ping(_ context.Context) error          { return nil }
func (m *mockChunkStore) Close(_ context.Context) error         { return nil }
func (m *mockChunkStore) RunMigrations(_ context.Context) error { return nil }

// mockVectorStore implements domain.VectorStore for testing.
type mockVectorStore struct {
	upsertCalls int
}

func (m *mockVectorStore) Upsert(_ context.Context, _ string, _ []domain.Chunk) error {
	m.upsertCalls++
	return nil
}

func (m *mockVectorStore) Query(_ context.Context, _ string, _ []float32, _ int) ([]domain.ScoredChunk, error) {
	return nil, nil
}

func (m *mockVectorStore) QueryAllNamespaces(_ context.Context, _ []float32, _ int) ([]domain.ScoredChunk, error) {
	return nil, nil
}

func (m *mockVectorStore) Delete(_ context.Context, _ string, _ []string) error { return nil }
func (m *mockVectorStore) Ping(_ context.Context) error                         { return nil }

// mockEmbedder implements domain.EmbeddingProvider for testing.
type mockEmbedder struct{}

func (m *mockEmbedder) Embed(_ context.Context, _ string) ([]float32, error) {
	return make([]float32, 512), nil
}

func TestIngestEndToEnd(t *testing.T) {
	cs := newMockChunkStore()
	vs := &mockVectorStore{}
	emb := &mockEmbedder{}
	chk := chunker.NewChunker(512, 64)

	stages := []pipeline.Stage{
		pipeline.NewChunkStage(chk),
		pipeline.NewEmbedStage(emb),
		pipeline.NewPersistStage(vs, cs),
	}

	w := worker.NewWorker(10, stages, cs)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.Start(ctx)
	defer w.Shutdown()

	h := handler.IngestHandler(w, cs)

	body := `{"content":"Laura Benitez - Senior Software Engineer. 9 years experience. Skills: Go, gRPC, AWS Bedrock."}`
	req := httptest.NewRequest(http.MethodPost, "/ingest", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusAccepted, rec.Code)

	var resp handler.IngestResponse
	err := json.NewDecoder(rec.Body).Decode(&resp)
	require.NoError(t, err)
	assert.Equal(t, "processing", resp.Status)
	assert.NotEmpty(t, resp.DocumentID)
}

func TestIngestDedup(t *testing.T) {
	cs := newMockChunkStore()
	vs := &mockVectorStore{}
	emb := &mockEmbedder{}
	chk := chunker.NewChunker(512, 64)

	stages := []pipeline.Stage{
		pipeline.NewChunkStage(chk),
		pipeline.NewEmbedStage(emb),
		pipeline.NewPersistStage(vs, cs),
	}

	w := worker.NewWorker(10, stages, cs)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.Start(ctx)
	defer w.Shutdown()

	// Pre-store a fingerprint.
	content := "Duplicate content test document."
	fp := chunker.Fingerprint(content)
	cs.fingerprints[fp] = "existing-doc-id"

	h := handler.IngestHandler(w, cs)

	body := `{"content":"Duplicate content test document."}`
	req := httptest.NewRequest(http.MethodPost, "/ingest", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	var resp handler.IngestResponse
	err := json.NewDecoder(rec.Body).Decode(&resp)
	require.NoError(t, err)
	assert.Equal(t, "already_ingested", resp.Status)
	assert.Equal(t, "existing-doc-id", resp.DocumentID)
}
