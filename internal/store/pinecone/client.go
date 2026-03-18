// Package pinecone provides the Pinecone vector store client.
package pinecone

import (
	"context"
	"fmt"
	"log"
	"sync"

	"github.com/Victoroide/go-ms-rag/internal/config"
	"github.com/Victoroide/go-ms-rag/internal/domain"
	"github.com/pinecone-io/go-pinecone/pinecone"
	"google.golang.org/protobuf/types/known/structpb"
)

// PineconeClient wraps the official Pinecone Go SDK.
// Namespace-scoped connections are cached and created on demand.
type PineconeClient struct {
	client    *pinecone.Client
	host      string
	cfg       *config.Config
	mu        sync.Mutex
	connCache map[string]*pinecone.IndexConnection
}

// NewPineconeClient creates a new Pinecone client and connects to the index.
func NewPineconeClient(ctx context.Context, cfg *config.Config) (*PineconeClient, error) {
	pc, err := pinecone.NewClient(pinecone.NewClientParams{
		ApiKey: cfg.PineconeAPIKey,
	})
	if err != nil {
		return nil, fmt.Errorf("pinecone: create client: %w", err)
	}

	idx, err := pc.DescribeIndex(ctx, cfg.PineconeIndexName)
	if err != nil {
		return nil, fmt.Errorf("pinecone: describe index %q: %w", cfg.PineconeIndexName, err)
	}

	log.Printf("pinecone: connected to index %q (host=%s, dimension=%d)", cfg.PineconeIndexName, idx.Host, idx.Dimension)
	return &PineconeClient{
		client:    pc,
		host:      idx.Host,
		cfg:       cfg,
		connCache: make(map[string]*pinecone.IndexConnection),
	}, nil
}

// conn returns a cached or new IndexConnection for the given namespace.
func (p *PineconeClient) conn(namespace string) (*pinecone.IndexConnection, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if c, ok := p.connCache[namespace]; ok {
		return c, nil
	}
	c, err := p.client.Index(pinecone.NewIndexConnParams{
		Host:      p.host,
		Namespace: namespace,
	})
	if err != nil {
		return nil, fmt.Errorf("pinecone: connect to namespace %q: %w", namespace, err)
	}
	p.connCache[namespace] = c
	return c, nil
}

// Upsert inserts or updates vectors in the specified namespace.
func (p *PineconeClient) Upsert(ctx context.Context, namespace string, chunks []domain.Chunk) error {
	if len(chunks) == 0 {
		return nil
	}

	vectors := make([]*pinecone.Vector, 0, len(chunks))
	for _, c := range chunks {
		if len(c.Embedding) == 0 {
			continue
		}
		// Build metadata with doc_type if available.
		mdMap := map[string]any{
			"doc_id":      string(c.DocumentID),
			"chunk_index": float64(c.Index),
			"text":        c.Text,
			"token_count": float64(c.TokenCount),
		}
		if c.Metadata != nil {
			if docType, ok := c.Metadata["doc_type"]; ok {
				mdMap["doc_type"] = docType
			}
		}
		md, err := structpb.NewStruct(mdMap)
		if err != nil {
			return fmt.Errorf("pinecone: build metadata for chunk %s: %w", c.ID, err)
		}
		vectors = append(vectors, &pinecone.Vector{
			Id:       string(c.ID),
			Values:   c.Embedding,
			Metadata: md,
		})
	}

	if len(vectors) == 0 {
		return nil
	}

	idxConn, err := p.conn(namespace)
	if err != nil {
		return err
	}
	count, err := idxConn.UpsertVectors(ctx, vectors)
	if err != nil {
		return fmt.Errorf("pinecone: upsert %d vectors to namespace %q: %w", len(vectors), namespace, err)
	}
	log.Printf("pinecone: upserted %d vectors to namespace %q", count, namespace)
	return nil
}

