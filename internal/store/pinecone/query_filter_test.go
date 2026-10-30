package pinecone

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/Victoroide/quarry-rag/internal/domain"
	"github.com/pinecone-io/go-pinecone/v5/pinecone"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestBuildFilterShapes(t *testing.T) {
	for _, tt := range []struct {
		name, docType string
		ids           []string
		want          map[string]any
	}{
		{name: "unrestricted"},
		{name: "type", docType: "report", want: map[string]any{"doc_type": map[string]any{"$eq": "report"}}},
		{name: "ids", ids: []string{"one", "two"}, want: map[string]any{"doc_id": map[string]any{"$in": []any{"one", "two"}}}},
		{name: "both", docType: "report", ids: []string{"one"}, want: map[string]any{"doc_type": map[string]any{"$eq": "report"}, "doc_id": map[string]any{"$in": []any{"one"}}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := buildFilter(tt.docType, tt.ids)
			if tt.want == nil {
				require.Nil(t, got)
			} else {
				require.Equal(t, tt.want, got.AsMap())
			}
		})
	}
}

func TestAllowlistAbove10000IsSplitAndMerged(t *testing.T) {
	ids := make([]string, 25000)
	for i := range ids {
		ids[i] = fmt.Sprint(i)
	}
	calls := 0
	conn := &fakeIndexConn{query: func(_ context.Context, req *pinecone.QueryByVectorValuesRequest) (*pinecone.QueryVectorsResponse, error) {
		require.Equal(t, uint32(2), req.TopK)
		require.True(t, req.IncludeMetadata)
		fields := req.MetadataFilter.GetFields()
		require.Equal(t, "report", fields["doc_type"].GetStructValue().GetFields()["$eq"].GetStringValue())
		group := fields["doc_id"].GetStructValue().GetFields()["$in"].GetListValue().GetValues()
		want := 10000
		if calls == 2 {
			want = 5000
		}
		require.Len(t, group, want)
		require.Equal(t, ids[calls*10000], group[0].GetStringValue())
		calls++
		md, err := structpb.NewStruct(map[string]any{"doc_id": "allowed", "text": "evidence"})
		require.NoError(t, err)
		return &pinecone.QueryVectorsResponse{Matches: []*pinecone.ScoredVector{
			{Vector: &pinecone.Vector{Id: "duplicate", Metadata: md}, Score: float32(calls) / 4},
			{Vector: &pinecone.Vector{Id: fmt.Sprint(calls), Metadata: md}, Score: float32(calls) / 10},
		}}, nil
	}}
	client := testClient(conn)
	client.dial = func(string) (indexConn, error) { t.Fatal("non-default namespace queried"); return nil, nil }
	got, err := NewDriver(client).QueryFiltered(t.Context(), []float32{1}, 2, domain.VectorFilter{DocType: "report", DocumentIDs: ids})
	require.NoError(t, err)
	require.Equal(t, 3, calls)
	require.Len(t, got, 2)
	require.Equal(t, domain.ChunkID("duplicate"), got[0].Chunk.ID)
	require.Equal(t, 0.75, got[0].Score)
	require.Equal(t, domain.ChunkID("3"), got[1].Chunk.ID)
}

func TestFilteredQueryEmptyAndErrors(t *testing.T) {
	calls := 0
	client := testClient(&fakeIndexConn{query: func(context.Context, *pinecone.QueryByVectorValuesRequest) (*pinecone.QueryVectorsResponse, error) {
		calls++
		return nil, errors.New("unavailable")
	}})
	got, err := client.QueryFiltered(t.Context(), nil, 10, domain.VectorFilter{DocumentIDs: []string{}})
	require.NoError(t, err)
	require.Empty(t, got)
	require.Zero(t, calls)
	_, err = client.QueryFiltered(t.Context(), []float32{1}, 10, domain.VectorFilter{})
	require.ErrorContains(t, err, "unavailable")
	require.Equal(t, 1, calls)
}
