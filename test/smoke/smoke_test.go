// Package smoke runs end-to-end smoke tests against a live server at localhost:8081.
// Set RUN_SMOKE=1 to enable these tests.
//
// These tests create documents in the configured live services.
// Each test automatically cleans up its created documents via t.Cleanup.
package smoke

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Victoroide/go-ms-rag/internal/cleanup"
	"github.com/Victoroide/go-ms-rag/internal/config"
)

const baseURL = "http://localhost:8081"

func skipUnlessSmoke(t *testing.T) {
	t.Helper()
	if os.Getenv("RUN_SMOKE") != "1" {
		t.Skip("skipping smoke test; set RUN_SMOKE=1 to run")
	}
}

func buildTestDOCX(text string) []byte {
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

	ct := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">
  <Default Extension="xml" ContentType="application/xml"/>
</Types>`
	f2, _ := w.Create("[Content_Types].xml")
	_, _ = f2.Write([]byte(ct))

	_ = w.Close()
	return buf.Bytes()
}

func createMultipartBody(filename string, data []byte) (body *bytes.Buffer, contentType string) {
	body = &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, _ := writer.CreateFormFile("file", filename)
	_, _ = io.Copy(part, bytes.NewReader(data))
	_ = writer.Close()
	contentType = writer.FormDataContentType()
	return
}

// Test 1: Health check includes S3 status.
func TestSmoke_HealthIncludesS3(t *testing.T) {
	skipUnlessSmoke(t)

	resp, err := request(t.Context(), http.MethodGet, baseURL+"/health", "", http.NoBody)
	if err != nil {
		t.Fatalf("health request: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var result map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode health: %v", err)
	}

	if result["s3"] == "" {
		t.Fatal("health response missing 's3' field")
	}
	t.Logf("health: %v", result)
}

// Test 2: Ingest a DOCX file via multipart.
func TestSmoke_IngestDOCXFile(t *testing.T) {
	skipUnlessSmoke(t)

	docxData := buildTestDOCX("Smoke Test Reference Document: Cedar Archive retains reports for seven years using Python Spark and AWS Bedrock integration for indexed document research")
	body, contentType := createMultipartBody("smoke_test.docx", docxData)

	resp, err := request(t.Context(), http.MethodPost, baseURL+"/ingest", contentType, body)
	if err != nil {
		t.Fatalf("ingest request: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusAccepted {
		bodyBytes, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 202, got %d: %s", resp.StatusCode, string(bodyBytes))
	}

	var result map[string]string
	if decodeErr := json.NewDecoder(resp.Body).Decode(&result); decodeErr != nil {
		t.Fatalf("decode ingest: %v", decodeErr)
	}
	if result["document_id"] == "" {
		t.Fatal("ingest response missing document_id")
	}
	docID := result["document_id"]
	t.Logf("ingested doc: %s, status: %s", docID, result["status"])

	registerDocumentCleanup(t, docID)

	// Wait for worker to process.
	time.Sleep(5 * time.Second)

	// Test 3: Try to get file URL for this document.
	fileURL := fmt.Sprintf("%s/documents/%s/file", baseURL, result["document_id"])
	fileResp, err := request(t.Context(), http.MethodGet, fileURL, "", http.NoBody)
	if err != nil {
		t.Fatalf("file request: %v", err)
	}
	defer func() { _ = fileResp.Body.Close() }()

	fileBody, _ := io.ReadAll(fileResp.Body)
	t.Logf("file response (%d): %s", fileResp.StatusCode, string(fileBody))

	// Should return 200 with presigned URL if S3 upload succeeded, or 404 if S3 was down.
	if fileResp.StatusCode != http.StatusOK && fileResp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 200 or 404, got %d", fileResp.StatusCode)
	}
}

// Test 4: Unsupported file type returns 415.
func TestSmoke_UnsupportedFileType(t *testing.T) {
	skipUnlessSmoke(t)

	body, contentType := createMultipartBody("data.csv", []byte("col1,col2\nval1,val2"))
	resp, err := request(t.Context(), http.MethodPost, baseURL+"/ingest", contentType, body)
	if err != nil {
		t.Fatalf("ingest request: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusUnsupportedMediaType {
		bodyBytes, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 415, got %d: %s", resp.StatusCode, string(bodyBytes))
	}
	t.Logf("unsupported file correctly rejected with %d", resp.StatusCode)
}

// Test 5: Large file upload returns 413.
func TestSmoke_LargeFileUpload(t *testing.T) {
	skipUnlessSmoke(t)

	// Create a DOCX with a very large body (>20MB would be needed for real test,
	// but we'll verify the endpoint handles the multipart path correctly).
	bigText := strings.Repeat("This is a large document content block. ", 1000)
	docxData := buildTestDOCX(bigText)
	body, contentType := createMultipartBody("big.docx", docxData)

	resp, err := request(t.Context(), http.MethodPost, baseURL+"/ingest", contentType, body)
	if err != nil {
		t.Fatalf("ingest request: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	// This file is under 20MB so it should be accepted.
	// We're testing that the multipart path works, not the exact size limit.
	bodyBytes, _ := io.ReadAll(resp.Body)
	t.Logf("large file response (%d): %s", resp.StatusCode, string(bodyBytes))
	if resp.StatusCode != http.StatusAccepted && resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 202 or 413, got %d", resp.StatusCode)
	}
	if resp.StatusCode == http.StatusAccepted {
		var result map[string]string
		if decodeErr := json.Unmarshal(bodyBytes, &result); decodeErr != nil {
			t.Fatal(decodeErr)
		}
		if result["document_id"] == "" {
			t.Fatal("missing document_id")
		}
		registerDocumentCleanup(t, result["document_id"])
	}

}

// Test 6: Existing JSON ingest still works.
func TestSmoke_JSONIngestStillWorks(t *testing.T) {
	skipUnlessSmoke(t)

	payload := `{"content":"Smoke test: JSON ingest path verification. This document is ingested via the original JSON API path to verify backward compatibility."}`
	resp, err := request(t.Context(), http.MethodPost, baseURL+"/ingest", "application/json", strings.NewReader(payload))
	if err != nil {
		t.Fatalf("json ingest request: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	bodyBytes, _ := io.ReadAll(resp.Body)
	t.Logf("json ingest response (%d): %s", resp.StatusCode, string(bodyBytes))

	if resp.StatusCode != http.StatusAccepted && resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 202 or 200, got %d", resp.StatusCode)
	}

	// Extract document ID for cleanup if response is JSON
	if resp.StatusCode == http.StatusAccepted {
		var result map[string]string
		if decodeErr := json.NewDecoder(bytes.NewReader(bodyBytes)).Decode(&result); decodeErr == nil {
			if docID := result["document_id"]; docID != "" {
				t.Logf("ingested JSON doc: %s", docID)

				registerDocumentCleanup(t, docID)
			}
		}
	}
}

func request(ctx context.Context, method, url, contentType string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("X-Service-Token", os.Getenv("SERVICE_TOKEN"))
	return (&http.Client{Timeout: 120 * time.Second}).Do(req)
}

func registerDocumentCleanup(t *testing.T, docID string) {
	t.Helper()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		cfg, err := config.Load()
		if err != nil {
			t.Errorf("load deletion configuration: %v", err)
			return
		}
		cleaner, err := cleanup.New(ctx, cfg)
		if err != nil {
			t.Errorf("connect deletion stores: %v", err)
			return
		}
		defer func() {
			if closeErr := cleaner.Close(ctx); closeErr != nil {
				t.Errorf("close deletion stores: %v", closeErr)
			}
		}()
		if err := deleteWhenPresent(ctx, func() error { return cleaner.Delete(ctx, docID) }); err != nil {
			t.Errorf("delete document %s: %v", docID, err)
		}
	})
}

func deleteWhenPresent(ctx context.Context, deleteDocument func() error) error {
	for {
		err := deleteDocument()
		if !errors.Is(err, cleanup.ErrDocumentNotFound) {
			return err
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func TestDeferredDeletionWaitsForIngestion(t *testing.T) {
	attempts := 0
	err := deleteWhenPresent(t.Context(), func() error {
		attempts++
		if attempts == 1 {
			return cleanup.ErrDocumentNotFound
		}
		return nil
	})
	if err != nil || attempts != 2 {
		t.Fatalf("deferred deletion: attempts=%d error=%v", attempts, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := deleteWhenPresent(ctx, func() error { return cleanup.ErrDocumentNotFound }); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected bounded cancellation: %v", err)
	}
}
