// Package retriever implements LOCAL/GLOBAL adaptive retrieval
// using scoped vector search.
package retriever

import (
	"context"
	"fmt"
	"log"
	"slices"
	"sort"

	"github.com/Victoroide/quarry-rag/internal/domain"
)

const defaultTopK = 10

// RetrievalMode selects between local and global retrieval strategies.
type RetrievalMode string

// Retrieval modes control the search scope.
const (
	RetrievalModeLocal  RetrievalMode = "LOCAL"
	RetrievalModeGlobal RetrievalMode = "GLOBAL"
)

// Retriever implements pure-vector adaptive local/global retrieval.
type Retriever struct {
	vectorStore domain.VectorStore
	embedder    domain.EmbeddingProvider
	threshold   float64
	topK        int
}

// NewRetriever creates a Retriever with the given dependencies.
func NewRetriever(vs domain.VectorStore, emb domain.EmbeddingProvider, threshold float64, topK int) *Retriever {
	if topK <= 0 {
		topK = defaultTopK
	}
	return &Retriever{
		vectorStore: vs,
		embedder:    emb,
		threshold:   threshold,
		topK:        topK,
	}
}

// Result holds the retrieval output.
type Result struct {
	Chunks   []domain.ScoredChunk
	Mode     RetrievalMode
	TopScore float64
}

// Retrieve executes the retrieval pipeline based on the determined mode.
//
// If the caller already produced an embedding for the query (e.g. the
// routing stage), it can be passed via RetrieveWithEmbedding to avoid
// a redundant Bedrock embed call. This wrapper embeds on demand.
func (r *Retriever) Retrieve(ctx context.Context, query string, mode RetrievalMode, topK int, filter domain.VectorFilter) (*Result, error) {
	if filter.MatchesNothing() {
		return &Result{Mode: mode}, nil
	}
	embedding, err := r.embedder.Embed(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("retriever: embed query: %w", err)
	}
	return r.RetrieveWithEmbedding(ctx, embedding, mode, "", topK, filter)
}

// RetrieveWithEmbedding runs the retrieval pipeline against a caller-
// supplied query embedding. Used by the query stage chain to avoid
// embedding the same text twice (once in routing, once here).
// LOCAL searches localDocID first and tops up sparse results from filter.
func (r *Retriever) RetrieveWithEmbedding(ctx context.Context, embedding []float32, mode RetrievalMode, localDocID string, topK int, filter domain.VectorFilter) (*Result, error) {
	if topK <= 0 {
		topK = r.topK
	}
	if filter.MatchesNothing() {
		return &Result{Mode: mode}, nil
	}
	queryFilter := filter
	if mode == RetrievalModeLocal {
		if localDocID == "" || (filter.DocumentIDs != nil && !slices.Contains(filter.DocumentIDs, localDocID)) {
			mode = RetrievalModeGlobal
		} else {
			queryFilter.DocumentIDs = []string{localDocID}
		}
	}
	chunks, err := r.queryFiltered(ctx, embedding, topK, queryFilter)
	if err != nil {
		return nil, fmt.Errorf("retriever: query: %w", err)
	}
	if mode == RetrievalModeLocal && len(chunks) < 3 {
		extra, err := r.queryFiltered(ctx, embedding, topK, filter)
		if err != nil {
			return nil, fmt.Errorf("retriever: top up: %w", err)
		}
		merged := make([]domain.ScoredChunk, 0, len(chunks)+len(extra))
		byID := make(map[domain.ChunkID]int, len(chunks)+len(extra))
		for _, batch := range [][]domain.ScoredChunk{chunks, extra} {
			for _, sc := range batch {
				if i, ok := byID[sc.Chunk.ID]; ok {
					if sc.Score > merged[i].Score {
						merged[i] = sc
					}
				} else {
					byID[sc.Chunk.ID] = len(merged)
					merged = append(merged, sc)
				}
			}
		}
		chunks = merged
	}

	// Sort by score descending.
	sort.Slice(chunks, func(i, j int) bool {
		return chunks[i].Score > chunks[j].Score
	})

	// Trim to topK.
	if len(chunks) > topK {
		chunks = chunks[:topK]
	}

	topScore := 0.0
	if len(chunks) > 0 {
		topScore = chunks[0].Score
	}

	log.Printf("retrieval: mode=%s chunks=%d similarity=%.4f", mode, len(chunks), topScore)

	return &Result{
		Chunks:   chunks,
		Mode:     mode,
		TopScore: topScore,
	}, nil
}

