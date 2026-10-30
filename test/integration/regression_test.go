package integration

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Victoroide/quarry-rag/internal/api/handler"
	"github.com/Victoroide/quarry-rag/internal/chunker"
	"github.com/Victoroide/quarry-rag/internal/config"
	"github.com/Victoroide/quarry-rag/internal/domain"
	"github.com/Victoroide/quarry-rag/internal/embedder"
	"github.com/Victoroide/quarry-rag/internal/llm"
	"github.com/Victoroide/quarry-rag/internal/pipeline"
	"github.com/Victoroide/quarry-rag/internal/retriever"
	"github.com/Victoroide/quarry-rag/internal/store/memory"
	pgstore "github.com/Victoroide/quarry-rag/internal/store/postgres"
	"github.com/Victoroide/quarry-rag/internal/worker"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestPersistOpaqueDocumentType(t *testing.T) {
	for _, label := range []string{"", "manual", "custom/type α"} {
		t.Run(label, func(t *testing.T) {
			cs := newMockChunkStore()
			vs := memory.NewFakeVectorStore()
			ic := pipeline.NewIngestContext("doc", strings.Repeat("界", 50), nil)
			ic.DocType = label
			ic.Chunks = []domain.Chunk{{ID: "chunk", DocumentID: "doc", Embedding: []float32{1}}}
			require.NoError(t, pipeline.NewPersistStage(vs, cs).Run(t.Context(), ic))
			want := label
			if want == "" {
				want = "document"
			}
			doc, err := cs.GetDocument(t.Context(), "doc")
			require.NoError(t, err)
			require.Equal(t, want, doc.Metadata["doc_type"])
			require.True(t, utf8.ValidString(doc.ContentPreview))
			chunks, err := vs.Query(t.Context(), "", []float32{1}, 10, want)
			require.NoError(t, err)
			require.Len(t, chunks, 1)
		})
	}
}

func TestJSONIngestRejectsWhitespaceAndTrailingJSON(t *testing.T) {
	for _, body := range []string{`{"content":"   "}`, `{"content":"valid text"} {"content":"second"}`} {
		req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/ingest", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		cs := newMockChunkStore()
		handler.IngestHandler(worker.NewWorker(1, nil, cs), cs, nil, 1024)(rec, req)
		require.Equal(t, http.StatusBadRequest, rec.Code)
	}
}

func TestJSONIngestBodyLimit(t *testing.T) {
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/ingest", strings.NewReader(`{"content":"text", "metadata":{"large":"`+strings.Repeat("x", 9<<20)+`"}}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	cs := newMockChunkStore()
	handler.IngestHandler(worker.NewWorker(1, nil, cs), cs, nil, 1024)(rec, req)
	require.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
}

type failedLookup struct{ domain.ChunkStore }

func (failedLookup) GetDocument(context.Context, domain.DocumentID) (*domain.Document, error) {
	return nil, errors.New("database unavailable")
}

