package handler

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/Victoroide/quarry-rag/internal/domain"
	"github.com/Victoroide/quarry-rag/internal/extractor"
)

func detectionUpload(t *testing.T, filename string, data []byte) *http.Request {
	t.Helper()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	part, err := w.CreateFormFile("file", filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/ingest", &body)
	r.Header.Set("Content-Type", w.FormDataContentType())
	return r
}

func detectionError(t *testing.T, rec *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if rec.Code != status || body["code"] != code || body["error"] != "extraction" || body["message"] == "" {
		t.Fatalf("status=%d body=%s; want status=%d code=%s", rec.Code, rec.Body, status, code)
	}
}

func TestExtensionContentMismatchIs415(t *testing.T) {
	for _, filename := range []string{"report.docx", "report.doc", "report.txt"} {
		t.Run(filename, func(t *testing.T) {
			rec := httptest.NewRecorder()
			IngestHandler(nil, nil, nil, 20<<20, WithExtractor(func(context.Context, extractor.FileType, []byte) (string, error) {
				t.Error("mismatched content reached extraction")
				return "", errors.New("parser details")
			}))(rec, detectionUpload(t, filename, []byte("%PDF-1.4\n")))
			detectionError(t, rec, 415, "type_mismatch")
		})
	}
}

func TestBinaryNamedTxtIsRejected(t *testing.T) {
	for _, filename := range []string{"binary.txt", "binary", "binary.bin"} {
		rec := httptest.NewRecorder()
		IngestHandler(nil, nil, nil, 20<<20)(rec, detectionUpload(t, filename, []byte{0, 1, 0xff, 0x80}))
		detectionError(t, rec, 415, "unsupported_type")
	}
}

type detectionAcceptStore struct {
	domain.ChunkStore
	accepted domain.AcceptRequest
}

func (s *detectionAcceptStore) AcceptDocument(_ context.Context, req domain.AcceptRequest) (domain.AcceptResult, error) {
	s.accepted = req
	return domain.AcceptResult{Outcome: domain.AcceptAccepted, DocumentID: req.Document.ID}, nil
}

func TestUTF16TextUploadIsDecoded(t *testing.T) {
	for _, bigEndian := range []bool{false, true} {
		var order binary.AppendByteOrder = binary.LittleEndian
		data := []byte{0xff, 0xfe}
		if bigEndian {
			order, data = binary.BigEndian, []byte{0xfe, 0xff}
		}
		for _, unit := range utf16.Encode([]rune("  Document text 世界 🌍\x00\n")) {
			data = order.AppendUint16(data, unit)
		}
		store := &detectionAcceptStore{}
		rec := httptest.NewRecorder()
		IngestHandler(nil, store, nil, 20<<20, WithExtractor(func(context.Context, extractor.FileType, []byte) (string, error) {
			t.Error("plain text reached the document sandbox")
			return "", extractor.ErrExtractionFailed
		}))(rec, detectionUpload(t, "notes.md", data))
		if rec.Code != 202 || store.accepted.Document.Content != "Document text 世界 🌍" || store.accepted.Document.FileType == nil || *store.accepted.Document.FileType != "text" {
			t.Fatalf("status=%d body=%s document=%+v", rec.Code, rec.Body, store.accepted.Document)
		}
	}
}

func TestTextUploadsWithoutKnownExtension(t *testing.T) {
	for _, filename := range []string{"notes", "notes.unknown", "notes.TXT", "notes.markdown"} {
		store := &detectionAcceptStore{}
		rec := httptest.NewRecorder()
		IngestHandler(nil, store, nil, 20<<20)(rec, detectionUpload(t, filename, []byte("\xef\xbb\xbfText upload contents\x00")))
		// A NUL outside UTF-16 disqualifies a file as plain text.
		detectionError(t, rec, 415, "unsupported_type")
		rec = httptest.NewRecorder()
		IngestHandler(nil, store, nil, 20<<20)(rec, detectionUpload(t, filename, []byte("\xef\xbb\xbfText upload contents")))
		if rec.Code != 202 || store.accepted.Document.Content != "Text upload contents" {
			t.Fatalf("status=%d body=%s document=%+v", rec.Code, rec.Body, store.accepted.Document)
		}
	}
}

func TestExtractionErrorsUseStableCodes(t *testing.T) {
	for _, tt := range []struct {
		err    error
		code   string
		status int
	}{
		{extractor.ErrContentTooLarge, "too_large", 422},
		{extractor.ErrExtractionTimeout, "timeout", 422},
		{context.DeadlineExceeded, "timeout", 422},
		{extractor.ErrExtractionFailed, "unparseable", 422},
		{extractor.ErrNoText, "no_text", 422},
		{extractor.ErrEncryptedDocument, "encrypted", 422},
		{extractor.ErrUnsupportedDocumentVersion, "unsupported_version", 422},
		{extractor.ErrExtractionUnavailable, "unavailable", 503},
	} {
		t.Run("wrapped "+tt.code, func(t *testing.T) {
			rec := httptest.NewRecorder()
			IngestHandler(nil, nil, nil, 20<<20, WithExtractor(func(context.Context, extractor.FileType, []byte) (string, error) {
				return "", errors.Join(tt.err, errors.New("private parser details"))
			}))(rec, detectionUpload(t, "report.pdf", []byte("%PDF-1.4\n")))
			detectionError(t, rec, tt.status, tt.code)
			if strings.Contains(rec.Body.String(), "private") {
				t.Fatalf("parser details exposed: %s", rec.Body)
			}
		})
	}
	for _, tt := range []struct {
		message, code string
		status        int
	}{
		{"extracted text exceeds 1000000 bytes", "too_large", 422},
		{"extractor: extraction timed out", "timeout", 422},
		{"context deadline exceeded", "timeout", 422},
		{"extractor: document could not be parsed", "unparseable", 422},
		{"extractor: no readable text found", "no_text", 422},
		{"extractor: doc: encrypted document is not supported", "encrypted", 422},
		{"extractor: doc: unsupported document version", "unsupported_version", 422},
		{"extractor: extraction is unavailable", "unavailable", 503},
		{"parser details: sensitive offset 12345", "unparseable", 422},
	} {
		t.Run(tt.code+tt.message, func(t *testing.T) {
			for _, wrapped := range []bool{false, true} {
				err := errors.New(tt.message)
				if wrapped {
					err = fmt.Errorf("extractor: pdf: %w", err)
				}
				rec := httptest.NewRecorder()
				IngestHandler(nil, nil, nil, 20<<20, WithExtractor(func(context.Context, extractor.FileType, []byte) (string, error) { return "", err }))(rec, detectionUpload(t, "report.pdf", []byte("%PDF-1.4\n")))
				detectionError(t, rec, tt.status, tt.code)
				if strings.Contains(rec.Body.String(), "sensitive") || strings.Contains(rec.Body.String(), "extractor:") {
					t.Fatalf("parser error exposed: %s", rec.Body)
				}
			}
		})
	}
}

func TestScannedPDFReportsNoText(t *testing.T) {
	var pdf bytes.Buffer
	pdf.WriteString("%PDF-1.4\n")
	objects := []string{"<< /Type /Catalog /Pages 2 0 R >>", "<< /Type /Pages /Kids [3 0 R] /Count 1 >>", "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] >>"}
	offsets := make([]int, len(objects))
	for i, object := range objects {
		offsets[i] = pdf.Len()
		fmt.Fprintf(&pdf, "%d 0 obj\n%s\nendobj\n", i+1, object)
	}
	xref := pdf.Len()
	fmt.Fprintf(&pdf, "xref\n0 4\n0000000000 65535 f \n")
	for _, offset := range offsets {
		fmt.Fprintf(&pdf, "%010d 00000 n \n", offset)
	}
	fmt.Fprintf(&pdf, "trailer\n<< /Size 4 /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", xref)
	rec := httptest.NewRecorder()
	IngestHandler(nil, nil, nil, 20<<20)(rec, detectionUpload(t, "scanned.pdf", pdf.Bytes()))
	detectionError(t, rec, 422, "no_text")
}
