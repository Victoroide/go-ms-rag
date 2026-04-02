package integration

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Victoroide/go-ms-rag/internal/api/handler"
	"github.com/Victoroide/go-ms-rag/internal/chunker"
	"github.com/Victoroide/go-ms-rag/internal/domain"
	"github.com/Victoroide/go-ms-rag/internal/extractor"
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

func (m *mockChunkStore) GetDocumentS3Key(_ context.Context, id domain.DocumentID) (*string, error) {
	doc, ok := m.documents[string(id)]
	if !ok {
		return nil, nil
	}
	return doc.S3Key, nil
}

func (m *mockChunkStore) UpdateDocumentStatus(_ context.Context, id domain.DocumentID, status string) error {
	if doc, ok := m.documents[string(id)]; ok {
		doc.Metadata = map[string]any{"status": status}
		m.documents[string(id)] = doc
	}
	return nil
}

func (m *mockChunkStore) DeleteDocument(_ context.Context, id domain.DocumentID) error {
	delete(m.documents, string(id))
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

func (m *mockVectorStore) Query(_ context.Context, _ string, _ []float32, _ int, _ string) ([]domain.ScoredChunk, error) {
	return nil, nil
}

func (m *mockVectorStore) QueryAllNamespaces(_ context.Context, _ []float32, _ int, _ string) ([]domain.ScoredChunk, error) {
	return nil, nil
}

func (m *mockVectorStore) Delete(_ context.Context, _ string, _ []string) error { return nil }
func (m *mockVectorStore) Ping(_ context.Context) error                         { return nil }

// mockEmbedder implements domain.EmbeddingProvider for testing.
type mockEmbedder struct{}

func (m *mockEmbedder) Embed(_ context.Context, _ string) ([]float32, error) {
	return make([]float32, 1024), nil
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

	h := handler.IngestHandler(w, cs, nil, 20971520)

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

	h := handler.IngestHandler(w, cs, nil, 20971520)

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

// buildTestDOCX creates a minimal valid DOCX file with the given text.
func buildTestDOCX(text string) []byte {
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)

	xmlContent := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">
  <w:body>
    <w:p>
      <w:r>
        <w:t>` + text + `</w:t>
      </w:r>
    </w:p>
  </w:body>
</w:document>`

	f, _ := w.Create("word/document.xml")
	_, _ = f.Write([]byte(xmlContent))

	ct := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">
  <Default Extension="xml" ContentType="application/xml"/>
</Types>`
	f2, _ := w.Create("[Content_Types].xml")
	_, _ = f2.Write([]byte(ct))

	_ = w.Close()
	return buf.Bytes()
}

func createMultipartRequest(t *testing.T, filename string, fileData []byte) *http.Request {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", filename)
	require.NoError(t, err)
	_, err = io.Copy(part, bytes.NewReader(fileData))
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	req := httptest.NewRequest(http.MethodPost, "/ingest", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	return req
}

func TestIngestDOCXFile(t *testing.T) {
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

	h := handler.IngestHandler(w, cs, nil, 20971520)

	docxData := buildTestDOCX("Laura Benitez Senior Software Engineer with extensive experience in Go and AWS Bedrock and microservices architecture")
	req := createMultipartRequest(t, "test_resume.docx", docxData)
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusAccepted, rec.Code)

	var resp handler.IngestResponse
	err := json.NewDecoder(rec.Body).Decode(&resp)
	require.NoError(t, err)
	assert.Equal(t, "processing", resp.Status)
	assert.NotEmpty(t, resp.DocumentID)
}

func TestIngestUnsupportedFile(t *testing.T) {
	cs := newMockChunkStore()
	w := worker.NewWorker(10, nil, cs)
	h := handler.IngestHandler(w, cs, nil, 20971520)

	req := createMultipartRequest(t, "data.csv", []byte("col1,col2\nval1,val2"))
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnsupportedMediaType, rec.Code)
	var errResp handler.APIError
	_ = json.NewDecoder(rec.Body).Decode(&errResp)
	assert.Contains(t, errResp.Message, "unsupported file type")
}

func TestIngestFileTooLarge(t *testing.T) {
	cs := newMockChunkStore()
	w := worker.NewWorker(10, nil, cs)
	// Set max to 100 bytes for this test.
	h := handler.IngestHandler(w, cs, nil, 100)

	// Create a DOCX larger than 100 bytes.
	bigContent := strings.Repeat("A long text content that exceeds the limit. ", 50)
	docxData := buildTestDOCX(bigContent)
	req := createMultipartRequest(t, "big.docx", docxData)
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
}

func TestGetDocumentFileNoS3Key(t *testing.T) {
	cs := newMockChunkStore()
	// Insert a document with no S3 key.
	cs.documents["550e8400-e29b-41d4-a716-446655440000"] = domain.Document{
		ID: "550e8400-e29b-41d4-a716-446655440000",
	}

	h := handler.FileHandler(cs, nil)
	req := httptest.NewRequest(http.MethodGet, "/documents/550e8400-e29b-41d4-a716-446655440000/file", http.NoBody)
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code)
	var errResp handler.APIError
	_ = json.NewDecoder(rec.Body).Decode(&errResp)
	assert.Contains(t, errResp.Message, "no file stored")
}

func TestGetDocumentFileInvalidUUID(t *testing.T) {
	cs := newMockChunkStore()
	h := handler.FileHandler(cs, nil)
	req := httptest.NewRequest(http.MethodGet, "/documents/not-a-uuid/file", http.NoBody)
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// Verify extractor.DetectFileType is used correctly from tests.
func TestDetectFileTypeIntegration(t *testing.T) {
	ft, err := extractor.DetectFileType("resume.pdf")
	require.NoError(t, err)
	assert.Equal(t, extractor.FileTypePDF, ft)

	_, err = extractor.DetectFileType("data.csv")
	assert.Error(t, err)
}
