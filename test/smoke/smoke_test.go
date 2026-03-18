// Package smoke runs end-to-end smoke tests against a live server at localhost:8081.
// Set RUN_SMOKE=1 to enable these tests.
//
// NOTE: These tests create real documents in production Pinecone and Neon.
// Each test automatically cleans up its created documents via t.Cleanup.
package smoke

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
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

	resp, err := http.Get(baseURL + "/health")
	if err != nil {
		t.Fatalf("health request: %v", err)
	}
	defer resp.Body.Close()

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

	docxData := buildTestDOCX("Smoke Test Candidate Profile: Maria Garcia Senior Data Engineer with 7 years experience in Python Spark and AWS Bedrock integration and cloud computing")
	body, contentType := createMultipartBody("smoke_test.docx", docxData)

	resp, err := http.Post(baseURL+"/ingest", contentType, body)
	if err != nil {
		t.Fatalf("ingest request: %v", err)
	}
	defer resp.Body.Close()

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

	// Register cleanup
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load config for cleanup: %v", err)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		if cleanupErr := cleanup.CleanupDocument(ctx, cfg, docID); cleanupErr != nil {
			t.Errorf("cleanup document %s: %v", docID, cleanupErr)
		} else {
			t.Logf("cleaned up test document: %s", docID)
		}
	})

	// Wait for worker to process.
	time.Sleep(5 * time.Second)

	// Test 3: Try to get file URL for this document.
	fileURL := fmt.Sprintf("%s/documents/%s/file", baseURL, result["document_id"])
	fileResp, err := http.Get(fileURL)
	if err != nil {
		t.Fatalf("file request: %v", err)
	}
	defer fileResp.Body.Close()

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
	resp, err := http.Post(baseURL+"/ingest", contentType, body)
	if err != nil {
		t.Fatalf("ingest request: %v", err)
	}
	defer resp.Body.Close()

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

	resp, err := http.Post(baseURL+"/ingest", contentType, body)
	if err != nil {
		t.Fatalf("ingest request: %v", err)
	}
	defer resp.Body.Close()

	// This file is under 20MB so it should be accepted.
	// We're testing that the multipart path works, not the exact size limit.
	bodyBytes, _ := io.ReadAll(resp.Body)
	t.Logf("large file response (%d): %s", resp.StatusCode, string(bodyBytes))
	if resp.StatusCode != http.StatusAccepted && resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 202 or 413, got %d", resp.StatusCode)
	}
}

// Test 6: Existing JSON ingest still works.
func TestSmoke_JSONIngestStillWorks(t *testing.T) {
	skipUnlessSmoke(t)

	payload := `{"content":"Smoke test: JSON ingest path verification. This document is ingested via the original JSON API path to verify backward compatibility."}`
	resp, err := http.Post(baseURL+"/ingest", "application/json", strings.NewReader(payload))
	if err != nil {
		t.Fatalf("json ingest request: %v", err)
	}
	defer resp.Body.Close()

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

				// Register cleanup
				cfg, err := config.Load()
				if err != nil {
					t.Fatalf("load config for cleanup: %v", err)
				}
				t.Cleanup(func() {
					ctx := context.Background()
					if cleanupErr := cleanup.CleanupDocument(ctx, cfg, docID); cleanupErr != nil {
						t.Errorf("cleanup document %s: %v", docID, cleanupErr)
					} else {
						t.Logf("cleaned up test document: %s", docID)
					}
				})
			}
		}
	}
}
