package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Victoroide/quarry-rag/internal/api/handler"
	"github.com/Victoroide/quarry-rag/internal/config"
	"github.com/Victoroide/quarry-rag/internal/domain"
	"github.com/Victoroide/quarry-rag/internal/embedder"
	"github.com/Victoroide/quarry-rag/internal/llm"
	"github.com/Victoroide/quarry-rag/internal/retriever"
	"github.com/Victoroide/quarry-rag/internal/store/memory"
	pgstore "github.com/Victoroide/quarry-rag/internal/store/postgres"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func scopeQuery(t *testing.T, h http.HandlerFunc, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/query", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec
}

func TestAllowlistIsAppliedInsideVectorQuery(t *testing.T) {
	cfg := &config.Config{BedrockFallbackModel: "fallback"}
	amb := llm.NewAmbassador(cfg)
	amb.SetClient(llm.NewFakeConverser())
	emb := embedder.NewFakeEmbedder(256)
	vector, err := emb.Embed(t.Context(), "archive")
	require.NoError(t, err)
	vs := memory.NewFakeVectorStore()
	chunks := make([]domain.Chunk, 0, 31)
	for i := range 30 {
		chunks = append(chunks, domain.Chunk{ID: domain.ChunkID(fmt.Sprint(i)), DocumentID: domain.DocumentID(uuid.NewString()), Text: "private", Embedding: vector})
	}
	id := uuid.NewString()
	allowed := make([]float32, len(vector))
	for i, v := range vector {
		allowed[i] = -v
	}
	chunks = append(chunks, domain.Chunk{ID: "allowed", DocumentID: domain.DocumentID(id), Text: "The archive opens at nine.", Embedding: allowed})
	require.NoError(t, vs.Upsert(t.Context(), "", chunks))
	h := handler.QueryHandler(retriever.NewRetriever(vs, emb, 0.7, 10), amb, cfg, nil)
	rec := scopeQuery(t, h, fmt.Sprintf(`{"query":"archive","mode":"global","max_chunks":10,"allowed_document_ids":[%q,%q]}`, strings.ToUpper(id), "urn:uuid:"+strings.ToUpper(id)))
	require.Equal(t, 200, rec.Code, rec.Body.String())
	var resp handler.QueryResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Equal(t, 1, resp.ChunksUsed)
	require.Equal(t, id, resp.ScoredChunks[0].DocID)
}

func TestAllowlistEntriesMustBeUUIDs(t *testing.T) {
	h := scopeQueryHandler(&config.Config{})
	rec := scopeQuery(t, h, `{"query":"archive","allowed_document_ids":["not-a-uuid"]}`)
	require.Equal(t, 400, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "allowed_document_ids must contain UUIDs")
}

func scopeQueryHandler(cfg *config.Config) http.HandlerFunc {
	amb := llm.NewAmbassador(cfg)
	amb.SetClient(llm.NewFakeConverser())
	return handler.QueryHandler(retriever.NewRetriever(memory.NewFakeVectorStore(), embedder.NewFakeEmbedder(256), 0.7, 10), amb, cfg, nil)
}

func TestAllowlistRequiredWhenConfigured(t *testing.T) {
	h := scopeQueryHandler(&config.Config{RequireDocumentAllowlist: true})
	for _, body := range []string{`{"query":"archive"}`, `{"query":"archive","allowed_document_ids":null}`} {
		rec := scopeQuery(t, h, body)
		require.Equal(t, 400, rec.Code, rec.Body.String())
		require.Contains(t, rec.Body.String(), "allowed_document_ids is required")
	}
	require.Equal(t, 200, scopeQuery(t, h, `{"query":"archive","allowed_document_ids":[]}`).Code)
}

func TestAllowlistLimitAndCanonicalUUIDs(t *testing.T) {
	cfg := &config.Config{MaxAllowedDocumentIDs: 1}
	h := scopeQueryHandler(cfg)
	id := uuid.NewString()
	rec := scopeQuery(t, h, fmt.Sprintf(`{"query":"archive","allowed_document_ids":[%q,%q]}`, id, id))
	require.Equal(t, 400, rec.Code, rec.Body.String())
	cfg.MaxAllowedDocumentIDs = 2
	rec = scopeQuery(t, h, fmt.Sprintf(`{"query":"archive","allowed_document_ids":[%q,%q]}`, id, strings.ToUpper(id)))
	require.Equal(t, 200, rec.Code, rec.Body.String())
}

func TestDocumentPreviewsAreRestrictedToCandidates(t *testing.T) {
	if testing.Short() {
		t.Skip("requires a Docker daemon")
	}
	client := visibilityClient(t)
	allowed, private, noPreview := uuid.NewString(), uuid.NewString(), uuid.NewString()
	for _, row := range []struct{ id, preview string }{{allowed, "Cedar Archive stores records."}, {private, "Falcon Station stores records."}} {
		_, err := client.Pool.Exec(t.Context(), `INSERT INTO documents (id, content, content_preview, status) VALUES ($1, 'text', $2, 'completed')`, row.id, row.preview)
		require.NoError(t, err)
	}
	_, err := client.Pool.Exec(t.Context(), `INSERT INTO documents (id,content,status) VALUES ($1,'text','completed')`, noPreview)
	require.NoError(t, err)
	previews := pgstore.Previews{Pool: client.Pool}
	got, err := previews.DocumentPreviews(t.Context(), []string{allowed, noPreview, uuid.NewString(), allowed})
	require.NoError(t, err)
	require.Equal(t, map[string]string{allowed: "Cedar Archive stores records."}, got)
	got, err = (pgstore.Previews{}).DocumentPreviews(t.Context(), nil)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Empty(t, got)
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = previews.DocumentPreviews(canceled, []string{allowed})
	require.ErrorIs(t, err, context.Canceled)
}
