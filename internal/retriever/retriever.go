// Package retriever implements LOCAL/GLOBAL adaptive retrieval
// using Pinecone vector search with fallback logic.
package retriever

import (
	"context"
	"fmt"
	"log"
	"sort"

	"github.com/Victoroide/go-ms-rag/internal/domain"
)

const (
	defaultTopK            = 10
	fallbackMinChunks      = 3
	defaultSimilarityLimit = 20
)

// RetrievalMode selects between local and global retrieval strategies.
type RetrievalMode string

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
func (r *Retriever) Retrieve(ctx context.Context, query string, mode RetrievalMode, namespace string, topK int, docTypeFilter string) (*Result, error) {
	if topK <= 0 {
		topK = r.topK
	}

	embedding, err := r.embedder.Embed(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("retriever: embed query: %w", err)
	}

	var chunks []domain.ScoredChunk

	switch mode {
	case RetrievalModeLocal:
		chunks, err = r.retrieveLocal(ctx, namespace, embedding, topK, docTypeFilter)
	case RetrievalModeGlobal:
		chunks, err = r.retrieveGlobal(ctx, embedding, topK, docTypeFilter)
	default:
		chunks, err = r.retrieveGlobal(ctx, embedding, topK, docTypeFilter)
	}
	if err != nil {
		return nil, err
	}

	// Fallback: if GLOBAL returns < 3 chunks, run unrestricted search.
	if mode == RetrievalModeGlobal && len(chunks) < fallbackMinChunks {
		log.Printf("retrieval: GLOBAL returned %d chunks (< %d), running fallback", len(chunks), fallbackMinChunks)
		fallbackChunks, fbErr := r.vectorStore.QueryAllNamespaces(ctx, embedding, defaultSimilarityLimit, docTypeFilter)
		if fbErr == nil && len(fallbackChunks) > 0 {
			existing := make(map[domain.ChunkID]bool)
			for _, c := range chunks {
				existing[c.Chunk.ID] = true
			}
			for _, fc := range fallbackChunks {
				if !existing[fc.Chunk.ID] {
					chunks = append(chunks, fc)
				}
			}
		}
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
func (r *Retriever) DetermineModeWithQuery(query string, subQueryCount int, topScore float64, namedEntity string) RetrievalMode {
	// Multiple sub-queries means the decomposition LLM identified
	// distinct information needs — retrieve broadly.  This is the
	// highest-priority signal because the LLM is authoritative for
	// multi-entity detection.
	if subQueryCount > 1 {
		return RetrievalModeGlobal
	}

	// Named entity from DB on a single sub-query: the query targets
	// a specific person/company — retrieve locally regardless of
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

// EmbedQuery generates an embedding for a query string.
func (r *Retriever) EmbedQuery(ctx context.Context, query string) (embedding []float32, topScore float64, err error) {
	embedding, err = r.embedder.Embed(ctx, query)
	if err != nil {
		return nil, 0, fmt.Errorf("retriever: embed query: %w", err)
	}

	// Get top similarity from global search to help with routing.
	scored, qErr := r.vectorStore.QueryAllNamespaces(ctx, embedding, 1, "")
	if qErr != nil {
		return embedding, 0, nil
	}
	if len(scored) > 0 {
		topScore = scored[0].Score
	}
	return embedding, topScore, nil
}

func (r *Retriever) retrieveLocal(ctx context.Context, namespace string, embedding []float32, topK int, docTypeFilter string) ([]domain.ScoredChunk, error) {
	if namespace == "" {
		// If no namespace, fall back to global.
		return r.retrieveGlobal(ctx, embedding, topK, docTypeFilter)
	}
	chunks, err := r.vectorStore.Query(ctx, namespace, embedding, topK, docTypeFilter)
	if err != nil {
		return nil, fmt.Errorf("retriever: local query namespace %q: %w", namespace, err)
	}
	return chunks, nil
}

func (r *Retriever) retrieveGlobal(ctx context.Context, embedding []float32, topK int, docTypeFilter string) ([]domain.ScoredChunk, error) {
	chunks, err := r.vectorStore.QueryAllNamespaces(ctx, embedding, topK, docTypeFilter)
	if err != nil {
		return nil, fmt.Errorf("retriever: global query: %w", err)
	}
	return chunks, nil
}
