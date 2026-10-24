package extractor_test

import (
	"bytes"
	"compress/zlib"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Victoroide/go-ms-rag/internal/api/handler"
	"github.com/Victoroide/go-ms-rag/internal/extractor"
)

func generatedPDF(t *testing.T, stream string, compressed bool, unicodeMap ...string) []byte {
	t.Helper()
	data := []byte(stream)
	filter := ""
	if compressed {
		var encoded bytes.Buffer
		writer := zlib.NewWriter(&encoded)
		if _, err := writer.Write(data); err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		data = encoded.Bytes()
		filter = " /Filter /FlateDecode"
	}
	objects := [][]byte{
		[]byte("<< /Type /Catalog /Pages 2 0 R >>"),
		[]byte("<< /Type /Pages /Kids [3 0 R] /Count 1 >>"),
		[]byte("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>"),
		append(append([]byte(fmt.Sprintf("<< /Length %d%s >>\nstream\n", len(data), filter)), data...), []byte("\nendstream")...),
		[]byte("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>"),
	}
	if len(unicodeMap) > 0 {
		objects[4] = []byte("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /ToUnicode 6 0 R >>")
		objects = append(objects, []byte(fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(unicodeMap[0]), unicodeMap[0])))
	}
	var out bytes.Buffer
	out.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objects))
	for i, object := range objects {
		offsets[i] = out.Len()
		fmt.Fprintf(&out, "%d 0 obj\n%s\nendobj\n", i+1, object)
	}
	xref := out.Len()
	fmt.Fprintf(&out, "xref\n0 %d\n0000000000 65535 f \n", len(objects)+1)
	for _, offset := range offsets {
		fmt.Fprintf(&out, "%010d 00000 n \n", offset)
	}
	fmt.Fprintf(&out, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objects)+1, xref)
	return out.Bytes()
}
func TestPDFRejectsExtractedTextAboveContentLimit(t *testing.T) {
	data := generatedPDF(t, "BT /F1 12 Tf ("+strings.Repeat("x", 1_000_001)+") Tj ET", false)
	_, err := extractor.New(extractor.FileTypePDF).Extract(t.Context(), bytes.NewReader(data))
	if err == nil || !strings.Contains(err.Error(), "maximum") {
		t.Fatalf("oversized extracted text: %v", err)
	}
}
func TestPDFBoundsInflatedContentBeforeParsing(t *testing.T) {
	data := generatedPDF(t, strings.Repeat(" ", 9<<20), true)
	_, err := extractor.New(extractor.FileTypePDF).Extract(t.Context(), bytes.NewReader(data))
	if err == nil || !strings.Contains(err.Error(), "maximum decoded") {
		t.Fatalf("oversized decoded stream: %v", err)
	}
}

func TestPDFContentLimitReturnsExtractionError(t *testing.T) {
	data := generatedPDF(t, "BT /F1 12 Tf ("+strings.Repeat("x", 1_000_001)+") Tj ET", true)
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "archive.pdf")
	if err != nil {
		t.Fatal(err)
	}
	if _, writeErr := part.Write(data); writeErr != nil {
		t.Fatal(writeErr)
	}
	if closeErr := writer.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/ingest", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rec := httptest.NewRecorder()
	handler.IngestHandler(nil, nil, nil, 20<<20)(rec, req)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("unexpected status: %d %s", rec.Code, rec.Body)
	}
	var response handler.APIError
	if decodeErr := json.Unmarshal(rec.Body.Bytes(), &response); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	if response.Error != "extraction" {
		t.Fatalf("unexpected error class: %+v", response)
	}
}

func TestPDFFontMapWorkIsBounded(t *testing.T) {
	mapping := "1 begincodespacerange <00> <ff> endcodespacerange\n10000 beginbfchar endbfchar"
	data := generatedPDF(t, "BT /F1 12 Tf (Archive operating hours.) Tj ET", false, mapping)
	_, err := extractor.New(extractor.FileTypePDF).Extract(t.Context(), bytes.NewReader(data))
	if err == nil || !strings.Contains(err.Error(), "maximum font map") {
		t.Fatalf("excessive mapping work accepted: %v", err)
	}
}
