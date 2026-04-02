package domain

import (
	"context"
	"io"
)

// EmbeddingProvider generates vector embeddings from text.
type EmbeddingProvider interface {
	Embed(ctx context.Context, text string) ([]float32, error)
}

// LLMProvider sends prompts to a language model and returns text responses.
type LLMProvider interface {
	Generate(ctx context.Context, prompt string, maxTokens int) (string, error)
}

// VectorStore manages vector embeddings in an external vector database.
type VectorStore interface {
	Upsert(ctx context.Context, namespace string, chunks []Chunk) error
	Query(ctx context.Context, namespace string, embedding []float32, topK int, docTypeFilter string) ([]ScoredChunk, error)
	QueryAllNamespaces(ctx context.Context, embedding []float32, topK int, docTypeFilter string) ([]ScoredChunk, error)
	Delete(ctx context.Context, namespace string, ids []string) error
	Ping(ctx context.Context) error
}

// ChunkStore manages chunk metadata and document fingerprints in a relational database.
type ChunkStore interface {
	InsertDocument(ctx context.Context, doc Document) error
	InsertChunks(ctx context.Context, chunks []Chunk) error
	GetDocument(ctx context.Context, id DocumentID) (*Document, error)
	GetDocumentS3Key(ctx context.Context, id DocumentID) (*string, error)
	UpdateDocumentStatus(ctx context.Context, id DocumentID, status string) error
	CheckFingerprint(ctx context.Context, fingerprint string) (DocumentID, bool, error)
	StoreFingerprint(ctx context.Context, fingerprint string, docID string) error
	Ping(ctx context.Context) error
	DeleteDocument(ctx context.Context, id DocumentID) error
	Close(ctx context.Context) error
	RunMigrations(ctx context.Context) error
}

// FileStore manages file storage in an object store (e.g. S3).
type FileStore interface {
	Upload(ctx context.Context, docID, docType, filename string, ft string, r io.Reader) (string, error)
	PresignGet(ctx context.Context, s3Key string) (string, error)
	Delete(ctx context.Context, s3Key string) error
	Ping(ctx context.Context) error
}
