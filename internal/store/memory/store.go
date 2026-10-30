// Package memory provides an in-memory domain.VectorStore implementation for
// GO_RAG_FAKE_MODE (offline QA). It does cosine-similarity search over stored
// vectors with namespace scoping and doc_type metadata filtering that mirrors
// the Pinecone driver, so the full ingest/query/delete HTTP API can be
// exercised locally without Pinecone. Not for production use.
package memory

import (
	"context"
	"log"
	"maps"
	"math"
	"sort"
	"sync"

	"github.com/Victoroide/quarry-rag/internal/domain"
)

// FakeVectorStore is a thread-safe in-memory vector store. It implements
// domain.VectorStore (Upsert, Query, QueryAllNamespaces, Delete, Ping) plus
// the domain.VectorDeleter methods (DeleteAllVectorsInNamespace, DeleteByDocID)
// used by the delete handler.
type FakeVectorStore struct {
	mu sync.RWMutex
	// namespace -> chunkID -> stored chunk (with embedding + metadata).
	ns map[string]map[string]domain.Chunk
}

// NewFakeVectorStore creates an empty in-memory store.
func NewFakeVectorStore() *FakeVectorStore {
	return &FakeVectorStore{ns: make(map[string]map[string]domain.Chunk)}
}

// Upsert inserts or replaces chunk vectors in the namespace.
func (s *FakeVectorStore) Upsert(_ context.Context, namespace string, chunks []domain.Chunk) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.ns[namespace]
	if !ok {
		m = make(map[string]domain.Chunk)
		s.ns[namespace] = m
	}
	stored := 0
	for _, c := range chunks {
		if len(c.Embedding) == 0 {
			continue
		}
		emb := make([]float32, len(c.Embedding))
		copy(emb, c.Embedding)
		cp := c
		cp.Embedding = emb
		cp.Metadata = maps.Clone(c.Metadata)
		m[string(c.ID)] = cp
		stored++
	}
	log.Printf("fake_vectorstore: upserted %d vectors to namespace %q (total ns=%d)", stored, namespace, len(s.ns))
	return nil
}

// Query searches a single namespace by cosine similarity, honoring docTypeFilter.
func (s *FakeVectorStore) Query(_ context.Context, namespace string, embedding []float32, topK int, docTypeFilter string) ([]domain.ScoredChunk, error) {
	return s.query(namespace, embedding, topK, domain.VectorFilter{DocType: docTypeFilter}), nil
}

// QueryAllNamespaces searches the default ("") namespace, mirroring the
// Pinecone driver's dual-write semantics where every vector also lives in "".
func (s *FakeVectorStore) QueryAllNamespaces(_ context.Context, embedding []float32, topK int, docTypeFilter string) ([]domain.ScoredChunk, error) {
	return s.query("", embedding, topK, domain.VectorFilter{DocType: docTypeFilter}), nil
}

// QueryFiltered applies document scope before selecting the highest scores.
func (s *FakeVectorStore) QueryFiltered(_ context.Context, embedding []float32, topK int, filter domain.VectorFilter) ([]domain.ScoredChunk, error) {
	return s.query("", embedding, topK, filter), nil
}

func (s *FakeVectorStore) query(namespace string, embedding []float32, topK int, filter domain.VectorFilter) []domain.ScoredChunk {
	if filter.MatchesNothing() {
		return []domain.ScoredChunk{}
	}
	allowed := make(map[string]struct{}, len(filter.DocumentIDs))
	for _, id := range filter.DocumentIDs {
		allowed[id] = struct{}{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	m, ok := s.ns[namespace]
	if !ok {
		return []domain.ScoredChunk{}
	}
	scored := make([]domain.ScoredChunk, 0, len(m))
	for _, c := range m {
		if filter.DocumentIDs != nil {
			if _, ok := allowed[string(c.DocumentID)]; !ok {
				continue
			}
		}
		if filter.DocType != "" {
			if dt, _ := c.Metadata["doc_type"].(string); dt != filter.DocType {
				continue
			}
		}
		out := domain.Chunk{
			ID:         c.ID,
			DocumentID: c.DocumentID,
			Index:      c.Index,
			Text:       c.Text,
			TokenCount: c.TokenCount,
		}
		out.Metadata = maps.Clone(c.Metadata)
		scored = append(scored, domain.ScoredChunk{
			Chunk:     out,
			Score:     cosine(embedding, c.Embedding),
			Namespace: namespace,
		})
	}
	sort.Slice(scored, func(i, j int) bool {
		if scored[i].Score == scored[j].Score {
			return scored[i].Chunk.ID < scored[j].Chunk.ID
		}
		return scored[i].Score > scored[j].Score
	})
	if topK > 0 && len(scored) > topK {
		scored = scored[:topK]
	}
	return scored
}

// Delete removes vectors by ID from the namespace.
func (s *FakeVectorStore) Delete(_ context.Context, namespace string, ids []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if m, ok := s.ns[namespace]; ok {
		for _, id := range ids {
			delete(m, id)
		}
	}
	return nil
}

// DeleteAllVectorsInNamespace drops an entire namespace (per-doc UUID namespace).
func (s *FakeVectorStore) DeleteAllVectorsInNamespace(_ context.Context, namespace string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.ns, namespace)
	return nil
}

// DeleteByDocID removes, from every namespace, all chunks whose DocumentID
// matches docID (mirrors the Pinecone metadata-filter delete on the default ns).
func (s *FakeVectorStore) DeleteByDocID(_ context.Context, docID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, m := range s.ns {
		for id, c := range m {
			if string(c.DocumentID) == docID {
				delete(m, id)
			}
		}
	}
	return nil
}

// Ping always succeeds for the in-memory store.
func (s *FakeVectorStore) Ping(_ context.Context) error { return nil }

func cosine(a, b []float32) float64 {
	if len(a) == 0 || len(b) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}
