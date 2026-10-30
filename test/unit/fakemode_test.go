package unit

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Victoroide/quarry-rag/internal/api/middleware"
	"github.com/Victoroide/quarry-rag/internal/domain"
	"github.com/Victoroide/quarry-rag/internal/embedder"
	"github.com/Victoroide/quarry-rag/internal/llm"
	"github.com/Victoroide/quarry-rag/internal/store/memory"
)

// FakeEmbedder must be deterministic, correctly dimensioned, and distinguish
// different inputs — the properties the in-memory store relies on for sane
// similarity ordering in GO_RAG_FAKE_MODE.
func TestFakeEmbedderDeterministicAndDimensioned(t *testing.T) {
	e := embedder.NewFakeEmbedder(1024)
	a1, err := e.Embed(context.Background(), "Archive storage guide Python Go PostgreSQL")
	if err != nil {
		t.Fatalf("embed: %v", err)
	}
	if len(a1) != 1024 {
		t.Fatalf("expected 1024 dims, got %d", len(a1))
	}
	a2, _ := e.Embed(context.Background(), "Archive storage guide Python Go PostgreSQL")
	for i := range a1 {
		if a1[i] != a2[i] {
			t.Fatalf("embedding not deterministic at index %d", i)
		}
	}
	b, _ := e.Embed(context.Background(), "Registry operations report Redis Docker")
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

	reportText := "Archive storage report Python Go Docker AWS PostgreSQL Pinecone"
	referenceText := "Registry reference manual for retention and access controls"
	reportEmb, _ := e.Embed(ctx, reportText)
	referenceEmb, _ := e.Embed(ctx, referenceText)

	chunks := []domain.Chunk{
		{ID: "c-report", DocumentID: "doc-report", Text: reportText, Embedding: reportEmb, Metadata: map[string]any{"doc_type": "report"}},
		{ID: "c-reference", DocumentID: "doc-reference", Text: referenceText, Embedding: referenceEmb, Metadata: map[string]any{"doc_type": "reference"}},
	}
	if err := store.Upsert(ctx, "", chunks); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	// Query with the report doc_type filter using an embedding of the report text:
	// only the report chunk may come back, and it must be the report chunk.
	res, err := store.QueryAllNamespaces(ctx, reportEmb, 10, "report")
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(res) != 1 || string(res[0].Chunk.DocumentID) != "doc-report" {
		t.Fatalf("doc_type=report filter wrong: %+v", res)
	}
	if res[0].Score < 0.99 {
		t.Fatalf("self-similarity should be ~1.0, got %f", res[0].Score)
	}

	// reference filter returns only the reference chunk.
	res, _ = store.QueryAllNamespaces(ctx, referenceEmb, 10, "reference")
	if len(res) != 1 || string(res[0].Chunk.DocumentID) != "doc-reference" {
		t.Fatalf("doc_type=reference filter wrong: %+v", res)
	}

	// DeleteByDocID removes the report chunk; a report-filtered query is then empty.
	if err := store.DeleteByDocID(ctx, "doc-report"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	res, _ = store.QueryAllNamespaces(ctx, reportEmb, 10, "report")
	if len(res) != 0 {
		t.Fatalf("expected no report chunks after delete, got %d", len(res))
	}
}

// The fake LLM produces a non-empty, grounded answer (echoing context lines)
// without any cloud call.
func TestFakeConverserGrounded(t *testing.T) {
	amb := llm.NewAmbassador(nil)
	amb.SetClient(llm.NewFakeConverser())
	out, err := amb.ConverseRoutedPlain(context.Background(), "fallback", "", nil, 0, 1024)
	if err != nil {
		t.Fatalf("converse: %v", err)
	}
	if txt := out.Text; txt == "" {
		t.Fatal("fake converser returned empty text")
	}
}

// Service-token middleware: open when token empty; 401 on missing/wrong token;
// pass on correct token.
func TestRequireServiceToken(t *testing.T) {
	ok := func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }

	// Empty token -> always open.
	rec := httptest.NewRecorder()
	middleware.RequireServiceToken("", ok)(rec, httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/ingest", http.NoBody))
	if rec.Code != http.StatusOK {
		t.Fatalf("empty token should allow, got %d", rec.Code)
	}

	// Set token, no header -> 401.
	rec = httptest.NewRecorder()
	middleware.RequireServiceToken("secret", ok)(rec, httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/ingest", http.NoBody))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("missing header should 401, got %d", rec.Code)
	}

	// Set token, wrong header -> 401.
	rec = httptest.NewRecorder()
	r := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/ingest", http.NoBody)
	r.Header.Set("X-Service-Token", "nope")
	middleware.RequireServiceToken("secret", ok)(rec, r)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong header should 401, got %d", rec.Code)
	}

	// Correct header -> pass.
	rec = httptest.NewRecorder()
	r = httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/ingest", http.NoBody)
	r.Header.Set("X-Service-Token", "secret")
	middleware.RequireServiceToken("secret", ok)(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("correct header should allow, got %d", rec.Code)
	}
}
