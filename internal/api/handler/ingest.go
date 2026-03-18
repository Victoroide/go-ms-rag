package handler

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/Victoroide/go-ms-rag/internal/chunker"
	"github.com/Victoroide/go-ms-rag/internal/domain"
	"github.com/Victoroide/go-ms-rag/internal/extractor"
	s3store "github.com/Victoroide/go-ms-rag/internal/store/s3"
	"github.com/Victoroide/go-ms-rag/internal/worker"
	"github.com/google/uuid"
)

// IngestRequest is the POST /ingest request body.
type IngestRequest struct {
	Content  string         `json:"content"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

// IngestResponse is the POST /ingest response body.
type IngestResponse struct {
	DocumentID string `json:"document_id"`
	Status     string `json:"status"`
}

func validateIngestRequest(req *IngestRequest) *APIError {
	if req.Content == "" {
		return &APIError{Error: "validation", Message: "content is required"}
	}
	if len(req.Content) > 1_000_000 {
		return &APIError{Error: "validation", Message: "content exceeds maximum length of 1000000 characters"}
	}
	return nil
}

// IngestHandler returns an HTTP handler for POST /ingest.
// Supports two content types:
//   - application/json: existing JSON body with "content" field
//   - multipart/form-data: file upload with text extraction
func IngestHandler(wk *worker.Worker, cs domain.ChunkStore, s3Client *s3store.Client, maxUploadBytes int64) http.HandlerFunc {
	return func(rw http.ResponseWriter, r *http.Request) {
		ct := r.Header.Get("Content-Type")
		if strings.HasPrefix(ct, "multipart/form-data") {
			handleFileIngest(rw, r, wk, cs, s3Client, maxUploadBytes)
			return
		}
		handleJSONIngest(rw, r, wk, cs)
	}
}

func handleJSONIngest(rw http.ResponseWriter, r *http.Request, wk *worker.Worker, cs domain.ChunkStore) {
	if r.Header.Get("Content-Type") != "application/json" {
		writeJSONError(rw, http.StatusUnsupportedMediaType, "validation", "Content-Type must be application/json or multipart/form-data")
		return
	}
	var req IngestRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(rw, http.StatusBadRequest, "validation", "invalid JSON body")
		return
	}
	if apiErr := validateIngestRequest(&req); apiErr != nil {
		writeJSONError(rw, http.StatusBadRequest, apiErr.Error, apiErr.Message)
		return
	}

	fp := chunker.Fingerprint(req.Content)
	existingDocID, exists, err := cs.CheckFingerprint(r.Context(), fp)
	if err != nil {
		log.Printf("ingest: fingerprint check: %v", err)
	}
	if exists {
		resp := IngestResponse{
			DocumentID: string(existingDocID),
			Status:     "already_ingested",
		}
		rw.Header().Set("Content-Type", "application/json")
		rw.WriteHeader(http.StatusOK)
		if err := json.NewEncoder(rw).Encode(resp); err != nil {
			log.Printf("ingest: encode duplicate response: %v", err)
		}
		return
	}

	docID := uuid.New().String()
	task := worker.IngestTask{
		DocumentID: domain.DocumentID(docID),
		Content:    req.Content,
		Metadata:   req.Metadata,
	}
	if !wk.Enqueue(task) {
		writeJSONError(rw, http.StatusServiceUnavailable, "queue", "ingest queue is full")
		return
	}

	resp := IngestResponse{
		DocumentID: docID,
		Status:     "processing",
	}
	rw.Header().Set("Content-Type", "application/json")
	rw.WriteHeader(http.StatusAccepted)
	if err := json.NewEncoder(rw).Encode(resp); err != nil {
		log.Printf("ingest: encode response: %v", err)
	}
}

func handleFileIngest(rw http.ResponseWriter, r *http.Request, wk *worker.Worker, cs domain.ChunkStore, s3Client *s3store.Client, maxUploadBytes int64) {
	r.Body = http.MaxBytesReader(rw, r.Body, maxUploadBytes)

	file, header, err := r.FormFile("file")
	if err != nil {
		if err.Error() == "http: request body too large" {
			writeJSONError(rw, http.StatusRequestEntityTooLarge, "validation", "file exceeds maximum size of 20MB")
			return
		}
		writeJSONError(rw, http.StatusBadRequest, "validation", "file field is required")
		return
	}
	defer file.Close()

	ft, err := extractor.DetectFileType(header.Filename)
	if err != nil {
		writeJSONError(rw, http.StatusUnsupportedMediaType, "validation", "unsupported file type, accepted: pdf, docx, doc")
		return
	}

	// Read the entire file into memory for extraction and S3 upload.
	fileData, err := io.ReadAll(file)
	if err != nil {
		if strings.Contains(err.Error(), "http: request body too large") {
			writeJSONError(rw, http.StatusRequestEntityTooLarge, "validation", "file exceeds maximum size of 20MB")
			return
		}
		writeJSONError(rw, http.StatusBadRequest, "validation", "failed to read file")
		return
	}

	// Extract text from the file.
	ext := extractor.New(ft)
	text, extractErr := ext.Extract(r.Context(), bytes.NewReader(fileData))
	if extractErr != nil {
		log.Printf("ingest: extract text from %s: %v", header.Filename, extractErr)
		writeJSONError(rw, http.StatusUnprocessableEntity, "extraction", extractErr.Error())
		return
	}

	// Check fingerprint for dedup.
	fp := chunker.Fingerprint(text)
	existingDocID, exists, fpErr := cs.CheckFingerprint(r.Context(), fp)
	if fpErr != nil {
		log.Printf("ingest: fingerprint check: %v", fpErr)
	}
	if exists {
		resp := IngestResponse{
			DocumentID: string(existingDocID),
			Status:     "already_ingested",
		}
		rw.Header().Set("Content-Type", "application/json")
		rw.WriteHeader(http.StatusOK)
		if err := json.NewEncoder(rw).Encode(resp); err != nil {
			log.Printf("ingest: encode duplicate response: %v", err)
		}
		return
	}

	docID := uuid.New().String()
	ftStr := string(ft)
	fileName := header.Filename

	// Upload to S3 in background via the task metadata.
	// S3 upload is done by the worker for consistency.
	var s3Key *string
	if s3Client != nil {
		// Detect doc_type for S3 key path.
		docType := "candidate"
		if strings.Contains(strings.ToLower(text), "job opening:") {
			docType = "job"
		}
		key, uploadErr := s3Client.Upload(r.Context(), docID, docType, fileName, ft, bytes.NewReader(fileData))
		if uploadErr != nil {
			log.Printf("ingest: s3 upload %s: %v", fileName, uploadErr)
			// Non-fatal: continue without S3.
		} else {
			s3Key = &key
		}
	}

	task := worker.IngestTask{
		DocumentID: domain.DocumentID(docID),
		Content:    text,
		Metadata:   map[string]any{"source_file": fileName},
		FileName:   &fileName,
		FileType:   &ftStr,
		S3Key:      s3Key,
	}
	if !wk.Enqueue(task) {
		writeJSONError(rw, http.StatusServiceUnavailable, "queue", "ingest queue is full")
		return
	}

	resp := IngestResponse{
		DocumentID: docID,
		Status:     "processing",
	}
	rw.Header().Set("Content-Type", "application/json")
	rw.WriteHeader(http.StatusAccepted)
	if err := json.NewEncoder(rw).Encode(resp); err != nil {
		log.Printf("ingest: encode response: %v", err)
	}
}
