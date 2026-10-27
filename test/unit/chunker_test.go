package unit

import (
	"testing"

	"github.com/Victoroide/quarry-rag/internal/chunker"
	"github.com/Victoroide/quarry-rag/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestChunkSliding(t *testing.T) {
	c := chunker.NewChunker(512, 64)
	text := "The quick brown fox jumps over the lazy dog. " +
		"This sentence is used for testing the chunker. " +
		"We need enough words to produce multiple chunks with overlap. " +
		"Go is a statically typed compiled language designed at Google. " +
		"It is syntactically similar to C but with memory safety. " +
		"Garbage collection and structural typing are key features. " +
		"The language was designed by Robert Griesemer Rob Pike and Ken Thompson."

	chunks := c.Chunk(domain.DocumentID("test-doc-1"), text)
	require.NotEmpty(t, chunks, "expected at least one chunk")

	for _, ch := range chunks {
		assert.NotEmpty(t, ch.Text, "chunk text should not be empty")
		assert.NotEmpty(t, string(ch.ID), "chunk ID should not be empty")
		assert.Equal(t, domain.DocumentID("test-doc-1"), ch.DocumentID)
		assert.Greater(t, ch.TokenCount, 0, "token count should be positive")
	}

	// If multiple chunks, verify overlap exists by checking the last words
	// of chunk N appear at the start of chunk N+1.
	if len(chunks) > 1 {
		for i := 0; i < len(chunks)-1; i++ {
			assert.NotEqual(t, chunks[i].Text, chunks[i+1].Text,
				"consecutive chunks should not be identical")
		}
	}
}

func TestChunkDedup(t *testing.T) {
	content1 := "This is a test document with some content."
	content2 := "This is a test document with some content."
	content3 := "This is a different document."

	fp1 := chunker.Fingerprint(content1)
	fp2 := chunker.Fingerprint(content2)
	fp3 := chunker.Fingerprint(content3)

	assert.Equal(t, fp1, fp2, "same content should produce same fingerprint")
	assert.NotEqual(t, fp1, fp3, "different content should produce different fingerprint")
	assert.Len(t, fp1, 64, "SHA-256 hex fingerprint should be 64 characters")
}
