package unit

import (
	"archive/zip"
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/Victoroide/quarry-rag/internal/extractor"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDetectFileType(t *testing.T) {
	tests := []struct {
		name     string
		filename string
		want     extractor.FileType
		wantErr  bool
	}{
		{"pdf", "document.pdf", extractor.FileTypePDF, false},
		{"docx", "guide.docx", extractor.FileTypeDOCX, false},
		{"doc", "old.doc", extractor.FileTypeDOC, false},
		{"txt unsupported", "data.txt", "", true},
		{"no extension", "noext", "", true},
		{"csv unsupported", "data.csv", "", true},
		{"uppercase pdf", "document.PDF", extractor.FileTypePDF, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := extractor.DetectFileType(tt.filename)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestPDFExtractor(t *testing.T) {
	// Minimal valid PDF with text content.
	pdfContent := buildMinimalPDF("Hello World from PDF extractor test document content here")
	ext := extractor.New(extractor.FileTypePDF)
	text, err := ext.Extract(context.Background(), bytes.NewReader(pdfContent))
	require.NoError(t, err)
	assert.NotEmpty(t, text)
	assert.True(t, len(text) >= 10, "extracted text should be at least 10 characters")
}

func TestDOCXExtractor(t *testing.T) {
	expected := "This is a test paragraph for DOCX extraction"
	docxData := buildMinimalDOCX(expected)
	ext := extractor.New(extractor.FileTypeDOCX)
	text, err := ext.Extract(context.Background(), bytes.NewReader(docxData))
	require.NoError(t, err)
	assert.Contains(t, text, "This is a test paragraph")
}

func TestExtractorShortTextError(t *testing.T) {
	// Text extractor with less than 10 chars should error.
	ext := extractor.New(extractor.FileTypeText)
	_, err := ext.Extract(context.Background(), strings.NewReader("Hi"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no readable text found")
}

func TestTextExtractor(t *testing.T) {
	ext := extractor.New(extractor.FileTypeText)
	text, err := ext.Extract(context.Background(), strings.NewReader("This is a sufficiently long text for extraction test."))
	require.NoError(t, err)
	assert.Equal(t, "This is a sufficiently long text for extraction test.", text)
}

// buildMinimalPDF creates a minimal valid PDF with the given text.
func buildMinimalPDF(text string) []byte {
	// Minimal PDF 1.4 with a single page containing text.
	var buf bytes.Buffer
	buf.WriteString("%PDF-1.4\n")

	// Object 1: Catalog
	obj1Offset := buf.Len()
	buf.WriteString("1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n")

	// Object 2: Pages
	obj2Offset := buf.Len()
	buf.WriteString("2 0 obj\n<< /Type /Pages /Kids [3 0 R] /Count 1 >>\nendobj\n")

	// Object 3: Page
	obj3Offset := buf.Len()
	buf.WriteString("3 0 obj\n<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>\nendobj\n")

	// Object 4: Content stream
	stream := "BT /F1 12 Tf 100 700 Td (" + text + ") Tj ET"
	obj4Offset := buf.Len()
	buf.WriteString("4 0 obj\n<< /Length " + itoa(len(stream)) + " >>\nstream\n" + stream + "\nendstream\nendobj\n")

	// Object 5: Font
	obj5Offset := buf.Len()
	buf.WriteString("5 0 obj\n<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>\nendobj\n")

	// Cross-reference table
	xrefOffset := buf.Len()
	buf.WriteString("xref\n0 6\n")
	buf.WriteString("0000000000 65535 f \n")
	buf.WriteString(padOffset(obj1Offset) + " 00000 n \n")
	buf.WriteString(padOffset(obj2Offset) + " 00000 n \n")
	buf.WriteString(padOffset(obj3Offset) + " 00000 n \n")
	buf.WriteString(padOffset(obj4Offset) + " 00000 n \n")
	buf.WriteString(padOffset(obj5Offset) + " 00000 n \n")

	// Trailer
	buf.WriteString("trailer\n<< /Size 6 /Root 1 0 R >>\n")
	buf.WriteString("startxref\n" + itoa(xrefOffset) + "\n%%EOF\n")

	return buf.Bytes()
}

func itoa(n int) string {
	return strings.TrimSpace(padOffset(n))
}

func padOffset(n int) string {
	s := strings.Repeat("0", 10) + strings.TrimSpace(intToStr(n))
	return s[len(s)-10:]
}

func intToStr(n int) string {
	if n == 0 {
		return "0"
	}
	result := ""
	for n > 0 {
		result = string(rune('0'+n%10)) + result
		n /= 10
	}
	return result
}

// buildMinimalDOCX creates a minimal valid DOCX (ZIP with word/document.xml).
func buildMinimalDOCX(text string) []byte {
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)

	xmlContent := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">
  <w:body>
    <w:p>
      <w:r>
        <w:t>` + text + `</w:t>
      </w:r>
    </w:p>
  </w:body>
</w:document>`

	f, _ := w.Create("word/document.xml")
	_, _ = f.Write([]byte(xmlContent))

	// Add required content types
	ct := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">
  <Default Extension="xml" ContentType="application/xml"/>
  <Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>
</Types>`
	f2, _ := w.Create("[Content_Types].xml")
	_, _ = f2.Write([]byte(ct))

	_ = w.Close()
	return buf.Bytes()
}
