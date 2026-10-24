package pinecone

import (
	"context"

	"github.com/Victoroide/go-ms-rag/internal/domain"
)

// Driver implements the domain.VectorStore interface using Pinecone.
type Driver struct {
	client *Client
}

// NewDriver creates a VectorStore backed by Pinecone.
func NewDriver(client *Client) *Driver {
	return &Driver{client: client}
}

// Upsert inserts or updates vectors in the specified namespace.
func (d *Driver) Upsert(ctx context.Context, namespace string, chunks []domain.Chunk) error {
	return d.client.Upsert(ctx, namespace, chunks)
}

// Query searches for similar vectors in the specified namespace.
func (d *Driver) Query(ctx context.Context, namespace string, embedding []float32, topK int, docTypeFilter string) ([]domain.ScoredChunk, error) {
	return d.client.Query(ctx, namespace, embedding, topK, docTypeFilter)
}

// QueryAllNamespaces queries across all namespaces.
func (d *Driver) QueryAllNamespaces(ctx context.Context, embedding []float32, topK int, docTypeFilter string) ([]domain.ScoredChunk, error) {
	return d.client.QueryAllNamespaces(ctx, embedding, topK, docTypeFilter)
}

// Delete removes vectors by ID from the specified namespace.
func (d *Driver) Delete(ctx context.Context, namespace string, ids []string) error {
	return d.client.Delete(ctx, namespace, ids)
}

// DeleteAllVectorsInNamespace deletes all vectors from the specified namespace.
func (d *Driver) DeleteAllVectorsInNamespace(ctx context.Context, namespace string) error {
	return d.client.DeleteAllVectorsInNamespace(ctx, namespace)
}

// DeleteByDocID deletes vectors from the default namespace whose metadata doc_id matches.
func (d *Driver) DeleteByDocID(ctx context.Context, docID string) error {
	return d.client.DeleteByDocID(ctx, docID)
}

// Ping verifies connectivity to the Pinecone index.
func (d *Driver) Ping(ctx context.Context) error {
	return d.client.Ping(ctx)
}
