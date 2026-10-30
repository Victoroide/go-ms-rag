package pipeline

import (
	"context"
	"errors"
	"testing"

	"github.com/Victoroide/quarry-rag/internal/domain"
	"github.com/Victoroide/quarry-rag/internal/retriever"
	"github.com/Victoroide/quarry-rag/internal/store/memory"
	"github.com/stretchr/testify/require"
)

type localRecordingStore struct {
	*memory.FakeVectorStore
	filters  []domain.VectorFilter
	probeErr error
}

func (s *localRecordingStore) QueryFiltered(ctx context.Context, embedding []float32, topK int, filter domain.VectorFilter) ([]domain.ScoredChunk, error) {
	s.filters = append(s.filters, filter)
	if len(s.filters) == 1 && s.probeErr != nil {
		return nil, s.probeErr
	}
	return s.FakeVectorStore.QueryFiltered(ctx, embedding, topK, filter)
}

func TestLocalRetrievalUsesRoutedDocument(t *testing.T) {
	for _, tt := range []struct {
		name, query, want string
		visible           map[string]struct{}
	}{
		{name: "named entity", query: "Cedar Archive", want: "cedar"},
		{name: "best match", query: "opening hours", want: "other"},
		{name: "best visible match", query: "opening hours", want: "cedar", visible: map[string]struct{}{"cedar": {}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			store := &localRecordingStore{FakeVectorStore: memory.NewFakeVectorStore()}
			require.NoError(t, store.Upsert(t.Context(), "", []domain.Chunk{
				{ID: "cedar-1", DocumentID: "cedar", Embedding: []float32{.8, .6}},
				{ID: "cedar-2", DocumentID: "cedar", Embedding: []float32{.6, .8}},
				{ID: "cedar-3", DocumentID: "cedar", Embedding: []float32{0, 1}},
				{ID: "other-1", DocumentID: "other", Embedding: []float32{1, 0}},
				{ID: "other-2", DocumentID: "other", Embedding: []float32{.9, .1}},
				{ID: "other-3", DocumentID: "other", Embedding: []float32{.9, .2}},
			}))
			emb := &scopeCountingEmbedder{}
			ret := retriever.NewRetriever(store, emb, .7, 10)
			var visibility domain.DocumentVisibility
			if tt.visible != nil {
				visibility = scopeVisibility{visible: tt.visible}
			}
			previews := &scopePreviews{values: map[string]string{"cedar": "Cedar Archive stores records."}}
			qc := &QueryContext{OriginalQuery: tt.query, SubQueries: []string{tt.query}}
			require.NoError(t, NewRoutingStageWithScope(ret, visibility, previews).Run(t.Context(), qc))
			require.NoError(t, NewRetrievalStageWithVisibility(ret, visibility).Run(t.Context(), qc))
			require.Equal(t, "LOCAL", qc.RetrievalMode)
			require.Len(t, qc.RetrievedChunks, 3)
			for _, sc := range qc.RetrievedChunks {
				require.Equal(t, domain.DocumentID(tt.want), sc.Chunk.DocumentID)
			}
			require.Len(t, store.filters, 2)
			require.Equal(t, []string{tt.want}, store.filters[1].DocumentIDs)
			require.Equal(t, 1, emb.calls)
		})
	}
}

func TestLocalTopsUpFromGlobal(t *testing.T) {
	store := &localRecordingStore{FakeVectorStore: memory.NewFakeVectorStore()}
	require.NoError(t, store.Upsert(t.Context(), "", []domain.Chunk{
		{ID: "cedar-1", DocumentID: "cedar", Embedding: []float32{.8, .6}, Metadata: map[string]any{"doc_type": "report"}},
		{ID: "other-1", DocumentID: "other", Embedding: []float32{1, 0}, Metadata: map[string]any{"doc_type": "report"}},
		{ID: "other-2", DocumentID: "other", Embedding: []float32{.9, .1}, Metadata: map[string]any{"doc_type": "report"}},
		{ID: "private", DocumentID: "private", Embedding: []float32{1, 0}, Metadata: map[string]any{"doc_type": "report"}},
		{ID: "wrong-type", DocumentID: "cedar", Embedding: []float32{1, 0}, Metadata: map[string]any{"doc_type": "note"}},
	}))
	ret := retriever.NewRetriever(store, &scopeCountingEmbedder{}, .7, 10)
	previews := &scopePreviews{values: map[string]string{"cedar": "Cedar Archive stores records."}}
	qc := &QueryContext{OriginalQuery: "Cedar Archive", MaxChunks: 3, AllowedDocumentIDs: []string{"cedar", "other"}, FilterDocType: "report"}
	require.NoError(t, NewRoutingStageWithScope(ret, nil, previews).Run(t.Context(), qc))
	require.NoError(t, NewRetrievalStage(ret).Run(t.Context(), qc))
	require.Len(t, store.filters, 3)
	require.Equal(t, []string{"cedar"}, store.filters[1].DocumentIDs)
	require.Equal(t, store.filters[0], store.filters[2])
	require.Equal(t, "report", store.filters[1].DocType)
	require.Len(t, qc.RetrievedChunks, 3)
	require.Equal(t, domain.ChunkID("other-1"), qc.RetrievedChunks[0].Chunk.ID)
	require.Equal(t, domain.ChunkID("other-2"), qc.RetrievedChunks[1].Chunk.ID)
	require.Equal(t, domain.ChunkID("cedar-1"), qc.RetrievedChunks[2].Chunk.ID)
}

func TestExplicitLocalProbesForTarget(t *testing.T) {
	store := &localRecordingStore{FakeVectorStore: memory.NewFakeVectorStore()}
	require.NoError(t, store.Upsert(t.Context(), "", []domain.Chunk{{ID: "one", DocumentID: "cedar", Embedding: []float32{0, 1}}}))
	emb := &scopeCountingEmbedder{}
	ret := retriever.NewRetriever(store, emb, .7, 10)
	qc := &QueryContext{OriginalQuery: "opening hours", RequestedMode: "LoCaL", SubQueries: []string{"hours", "location"}}
	require.NoError(t, NewRoutingStage(ret).Run(t.Context(), qc))
	require.Len(t, store.filters, 1)
	require.Len(t, qc.Candidates, 1)
	require.Equal(t, "LOCAL", qc.RetrievalMode)
	require.Equal(t, 1, emb.calls)
}

func TestLocalWithoutCandidateUsesGlobal(t *testing.T) {
	for _, requested := range []string{"auto", "local"} {
		for _, reason := range []string{"empty", "invisible", "probe failure"} {
			t.Run(requested+"/"+reason, func(t *testing.T) {
				store := &localRecordingStore{FakeVectorStore: memory.NewFakeVectorStore()}
				if reason == "invisible" {
					require.NoError(t, store.Upsert(t.Context(), "", []domain.Chunk{{ID: "one", DocumentID: "hidden", Embedding: []float32{1, 0}}}))
				}
				if reason == "probe failure" {
					store.probeErr = errors.New("search unavailable")
				}
				ret := retriever.NewRetriever(store, &scopeCountingEmbedder{}, 0, 10)
				qc := &QueryContext{OriginalQuery: "opening hours", RequestedMode: requested}
				require.NoError(t, NewRoutingStageWithScope(ret, scopeVisibility{visible: map[string]struct{}{}}, nil).Run(t.Context(), qc))
				require.Equal(t, "GLOBAL", qc.RetrievalMode)
				require.NotEmpty(t, qc.QueryEmbedding)
			})
		}
	}
}
