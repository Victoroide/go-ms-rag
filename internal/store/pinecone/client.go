// Package pinecone provides the Pinecone vector store client.
package pinecone

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"sync"
	"time"

	"github.com/Victoroide/quarry-rag/internal/config"
	"github.com/Victoroide/quarry-rag/internal/domain"
	"github.com/pinecone-io/go-pinecone/v5/pinecone"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
)

const maxUpsertBytes = 1572864

// Client wraps the official Pinecone Go SDK.
// All namespaces share one connection.
type Client struct {
	client      *pinecone.Client
	cfg         *config.Config
	defaultConn indexConn
	dial        func(namespace string) (indexConn, error)
	batchSize   int
	timeout     time.Duration
	maxAttempts int
	dims        int
	closeOnce   sync.Once
	closeErr    error
}

// NewPineconeClient creates a new Pinecone client and connects to the index.
func NewPineconeClient(ctx context.Context, cfg *config.Config) (*Client, error) {
	pc, err := pinecone.NewClient(pinecone.NewClientParams{ApiKey: cfg.PineconeAPIKey})
	if err != nil {
		return nil, fmt.Errorf("pinecone: create client: %w", err)
	}
	startupCtx, cancel := context.WithTimeout(ctx, cfg.PineconeTimeout)
	defer cancel()
	idx, err := pc.DescribeIndex(startupCtx, cfg.PineconeIndexName)
	if err != nil {
		return nil, fmt.Errorf("pinecone: describe index %q: %w", cfg.PineconeIndexName, err)
	}
	if validationErr := validateIndex(idx, cfg); validationErr != nil {
		return nil, validationErr
	}
	defaultConn, err := pc.Index(pinecone.NewIndexConnParams{Host: idx.Host})
	if err != nil {
		return nil, fmt.Errorf("pinecone: connect to default namespace: %w", err)
	}
	p := newClient(pc, cfg, defaultConn)
	slog.InfoContext(ctx, "pinecone connected", "index", cfg.PineconeIndexName, "host", idx.Host, "dimensions", *idx.Dimension)
	return p, nil
}

func newClient(pc *pinecone.Client, cfg *config.Config, root *pinecone.IndexConnection) *Client {
	return &Client{
		client: pc, cfg: cfg, defaultConn: root, batchSize: cfg.PineconeUpsertBatchSize,
		timeout: cfg.PineconeTimeout, maxAttempts: cfg.PineconeMaxAttempts, dims: cfg.EmbeddingDimensions,
		dial: func(namespace string) (indexConn, error) {
			return root.WithNamespace(namespace), nil
		},
	}
}

func (p *Client) conn(namespace string) (indexConn, func(), error) {
	if namespace == "" {
		return p.defaultConn, func() {}, nil
	}
	conn, err := p.dial(namespace)
	if err != nil {
		return nil, nil, fmt.Errorf("pinecone: connect to namespace %q: %w", namespace, err)
	}
	// Derived connections share the default transport and must not be closed.
	return conn, func() {}, nil
}

// Upsert inserts or updates vectors in the specified namespace.
func (p *Client) Upsert(ctx context.Context, namespace string, chunks []domain.Chunk) error {
	vectors := make([]*pinecone.Vector, 0, len(chunks))
	sizes := make([]int, 0, len(chunks))
	for _, c := range chunks {
		if len(c.Embedding) == 0 {
			continue
		}
		mdMap := map[string]any{
			"doc_id": string(c.DocumentID), "chunk_index": float64(c.Index),
			"text": c.Text, "token_count": float64(c.TokenCount),
		}
		for _, key := range []string{"doc_type", "kind", "quality"} {
			if value, ok := c.Metadata[key]; ok {
				mdMap[key] = value
			}
		}
		encoded, err := json.Marshal(mdMap)
		if err != nil {
			return fmt.Errorf("pinecone: serialize metadata for chunk %s: %w", c.ID, err)
		}
		if len(encoded) > 40960 {
			return fmt.Errorf("%w: chunk %s (%d bytes)", ErrMetadataTooLarge, c.ID, len(encoded))
		}
		md, err := structpb.NewStruct(mdMap)
		if err != nil {
			return fmt.Errorf("pinecone: build metadata for chunk %s: %w", c.ID, err)
		}
		size := 4*len(c.Embedding) + len(c.ID) + proto.Size(md) + 32
		if size > maxUpsertBytes {
			return fmt.Errorf("pinecone: chunk %s exceeds the upsert byte limit", c.ID)
		}
		vectors = append(vectors, &pinecone.Vector{Id: string(c.ID), Values: &c.Embedding, Metadata: md})
		sizes = append(sizes, size)
	}
	if len(vectors) == 0 {
		return nil
	}
	conn, release, err := p.conn(namespace)
	if err != nil {
		return err
	}
	defer release()
	for start := 0; start < len(vectors); {
		end, size := start, 0
		for end < len(vectors) && end-start < p.batchSize && size+sizes[end] <= maxUpsertBytes {
			size += sizes[end]
			end++
		}
		if err := p.do(ctx, p.maxAttempts, func(attemptCtx context.Context) error {
			_, upsertErr := conn.UpsertVectors(attemptCtx, vectors[start:end])
			return upsertErr
		}); err != nil {
			return fmt.Errorf("pinecone: upsert %d vectors to namespace %q: %w", end-start, namespace, err)
		}
		start = end
	}
	slog.DebugContext(ctx, "pinecone vectors upserted", "vectors", len(vectors), "namespace", namespace)
	return nil
}

