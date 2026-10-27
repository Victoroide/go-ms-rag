package unit

import (
	"strings"
	"testing"

	"github.com/Victoroide/quarry-rag/internal/chunker"
	"github.com/Victoroide/quarry-rag/internal/domain"
	"github.com/Victoroide/quarry-rag/internal/textutil"
	"github.com/stretchr/testify/assert"
)

func TestNormalizeForIngest_RejoinsHyphenatedWord(t *testing.T) {
	// PDFs justify text and break long words with a soft hyphen at
	// end-of-line: "documents" becomes "docu-\nments". Our normalizer
	// must rejoin the halves.
	raw := "The archive retains docu-\nments for future research."
	got := textutil.NormalizeForIngest(raw)
	assert.Contains(t, got, "documents", "hyphenation across newline should be rejoined")
	assert.NotContains(t, got, "docu-", "trailing hyphen should be removed")
}

func TestNormalizeForIngest_PreservesParagraphBreaks(t *testing.T) {
	raw := "First paragraph line one.\nFirst paragraph line two.\n\nSecond paragraph here."
	got := textutil.NormalizeForIngest(raw)
	parts := textutil.SplitParagraphs(got)
	assert.Equal(t, 2, len(parts), "should preserve exactly one paragraph break")
	assert.Contains(t, parts[0], "First paragraph line one.")
	assert.Contains(t, parts[0], "First paragraph line two.")
	assert.Contains(t, parts[1], "Second paragraph here.")
}

func TestNormalizeForIngest_CollapsesDecorationRuns(t *testing.T) {
	raw := "Systems:\n--------\nPython\n========\nRedis"
	got := textutil.NormalizeForIngest(raw)
	assert.NotContains(t, got, "--------", "decoration runs should be collapsed")
	assert.NotContains(t, got, "========", "decoration runs should be collapsed")
	assert.Contains(t, got, "Systems")
	assert.Contains(t, got, "Python")
	assert.Contains(t, got, "Redis")
}

func TestNormalizeForIngest_DropsZeroWidthBOM(t *testing.T) {
	// BOM (U+FEFF) often appears at the start of UTF-8 PDF extracts.
	raw := "\uFEFFTítulo del documento\nUniversity graduate"
	got := textutil.NormalizeForIngest(raw)
	assert.False(t, strings.ContainsRune(got, '\uFEFF'), "BOM should be removed")
	assert.Contains(t, got, "Título del documento")
}

func TestSplitSentences_BasicSpanish(t *testing.T) {
	got := textutil.SplitSentences("El archivo conserva documentos. Funciona con Redis y AWS. Retiene informes durante cinco años.")
	assert.Equal(t, 3, len(got))
	assert.Equal(t, "El archivo conserva documentos.", got[0])
	assert.Equal(t, "Funciona con Redis y AWS.", got[1])
	assert.Equal(t, "Retiene informes durante cinco años.", got[2])
}

func TestClassify_HeaderOnlyChunk(t *testing.T) {
	kind, quality := chunker.Classify("NORMAS DEL ARCHIVO RETENCIÓN ALMACENAMIENTO")
	assert.Equal(t, chunker.KindHeader, kind)
	assert.Equal(t, chunker.QualityLow, quality)
}

func TestClassify_ContactOnlyChunk(t *testing.T) {
	kind, quality := chunker.Classify("archive@example.invalid   +591 70123456   www.example.invalid/archive")
	assert.Equal(t, chunker.KindContact, kind)
	assert.Equal(t, chunker.QualityLow, quality)
}

func TestClassify_BodyHighQuality(t *testing.T) {
	kind, quality := chunker.Classify("El archivo conserva documentos durante cinco años y publica índices mediante servicios REST en Python y Redis, con almacenamiento en AWS S3.")
	assert.Equal(t, chunker.KindBody, kind)
	assert.Equal(t, chunker.QualityHigh, quality)
}

func TestChunker_SemanticBoundaries(t *testing.T) {
	text := "El archivo genera índices con servicios escritos en Python. " +
		"Los servicios REST consultan Redis y PostgreSQL. " +
		"El archivo utiliza AWS desde hace tres años. " +
		"PostgreSQL conserva el catálogo de documentos. " +
		"El sistema aplica restricciones de acceso desde 2023.\n\n" +
		"Retención: los registros se conservan durante cinco años. " +
		"Los informes describen la arquitectura de almacenamiento."
	// chunkSize=30 -> ~23 words per chunk, forces 2+ chunks for the
	// ~45 words above.
	c := chunker.NewChunker(30, 6)
	chunks := c.Chunk(domain.DocumentID("doc-semantic-1"), text)
	assert.GreaterOrEqual(t, len(chunks), 2, "expected at least two chunks")
	for _, ch := range chunks {
		txt := strings.TrimSpace(ch.Text)
		assert.NotEmpty(t, txt)
		// Each chunk should carry classification metadata.
		assert.NotNil(t, ch.Metadata)
		_, ok := ch.Metadata["kind"]
		assert.True(t, ok, "chunk should carry kind metadata")
		_, ok = ch.Metadata["quality"]
		assert.True(t, ok, "chunk should carry quality metadata")
	}
}

func TestChunker_HeaderChunkIsTaggedLowQuality(t *testing.T) {
	// A document that opens with contact + section headers should produce
	// at least one chunk tagged as low-quality so retrieval can
	// de-rank it. Small chunk size so the contact/header block lands
	// in its own bucket rather than getting averaged in with prose.
	text := "Archivo Central. " +
		"archive@example.invalid. " +
		"+591 70 12 34 56. " +
		"www.example.invalid/archive.\n\n" +
		"NORMAS DEL ARCHIVO.\n\n" +
		"El archivo conserva registros durante cinco años y mantiene servicios " +
		"REST en Python y Redis."
	c := chunker.NewChunker(20, 4)
	chunks := c.Chunk(domain.DocumentID("doc-header-1"), text)
	if len(chunks) == 0 {
		t.Fatal("expected at least one chunk")
	}
	sawLow := false
	for _, ch := range chunks {
		if q, ok := ch.Metadata["quality"].(string); ok && q == string(chunker.QualityLow) {
			sawLow = true
			break
		}
	}
	assert.True(t, sawLow, "document header/contact chunk should be tagged low-quality")
}

func TestNormalizeForIngestPreservesWindowsParagraphs(t *testing.T) {
	got := textutil.NormalizeForIngest("First line.\r\n\r\nSecond paragraph.")
	assert.Equal(t, "First line.\n\nSecond paragraph.", got)
}
