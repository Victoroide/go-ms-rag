package domain

import "context"

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
	Query(ctx context.Context, namespace string, embedding []float32, topK int) ([]ScoredChunk, error)
	QueryAllNamespaces(ctx context.Context, embedding []float32, topK int) ([]ScoredChunk, error)
	Delete(ctx context.Context, namespace string, ids []string) error
	Ping(ctx context.Context) error
}

// ChunkStore manages chunk metadata and document fingerprints in a relational database.
type ChunkStore interface {
	InsertDocument(ctx context.Context, doc Document) error
	InsertChunks(ctx context.Context, chunks []Chunk) error
	GetDocument(ctx context.Context, id DocumentID) (*Document, error)
	CheckFingerprint(ctx context.Context, fingerprint string) (DocumentID, bool, error)
	StoreFingerprint(ctx context.Context, fingerprint string, docID string) error
	Ping(ctx context.Context) error
	Close(ctx context.Context) error
	RunMigrations(ctx context.Context) error
}
