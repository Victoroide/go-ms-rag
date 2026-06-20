package pipeline

import (
	"testing"

	"github.com/Victoroide/go-ms-rag/internal/domain"
)

func sc(docID string, score float64) domain.ScoredChunk {
	return domain.ScoredChunk{
		Chunk: domain.Chunk{DocumentID: domain.DocumentID(docID), Text: "t-" + docID},
		Score: score,
	}
}

func docIDs(chunks []domain.ScoredChunk) []string {
	out := make([]string, 0, len(chunks))
	for _, c := range chunks {
		out = append(out, string(c.Chunk.DocumentID))
	}
	return out
}

func TestFilterChunksByAllowedDocIDs_NilAllowlistIsNoOp(t *testing.T) {
	in := []domain.ScoredChunk{sc("a", 0.9), sc("b", 0.8)}
	got := filterChunksByAllowedDocIDs(in, nil)
	if len(got) != 2 {
		t.Fatalf("nil allowlist must not filter; got %v", docIDs(got))
	}
}

func TestFilterChunksByAllowedDocIDs_RestrictsToAllowed(t *testing.T) {
	// Simulates a company rep: only candidate "a" applied to their company,
	// so "b" (another company's candidate) must never reach synthesis.
	in := []domain.ScoredChunk{sc("a", 0.9), sc("b", 0.8), sc("c", 0.7)}
	got := filterChunksByAllowedDocIDs(in, []string{"a"})
	if len(got) != 1 || string(got[0].Chunk.DocumentID) != "a" {
		t.Fatalf("expected only [a], got %v", docIDs(got))
	}
}

func TestFilterChunksByAllowedDocIDs_EmptyAllowlistMatchesNothing(t *testing.T) {
	// A rep with zero applicants must retrieve nothing — never the full corpus.
	in := []domain.ScoredChunk{sc("a", 0.9), sc("b", 0.8)}
	got := filterChunksByAllowedDocIDs(in, []string{})
	if len(got) != 0 {
		t.Fatalf("empty (non-nil) allowlist must match nothing; got %v", docIDs(got))
	}
}

func TestFilterChunksByAllowedDocIDs_PreservesOrder(t *testing.T) {
	in := []domain.ScoredChunk{sc("a", 0.9), sc("b", 0.8), sc("c", 0.7)}
	got := filterChunksByAllowedDocIDs(in, []string{"c", "a"})
	if len(got) != 2 || string(got[0].Chunk.DocumentID) != "a" || string(got[1].Chunk.DocumentID) != "c" {
		t.Fatalf("expected score order [a,c], got %v", docIDs(got))
	}
}
