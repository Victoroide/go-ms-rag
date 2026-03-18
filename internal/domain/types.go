// Package domain defines the core data types shared across the RAG pipeline.
package domain

// DocumentID uniquely identifies an ingested document.
type DocumentID string

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

// QueryResult holds the final output of a retrieval-augmented query.
type QueryResult struct {
	Answer     string     `json:"answer"`
	Mode       string     `json:"mode"`
	ChunksUsed int        `json:"chunks_used"`
	TokenUsage TokenUsage `json:"token_usage"`
	Chunks     []Chunk    `json:"chunks,omitempty"`
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
