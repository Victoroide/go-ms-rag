package pipeline

import (
	"context"
	"testing"

	"github.com/Victoroide/quarry-rag/internal/domain"
	"github.com/Victoroide/quarry-rag/internal/retriever"
	"github.com/Victoroide/quarry-rag/internal/store/memory"
	"github.com/stretchr/testify/require"
)

type scopeCountingEmbedder struct{ calls int }

func (e *scopeCountingEmbedder) Embed(context.Context, string) ([]float32, error) {
	e.calls++
	return []float32{1, 0}, nil
}

type scopeCountingStore struct {
	domain.VectorStore
	calls int
}

func (s *scopeCountingStore) QueryAllNamespaces(context.Context, []float32, int, string) ([]domain.ScoredChunk, error) {
	s.calls++
	return nil, nil
}

func TestRoutingIgnoresDocumentsOutsideAllowlist(t *testing.T) {
	vs := memory.NewFakeVectorStore()
	require.NoError(t, vs.Upsert(t.Context(), "", []domain.Chunk{
		{ID: "private", DocumentID: "private", Embedding: []float32{1, 0}},
		{ID: "allowed", DocumentID: "allowed", Embedding: []float32{0, 1}},
	}))
	qc := &QueryContext{OriginalQuery: "archive", SubQueries: []string{"archive"}, AllowedDocumentIDs: []string{"allowed"}}
	ret := retriever.NewRetriever(vs, &scopeCountingEmbedder{}, 0.7, 10)
	require.NoError(t, NewRoutingStage(ret).Run(t.Context(), qc))
	require.Equal(t, "GLOBAL", qc.RetrievalMode)
	require.Zero(t, qc.TopScore)
}

func TestEmptyAllowlistSkipsEmbeddingAndSearch(t *testing.T) {
	for _, mode := range []string{"", "local", "global"} {
		t.Run(mode, func(t *testing.T) {
			vs, emb := &scopeCountingStore{}, &scopeCountingEmbedder{}
			ret := retriever.NewRetriever(vs, emb, 0.7, 10)
			qc := &QueryContext{OriginalQuery: "archive", RequestedMode: mode, AllowedDocumentIDs: []string{}}
			_, err := RunRetrievalPipeline(t.Context(), qc, []QueryStage{NewRoutingStage(ret), NewRetrievalStage(ret), NewSynthesisStage(nil, nil)})
			require.NoError(t, err)
			require.Zero(t, emb.calls)
			require.Zero(t, vs.calls)
			require.Equal(t, domain.TokenUsage{}, qc.TokenUsage)
			require.True(t, qc.EarlyExit)
		})
	}
}

func TestGlobalRetrievalIssuesOneQuery(t *testing.T) {
	vs := &scopeCountingStore{}
	ret := retriever.NewRetriever(vs, &scopeCountingEmbedder{}, 0.7, 10)
	qc := &QueryContext{OriginalQuery: "archive", RetrievalMode: "GLOBAL", QueryEmbedding: []float32{1, 0}}
	require.NoError(t, NewRetrievalStage(ret).Run(t.Context(), qc))
	require.Equal(t, 1, vs.calls)
}

type scopePreviews struct {
	requested []string
	values    map[string]string
}

func (p *scopePreviews) DocumentPreviews(_ context.Context, ids []string) (map[string]string, error) {
	p.requested = ids
	return p.values, nil
}

type scopeVisibility struct {
	visible map[string]struct{}
	err     error
}

func (v scopeVisibility) VisibleDocumentIDs(context.Context, []string) (map[string]struct{}, error) {
	return v.visible, v.err
}

