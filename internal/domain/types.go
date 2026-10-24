// Package domain defines the core data types shared across the RAG pipeline.
package domain

// MaxContentBytes bounds decoded document text.
const MaxContentBytes = 1_000_000

// DefaultDocType is used when a caller omits the document label.
const DefaultDocType = "document"

// DocumentID uniquely identifies an ingested document.
type DocumentID string

// DocumentRef is a lightweight document record for reconciliation listings.
type DocumentRef struct {
	ID      string  `json:"document_id"`
	DocType string  `json:"doc_type"`
	S3Key   *string `json:"s3_key,omitempty"`
}

// ChunkID uniquely identifies a chunk within a document.
type ChunkID string

// Chunk represents a segment of a document with its embedding.
type Chunk struct {
	ID         ChunkID        `json:"id"`
	DocumentID DocumentID     `json:"document_id"`
	Index      int            `json:"chunk_index"`
	Text       string         `json:"text"`
	TokenCount int            `json:"token_count"`
	Embedding  []float32      `json:"embedding,omitempty"`
	Metadata   map[string]any `json:"metadata,omitempty"`
}

// Document represents an ingested document with metadata.
type Document struct {
	ID             DocumentID     `json:"id"`
	Content        string         `json:"content"`
	ContentPreview string         `json:"content_preview,omitempty"`
	Metadata       map[string]any `json:"metadata,omitempty"`
	Fingerprint    string         `json:"fingerprint"`
	ChunkCount     int            `json:"chunk_count"`
	FileName       *string        `json:"file_name,omitempty"`
	FileType       *string        `json:"file_type,omitempty"`
	S3Key          *string        `json:"s3_key,omitempty"`
}

// TokenUsage tracks token consumption from Bedrock calls.
type TokenUsage struct {
	InputTokens  int32 `json:"input_tokens"`
	OutputTokens int32 `json:"output_tokens"`
	TotalTokens  int32 `json:"total_tokens"`
}

// ScoredChunk pairs a chunk with its similarity score from vector search.
type ScoredChunk struct {
	Chunk     Chunk   `json:"chunk"`
	Score     float64 `json:"score"`
	Namespace string  `json:"namespace,omitempty"`
}
