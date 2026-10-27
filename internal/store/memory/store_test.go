package memory

import (
	"testing"

	"github.com/Victoroide/quarry-rag/internal/domain"
)

func TestMetadataOwnershipAndQuality(t *testing.T) {
	s := NewFakeVectorStore()
	md := map[string]any{"doc_type": "manual", "kind": "body", "quality": "high"}
	if err := s.Upsert(t.Context(), "", []domain.Chunk{{ID: "1", Embedding: []float32{1}, Metadata: md}}); err != nil {
		t.Fatal(err)
	}
	md["doc_type"] = "changed"
	result, err := s.Query(t.Context(), "", []float32{1}, 1, "manual")
	if err != nil || len(result) != 1 {
		t.Fatalf("stored metadata changed through caller alias: %v %v", result, err)
	}
	if result[0].Chunk.Metadata["quality"] != "high" {
		t.Fatal("quality metadata was dropped")
	}
	result[0].Chunk.Metadata["doc_type"] = "changed"
	again, err := s.Query(t.Context(), "", []float32{1}, 1, "manual")
	if err != nil || len(again) != 1 {
		t.Fatal("query exposed stored metadata")
	}
}
