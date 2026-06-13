// Package chunker splits raw documents into token-bounded chunks
// with semantic-boundary awareness. The chunker first normalizes the
// raw extract (NFKC, hyphenation, decoration noise), then walks
// paragraphs and sentences building chunks that try to honor structure
// instead of slicing mid-word. Each chunk is tagged with a (kind,
// quality) classification that retrieval can use to de-prioritise
// header / contact noise without losing it.
package chunker

import (
	"crypto/sha256"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/Victoroide/go-ms-rag/internal/domain"
	"github.com/Victoroide/go-ms-rag/internal/textutil"
)

const (
	// tokenApproximationFactor is the multiplier applied to word count
	// to approximate subword token count in typical LLM tokenizers.
	tokenApproximationFactor = 1.3
)

// Chunker splits raw text into Chunks using semantic-aware boundaries.
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

// Chunk splits text into Chunks of approximately chunkSize tokens.
//
// The strategy:
//
//  1. Strip HTML.
//  2. Normalise for ingest: NFKC + hyphenation fix + paragraph-aware
//     line break handling (via textutil.NormalizeForIngest).
//  3. Split into paragraphs.
//  4. Within a paragraph, split into sentences.
//  5. Greedily accumulate sentences into a chunk until the next
//     sentence would push the chunk past wordsPerChunk; then emit and
//     carry over the last `overlap` sentences to the next chunk.
//  6. Cross paragraph boundaries with a paragraph-break marker so
//     prose stays readable inside the chunk text.
//  7. Tag each emitted chunk with a (kind, quality) classification.
//
// The signature is unchanged from the previous implementation so the
// rest of the pipeline does not need to be updated.
func (c *Chunker) Chunk(docID domain.DocumentID, text string) []domain.Chunk {
	cleaned := stripHTML(text)
	cleaned = textutil.NormalizeForIngest(cleaned)
	if cleaned == "" {
		return nil
	}

	wordsPerChunk := int(float64(c.chunkSize) / tokenApproximationFactor)
	if wordsPerChunk < 1 {
		wordsPerChunk = 1
	}
	wordsOverlap := int(float64(c.overlap) / tokenApproximationFactor)
	if wordsOverlap >= wordsPerChunk {
		wordsOverlap = wordsPerChunk - 1
	}
	if wordsOverlap < 0 {
		wordsOverlap = 0
	}

	type sentence struct {
		text      string
		wordCount int
		paragraph int
	}

	// Flatten the document into a stream of sentences tagged with
	// their owning paragraph index, so we can re-insert paragraph
	// breaks inside emitted chunks when the chunk spans paragraphs.
	var stream []sentence
	for i, para := range textutil.SplitParagraphs(cleaned) {
		for _, sent := range textutil.SplitSentences(para) {
			wc := len(strings.Fields(sent))
			if wc == 0 {
				continue
			}
			stream = append(stream, sentence{
				text:      sent,
				wordCount: wc,
				paragraph: i,
			})
		}
	}
	if len(stream) == 0 {
		return nil
	}

	var chunks []domain.Chunk
	chunkIndex := 0
	startIdx := 0

	for startIdx < len(stream) {
		endIdx := startIdx
		wordTotal := 0
		// Accumulate sentences until adding the next one would exceed
		// wordsPerChunk. Always include at least one sentence so a
		// single very long sentence still emits.
		for endIdx < len(stream) {
			next := stream[endIdx].wordCount
			if endIdx > startIdx && wordTotal+next > wordsPerChunk {
				break
			}
			wordTotal += next
			endIdx++
		}
		// If a single sentence is itself larger than wordsPerChunk
		// (e.g. PDF extracted a whole bulleted block as one line),
		// hard-split it on word boundary as a fallback.
		if endIdx == startIdx+1 && stream[startIdx].wordCount > wordsPerChunk {
			words := strings.Fields(stream[startIdx].text)
			for w := 0; w < len(words); w += wordsPerChunk {
				end := w + wordsPerChunk
				if end > len(words) {
					end = len(words)
				}
				piece := strings.Join(words[w:end], " ")
				chunks = append(chunks, makeChunk(docID, chunkIndex, piece))
				chunkIndex++
			}
			startIdx = endIdx
			continue
		}

		// Build the chunk text, inserting "\n\n" between sentences
		// that come from different paragraphs so the LLM downstream
		// can still see structure.
		var b strings.Builder
		for i := startIdx; i < endIdx; i++ {
			if i > startIdx {
				if stream[i].paragraph != stream[i-1].paragraph {
					b.WriteString("\n\n")
				} else {
					b.WriteByte(' ')
				}
			}
			b.WriteString(stream[i].text)
		}
		chunkText := strings.TrimSpace(b.String())
		if chunkText != "" {
			chunks = append(chunks, makeChunk(docID, chunkIndex, chunkText))
			chunkIndex++
		}

		// Reached the end?
		if endIdx >= len(stream) {
			break
		}
		// Build overlap by walking back from endIdx until we either
		// reach the chunk start or have collected wordsOverlap words.
		overlapStart := endIdx
		overlapWords := 0
		for overlapStart > startIdx+1 && overlapWords < wordsOverlap {
			overlapStart--
			overlapWords += stream[overlapStart].wordCount
		}
		// Guarantee forward progress so we never loop infinitely.
		if overlapStart <= startIdx {
			overlapStart = startIdx + 1
		}
		startIdx = overlapStart
	}

	return chunks
}

func makeChunk(docID domain.DocumentID, index int, text string) domain.Chunk {
	tokenCount := approximateTokens(strings.Fields(text))
	kind, quality := Classify(text)
	return domain.Chunk{
		ID:         domain.ChunkID(chunkID(string(docID), index)),
		DocumentID: docID,
		Index:      index,
		Text:       text,
		TokenCount: tokenCount,
		Metadata: map[string]any{
			"kind":    string(kind),
			"quality": string(quality),
		},
	}
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

func approximateTokens(words []string) int {
	return int(float64(len(words)) * tokenApproximationFactor)
}

func chunkID(docID string, index int) string {
	h := sha256.Sum256([]byte(docID + strconv.Itoa(index)))
	return fmt.Sprintf("%x", h[:16])
}
