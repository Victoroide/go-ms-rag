package integration

import (
	"context"
	"encoding/json"
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

func visibilityClient(t *testing.T) *pgstore.Client {
	t.Helper()
	client, err := pgstore.NewClient(t.Context(), startPostgres(t))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, client.Close(context.Background())) })
	require.NoError(t, client.RunMigrations(t.Context()))
	return client
}

func TestVisibilityRules(t *testing.T) {
	if testing.Short() {
		t.Skip("requires a Docker daemon")
	}
	client := visibilityClient(t)
	completed, legacy, processing, failed, missing := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	for _, row := range []struct{ id, status string }{
		{completed, "completed"}, {legacy, "processing"}, {processing, "processing"}, {failed, "failed"},
	} {
		_, err := client.Pool.Exec(t.Context(), `INSERT INTO documents (id, content, status) VALUES ($1, 'text', $2)`, row.id, row.status)
		require.NoError(t, err)
	}
	require.NoError(t, client.StoreFingerprint(t.Context(), "legacy", legacy))
	require.NoError(t, client.StoreFingerprint(t.Context(), "failed", failed))
	vis := pgstore.Visibility{Pool: client.Pool}
	got, err := vis.VisibleDocumentIDs(t.Context(), []string{completed, strings.ToUpper(completed), completed, legacy, processing, failed, missing, "malformed", ""})
	require.NoError(t, err)
	require.Equal(t, map[string]struct{}{completed: {}, legacy: {}}, got)
	for _, ids := range [][]string{nil, {}, {"malformed", ""}} {
		got, err = (pgstore.Visibility{}).VisibleDocumentIDs(t.Context(), ids)
		require.NoError(t, err)
		require.NotNil(t, got)
		require.Empty(t, got)
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = vis.VisibleDocumentIDs(canceled, []string{completed})
	require.ErrorIs(t, err, context.Canceled)
}

type visibilityLaggingVectors struct{ *memory.FakeVectorStore }

func (visibilityLaggingVectors) DeleteAllVectorsInNamespace(context.Context, string) error {
	return nil
}
func (visibilityLaggingVectors) DeleteByDocID(context.Context, string) error { return nil }

func TestOrphanVectorsAreNotCited(t *testing.T) {
	if testing.Short() {
		t.Skip("requires a Docker daemon")
	}
	client := visibilityClient(t)
	completed, failed, processing, missing := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	for _, row := range []struct{ id, status string }{{completed, "completed"}, {failed, "failed"}, {processing, "processing"}} {
		_, err := client.Pool.Exec(t.Context(), `INSERT INTO documents (id, content, status) VALUES ($1, 'text', $2)`, row.id, row.status)
		require.NoError(t, err)
	}
	vs := visibilityLaggingVectors{memory.NewFakeVectorStore()}
	emb := embedder.NewFakeEmbedder(256)
	vector, err := emb.Embed(t.Context(), "policy")
	require.NoError(t, err)
	for _, row := range []struct{ id, text string }{
		{completed, "Completed policy lasts three years."}, {failed, "Failed policy is confidential."},
		{processing, "Unfinished policy is confidential."}, {missing, "Orphan policy is confidential."},
	} {
		require.NoError(t, vs.Upsert(t.Context(), "", []domain.Chunk{{ID: domain.ChunkID(uuid.NewString()), DocumentID: domain.DocumentID(row.id), Text: row.text, Embedding: vector}}))
	}
	cfg := &config.Config{BedrockFallbackModel: "fallback"}
	amb := llm.NewAmbassador(cfg)
	amb.SetClient(llm.NewFakeConverser())
	query := handler.QueryHandler(retriever.NewRetriever(vs, emb, 0.7, 10), amb, cfg, client.Pool)
	for _, deleted := range []bool{false, true} {
		if deleted {
			req := httptest.NewRequestWithContext(t.Context(), http.MethodDelete, "/documents/"+completed, http.NoBody)
			req.SetPathValue("doc_id", completed)
			rec := httptest.NewRecorder()
			handler.DeleteHandler(client, vs, nil)(rec, req)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		}
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/query", strings.NewReader(`{"query":"policy"}`))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		query(rec, req)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var response handler.QueryResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response))
		require.NotContains(t, response.Answer, "confidential")
		if deleted {
			require.Zero(t, response.ChunksUsed)
			require.Empty(t, response.ScoredChunks)
			require.True(t, response.EarlyExit)
			require.Equal(t, domain.TokenUsage{}, response.TokenUsage)
		} else {
			require.Equal(t, 1, response.ChunksUsed)
			require.Len(t, response.ScoredChunks, 1)
			require.Equal(t, completed, response.ScoredChunks[0].DocID)
			require.Contains(t, response.Answer, "Completed policy")
		}
	}
}
