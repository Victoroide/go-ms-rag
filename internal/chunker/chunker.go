// Package chunker splits raw documents into token-bounded chunks
// with sliding window overlap and SHA-256 deduplication.
package chunker

import (
	"crypto/sha256"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/Victoroide/go-ms-rag/internal/domain"
)

const (
	// tokenApproximationFactor is the multiplier applied to word count
	// to approximate subword token count in typical LLM tokenizers.
	tokenApproximationFactor = 1.3
)

// Chunker splits raw text into Chunks using a sliding window.
type Chunker struct {
	chunkSize int
	overlap   int
}

// NewChunker creates a Chunker with the given chunk size and overlap
// expressed in approximate token counts.
func NewChunker(chunkSize, overlap int) *Chunker {
	return &Chunker{
		chunkSize: chunkSize,
		overlap:   overlap,
	}
}

var htmlTagRe = regexp.MustCompile(`<[^>]*>`)

// Chunk splits text into Chunks of approximately chunkSize tokens
// with overlap tokens shared between consecutive chunks.
func (c *Chunker) Chunk(docID domain.DocumentID, text string) []domain.Chunk {
	cleaned := stripHTML(text)
	cleaned = normalizeWhitespace(cleaned)
	words := strings.Fields(cleaned)
	if len(words) == 0 {
		return nil
	}
	wordsPerChunk := int(float64(c.chunkSize) / tokenApproximationFactor)
	wordsOverlap := int(float64(c.overlap) / tokenApproximationFactor)
	if wordsPerChunk < 1 {
		wordsPerChunk = 1
	}
	if wordsOverlap >= wordsPerChunk {
		wordsOverlap = wordsPerChunk - 1
	}
	var chunks []domain.Chunk
	stride := max(wordsPerChunk-wordsOverlap, 1)
	for i, idx := 0, 0; idx < len(words); i, idx = i+1, idx+stride {
		end := min(idx+wordsPerChunk, len(words))
		chunkText := strings.Join(words[idx:end], " ")
		tokenCount := approximateTokens(words[idx:end])
		cid := chunkID(string(docID), i)
		chunks = append(chunks, domain.Chunk{
			ID:         domain.ChunkID(cid),
			DocumentID: docID,
			Index:      i,
			Text:       chunkText,
			TokenCount: tokenCount,
		})
		if end == len(words) {
			break
		}
	}
	return chunks
}

// Fingerprint computes a SHA-256 hex fingerprint for the given content.
func Fingerprint(content string) string {
	h := sha256.Sum256([]byte(content))
	return fmt.Sprintf("%x", h)
}

// ChunkFingerprint computes a SHA-256 hex fingerprint for a single chunk.
func ChunkFingerprint(text string) string {
	return Fingerprint(text)
}

func stripHTML(s string) string {
	return htmlTagRe.ReplaceAllString(s, "")
}

func normalizeWhitespace(s string) string {
	var b strings.Builder
	prevSpace := false
	for _, r := range s {
		if unicode.IsSpace(r) {
			if !prevSpace {
				b.WriteRune(' ')
			}
			prevSpace = true
		} else {
			b.WriteRune(r)
			prevSpace = false
		}
	}
	return strings.TrimSpace(b.String())
}

func approximateTokens(words []string) int {
	return int(float64(len(words)) * tokenApproximationFactor)
}

func chunkID(docID string, index int) string {
	h := sha256.Sum256([]byte(docID + strconv.Itoa(index)))
	return fmt.Sprintf("%x", h[:16])
}
