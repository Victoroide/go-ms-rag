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

type visibilityLookup func(context.Context, []string) (map[string]struct{}, error)

func (f visibilityLookup) VisibleDocumentIDs(ctx context.Context, ids []string) (map[string]struct{}, error) {
	return f(ctx, ids)
}

func visibilityRetriever(t *testing.T) *retriever.Retriever {
	t.Helper()
	vs := memory.NewFakeVectorStore()
	require.NoError(t, vs.Upsert(t.Context(), "", []domain.Chunk{
		{ID: "failed", DocumentID: "failed", Text: "failed text", Embedding: []float32{1, 0}},
		{ID: "missing", DocumentID: "missing", Text: "orphan text", Embedding: []float32{1, 1}},
		{ID: "completed", DocumentID: "completed", Text: "completed text", Embedding: []float32{0, 1}},
	}))
	return retriever.NewRetriever(vs, nil, 0.7, 10)
}

func TestRetrievalDropsChunksOfUnavailableDocuments(t *testing.T) {
	for _, tt := range []struct {
		name    string
		allowed []string
		visible map[string]struct{}
		wantIDs []string
	}{
		{"completed only", nil, map[string]struct{}{"completed": {}}, []string{"completed"}},
		{"none visible", nil, map[string]struct{}{}, []string{}},
		{"allowlist still applies", []string{"failed"}, map[string]struct{}{"completed": {}}, []string{}},
		{"score order", nil, map[string]struct{}{"missing": {}, "completed": {}}, []string{"missing", "completed"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			qc := &QueryContext{QueryEmbedding: []float32{1, 0}, RetrievalMode: "GLOBAL", AllowedDocumentIDs: tt.allowed}
			vis := visibilityLookup(func(context.Context, []string) (map[string]struct{}, error) {
				return tt.visible, nil
			})
			require.NoError(t, NewRetrievalStageWithVisibility(visibilityRetriever(t), vis).Run(t.Context(), qc))
			require.Equal(t, tt.wantIDs, docIDs(qc.RetrievedChunks))
			if len(qc.RetrievedChunks) == 0 {
				require.Zero(t, qc.TopScore)
			} else {
				require.Equal(t, qc.RetrievedChunks[0].Score, qc.TopScore)
				require.Less(t, qc.TopScore, 1.0)
			}
		})
	}
}

func TestVisibilityLookupFailureFailsClosed(t *testing.T) {
	lookupErr := errors.New("database unavailable")
	vis := visibilityLookup(func(context.Context, []string) (map[string]struct{}, error) {
		return nil, lookupErr
	})
	qc := &QueryContext{QueryEmbedding: []float32{1, 0}, RetrievalMode: "GLOBAL"}
	err := NewRetrievalStageWithVisibility(visibilityRetriever(t), vis).Run(t.Context(), qc)
	require.ErrorIs(t, err, lookupErr)
	require.ErrorContains(t, err, "retrieval_stage: visibility:")
	require.Empty(t, qc.RetrievedChunks)
	require.Zero(t, qc.TopScore)
}

func TestNilVisibilityKeepsChunks(t *testing.T) {
	ret := visibilityRetriever(t)
	for _, stage := range []*RetrievalStage{NewRetrievalStage(ret), NewRetrievalStageWithVisibility(ret, nil)} {
		qc := &QueryContext{QueryEmbedding: []float32{1, 0}, RetrievalMode: "GLOBAL"}
		require.NoError(t, stage.Run(t.Context(), qc))
		require.Equal(t, []string{"failed", "missing", "completed"}, docIDs(qc.RetrievedChunks))
		require.Equal(t, 1.0, qc.TopScore)
	}
}
