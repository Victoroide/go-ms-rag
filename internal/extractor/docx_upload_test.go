package extractor_test

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Victoroide/quarry-rag/internal/api/handler"
	"github.com/Victoroide/quarry-rag/internal/domain"
)

type officeUploadDuplicateStore struct{ domain.ChunkStore }

func (officeUploadDuplicateStore) CheckFingerprint(context.Context, string) (domain.DocumentID, bool, error) {
	return "existing-document", true, nil
}

func TestOversizedDOCXUploadReturns422(t *testing.T) {
	var file bytes.Buffer
	archive := zip.NewWriter(&file)
	document, err := archive.Create("word/document.xml")
	if err != nil {
		t.Fatal(err)
	}
	xml := `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:p><w:r><w:t>` + strings.Repeat("x", domain.MaxContentBytes+1) + `</w:t></w:r></w:p></w:body></w:document>`
	if _, writeErr := document.Write([]byte(xml)); writeErr != nil {
		t.Fatal(writeErr)
	}
	if closeErr := archive.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "archive.docx")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(file.Bytes()); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/ingest", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rec := httptest.NewRecorder()
	handler.IngestHandler(nil, officeUploadDuplicateStore{}, nil, 20<<20)(rec, req)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("unexpected status: %d %s", rec.Code, rec.Body)
	}
	var response handler.APIError
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Error != "extraction" {
		t.Fatalf("unexpected error class: %+v", response)
	}
}
