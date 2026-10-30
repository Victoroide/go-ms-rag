package domain

import (
	"context"
)

// EmbeddingProvider generates vector embeddings from text.
type EmbeddingProvider interface {
	Embed(ctx context.Context, text string) ([]float32, error)
}

// VectorStore manages vector embeddings in an external vector database.
type VectorStore interface {
	Upsert(ctx context.Context, namespace string, chunks []Chunk) error
	Query(ctx context.Context, namespace string, embedding []float32, topK int, docTypeFilter string) ([]ScoredChunk, error)
	QueryAllNamespaces(ctx context.Context, embedding []float32, topK int, docTypeFilter string) ([]ScoredChunk, error)
	Ping(ctx context.Context) error
}

// VectorDeleter is the subset of vector-store delete operations the document
// delete handler needs. Both the Pinecone driver and the in-memory fake store
// satisfy it, so the handler does not depend on a concrete implementation.
type VectorDeleter interface {
	DeleteAllVectorsInNamespace(ctx context.Context, namespace string) error
	DeleteByDocID(ctx context.Context, docID string) error
}

// ChunkStore manages chunk metadata and document fingerprints in a relational database.
type ChunkStore interface {
	InsertDocument(ctx context.Context, doc Document) error
	InsertChunks(ctx context.Context, chunks []Chunk) error
	GetDocument(ctx context.Context, id DocumentID) (*Document, error)
	GetDocumentS3Key(ctx context.Context, id DocumentID) (*string, error)
	ListDocuments(ctx context.Context) ([]DocumentRef, error)
	UpdateDocumentStatus(ctx context.Context, id DocumentID, status string) error
	CheckFingerprint(ctx context.Context, fingerprint string) (DocumentID, bool, error)
	StoreFingerprint(ctx context.Context, fingerprint string, docID string) error
	Ping(ctx context.Context) error
	DeleteDocument(ctx context.Context, id DocumentID) error
	Close(ctx context.Context) error
	RunMigrations(ctx context.Context) error
}
