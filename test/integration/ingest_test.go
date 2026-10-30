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
	"sync"
	"testing"
	"time"

	"github.com/Victoroide/quarry-rag/internal/api/handler"
	"github.com/Victoroide/quarry-rag/internal/chunker"
	"github.com/Victoroide/quarry-rag/internal/domain"
	"github.com/Victoroide/quarry-rag/internal/extractor"
	"github.com/Victoroide/quarry-rag/internal/pipeline"
	"github.com/Victoroide/quarry-rag/internal/worker"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockChunkStore implements domain.ChunkStore for testing.
type mockChunkStore struct {
	mu           sync.Mutex
	jobs         []domain.Job
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
	m.mu.Lock()
	defer m.mu.Unlock()
	m.documents[string(doc.ID)] = doc
	return nil
}

func (m *mockChunkStore) InsertChunks(_ context.Context, chunks []domain.Chunk) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, c := range chunks {
		m.chunks[string(c.ID)] = c
	}
	return nil
}

func (m *mockChunkStore) GetDocument(_ context.Context, id domain.DocumentID) (*domain.Document, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	doc, ok := m.documents[string(id)]
	if !ok {
		return nil, nil
	}
	return &doc, nil
}

func (m *mockChunkStore) CheckFingerprint(_ context.Context, fp string) (domain.DocumentID, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	docID, ok := m.fingerprints[fp]
	if !ok {
		return "", false, nil
	}
	return domain.DocumentID(docID), true, nil
}

func (m *mockChunkStore) StoreFingerprint(_ context.Context, fp, docID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.fingerprints[fp] = docID
	return nil
}

func (m *mockChunkStore) GetDocumentS3Key(_ context.Context, id domain.DocumentID) (*string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	doc, ok := m.documents[string(id)]
	if !ok {
		return nil, nil
	}
	return doc.S3Key, nil
}

func (m *mockChunkStore) UpdateDocumentStatus(_ context.Context, id domain.DocumentID, status string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if doc, ok := m.documents[string(id)]; ok {
		doc.Metadata = map[string]any{"status": status}
		m.documents[string(id)] = doc
	}
	return nil
}

func (m *mockChunkStore) DeleteDocument(_ context.Context, id domain.DocumentID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.documents, string(id))
	return nil
}

func (m *mockChunkStore) ListDocuments(_ context.Context) ([]domain.DocumentRef, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]domain.DocumentRef, 0, len(m.documents))
	for id, doc := range m.documents {
		ref := domain.DocumentRef{ID: id, S3Key: doc.S3Key}
		if doc.Metadata != nil {
			if dt, ok := doc.Metadata["doc_type"].(string); ok {
				ref.DocType = dt
			}
		}
		out = append(out, ref)
	}
	return out, nil
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
		pipeline.NewEmbedStage(emb, 2),
		pipeline.NewPersistStage(vs),
	}

	w := worker.New(cs, stages, nil, worker.Options{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.Start(ctx)
	defer w.Shutdown()

	h := handler.IngestHandler(w, cs, nil, 20971520)

	body := `{"content":"Cedar Archive operating guide. Retains technical reports for nine years. Systems: Go, gRPC, AWS Bedrock."}`
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/ingest", bytes.NewBufferString(body))
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
		pipeline.NewEmbedStage(emb, 2),
		pipeline.NewPersistStage(vs),
	}

	w := worker.New(cs, stages, nil, worker.Options{})
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
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/ingest", bytes.NewBufferString(body))
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

	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/ingest", &body)
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
		pipeline.NewEmbedStage(emb, 2),
		pipeline.NewPersistStage(vs),
	}

	w := worker.New(cs, stages, nil, worker.Options{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.Start(ctx)
	defer w.Shutdown()

	h := handler.IngestHandler(w, cs, nil, 20971520)

	docxData := buildTestDOCX("Cedar Archive technical guide covering Go and AWS Bedrock integration with microservices and long-term document preservation")
	req := createMultipartRequest(t, "test_document.docx", docxData)
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
	w := worker.New(cs, nil, nil, worker.Options{})
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
	w := worker.New(cs, nil, nil, worker.Options{})
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

	mux := http.NewServeMux()
	mux.HandleFunc("GET /documents/{doc_id}/file", handler.FileHandler(cs, nil))
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/documents/550e8400-e29b-41d4-a716-446655440000/file", http.NoBody)
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code)
	var errResp handler.APIError
	_ = json.NewDecoder(rec.Body).Decode(&errResp)
	assert.Contains(t, errResp.Message, "file storage is not configured")
}

func TestGetDocumentFileInvalidUUID(t *testing.T) {
	cs := newMockChunkStore()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /documents/{doc_id}/file", handler.FileHandler(cs, nil))
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/documents/not-a-uuid/file", http.NoBody)
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// Verify extractor.DetectFileType is used correctly from tests.
func TestDetectFileTypeIntegration(t *testing.T) {
	ft, err := extractor.DetectFileType("document.pdf")
	require.NoError(t, err)
	assert.Equal(t, extractor.FileTypePDF, ft)

	_, err = extractor.DetectFileType("data.csv")
	assert.Error(t, err)
}

func (m *mockChunkStore) AcceptDocument(_ context.Context, r domain.AcceptRequest) (domain.AcceptResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := domain.AcceptResult{DocumentID: r.Document.ID, Outcome: domain.AcceptAccepted}
	if !r.CallerSupplied {
		if id, ok := m.fingerprints[r.Fingerprint]; ok {
			result.DocumentID = domain.DocumentID(id)
			result.Outcome = domain.AcceptDuplicate
			return result, nil
		}
	}
	if doc, ok := m.documents[string(r.Document.ID)]; ok {
		result.Outcome = domain.AcceptDuplicate
		if chunker.Fingerprint(doc.Content) != r.Fingerprint {
			result.Outcome = domain.AcceptConflict
		}
		return result, nil
	}
	m.documents[string(r.Document.ID)] = r.Document
	m.fingerprints[r.Fingerprint] = string(r.Document.ID)
	m.jobs = append(m.jobs, domain.Job{Document: r.Document, DocType: r.DocType, Attempts: 1})
	return result, nil
}
func (m *mockChunkStore) ClaimJob(_ context.Context, owner string, _ time.Duration, _ int) (*domain.Job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.jobs) == 0 {
		return nil, nil
	}
	j := m.jobs[0]
	m.jobs = m.jobs[1:]
	j.LeaseOwner = owner
	return &j, nil
}
func (*mockChunkStore) HeartbeatJob(context.Context, domain.Job, time.Duration) (bool, error) {
	return true, nil
}
func (m *mockChunkStore) CompleteJob(_ context.Context, j domain.Job, chunks []domain.Chunk) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	doc, ok := m.documents[string(j.Document.ID)]
	if !ok {
		return false, nil
	}
	doc.ChunkCount = len(chunks)
	m.documents[string(doc.ID)] = doc
	for _, ch := range chunks {
		m.chunks[string(ch.ID)] = ch
	}
	return true, nil
}
func (*mockChunkStore) RetryJob(context.Context, domain.Job, time.Duration, string) error { return nil }
func (*mockChunkStore) FailJob(context.Context, domain.Job, string) error                 { return nil }
func (*mockChunkStore) ReleaseJob(context.Context, domain.Job) error                      { return nil }
func (m *mockChunkStore) DocumentExists(_ context.Context, id domain.DocumentID) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.documents[string(id)]
	return ok, nil
}