// DetermineMode decides LOCAL or GLOBAL mode based on sub-query count
// and embedding similarity score.
//
// LOCAL mode criteria:
//   - One or fewer sub-queries AND similarity >= threshold AND not a multi-entity query
//
// GLOBAL mode criteria:
//   - Two or more sub-queries OR similarity < threshold OR multi-entity query
func (r *Retriever) DetermineMode(subQueryCount int, topScore float64) RetrievalMode {
	if subQueryCount <= 1 && topScore >= r.threshold {
		return RetrievalModeLocal
	}
	return RetrievalModeGlobal
}

// DetermineModeWithQuery decides LOCAL or GLOBAL mode using structural signals only.
func (r *Retriever) DetermineModeWithQuery(subQueryCount int, topScore float64, namedEntity string) RetrievalMode {
	// Multiple information needs take priority over a single named entity.
	if subQueryCount > 1 {
		return RetrievalModeGlobal
	}

	// Named entity from DB on a single sub-query: the query targets
	// a specific entity — retrieve locally regardless of
	// similarity score.
	if namedEntity != "" {
		return RetrievalModeLocal
	}

	// Single sub-query, no named entity: use similarity as the signal.
	if topScore >= r.threshold {
		return RetrievalModeLocal
	}

	return RetrievalModeGlobal
}

// EmbedQuery embeds the query and optionally probes the authorized scope.
func (r *Retriever) EmbedQuery(ctx context.Context, query string, filter domain.VectorFilter, probeK int) (embedding []float32, candidates []domain.ScoredChunk, probeFailed bool, err error) {
	if filter.MatchesNothing() {
		return nil, nil, false, nil
	}
	embedding, err = r.embedder.Embed(ctx, query)
	if err != nil {
		return nil, nil, false, fmt.Errorf("retriever: embed query: %w", err)
	}
	if probeK <= 0 {
		return embedding, nil, false, nil
	}
	candidates, err = r.queryFiltered(ctx, embedding, probeK, filter)
	if err != nil {
		log.Printf("retriever: routing probe failed; using GLOBAL retrieval: %v", err)
		return embedding, nil, true, nil
	}
	return embedding, candidates, false, nil
}

func (r *Retriever) queryFiltered(ctx context.Context, embedding []float32, topK int, filter domain.VectorFilter) ([]domain.ScoredChunk, error) {
	if filter.MatchesNothing() {
		return []domain.ScoredChunk{}, nil
	}
	if store, ok := r.vectorStore.(domain.FilteredVectorStore); ok {
		return store.QueryFiltered(ctx, embedding, topK, filter)
	}
	chunks, err := r.vectorStore.QueryAllNamespaces(ctx, embedding, topK, filter.DocType)
	if err != nil {
		return nil, err
	}
	allowed := make(map[string]struct{}, len(filter.DocumentIDs))
	for _, id := range filter.DocumentIDs {
		allowed[id] = struct{}{}
	}
	filtered := make([]domain.ScoredChunk, 0, len(chunks))
	for _, sc := range chunks {
		if filter.DocType != "" {
			if docType, _ := sc.Chunk.Metadata["doc_type"].(string); docType != filter.DocType {
				continue
			}
		}
		if filter.DocumentIDs != nil {
			if _, ok := allowed[string(sc.Chunk.DocumentID)]; !ok {
				continue
			}
		}
		filtered = append(filtered, sc)
	}
	sort.SliceStable(filtered, func(i, j int) bool { return filtered[i].Score > filtered[j].Score })
	if len(filtered) > topK {
		filtered = filtered[:topK]
	}
	return filtered, nil
}