func TestDeleteDoesNotHideStoreFailure(t *testing.T) {
	req := httptest.NewRequestWithContext(context.Background(), http.MethodDelete, "/documents/"+uuid.NewString(), http.NoBody)
	req.SetPathValue("doc_id", uuid.NewString())
	rec := httptest.NewRecorder()
	handler.DeleteHandler(failedLookup{}, nil, nil)(rec, req)
	require.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestPostgresDocumentLifecycle(t *testing.T) {
	if testing.Short() {
		t.Skip("needs a Docker daemon")
	}
	cs, err := pgstore.NewClient(t.Context(), startPostgres(t))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, cs.Close(context.Background())) })
	require.NoError(t, cs.RunMigrations(t.Context()))
	require.NoError(t, cs.RunMigrations(t.Context()))
	var defaultType string
	require.NoError(t, cs.Pool.QueryRow(t.Context(), `INSERT INTO documents(content) VALUES ('default') RETURNING doc_type`).Scan(&defaultType))
	require.Equal(t, "document", defaultType)
	for _, label := range []string{"", "reference"} {
		id := domain.DocumentID(uuid.NewString())
		metadata := map[string]any{}
		if label != "" {
			metadata["doc_type"] = label
		}
		require.NoError(t, cs.InsertDocument(t.Context(), domain.Document{ID: id, Content: "Stored text", Metadata: metadata}))
		var gotType string
		require.NoError(t, cs.Pool.QueryRow(t.Context(), `SELECT doc_type FROM documents WHERE id=$1`, id).Scan(&gotType))
		want := label
		if want == "" {
			want = "document"
		}
		require.Equal(t, want, gotType)
		require.NoError(t, cs.InsertChunks(t.Context(), []domain.Chunk{{ID: domain.ChunkID(uuid.NewString()), DocumentID: id, Text: "Stored text", TokenCount: 2}}))
		require.NoError(t, cs.StoreFingerprint(t.Context(), string(id), string(id)))
		doc, lookupErr := cs.GetDocument(t.Context(), id)
		require.NoError(t, lookupErr)
		require.Equal(t, "Stored text", doc.Content)
		require.NoError(t, cs.DeleteDocument(t.Context(), id))
		_, exists, fingerprintErr := cs.CheckFingerprint(t.Context(), string(id))
		require.NoError(t, fingerprintErr)
		require.False(t, exists)
		var count int
		require.NoError(t, cs.Pool.QueryRow(t.Context(), `SELECT count(*) FROM chunks WHERE document_id=$1`, id).Scan(&count))
		require.Zero(t, count)
	}
	t.Run("chunk batches are atomic and refresh conflicts", func(t *testing.T) {
		id := domain.DocumentID(uuid.NewString())
		chunkID := domain.ChunkID(uuid.NewString())
		require.NoError(t, cs.InsertDocument(t.Context(), domain.Document{ID: id, Content: "body"}))
		first := domain.Chunk{ID: chunkID, DocumentID: id, Text: "original", TokenCount: 1}
		bad := domain.Chunk{ID: "not-a-uuid", DocumentID: id, Text: "invalid", TokenCount: 1}
		require.Error(t, cs.InsertChunks(t.Context(), []domain.Chunk{first, bad}))
		var count int
		require.NoError(t, cs.Pool.QueryRow(t.Context(), `SELECT count(*) FROM chunks WHERE document_id=$1`, id).Scan(&count))
		require.Zero(t, count)
		require.NoError(t, cs.InsertChunks(t.Context(), []domain.Chunk{first}))
		first.Text = "revised"
		require.NoError(t, cs.InsertChunks(t.Context(), []domain.Chunk{first}))
		var text string
		require.NoError(t, cs.Pool.QueryRow(t.Context(), `SELECT text FROM chunks WHERE id=$1`, chunkID).Scan(&text))
		require.Equal(t, "revised", text)
	})
	err = cs.InsertDocument(t.Context(), domain.Document{ID: domain.DocumentID(uuid.NewString()), Content: "bad", Metadata: map[string]any{"bad": make(chan int)}})
	require.Error(t, err)
	var syntaxErr *json.UnsupportedTypeError
	require.ErrorAs(t, err, &syntaxErr)
}

func TestIngestShutdownDrainsAcceptedDocument(t *testing.T) {
	cs := newMockChunkStore()
	w := worker.NewWorker(10, []pipeline.Stage{pipeline.NewChunkStage(chunker.NewChunker(30, 4)), pipeline.NewEmbedStage(embedder.NewFakeEmbedder(256)), pipeline.NewPersistStage(memory.NewFakeVectorStore(), cs)}, cs)
	w.Start(t.Context())
	h := handler.IngestHandler(w, cs, nil, 1024)
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/ingest", strings.NewReader(`{"content":"The archive is open every weekday for document research."}`))
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	rec := httptest.NewRecorder()
	h(rec, req)
	require.Equal(t, 202, rec.Code)
	var response handler.IngestResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response))
	w.Shutdown()
	doc, err := cs.GetDocument(t.Context(), domain.DocumentID(response.DocumentID))
	require.NoError(t, err)
	require.NotNil(t, doc)
}

func TestQueryValidationAndAuthorization(t *testing.T) {
	cfg := &config.Config{BedrockFallbackModel: "fallback"}
	amb := llm.NewAmbassador(cfg)
	amb.SetClient(llm.NewFakeConverser())
	emb := embedder.NewFakeEmbedder(256)
	vs := memory.NewFakeVectorStore()
	text := strings.Repeat("界", 650)
	vector, err := emb.Embed(t.Context(), "policy")
	require.NoError(t, err)
	require.NoError(t, vs.Upsert(t.Context(), "", []domain.Chunk{{ID: "1", DocumentID: "allowed", Text: text, Embedding: vector}, {ID: "2", DocumentID: "private", Text: "secret", Embedding: vector}}))
	h := handler.QueryHandler(retriever.NewRetriever(vs, emb, 0.7, 10), amb, cfg, nil)
	for _, tt := range []struct {
		body        string
		code, count int
	}{
		{`{"query":" "}`, 400, 0}, {`{"query":"policy","mode":"invalid"}`, 400, 0}, {`{"query":"policy","max_chunks":101}`, 400, 0},
		{`{"query":"policy"} {}`, 400, 0}, {`{"query":"policy","allowed_document_ids":[]}`, 200, 0}, {`{"query":"policy","allowed_document_ids":["allowed"]}`, 200, 1},
	} {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/query", strings.NewReader(tt.body))
		req.Header.Set("Content-Type", "application/json; charset=utf-8")
		rec := httptest.NewRecorder()
		h(rec, req)
		require.Equal(t, tt.code, rec.Code, rec.Body.String())
		if tt.code == 200 {
			var resp handler.QueryResponse
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
			require.Equal(t, tt.count, resp.ChunksUsed)
			if tt.count > 0 {
				require.Equal(t, strings.Repeat("界", 600), resp.ScoredChunks[0].Text)
				require.NotContains(t, resp.Answer, "secret")
			}
		}
	}
}