func TestEntityRoutingUsesOnlyAuthorizedCandidates(t *testing.T) {
	vs := memory.NewFakeVectorStore()
	require.NoError(t, vs.Upsert(t.Context(), "", []domain.Chunk{
		{ID: "private", DocumentID: "private", Embedding: []float32{1, 0}},
		{ID: "invisible", DocumentID: "invisible", Embedding: []float32{1, 0}},
		{ID: "allowed", DocumentID: "allowed", Embedding: []float32{0, 1}},
	}))
	ret := retriever.NewRetriever(vs, &scopeCountingEmbedder{}, 0.7, 10)
	previews := &scopePreviews{values: map[string]string{"allowed": "Cedar Archive stores records."}}
	stage := NewRoutingStageWithScope(ret, scopeVisibility{visible: map[string]struct{}{"allowed": {}}}, previews)
	qc := &QueryContext{OriginalQuery: "Falcon Station", SubQueries: []string{"Falcon Station"}, AllowedDocumentIDs: []string{"allowed", "invisible"}, NamedEntity: "falcon station"}
	require.NoError(t, stage.Run(t.Context(), qc))
	require.Equal(t, "GLOBAL", qc.RetrievalMode)
	require.Empty(t, qc.NamedEntity)
	require.Empty(t, qc.EntityDocumentID)
	require.Equal(t, []string{"allowed"}, previews.requested)
	require.Len(t, qc.Candidates, 1)
	require.Zero(t, qc.TopScore)
	qc.OriginalQuery = "Cedar Archive"
	require.NoError(t, stage.Run(t.Context(), qc))
	require.Equal(t, "LOCAL", qc.RetrievalMode)
	require.Equal(t, "cedar archive", qc.NamedEntity)
	require.Equal(t, "allowed", qc.EntityDocumentID)
}

func TestEntityMatchingUsesWholeWords(t *testing.T) {
	entity, id := MatchNamedEntity("start here", map[string]string{"doc": "Art Center stores records."}, []string{"doc"})
	require.Empty(t, entity)
	require.Empty(t, id)
	entity, id = MatchNamedEntity("ART, center?", map[string]string{"doc": "Art Center stores records."}, []string{"doc"})
	require.Equal(t, "art center", entity)
	require.Equal(t, "doc", id)
	entity, id = MatchNamedEntity("ÉRABLE central", map[string]string{"doc": "Érable Central stores records."}, []string{"doc"})
	require.Equal(t, "érable central", entity)
	require.Equal(t, "doc", id)
}

func TestEntitySelectionIsDeterministic(t *testing.T) {
	previews := map[string]string{"first": "Cedar Archive stores records.", "second": "Cedar Archive stores records.", "long": "Cedar Archive North stores records.", "word": "Archive Station stores records."}
	for range 100 {
		entity, id := MatchNamedEntity("Cedar Archive North", previews, []string{"word", "second", "first", "long"})
		require.Equal(t, "cedar archive north", entity)
		require.Equal(t, "long", id)
		entity, id = MatchNamedEntity("Cedar Archive", previews, []string{"word", "second", "first", "long"})
		require.Equal(t, "cedar archive", entity)
		require.Equal(t, "second", id)
	}
}

func TestExplicitGlobalSkipsProbe(t *testing.T) {
	vs, emb := &scopeCountingStore{}, &scopeCountingEmbedder{}
	ret := retriever.NewRetriever(vs, emb, 0.7, 10)
	qc := &QueryContext{OriginalQuery: "archive", RequestedMode: "global"}
	require.NoError(t, NewRoutingStage(ret).Run(t.Context(), qc))
	require.Equal(t, 1, emb.calls)
	require.Zero(t, vs.calls)
	require.NoError(t, NewRetrievalStage(ret).Run(t.Context(), qc))
	require.Equal(t, 1, vs.calls)
}

func TestRoutingAppliesDocumentType(t *testing.T) {
	vs := memory.NewFakeVectorStore()
	require.NoError(t, vs.Upsert(t.Context(), "", []domain.Chunk{
		{ID: "other-type", DocumentID: "other", Embedding: []float32{1, 0}, Metadata: map[string]any{"doc_type": "note"}},
		{ID: "allowed", DocumentID: "allowed", Embedding: []float32{0, 1}, Metadata: map[string]any{"doc_type": "report"}},
	}))
	qc := &QueryContext{OriginalQuery: "archive", SubQueries: []string{"archive"}, FilterDocType: "report"}
	ret := retriever.NewRetriever(vs, &scopeCountingEmbedder{}, 0.7, 10)
	require.NoError(t, NewRoutingStage(ret).Run(t.Context(), qc))
	require.Equal(t, "GLOBAL", qc.RetrievalMode)
	require.Len(t, qc.Candidates, 1)
	require.Equal(t, domain.DocumentID("allowed"), qc.Candidates[0].Chunk.DocumentID)
}
