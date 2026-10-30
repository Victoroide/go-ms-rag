package pinecone

import (
	"context"
	"fmt"
	"math"
	"sort"

	"github.com/Victoroide/quarry-rag/internal/domain"
	"github.com/pinecone-io/go-pinecone/pinecone"
	"google.golang.org/protobuf/types/known/structpb"
)

const maxInValues = 10000

func buildFilter(docType string, ids []string) *structpb.Struct {
	fields := make(map[string]*structpb.Value)
	if docType != "" {
		fields["doc_type"] = structpb.NewStructValue(&structpb.Struct{Fields: map[string]*structpb.Value{"$eq": structpb.NewStringValue(docType)}})
	}
	if ids != nil {
		values := make([]*structpb.Value, len(ids))
		for i, id := range ids {
			values[i] = structpb.NewStringValue(id)
		}
		fields["doc_id"] = structpb.NewStructValue(&structpb.Struct{Fields: map[string]*structpb.Value{"$in": structpb.NewListValue(&structpb.ListValue{Values: values})}})
	}
	if len(fields) == 0 {
		return nil
	}
	return &structpb.Struct{Fields: fields}
}

// QueryFiltered searches the default namespace with document scope predicates.
func (p *Client) QueryFiltered(ctx context.Context, embedding []float32, topK int, filter domain.VectorFilter) ([]domain.ScoredChunk, error) {
	if filter.MatchesNothing() {
		return []domain.ScoredChunk{}, nil
	}
	if topK < 1 || uint64(topK) > math.MaxUint32 {
		return nil, fmt.Errorf("pinecone: topK must be between 1 and %d", uint64(math.MaxUint32))
	}
	merged := make(map[domain.ChunkID]domain.ScoredChunk)
	for start := 0; start == 0 || start < len(filter.DocumentIDs); start += maxInValues {
		ids := filter.DocumentIDs[start:min(start+maxInValues, len(filter.DocumentIDs))]
		req := &pinecone.QueryByVectorValuesRequest{Vector: embedding, TopK: uint32(topK), IncludeMetadata: true, MetadataFilter: buildFilter(filter.DocType, ids)}
		chunks, err := p.queryNamespace(ctx, "", req)
		if err != nil {
			return nil, err
		}
		for _, sc := range chunks {
			if prev, ok := merged[sc.Chunk.ID]; !ok || sc.Score > prev.Score {
				merged[sc.Chunk.ID] = sc
			}
		}
	}
	chunks := make([]domain.ScoredChunk, 0, len(merged))
	for _, sc := range merged {
		chunks = append(chunks, sc)
	}
	sort.Slice(chunks, func(i, j int) bool {
		if chunks[i].Score == chunks[j].Score {
			return chunks[i].Chunk.ID < chunks[j].Chunk.ID
		}
		return chunks[i].Score > chunks[j].Score
	})
	if len(chunks) > topK {
		chunks = chunks[:topK]
	}
	return chunks, nil
}
