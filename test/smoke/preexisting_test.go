package smoke

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestSmoke_PreexistingDocumentRemainsRetrievable(t *testing.T) {
	skipUnlessSmoke(t)
	docID := strings.TrimSpace(os.Getenv("SMOKE_EXISTING_DOCUMENT_ID"))
	if docID == "" {
		t.Skip("set SMOKE_EXISTING_DOCUMENT_ID to a document ingested before the upgrade")
	}
	id, err := uuid.Parse(docID)
	if err != nil {
		t.Fatalf("invalid SMOKE_EXISTING_DOCUMENT_ID: %v", err)
	}
	docID = id.String()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	index, dims := vectorSmokeIndex(ctx, t)
	present, err := vectorSmokeHasDocument(ctx, index, dims, docID)
	if err != nil {
		t.Fatal(err)
	}
	if !present {
		t.Fatalf("vectors of document %s are not visible in the default namespace", docID)
	}
	payload, err := json.Marshal(map[string]any{
		"query": "What information is in this document?", "mode": "global",
		"allowed_document_ids": []string{docID}, "max_chunks": 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := request(ctx, http.MethodPost, baseURL+"/query", "application/json", bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("service query returned %d after confirming the document vectors are visible", resp.StatusCode)
	}
	var result struct {
		FinishReason string `json:"finish_reason"`
		ChunksUsed   int    `json:"chunks_used"`
		ScoredChunks []struct {
			DocID string `json:"doc_id"`
			Text  string `json:"text"`
		} `json:"scored_chunks"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if result.FinishReason == "no_evidence" {
		t.Fatal("document vectors are visible, but the service returned no_evidence; check MIN_RELEVANCE_SCORE and the query before retrying")
	}
	if result.ChunksUsed < 1 || len(result.ScoredChunks) < 1 {
		t.Fatal("preexisting document returned no chunks")
	}
	for _, chunk := range result.ScoredChunks {
		if chunk.DocID != docID || strings.TrimSpace(chunk.Text) == "" {
			t.Fatalf("expected text from document %s, got document %s", docID, chunk.DocID)
		}
	}
}
