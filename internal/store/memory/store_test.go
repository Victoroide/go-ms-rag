package memory

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/Victoroide/quarry-rag/internal/domain"
	"github.com/stretchr/testify/require"
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

func TestOfflineStoreRejectsOversizedMetadata(t *testing.T) {
	for _, chunk := range []domain.Chunk{
		{Text: strings.Repeat("x", 40960)},
		{Metadata: map[string]any{"doc_type": strings.Repeat("x", 40960)}},
		{Text: strings.Repeat("\n", 20480)},
	} {
		chunk.ID, chunk.Embedding = "large", []float32{1}
		s := NewFakeVectorStore()
		require.Error(t, s.Upsert(t.Context(), "", []domain.Chunk{chunk}))
		got, err := s.QueryFiltered(t.Context(), []float32{1}, 10, domain.VectorFilter{})
		require.NoError(t, err)
		require.Empty(t, got)
	}
}

func TestOfflineStoreMetadataSizeBoundary(t *testing.T) {
	// The four required metadata fields occupy 55 bytes with empty text.
	for _, size := range []int{40960, 40961} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			s := NewFakeVectorStore()
			chunk := domain.Chunk{ID: "boundary", Text: strings.Repeat("x", size-55), Embedding: []float32{1}}
			err := s.Upsert(t.Context(), "", []domain.Chunk{chunk})
			if size == 40960 {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}

func TestOfflineStoreRejectsNestedMetadata(t *testing.T) {
	for name, value := range map[string]any{
		"object":       map[string]any{"nested": "value"},
		"null":         nil,
		"null list":    []string(nil),
		"mixed list":   []any{"value", 1},
		"nested list":  []any{[]string{"value"}},
		"object list":  []any{map[string]any{"nested": "value"}},
		"not a number": math.NaN(),
		"infinity":     math.Inf(1),
	} {
		t.Run(name, func(t *testing.T) {
			s := NewFakeVectorStore()
			err := s.Upsert(t.Context(), "", []domain.Chunk{{ID: "invalid", Embedding: []float32{1}, Metadata: map[string]any{"kind": value}}})
			require.Error(t, err)
		})
	}
}

func TestOfflineStoreAcceptsFlatMetadata(t *testing.T) {
	for name, value := range map[string]any{
		"string": "body", "integer": 3, "unsigned": uint64(4), "float": 1.5,
		"json number": json.Number("2.5"), "boolean": true,
		"strings": []string{"one", "two"}, "decoded strings": []any{"one", "two"}, "empty list": []string{},
	} {
		t.Run(name, func(t *testing.T) {
			s := NewFakeVectorStore()
			require.NoError(t, s.Upsert(t.Context(), "", []domain.Chunk{{ID: "valid", Embedding: []float32{1}, Metadata: map[string]any{"kind": value}}}))
			got, err := s.QueryFiltered(t.Context(), []float32{1}, 1, domain.VectorFilter{})
			require.NoError(t, err)
			require.Len(t, got, 1)
			require.Equal(t, value, got[0].Chunk.Metadata["kind"])
		})
	}
}

func TestOfflineStoreRejectsDimensionMismatch(t *testing.T) {
	s := NewFakeVectorStore()
	require.NoError(t, s.Upsert(t.Context(), "", []domain.Chunk{{ID: "first", Embedding: []float32{1, 0}}}))
	for _, namespace := range []string{"", "other"} {
		err := s.Upsert(t.Context(), namespace, []domain.Chunk{{ID: "mismatch", Embedding: []float32{1}}})
		require.Error(t, err, "dimension must be shared by every namespace")
	}
	require.NoError(t, s.Upsert(t.Context(), "other", []domain.Chunk{{ID: "matching", Embedding: []float32{0, 1}}}))
}

func TestOfflineStoreRejectsOversizedID(t *testing.T) {
	for _, id := range []string{strings.Repeat("x", 512), strings.Repeat("x", 513), strings.Repeat("é", 256), strings.Repeat("é", 257)} {
		s := NewFakeVectorStore()
		err := s.Upsert(t.Context(), "", []domain.Chunk{{ID: domain.ChunkID(id), Embedding: []float32{1}}})
		if len(id) <= 512 {
			require.NoError(t, err)
		} else {
			require.Error(t, err)
		}
	}
}

func TestOfflineStoreRejectsExcessiveTopK(t *testing.T) {
	s := NewFakeVectorStore()
	_, err := s.QueryFiltered(t.Context(), []float32{1}, 10000, domain.VectorFilter{})
	require.NoError(t, err)
	for _, filter := range []domain.VectorFilter{{}, {DocumentIDs: []string{}}} {
		_, err = s.QueryFiltered(t.Context(), []float32{1}, 10001, filter)
		require.Error(t, err)
	}
}

func TestOfflineStoreAcceptsLargeBatch(t *testing.T) {
	s := NewFakeVectorStore()
	chunks := make([]domain.Chunk, 1001)
	for i := range chunks {
		chunks[i] = domain.Chunk{ID: domain.ChunkID(fmt.Sprint(i)), Embedding: []float32{1}}
	}
	require.NoError(t, s.Upsert(t.Context(), "", chunks))
	got, err := s.QueryFiltered(t.Context(), []float32{1}, 10000, domain.VectorFilter{})
	require.NoError(t, err)
	require.Len(t, got, 1001)
}

func TestOfflineStoreRejectedUpsertPreservesRecordsAndDimension(t *testing.T) {
	s := NewFakeVectorStore()
	require.Error(t, s.Upsert(t.Context(), "", []domain.Chunk{
		{ID: "first", Embedding: []float32{1, 0}},
		{ID: "invalid", Embedding: []float32{1, 0}, Metadata: map[string]any{"quality": nil}},
	}))
	got, err := s.QueryFiltered(t.Context(), []float32{1}, 1, domain.VectorFilter{})
	require.NoError(t, err)
	require.Empty(t, got)
	require.NoError(t, s.Upsert(t.Context(), "", []domain.Chunk{{ID: "first", Text: "original", Embedding: []float32{1}}}))
	require.Error(t, s.Upsert(t.Context(), "", []domain.Chunk{{ID: "first", Text: "replacement", Embedding: []float32{1}, Metadata: map[string]any{"kind": nil}}}))
	got, err = s.QueryFiltered(t.Context(), []float32{1}, 1, domain.VectorFilter{})
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, "original", got[0].Chunk.Text)
}

func TestOfflineStoreOwnsMetadataLists(t *testing.T) {
	for name, value := range map[string]any{"strings": []string{"original"}, "decoded strings": []any{"original"}} {
		t.Run(name, func(t *testing.T) {
			s := NewFakeVectorStore()
			require.NoError(t, s.Upsert(t.Context(), "", []domain.Chunk{{ID: "list", Embedding: []float32{1}, Metadata: map[string]any{"kind": value}}}))
			mutate := func(v any) {
				switch list := v.(type) {
				case []string:
					list[0] = "changed"
				case []any:
					list[0] = "changed"
				}
			}
			mutate(value)
			for range 2 {
				got, err := s.QueryFiltered(t.Context(), []float32{1}, 1, domain.VectorFilter{})
				require.NoError(t, err)
				require.Len(t, got, 1)
				switch list := got[0].Chunk.Metadata["kind"].(type) {
				case []string:
					require.Equal(t, "original", list[0])
				case []any:
					require.Equal(t, "original", list[0])
				default:
					t.Fatalf("unexpected metadata type %T", list)
				}
				mutate(got[0].Chunk.Metadata["kind"])
			}
		})
	}
}
