// Package pinecone provides the Pinecone vector store client.
package pinecone

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"sync"

	"github.com/Victoroide/go-ms-rag/internal/config"
	"github.com/Victoroide/go-ms-rag/internal/domain"
	"github.com/pinecone-io/go-pinecone/pinecone"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"
)

// Client wraps the official Pinecone Go SDK.
// Namespace-scoped connections are cached and created on demand.
type Client struct {
	client    *pinecone.Client
	host      string
	cfg       *config.Config
	mu        sync.Mutex
	connCache map[string]*pinecone.IndexConnection
}

// NewPineconeClient creates a new Pinecone client and connects to the index.
func NewPineconeClient(ctx context.Context, cfg *config.Config) (*Client, error) {
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
	return &Client{
		client:    pc,
		host:      idx.Host,
		cfg:       cfg,
		connCache: make(map[string]*pinecone.IndexConnection),
	}, nil
}

// conn returns a cached or new IndexConnection for the given namespace.
func (p *Client) conn(namespace string) (*pinecone.IndexConnection, error) {
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
func (p *Client) Upsert(ctx context.Context, namespace string, chunks []domain.Chunk) error {
	if len(chunks) == 0 {
		return nil
	}

	vectors := make([]*pinecone.Vector, 0, len(chunks))
	for _, c := range chunks {
		if len(c.Embedding) == 0 {
			continue
		}
		// Build metadata with doc_type if available. We also carry
		// the chunk quality signals (kind, quality) produced by the
		// chunker classifier so retrieval can filter or down-rank
		// header / contact-only chunks without losing them.
		mdMap := map[string]any{
			"doc_id":      string(c.DocumentID),
			"chunk_index": float64(c.Index),
			"text":        c.Text,
			"token_count": float64(c.TokenCount),
		}
		if c.Metadata != nil {
			for _, k := range []string{"doc_type", "kind", "quality"} {
				if v, ok := c.Metadata[k]; ok {
					mdMap[k] = v
				}
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
func (p *Client) Query(ctx context.Context, namespace string, embedding []float32, topK int, docTypeFilter string) ([]domain.ScoredChunk, error) {
	idxConn, err := p.conn(namespace)
	if err != nil {
		return nil, err
	}
	if topK < 1 || uint64(topK) > math.MaxUint32 {
		return nil, fmt.Errorf("pinecone: topK must be between 1 and %d", uint64(math.MaxUint32))
	}
	topKU32 := uint32(topK)
	req := &pinecone.QueryByVectorValuesRequest{
		Vector:          embedding,
		TopK:            topKU32,
		IncludeMetadata: true,
	}
	if docTypeFilter != "" {
		req.MetadataFilter = &structpb.Struct{
			Fields: map[string]*structpb.Value{
				"doc_type": structpb.NewStringValue(docTypeFilter),
			},
		}
	}
	res, err := idxConn.QueryByVectorValues(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("pinecone: query namespace %q: %w", namespace, err)
	}

	return matchesToScoredChunks(res.Matches, namespace), nil
}

// QueryAllNamespaces queries the default namespace (empty string) which
// contains all vectors when using the dual-write strategy.
func (p *Client) QueryAllNamespaces(ctx context.Context, embedding []float32, topK int, docTypeFilter string) ([]domain.ScoredChunk, error) {
	idxConn, err := p.conn("")
	if err != nil {
		return nil, err
	}
	if topK < 1 || uint64(topK) > math.MaxUint32 {
		return nil, fmt.Errorf("pinecone: topK must be between 1 and %d", uint64(math.MaxUint32))
	}
	topKU32 := uint32(topK)
	req := &pinecone.QueryByVectorValuesRequest{
		Vector:          embedding,
		TopK:            topKU32,
		IncludeMetadata: true,
	}
	if docTypeFilter != "" {
		req.MetadataFilter = &structpb.Struct{
			Fields: map[string]*structpb.Value{
				"doc_type": structpb.NewStringValue(docTypeFilter),
			},
		}
	}
	res, err := idxConn.QueryByVectorValues(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("pinecone: query all namespaces: %w", err)
	}

	return matchesToScoredChunks(res.Matches, ""), nil
}

// Delete removes vectors by ID from the specified namespace.
func (p *Client) Delete(ctx context.Context, namespace string, ids []string) error {
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

// DeleteAllVectorsInNamespace deletes all vectors from the specified namespace
// using the SDK's native delete-all call (no query required).
func (p *Client) DeleteAllVectorsInNamespace(ctx context.Context, namespace string) error {
	idxConn, err := p.conn(namespace)
	if err != nil {
		return err
	}

	if err := idxConn.DeleteAllVectorsInNamespace(ctx); err != nil && status.Code(err) != codes.NotFound {
		return fmt.Errorf("pinecone: delete all vectors in namespace %q: %w", namespace, err)
	}
	log.Printf("pinecone: deleted all vectors in namespace %q", namespace)
	return nil
}

// DeleteByDocID deletes vectors from the default namespace whose metadata doc_id matches,
// using the SDK's native DeleteVectorsByFilter (no dummy query needed).
func (p *Client) DeleteByDocID(ctx context.Context, docID string) error {
	idxConn, err := p.conn("")
	if err != nil {
		return err
	}

	filter := &structpb.Struct{
		Fields: map[string]*structpb.Value{
			"doc_id": structpb.NewStringValue(docID),
		},
	}

	if err := idxConn.DeleteVectorsByFilter(ctx, filter); err != nil && status.Code(err) != codes.NotFound {
		return fmt.Errorf("pinecone: delete vectors by doc_id %s: %w", docID, err)
	}
	log.Printf("pinecone: deleted vectors from default namespace for doc_id=%s", docID)
	return nil
}

// DescribeIndexStats returns index-level statistics including per-namespace vector counts.
func (p *Client) DescribeIndexStats(ctx context.Context) (*pinecone.DescribeIndexStatsResponse, error) {
	idxConn, err := p.conn("")
	if err != nil {
		return nil, err
	}
	return idxConn.DescribeIndexStats(ctx)
}

// Ping verifies connectivity to the Pinecone index.
func (p *Client) Ping(ctx context.Context) error {
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
			chunk.Metadata = make(map[string]any)
			for _, key := range []string{"doc_type", "kind", "quality"} {
				if v, ok := fields[key]; ok {
					chunk.Metadata[key] = v.GetStringValue()
				}
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

// Close releases cached namespace connections.
func (p *Client) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	var errs []error
	for namespace, conn := range p.connCache {
		if err := conn.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close namespace %q: %w", namespace, err))
		}
		delete(p.connCache, namespace)
	}
	return errors.Join(errs...)
}
