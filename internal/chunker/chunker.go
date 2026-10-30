// Package chunker splits raw documents into token-bounded chunks
// with semantic-boundary awareness. The chunker first normalizes the
// raw extract (NFC, width folding, hyphenation, decoration noise), then walks
// paragraphs and sentences building chunks that try to honor structure
// instead of slicing mid-word. Each chunk is tagged with a (kind,
// quality) classification that retrieval can use to de-prioritize
// header / contact noise without losing it.
package chunker

import (
	"crypto/sha256"
	"fmt"
	"strconv"
	"strings"

	"github.com/Victoroide/quarry-rag/internal/domain"
	"github.com/Victoroide/quarry-rag/internal/textutil"
)

// Chunker splits raw text into Chunks using semantic-aware boundaries.
type Chunker struct {
	chunkSize int
	overlap   int
}

// NewChunker creates a Chunker with estimated token budgets. Sizes below two
// are treated as two, the minimum estimate for a non-dense character.
func NewChunker(chunkSize, overlap int) *Chunker {
	chunkSize = max(2, chunkSize)
	return &Chunker{
		chunkSize: chunkSize,
		overlap:   min(max(0, overlap), chunkSize-1),
	}
}

type textUnit struct {
	text      string
	separator string
	estimate  tokenEstimate
}

// Chunk splits normalized text at paragraph, sentence, word and rune boundaries.
// Every chunk stays within the estimated token and byte limits, and overlapping
// windows always include previously unemitted text.
func (c *Chunker) Chunk(docID domain.DocumentID, text string) []domain.Chunk {
	cleaned := textutil.NormalizeForIngest(stripHTML(text))
	var units []textUnit
	for i, paragraph := range textutil.SplitParagraphs(cleaned) {
		for j, sentence := range textutil.SplitSentences(paragraph) {
			separator := " "
			if i > 0 && j == 0 {
				separator = "\n\n"
			}
			units = c.appendSentence(units, sentence, separator)
		}
	}

	var chunks []domain.Chunk
	start, nextNew := 0, 0
	for nextNew < len(units) {
		end, size := start, 0
		var estimate tokenEstimate
		for end < len(units) {
			unit := units[end]
			separator := ""
			if end > start {
				separator = unit.separator
			}
			nextEstimate := estimate.append(unit.estimate, separator != "")
			nextSize := size + len(separator) + len(unit.text)
			if nextEstimate.count() > c.chunkSize || nextSize > MaxChunkBytes {
				break
			}
			estimate, size = nextEstimate, nextSize
			end++
		}
		var b strings.Builder
		b.Grow(size)
		for i := start; i < end; i++ {
			if i > start {
				b.WriteString(units[i].separator)
			}
			b.WriteString(units[i].text)
		}
		chunks = append(chunks, makeChunk(docID, len(chunks), b.String()))
		nextNew = end
		if end == len(units) {
			break
		}

		overlapStart := end
		suffixSize := 0
		var suffix tokenEstimate
		for i := end - 1; i > start; i-- {
			nextSuffix := units[i].estimate
			nextSize := len(units[i].text)
			if i < end-1 {
				nextSuffix = nextSuffix.append(suffix, units[i+1].separator != "")
				nextSize += len(units[i+1].separator) + suffixSize
			}
			// Retain only a suffix that leaves room for the first new unit.
			withNew := nextSuffix.append(units[nextNew].estimate, units[nextNew].separator != "")
			if nextSuffix.count() > c.overlap || withNew.count() > c.chunkSize ||
				nextSize+len(units[nextNew].separator)+len(units[nextNew].text) > MaxChunkBytes {
				break
			}
			suffix = nextSuffix
			suffixSize = nextSize
			overlapStart = i
		}
		start = overlapStart
	}
	return chunks
}

func (c *Chunker) appendSentence(units []textUnit, sentence, separator string) []textUnit {
	estimate := measureTokens(sentence)
	if estimate.count() <= c.chunkSize && len(sentence) <= MaxChunkBytes {
		return append(units, textUnit{sentence, separator, estimate})
	}
	for i, word := range strings.Fields(sentence) {
		if i > 0 {
			separator = " "
		}
		estimate = measureTokens(word)
		if estimate.count() <= c.chunkSize && len(word) <= MaxChunkBytes {
			units = append(units, textUnit{word, separator, estimate})
			continue
		}
		units = c.appendWord(units, word, separator)
	}
	return units
}

func (c *Chunker) appendWord(units []textUnit, word, separator string) []textUnit {
	// Smaller fragments let successive windows share part of a long word.
	fragmentSize := c.chunkSize
	if c.overlap > 0 {
		fragmentSize = min(c.overlap, c.chunkSize-c.overlap)
	}
	start := 0
	var estimate tokenEstimate
	for offset, r := range word {
		next := estimate
		next.addRune(r)
		if offset > start && (next.count() > fragmentSize || offset-start+len(string(r)) > MaxChunkBytes) {
			units = append(units, textUnit{word[start:offset], separator, estimate})
			start, separator, estimate = offset, "", tokenEstimate{}
			estimate.addRune(r)
		} else {
			estimate = next
		}
	}
	return append(units, textUnit{word[start:], separator, estimate})
}

func makeChunk(docID domain.DocumentID, index int, text string) domain.Chunk {
	kind, quality := Classify(text)
	return domain.Chunk{
		ID:         domain.ChunkID(chunkID(string(docID), index)),
		DocumentID: docID,
		Index:      index,
		Text:       text,
		TokenCount: EstimateTokens(text),
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

func chunkID(docID string, index int) string {
	h := sha256.Sum256([]byte(docID + strconv.Itoa(index)))
	return fmt.Sprintf("%x", h[:16])
}
