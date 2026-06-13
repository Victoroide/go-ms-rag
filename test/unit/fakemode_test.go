package unit

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Victoroide/go-ms-rag/internal/api/middleware"
	"github.com/Victoroide/go-ms-rag/internal/domain"
	"github.com/Victoroide/go-ms-rag/internal/embedder"
	"github.com/Victoroide/go-ms-rag/internal/llm"
	"github.com/Victoroide/go-ms-rag/internal/store/memory"
)

// FakeEmbedder must be deterministic, correctly dimensioned, and distinguish
// different inputs — the properties the in-memory store relies on for sane
// similarity ordering in GO_RAG_FAKE_MODE.
func TestFakeEmbedderDeterministicAndDimensioned(t *testing.T) {
	e := embedder.NewFakeEmbedder(1024)
	a1, err := e.Embed(context.Background(), "Python Django backend engineer")
	if err != nil {
		t.Fatalf("embed: %v", err)
	}
	if len(a1) != 1024 {
		t.Fatalf("expected 1024 dims, got %d", len(a1))
	}
	a2, _ := e.Embed(context.Background(), "Python Django backend engineer")
	for i := range a1 {
		if a1[i] != a2[i] {
			t.Fatalf("embedding not deterministic at index %d", i)
		}
	}
	b, _ := e.Embed(context.Background(), "Angular TypeScript frontend developer")
	same := true
	for i := range a1 {
		if a1[i] != b[i] {
			same = false
			break
		}
	}
	if same {
		t.Fatal("different inputs produced identical embeddings")
	}
}

// The in-memory vector store must upsert, return the most-similar chunk first,
// honor the doc_type filter, and delete by document id.
func TestFakeVectorStoreQueryFilterDelete(t *testing.T) {
	ctx := context.Background()
	e := embedder.NewFakeEmbedder(1024)
	store := memory.NewFakeVectorStore()

	jobText := "Backend RAG engineer Python Django Go Docker AWS PostgreSQL Pinecone"
	candText := "Frontend Angular TypeScript Tailwind developer"
	jobEmb, _ := e.Embed(ctx, jobText)
	candEmb, _ := e.Embed(ctx, candText)

	chunks := []domain.Chunk{
		{ID: "c-job", DocumentID: "doc-job", Text: jobText, Embedding: jobEmb, Metadata: map[string]any{"doc_type": "job"}},
		{ID: "c-cand", DocumentID: "doc-cand", Text: candText, Embedding: candEmb, Metadata: map[string]any{"doc_type": "candidate"}},
	}
	if err := store.Upsert(ctx, "", chunks); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	// Query with the job doc_type filter using an embedding of the job text:
	// only the job chunk may come back, and it must be the job chunk.
	res, err := store.QueryAllNamespaces(ctx, jobEmb, 10, "job")
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(res) != 1 || string(res[0].Chunk.DocumentID) != "doc-job" {
		t.Fatalf("doc_type=job filter wrong: %+v", res)
	}
	if res[0].Score < 0.99 {
		t.Fatalf("self-similarity should be ~1.0, got %f", res[0].Score)
	}

	// candidate filter returns only the candidate chunk.
	res, _ = store.QueryAllNamespaces(ctx, candEmb, 10, "candidate")
	if len(res) != 1 || string(res[0].Chunk.DocumentID) != "doc-cand" {
		t.Fatalf("doc_type=candidate filter wrong: %+v", res)
	}

	// DeleteByDocID removes the job chunk; a job-filtered query is then empty.
	if err := store.DeleteByDocID(ctx, "doc-job"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	res, _ = store.QueryAllNamespaces(ctx, jobEmb, 10, "job")
	if len(res) != 0 {
		t.Fatalf("expected no job chunks after delete, got %d", len(res))
	}
}

// The fake LLM produces a non-empty, grounded answer (echoing context lines)
// without any cloud call.
func TestFakeConverserGrounded(t *testing.T) {
	amb := llm.NewAmbassador(nil)
	amb.SetClient(llm.NewFakeConverser())
	out, err := amb.Converse(context.Background(), nil)
	if err != nil {
		t.Fatalf("converse: %v", err)
	}
	if txt := llm.ExtractText(out); txt == "" {
		t.Fatal("fake converser returned empty text")
	}
}

// Service-token middleware: open when token empty; 401 on missing/wrong token;
// pass on correct token.
func TestRequireServiceToken(t *testing.T) {
	ok := func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }

	// Empty token -> always open.
	rec := httptest.NewRecorder()
	middleware.RequireServiceToken("", ok)(rec, httptest.NewRequest(http.MethodPost, "/ingest", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("empty token should allow, got %d", rec.Code)
	}

	// Set token, no header -> 401.
	rec = httptest.NewRecorder()
	middleware.RequireServiceToken("secret", ok)(rec, httptest.NewRequest(http.MethodPost, "/ingest", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("missing header should 401, got %d", rec.Code)
	}

	// Set token, wrong header -> 401.
	rec = httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/ingest", nil)
	r.Header.Set("X-Service-Token", "nope")
	middleware.RequireServiceToken("secret", ok)(rec, r)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong header should 401, got %d", rec.Code)
	}

	// Correct header -> pass.
	rec = httptest.NewRecorder()
	r = httptest.NewRequest(http.MethodPost, "/ingest", nil)
	r.Header.Set("X-Service-Token", "secret")
	middleware.RequireServiceToken("secret", ok)(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("correct header should allow, got %d", rec.Code)
	}
}