func TestDeleteKeepsMetadataWhenVectorDeletionFails(t *testing.T) {
	cs := newMockChunkStore()
	id := uuid.NewString()
	cs.documents[id] = domain.Document{ID: domain.DocumentID(id)}
	req := httptest.NewRequestWithContext(t.Context(), http.MethodDelete, "/documents/"+id, http.NoBody)
	req.SetPathValue("doc_id", id)
	rec := httptest.NewRecorder()
	handler.DeleteHandler(cs, failedVectorDelete{}, nil)(rec, req)
	require.Equal(t, 500, rec.Code)
	doc, err := cs.GetDocument(t.Context(), domain.DocumentID(id))
	require.NoError(t, err)
	require.NotNil(t, doc)
}

type failedVectorDelete struct{}

func (failedVectorDelete) DeleteAllVectorsInNamespace(context.Context, string) error {
	return errors.New("vector service unavailable")
}
func (failedVectorDelete) DeleteByDocID(context.Context, string) error { return nil }

func TestNamedEntityCacheIsScopedToDatabase(t *testing.T) {
	if testing.Short() {
		t.Skip("needs a Docker daemon")
	}
	databaseURL := startPostgres(t)
	first, err := pgstore.NewClient(t.Context(), databaseURL)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, first.Close(context.Background())) })
	require.NoError(t, first.RunMigrations(t.Context()))
	require.NoError(t, first.InsertDocument(t.Context(), domain.Document{ID: domain.DocumentID(uuid.NewString()), Content: "text", ContentPreview: "Cedar Archive stores records."}))
	entities, err := pipeline.FetchNamedEntitiesFromDB(t.Context(), first.Pool)
	require.NoError(t, err)
	require.True(t, entities["cedar archive"])
	_, err = first.Pool.Exec(t.Context(), `CREATE DATABASE other`)
	require.NoError(t, err)
	address, err := url.Parse(databaseURL)
	require.NoError(t, err)
	address.Path = "/other"
	second, err := pgstore.NewClient(t.Context(), address.String())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, second.Close(context.Background())) })
	require.NoError(t, second.RunMigrations(t.Context()))
	require.NoError(t, second.InsertDocument(t.Context(), domain.Document{ID: domain.DocumentID(uuid.NewString()), Content: "text", ContentPreview: "Alpine Station stores records."}))
	entities, err = pipeline.FetchNamedEntitiesFromDB(t.Context(), second.Pool)
	require.NoError(t, err)
	require.True(t, entities["alpine station"])
	require.False(t, entities["cedar archive"])
}

type retryVectorDelete struct{ attempts int }

func (d *retryVectorDelete) DeleteAllVectorsInNamespace(context.Context, string) error {
	d.attempts++
	if d.attempts > 1 {
		return status.Error(codes.NotFound, "namespace absent")
	}
	return nil
}
func (d *retryVectorDelete) DeleteByDocID(context.Context, string) error {
	if d.attempts == 1 {
		return errors.New("temporary delete failure")
	}
	return status.Error(codes.NotFound, "global namespace absent")
}
func TestDeleteRetriesPartialDeletionWithMissingNamespaces(t *testing.T) {
	cs := newMockChunkStore()
	id := uuid.NewString()
	cs.documents[id] = domain.Document{ID: domain.DocumentID(id)}
	vectors := &retryVectorDelete{}
	for _, want := range []int{500, 200} {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodDelete, "/documents/"+id, http.NoBody)
		req.SetPathValue("doc_id", id)
		rec := httptest.NewRecorder()
		handler.DeleteHandler(cs, vectors, nil)(rec, req)
		require.Equal(t, want, rec.Code, rec.Body.String())
	}
	doc, err := cs.GetDocument(t.Context(), domain.DocumentID(id))
	require.NoError(t, err)
	require.Nil(t, doc)
}
func TestEscapedJSONNearDecodedContentLimitIsAccepted(t *testing.T) {
	cs := newMockChunkStore()
	body := `{"content":"` + strings.Repeat(`\u003c`, 999_999) + `"}`
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/ingest", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.IngestHandler(worker.NewWorker(1, nil, cs), cs, nil, 20<<20)(rec, req)
	require.Equal(t, 202, rec.Code, rec.Body.String())
}
