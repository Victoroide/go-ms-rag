package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Victoroide/quarry-rag/internal/api"
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
)

func TestOfflineQueryEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("requires a Docker daemon")
	}
	db, err := pgstore.NewClient(t.Context(), startPostgres(t))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close(context.Background())) })
	require.NoError(t, db.RunMigrations(t.Context()))

	vectors := memory.NewFakeVectorStore()
	emb := embedder.NewFakeEmbedder(256)
	cfg := &config.Config{
		FakeMode: true, BedrockFallbackModel: "offline", MaxUploadBytes: 1 << 20,
		MaxChunksPerQuery: 10, WorkerQueueSize: 10, ExtractConcurrency: 1,
	}
	amb := llm.NewAmbassador(cfg)
	amb.SetClient(llm.NewFakeConverser())
	w := worker.New(db, []pipeline.Stage{
		pipeline.NewChunkStage(chunker.NewChunker(128, 16)),
		pipeline.NewEmbedStage(emb, 2), pipeline.NewPersistStage(vectors),
	}, vectors, worker.Options{Workers: 1, Owner: "offline-query", PollInterval: 10 * time.Millisecond})
	w.Start(t.Context())
	t.Cleanup(w.Shutdown)
	server := httptest.NewServer(api.NewRouter(&api.RouterDeps{
		VectorStore: vectors, ChunkStore: db, Retriever: retriever.NewRetriever(vectors, emb, 0.7, 10),
		Ambassador: amb, Config: cfg, Worker: w, PineconeDriver: vectors, PGPool: db.Pool,
	}))
	t.Cleanup(server.Close)
	client := server.Client()
	client.Timeout = 5 * time.Second

	const content = "The archive retains reports for seven years."
	var ingested handler.IngestResponse
	offlineQueryHTTP(t, client, http.MethodPost, server.URL+"/ingest", map[string]any{"content": content}, http.StatusAccepted, &ingested)
	require.NotEmpty(t, ingested.DocumentID)
	require.Eventually(t, func() bool {
		var status domain.DocumentSummary
		offlineQueryHTTP(t, client, http.MethodGet, server.URL+"/documents/"+ingested.DocumentID, nil, http.StatusOK, &status)
		require.NotEqual(t, "failed", status.Status, "%+v", status)
		return status.Status == "completed" && status.ChunkCount > 0
	}, 10*time.Second, 10*time.Millisecond)

	var answer handler.QueryResponse
	offlineQueryHTTP(t, client, http.MethodPost, server.URL+"/query", handler.QueryRequest{Query: content}, http.StatusOK, &answer)
	require.Positive(t, answer.ChunksUsed)
	require.Contains(t, answer.Answer, `id="1"`)
	require.Contains(t, answer.Answer, content)
	require.NotEmpty(t, answer.ScoredChunks)
	for _, chunk := range answer.ScoredChunks {
		require.Equal(t, ingested.DocumentID, chunk.DocID)
		require.Equal(t, content, chunk.Text)
	}

	var restricted handler.QueryResponse
	offlineQueryHTTP(t, client, http.MethodPost, server.URL+"/query", handler.QueryRequest{
		Query: content, AllowedDocumentIDs: []string{uuid.NewString()},
	}, http.StatusOK, &restricted)
	require.Equal(t, "No relevant information was found to answer this question.", restricted.Answer)
	require.Empty(t, restricted.ScoredChunks)
	require.Zero(t, restricted.ChunksUsed)
	require.Zero(t, restricted.TokenUsage.TotalTokens)
}

func offlineQueryHTTP(t *testing.T, client *http.Client, method, address string, body any, status int, response any) {
	t.Helper()
	encoded, err := json.Marshal(body)
	require.NoError(t, err)
	req, err := http.NewRequestWithContext(t.Context(), method, address, bytes.NewReader(encoded))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer func() { require.NoError(t, resp.Body.Close()) }()
	require.Equal(t, status, resp.StatusCode)
	require.NoError(t, json.NewDecoder(resp.Body).Decode(response))
}
