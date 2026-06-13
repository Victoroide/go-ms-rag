package unit

import (
	"strings"
	"testing"

	"github.com/Victoroide/go-ms-rag/internal/chunker"
	"github.com/Victoroide/go-ms-rag/internal/domain"
	"github.com/Victoroide/go-ms-rag/internal/textutil"
	"github.com/stretchr/testify/assert"
)

func TestNormalizeForIngest_RejoinsHyphenatedWord(t *testing.T) {
	// PDFs justify text and break long words with a soft hyphen at
	// end-of-line: "developer" becomes "devel-\noper". Our normalizer
	// must rejoin the halves.
	raw := "He has experience as a software devel-\noper at a startup."
	got := textutil.NormalizeForIngest(raw)
	assert.Contains(t, got, "developer", "hyphenation across newline should be rejoined")
	assert.NotContains(t, got, "devel-", "trailing hyphen should be removed")
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
	raw := "Skills:\n--------\nPython\n========\nDjango"
	got := textutil.NormalizeForIngest(raw)
	assert.NotContains(t, got, "--------", "decoration runs should be collapsed")
	assert.NotContains(t, got, "========", "decoration runs should be collapsed")
	assert.Contains(t, got, "Skills")
	assert.Contains(t, got, "Python")
	assert.Contains(t, got, "Django")
}

func TestNormalizeForIngest_DropsZeroWidthBOM(t *testing.T) {
	// BOM (U+FEFF) often appears at the start of UTF-8 PDF extracts.
	raw := "\uFEFFNombre del candidato\nFICCT graduate"
	got := textutil.NormalizeForIngest(raw)
	assert.False(t, strings.ContainsRune(got, '\uFEFF'), "BOM should be removed")
	assert.Contains(t, got, "Nombre del candidato")
}

func TestSplitSentences_BasicSpanish(t *testing.T) {
	got := textutil.SplitSentences("Soy desarrollador backend. Trabajo con Django y AWS. Tengo cinco años de experiencia.")
	assert.Equal(t, 3, len(got))
	assert.Equal(t, "Soy desarrollador backend.", got[0])
	assert.Equal(t, "Trabajo con Django y AWS.", got[1])
	assert.Equal(t, "Tengo cinco años de experiencia.", got[2])
}

func TestClassify_HeaderOnlyChunk(t *testing.T) {
	kind, quality := chunker.Classify("EXPERIENCIA PROFESIONAL EDUCACIÓN HABILIDADES")
	assert.Equal(t, chunker.KindHeader, kind)
	assert.Equal(t, chunker.QualityLow, quality)
}

func TestClassify_ContactOnlyChunk(t *testing.T) {
	kind, quality := chunker.Classify("juan.perez@gmail.com   +591 70123456   https://linkedin.com/in/juanperez")
	assert.Equal(t, chunker.KindContact, kind)
	assert.Equal(t, chunker.QualityLow, quality)
}

func TestClassify_BodyHighQuality(t *testing.T) {
	kind, quality := chunker.Classify("Soy ingeniero de software con cinco años de experiencia construyendo APIs REST en Python y Django, además de integraciones con AWS S3 y Lambda.")
	assert.Equal(t, chunker.KindBody, kind)
	assert.Equal(t, chunker.QualityHigh, quality)
}

func TestChunker_SemanticBoundaries(t *testing.T) {
	text := "Soy desarrollador backend con experiencia en Python. " +
		"He construido APIs REST con Django y FastAPI. " +
		"Trabajo con AWS desde hace tres años. " +
		"Conozco PostgreSQL en profundidad. " +
		"Lidero un equipo pequeño desde 2023.\n\n" +
		"Educación: egresado de Ingeniería de Sistemas FICCT-UAGRM. " +
		"Diplomado en arquitecturas cloud."
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
	// A CV that opens with contact + section headers should produce
	// at least one chunk tagged as low-quality so retrieval can
	// de-rank it. Small chunk size so the contact/header block lands
	// in its own bucket rather than getting averaged in with prose.
	text := "Juan Perez Martinez. " +
		"juan.perez@gmail.com. " +
		"+591 70 12 34 56. " +
		"https://linkedin.com/in/juanperez.\n\n" +
		"EXPERIENCIA PROFESIONAL.\n\n" +
		"Soy ingeniero de software con cinco años de experiencia construyendo APIs " +
		"REST en Python y Django."
	c := chunker.NewChunker(20, 4)
	chunks := c.Chunk(domain.DocumentID("doc-cv-1"), text)
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
	assert.True(t, sawLow, "CV header/contact chunk should be tagged low-quality")
}