// Query searches for similar vectors in the specified namespace.
func (p *Client) Query(ctx context.Context, namespace string, embedding []float32, topK int, docTypeFilter string) ([]domain.ScoredChunk, error) {
	if topK < 1 || uint64(topK) > math.MaxUint32 {
		return nil, fmt.Errorf("pinecone: topK must be between 1 and %d", uint64(math.MaxUint32))
	}
	req := &pinecone.QueryByVectorValuesRequest{Vector: embedding, TopK: uint32(topK), IncludeMetadata: true}
	if docTypeFilter != "" {
		req.MetadataFilter = &structpb.Struct{Fields: map[string]*structpb.Value{"doc_type": structpb.NewStringValue(docTypeFilter)}}
	}
	return p.queryNamespace(ctx, namespace, req)
}

func (p *Client) queryNamespace(ctx context.Context, namespace string, req *pinecone.QueryByVectorValuesRequest) ([]domain.ScoredChunk, error) {
	conn, release, err := p.conn(namespace)
	if err != nil {
		return nil, err
	}
	defer release()
	var res *pinecone.QueryVectorsResponse
	if err := p.do(ctx, min(3, p.maxAttempts), func(attemptCtx context.Context) error {
		var queryErr error
		res, queryErr = conn.QueryByVectorValues(attemptCtx, req)
		return queryErr
	}); err != nil {
		return nil, fmt.Errorf("pinecone: query namespace %q: %w", namespace, err)
	}
	return matchesToScoredChunks(res.Matches, namespace), nil
}

// QueryAllNamespaces searches the default namespace containing corpus vectors.
func (p *Client) QueryAllNamespaces(ctx context.Context, embedding []float32, topK int, docTypeFilter string) ([]domain.ScoredChunk, error) {
	return p.Query(ctx, "", embedding, topK, docTypeFilter)
}

// Delete removes vectors by ID from the specified namespace.
func (p *Client) Delete(ctx context.Context, namespace string, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	conn, release, err := p.conn(namespace)
	if err != nil {
		return err
	}
	defer release()
	for start := 0; start < len(ids); start += 1000 {
		batch := ids[start:min(start+1000, len(ids))]
		if err := p.do(ctx, p.maxAttempts, func(attemptCtx context.Context) error {
			return ignoreNotFound(conn.DeleteVectorsById(attemptCtx, batch))
		}); err != nil {
			return fmt.Errorf("pinecone: delete %d vectors from namespace %q: %w", len(batch), namespace, err)
		}
	}
	return nil
}

// DeleteAllVectorsInNamespace deletes all vectors from the specified namespace.
func (p *Client) DeleteAllVectorsInNamespace(ctx context.Context, namespace string) error {
	conn, release, err := p.conn(namespace)
	if err != nil {
		return err
	}
	defer release()
	if err := p.do(ctx, p.maxAttempts, func(attemptCtx context.Context) error {
		return ignoreNotFound(conn.DeleteAllVectorsInNamespace(attemptCtx))
	}); err != nil {
		return fmt.Errorf("pinecone: delete all vectors in namespace %q: %w", namespace, err)
	}
	slog.DebugContext(ctx, "pinecone namespace cleared", "namespace", namespace)
	return nil
}

// DeleteByDocID deletes vectors from the default namespace whose doc_id matches.
func (p *Client) DeleteByDocID(ctx context.Context, docID string) error {
	filter, err := structpb.NewStruct(map[string]any{"doc_id": map[string]any{"$eq": docID}})
	if err != nil {
		return fmt.Errorf("pinecone: build document filter: %w", err)
	}
	err = p.do(ctx, p.maxAttempts, func(attemptCtx context.Context) error {
		return ignoreNotFound(p.defaultConn.DeleteVectorsByFilter(attemptCtx, filter))
	})
	switch status.Code(err) {
	case codes.InvalidArgument, codes.Unimplemented, codes.FailedPrecondition:
		err = p.deleteByDocIDs(ctx, filter)
	}
	if err != nil {
		return fmt.Errorf("pinecone: delete vectors by doc_id %s: %w", docID, err)
	}
	slog.DebugContext(ctx, "pinecone document vectors deleted", "document_id", docID)
	return nil
}

func (p *Client) deleteByDocIDs(ctx context.Context, filter *structpb.Struct) error {
	vector := make([]float32, p.dims)
	vector[0] = 1
	req := &pinecone.QueryByVectorValuesRequest{Vector: vector, TopK: 1000, MetadataFilter: filter}
	deleted := make(map[string]struct{})
	for range 10 {
		matches, err := p.queryNamespace(ctx, "", req)
		if err != nil {
			return err
		}
		ids := make([]string, 0, len(matches))
		for _, match := range matches {
			id := string(match.Chunk.ID)
			if _, ok := deleted[id]; !ok {
				ids = append(ids, id)
			}
		}
		if len(ids) == 0 {
			return nil
		}
		if err := p.Delete(ctx, "", ids); err != nil {
			return err
		}
		for _, id := range ids {
			deleted[id] = struct{}{}
		}
		if len(matches) < int(req.TopK) {
			return nil
		}
	}
	return errors.New("pinecone: document deletion did not converge after 10 rounds")
}

func ignoreNotFound(err error) error {
	if status.Code(err) == codes.NotFound {
		return nil
	}
	return err
}

// Ping verifies connectivity to the Pinecone index.
func (p *Client) Ping(ctx context.Context) error {
	return p.do(ctx, min(3, p.maxAttempts), func(attemptCtx context.Context) error {
		_, err := p.client.DescribeIndex(attemptCtx, p.cfg.PineconeIndexName)
		if err != nil {
			return fmt.Errorf("pinecone: ping: %w", err)
		}
		return nil
	})
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

// Close releases the shared default connection.
func (p *Client) Close() error {
	p.closeOnce.Do(func() { p.closeErr = p.defaultConn.Close() })
	return p.closeErr
}
