package pipeline

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/Victoroide/go-ms-rag/internal/chunker"
	"github.com/Victoroide/go-ms-rag/internal/domain"
)

// ChunkStage splits a raw document into chunks using the sliding window chunker.
type ChunkStage struct {
	chunker *chunker.Chunker
}

// NewChunkStage creates a ChunkStage.
func NewChunkStage(c *chunker.Chunker) *ChunkStage {
	return &ChunkStage{chunker: c}
}

// ID returns the stage identifier.
func (s *ChunkStage) ID() StageID { return StageChunk }

// Run splits the raw text into chunks.
func (s *ChunkStage) Run(_ context.Context, ic *IngestContext) error {
	ic.Chunks = s.chunker.Chunk(ic.DocumentID, ic.RawText)
	if len(ic.Chunks) == 0 {
		return fmt.Errorf("chunk_stage: no chunks produced from document %s", ic.DocumentID)
	}
	log.Printf("chunk_stage: document=%s chunks=%d", ic.DocumentID, len(ic.Chunks))
	return nil
}

// EmbedStage generates embeddings for all chunks concurrently.
type EmbedStage struct {
	embedder domain.EmbeddingProvider
}

// NewEmbedStage creates an EmbedStage.
func NewEmbedStage(emb domain.EmbeddingProvider) *EmbedStage {
	return &EmbedStage{embedder: emb}
}

// ID returns the stage identifier.
func (s *EmbedStage) ID() StageID { return StageEmbed }

// Run generates embeddings for all chunks.
func (s *EmbedStage) Run(ctx context.Context, ic *IngestContext) error {
	for i := range ic.Chunks {
		emb, err := s.embedder.Embed(ctx, ic.Chunks[i].Text)
		if err != nil {
			return fmt.Errorf("embed_stage: chunk %s: %w", ic.Chunks[i].ID, err)
		}
		ic.Chunks[i].Embedding = emb
	}
	log.Printf("embed_stage: document=%s embedded=%d chunks", ic.DocumentID, len(ic.Chunks))
	return nil
}

// PersistStage upserts chunks to Pinecone and inserts metadata to Postgres.
// Idempotent via fingerprint check.
type PersistStage struct {
	vectorStore domain.VectorStore
	chunkStore  domain.ChunkStore
}

// NewPersistStage creates a PersistStage.
func NewPersistStage(vs domain.VectorStore, cs domain.ChunkStore) *PersistStage {
	return &PersistStage{vectorStore: vs, chunkStore: cs}
}

// ID returns the stage identifier.
func (s *PersistStage) ID() StageID { return StagePersist }

// detectDocType determines whether a document is a job posting or a candidate profile.
func detectDocType(content string) string {
	if strings.Contains(strings.ToLower(content), "job opening:") {
		return "job"
	}
	return "candidate"
}

// Run persists chunks to both Pinecone and Postgres.
// Order: 1) Upsert vectors to Pinecone, 2) Save metadata to Postgres.
func (s *PersistStage) Run(ctx context.Context, ic *IngestContext) error {
	// Detect doc_type and inject into metadata for all chunks.
	docType := detectDocType(ic.RawText)
	if ic.Metadata == nil {
		ic.Metadata = make(map[string]any)
	}
	ic.Metadata["doc_type"] = docType

	// Inject doc_type into each chunk's metadata for Pinecone.
	for i := range ic.Chunks {
		if ic.Chunks[i].Metadata == nil {
			ic.Chunks[i].Metadata = make(map[string]any)
		}
		ic.Chunks[i].Metadata["doc_type"] = docType
	}

	// 1. Upsert vectors to Pinecone (namespace = doc_id).
	namespace := string(ic.DocumentID)
	if err := s.vectorStore.Upsert(ctx, namespace, ic.Chunks); err != nil {
		return fmt.Errorf("persist_stage: upsert to pinecone namespace %q: %w", namespace, err)
	}

	// 2. Also upsert to default namespace for global queries.
	if err := s.vectorStore.Upsert(ctx, "", ic.Chunks); err != nil {
		return fmt.Errorf("persist_stage: upsert to default namespace: %w", err)
	}

	// 3. Insert document record.
	preview := ic.RawText
	if len(preview) > 60 {
		preview = preview[:60]
	}
	doc := domain.Document{
		ID:             ic.DocumentID,
		Content:        ic.RawText,
		ContentPreview: preview,
		Metadata:       ic.Metadata,
		ChunkCount:     len(ic.Chunks),
		FileName:       ic.FileName,
		FileType:       ic.FileType,
		S3Key:          ic.S3Key,
	}
	if err := s.chunkStore.InsertDocument(ctx, doc); err != nil {
		return fmt.Errorf("persist_stage: insert document: %w", err)
	}

	// 4. Insert chunk metadata to Postgres.
	if err := s.chunkStore.InsertChunks(ctx, ic.Chunks); err != nil {
		return fmt.Errorf("persist_stage: insert chunks: %w", err)
	}

	// 5. Store fingerprint (after document exists for FK constraint).
	fp := chunker.Fingerprint(ic.RawText)
	if err := s.chunkStore.StoreFingerprint(ctx, fp, string(ic.DocumentID)); err != nil {
		log.Printf("persist_stage: store fingerprint: %v", err)
	}

	log.Printf("persist_stage: document=%s doc_type=%s chunks=%d persisted", ic.DocumentID, docType, len(ic.Chunks))
	return nil
}