// Query searches for similar vectors in the specified namespace.
func (p *PineconeClient) Query(ctx context.Context, namespace string, embedding []float32, topK int) ([]domain.ScoredChunk, error) {
	idxConn, err := p.conn(namespace)
	if err != nil {
		return nil, err
	}
	topKU32 := uint32(topK)
	res, err := idxConn.QueryByVectorValues(ctx, &pinecone.QueryByVectorValuesRequest{
		Vector:          embedding,
		TopK:            topKU32,
		IncludeMetadata: true,
	})
	if err != nil {
		return nil, fmt.Errorf("pinecone: query namespace %q: %w", namespace, err)
	}

	return matchesToScoredChunks(res.Matches, namespace), nil
}

// QueryAllNamespaces queries the default namespace (empty string) which
// contains all vectors when using the dual-write strategy.
func (p *PineconeClient) QueryAllNamespaces(ctx context.Context, embedding []float32, topK int) ([]domain.ScoredChunk, error) {
	idxConn, err := p.conn("")
	if err != nil {
		return nil, err
	}
	topKU32 := uint32(topK)
	res, err := idxConn.QueryByVectorValues(ctx, &pinecone.QueryByVectorValuesRequest{
		Vector:          embedding,
		TopK:            topKU32,
		IncludeMetadata: true,
	})
	if err != nil {
		return nil, fmt.Errorf("pinecone: query all namespaces: %w", err)
	}

	return matchesToScoredChunks(res.Matches, ""), nil
}

// Delete removes vectors by ID from the specified namespace.
func (p *PineconeClient) Delete(ctx context.Context, namespace string, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	idxConn, err := p.conn(namespace)
	if err != nil {
		return err
	}
	err = idxConn.DeleteVectorsById(ctx, ids)
	if err != nil {
		return fmt.Errorf("pinecone: delete %d vectors from namespace %q: %w", len(ids), namespace, err)
	}
	return nil
}

// DeleteAllVectorsInNamespace deletes all vectors from the specified namespace.
func (p *PineconeClient) DeleteAllVectorsInNamespace(ctx context.Context, namespace string) error {
	idxConn, err := p.conn(namespace)
	if err != nil {
		return err
	}

	// First, query to get all vector IDs in the namespace
	res, err := idxConn.QueryByVectorValues(ctx, &pinecone.QueryByVectorValuesRequest{
		TopK:            10000, // Fetch up to 10k vectors
		IncludeValues:   false,
		IncludeMetadata: false,
	})
	if err != nil {
		return fmt.Errorf("pinecone: query vectors in namespace %q: %w", namespace, err)
	}

	// If no vectors found, we're done
	if len(res.Matches) == 0 {
		return nil
	}

	// Collect all vector IDs
	var ids []string
	for _, match := range res.Matches {
		ids = append(ids, match.Vector.Id)
	}

	// Delete all vectors by ID
	err = idxConn.DeleteVectorsById(ctx, ids)
	if err != nil {
		return fmt.Errorf("pinecone: delete %d vectors from namespace %q: %w", len(ids), namespace, err)
	}

	return nil
}

// Ping verifies connectivity to the Pinecone index.
func (p *PineconeClient) Ping(ctx context.Context) error {
	_, err := p.client.DescribeIndex(ctx, p.cfg.PineconeIndexName)
	if err != nil {
		return fmt.Errorf("pinecone: ping: %w", err)
	}
	return nil
}

func matchesToScoredChunks(matches []*pinecone.ScoredVector, namespace string) []domain.ScoredChunk {
	scored := make([]domain.ScoredChunk, 0, len(matches))
	for _, m := range matches {
		chunk := domain.Chunk{
			ID: domain.ChunkID(m.Vector.Id),
		}
		if m.Vector.Metadata != nil {
			fields := m.Vector.Metadata.GetFields()
			if v, ok := fields["doc_id"]; ok {
				chunk.DocumentID = domain.DocumentID(v.GetStringValue())
			}
			if v, ok := fields["chunk_index"]; ok {
				chunk.Index = int(v.GetNumberValue())
			}
			if v, ok := fields["text"]; ok {
				chunk.Text = v.GetStringValue()
			}
			if v, ok := fields["token_count"]; ok {
				chunk.TokenCount = int(v.GetNumberValue())
			}
		}
		scored = append(scored, domain.ScoredChunk{
			Chunk:     chunk,
			Score:     float64(m.Score),
			Namespace: namespace,
		})
	}
	return scored
}
