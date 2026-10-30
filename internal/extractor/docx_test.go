package extractor

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Victoroide/quarry-rag/internal/domain"
)

func docxBytes(t *testing.T, xml string) []byte {
	t.Helper()
	var out bytes.Buffer
	writer := zip.NewWriter(&out)
	file, err := writer.Create("word/document.xml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte(xml)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}
func TestDOCXPreservesTextAcrossFormattingRuns(t *testing.T) {
	data := docxBytes(t, `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:p><w:r><w:t>Document pre</w:t></w:r><w:r><w:t>servation requires </w:t></w:r><w:r><w:t>stable storage.</w:t></w:r></w:p></w:body></w:document>`)
	got, err := New(FileTypeDOCX).Extract(t.Context(), bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if got != "Document preservation requires stable storage." {
		t.Fatalf("formatting changed text: %q", got)
	}
}
func TestExtractorsHonorCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for _, format := range []FileType{FileTypeText, FileTypeDOC, FileTypeDOCX, FileTypePDF} {
		_, err := New(format).Extract(ctx, strings.NewReader("Sufficiently long source text."))
		if !errors.Is(err, context.Canceled) {
			t.Errorf("%s: got %v want cancellation", format, err)
		}
	}
}
func TestDOCXRejectsExcessiveExpansion(t *testing.T) {
	data := docxBytes(t, `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:p><w:r><w:t>`+strings.Repeat("x", (16<<20)+1)+`</w:t></w:r></w:p></w:body></w:document>`)
	_, err := New(FileTypeDOCX).Extract(t.Context(), bytes.NewReader(data))
	if err == nil {
		t.Fatal("oversized document XML accepted")
	}
	if !strings.Contains(err.Error(), "maximum") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDOCXRejectsTextAboveContentLimit(t *testing.T) {
	for name, paragraphs := range map[string]string{
		"single run":           `<w:p><w:r><w:t>` + strings.Repeat("x", domain.MaxContentBytes+1) + `</w:t></w:r></w:p>`,
		"formatting runs":      `<w:p>` + strings.Repeat(`<w:r><w:t>`+strings.Repeat("x", 1000)+`</w:t></w:r>`, 1001) + `</w:p>`,
		"paragraph separators": strings.Repeat(`<w:p><w:r><w:t>`+strings.Repeat("x", 1000)+`</w:t></w:r></w:p>`, 1000),
		"tabs":                 `<w:p><w:r><w:t>` + strings.Repeat("x", domain.MaxContentBytes-1) + `</w:t><w:tab/><w:t>x</w:t></w:r></w:p>`,
		"breaks":               `<w:p><w:r><w:t>` + strings.Repeat("x", domain.MaxContentBytes-1) + `</w:t><w:br/><w:t>x</w:t></w:r></w:p>`,
		"carriage returns":     `<w:p><w:r><w:t>` + strings.Repeat("x", domain.MaxContentBytes-1) + `</w:t><w:cr/><w:t>x</w:t></w:r></w:p>`,
		"unicode":              `<w:p><w:r><w:t>` + strings.Repeat("界", domain.MaxContentBytes/3+1) + `</w:t></w:r></w:p>`,
	} {
		t.Run(name, func(t *testing.T) {
			data := docxBytes(t, `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>`+paragraphs+`</w:body></w:document>`)
			_, err := New(FileTypeDOCX).Extract(t.Context(), bytes.NewReader(data))
			if !errors.Is(err, ErrContentTooLarge) {
				t.Fatalf("expected the text limit, got %v", err)
			}
		})
	}
}

func TestDOCXAcceptsTextAtContentLimit(t *testing.T) {
	data := docxBytes(t, `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:p><w:r><w:t>`+strings.Repeat("x", domain.MaxContentBytes)+`</w:t></w:r></w:p></w:body></w:document>`)
	text, err := New(FileTypeDOCX).Extract(t.Context(), bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if len(text) != domain.MaxContentBytes {
		t.Fatalf("returned %d text bytes", len(text))
	}
}

func TestDOCXStopsAtTextLimitBeforeMalformedXML(t *testing.T) {
	data := docxBytes(t, `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:p><w:r><w:t>`+strings.Repeat("x", domain.MaxContentBytes+1)+`</w:t></w:r></w:p><`)
	_, err := New(FileTypeDOCX).Extract(t.Context(), bytes.NewReader(data))
	if !errors.Is(err, ErrContentTooLarge) {
		t.Fatalf("expected the text limit before the XML error, got %v", err)
	}
}
