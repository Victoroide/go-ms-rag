package integration

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Victoroide/quarry-rag/internal/api/handler"
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
			vs := memory.NewFakeVectorStore()
			ic := pipeline.NewIngestContext("doc", strings.Repeat("界", 50), nil)
			ic.DocType = label
			ic.Chunks = []domain.Chunk{{ID: "chunk", DocumentID: "doc", Embedding: []float32{1}}}
			require.NoError(t, pipeline.NewPersistStage(vs).Run(t.Context(), ic))
			want := label
			if want == "" {
				want = "document"
			}
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
		handler.IngestHandler(worker.New(cs, nil, nil, worker.Options{}), cs, nil, 1024)(rec, req)
		require.Equal(t, http.StatusBadRequest, rec.Code)
	}
}

func TestJSONIngestBodyLimit(t *testing.T) {
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/ingest", strings.NewReader(`{"content":"text", "metadata":{"large":"`+strings.Repeat("x", 9<<20)+`"}}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	cs := newMockChunkStore()
	handler.IngestHandler(worker.New(cs, nil, nil, worker.Options{}), cs, nil, 1024)(rec, req)
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

func TestShutdownReleasesLease(t *testing.T) {
	c := durableDatabase(t)
	v := memory.NewFakeVectorStore()
	e := durableBlockingEmbedder{make(chan struct{}, 1), make(chan struct{})}
	w := durableWorker(c, v, e)
	rec := durableSubmit(t, w, c, `{"content":"Release the unfinished document on shutdown."}`)
	require.Equal(t, 202, rec.Code)
	w.Start(t.Context())
	select {
	case <-e.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("processing did not start")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	require.ErrorIs(t, w.ShutdownContext(ctx), context.Canceled)
	job, err := c.ClaimJob(t.Context(), "replacement", time.Minute, 3)
	require.NoError(t, err)
	require.NotNil(t, job)
	require.Equal(t, 1, job.Attempts)
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
	require.NoError(t, vs.Upsert(t.Context(), "", []domain.Chunk{{ID: "1", DocumentID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", Text: text, Embedding: vector}, {ID: "2", DocumentID: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", Text: "secret", Embedding: vector}}))
	h := handler.QueryHandler(retriever.NewRetriever(vs, emb, 0.7, 10), amb, cfg, nil)
	for _, tt := range []struct {
		body        string
		code, count int
	}{
		{`{"query":" "}`, 400, 0}, {`{"query":"policy","mode":"invalid"}`, 400, 0}, {`{"query":"policy","max_chunks":101}`, 400, 0},
		{`{"query":"policy"} {}`, 400, 0}, {`{"query":"policy","allowed_document_ids":[]}`, 200, 0}, {`{"query":"policy","allowed_document_ids":["aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"]}`, 200, 1},
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
	handler.IngestHandler(worker.New(cs, nil, nil, worker.Options{}), cs, nil, 20<<20)(rec, req)
	require.Equal(t, 202, rec.Code, rec.Body.String())
}
