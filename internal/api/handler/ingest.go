package handler

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log"
	"log/slog"
	"net/http"
	"strings"

	"github.com/Victoroide/go-ms-rag/internal/chunker"
	"github.com/Victoroide/go-ms-rag/internal/domain"
	"github.com/Victoroide/go-ms-rag/internal/extractor"
	s3store "github.com/Victoroide/go-ms-rag/internal/store/s3"
	"github.com/Victoroide/go-ms-rag/internal/worker"
	"github.com/google/uuid"
)

// JSON escaping can expand each content byte sixfold; reserve room for metadata.
const ingestJSONBodyLimit = 6*domain.MaxContentBytes + 2<<20

// IngestRequest is the POST /ingest request body.
//
// DocumentID is optional. When the caller supplies a valid UUID it becomes
// the persisted document identity (Pinecone namespace + metadata doc_id +
// Postgres documents.id). Callers may keep their own primary key as the
// canonical reference. Omitted or malformed values generate a fresh UUID.
type IngestRequest struct {
	DocumentID string         `json:"document_id,omitempty"`
	Content    string         `json:"content"`
	Metadata   map[string]any `json:"metadata,omitempty"`
	DocType    string         `json:"doc_type,omitempty"`
}

// IngestResponse is the POST /ingest response body.
type IngestResponse struct {
	DocumentID string `json:"document_id"`
	Status     string `json:"status"`
}

func validateIngestRequest(req *IngestRequest) *APIError {
	if strings.TrimSpace(req.Content) == "" {
		return &APIError{Error: "validation", Message: "content is required"}
	}
	if len(req.Content) > domain.MaxContentBytes {
		return &APIError{Error: "validation", Message: "content exceeds maximum length of 1000000 bytes"}
	}
	return nil
}

// resolveDocumentID returns the caller's UUID when supplied and valid;
// otherwise a freshly generated one. This preserves callers that don't
// send an id while letting upstream systems carry their own primary key
// through Pinecone/Postgres.
func resolveDocumentID(supplied string) string {
	if supplied != "" {
		if parsed, err := uuid.Parse(supplied); err == nil {
			return parsed.String()
		}
	}
	return uuid.New().String()
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
	if !isJSONRequest(r) {
		writeJSONError(rw, http.StatusUnsupportedMediaType, "validation", "Content-Type must be application/json or multipart/form-data")
		return
	}
	var req IngestRequest
	if !decodeJSON(rw, r, &req, ingestJSONBodyLimit) {
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

	docID := resolveDocumentID(req.DocumentID)
	task := worker.IngestTask{
		DocumentID: domain.DocumentID(docID),
		Content:    req.Content,
		Metadata:   req.Metadata,
		DocType:    req.DocType,
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

	defer func() {
		if r.MultipartForm != nil {
			if err := r.MultipartForm.RemoveAll(); err != nil {
				log.Printf("ingest: remove multipart temporary files: %v", err)
			}
		}
	}()
	file, header, err := r.FormFile("file")
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeJSONError(rw, http.StatusRequestEntityTooLarge, "validation", "request body exceeds maximum upload size")
			return
		}
		writeJSONError(rw, http.StatusBadRequest, "validation", "file field is required")
		return
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			log.Printf("ingest: close file: %v", closeErr)
		}
	}()

	ft, err := extractor.DetectFileType(header.Filename)
	if err != nil {
		writeJSONError(rw, http.StatusUnsupportedMediaType, "validation", "unsupported file type, accepted: pdf, docx, doc")
		return
	}

	// Read the entire file into memory for extraction and S3 upload.
	fileData, err := io.ReadAll(file)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeJSONError(rw, http.StatusRequestEntityTooLarge, "validation", "request body exceeds maximum upload size")
			return
		}
		writeJSONError(rw, http.StatusBadRequest, "validation", "failed to read file")
		return
	}

	// Extract text from the file.
	ext := extractor.New(ft)
	text, extractErr := ext.Extract(r.Context(), bytes.NewReader(fileData))
	if extractErr != nil {
		slog.ErrorContext(r.Context(), "extract uploaded text", "file_name", header.Filename, "error", extractErr)
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

	// Read optional doc_type form field.
	formDocType := r.FormValue("doc_type")

	docID := uuid.New().String()
	ftStr := string(ft)
	fileName := header.Filename

	var s3Key *string
	if s3Client != nil {
		docType := formDocType
		if docType == "" {
			docType = domain.DefaultDocType
		}

		key, uploadErr := s3Client.Upload(r.Context(), docID, docType, fileName, ft, bytes.NewReader(fileData))
		if uploadErr != nil {
			slog.ErrorContext(r.Context(), "upload source file", "file_name", fileName, "error", uploadErr)
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
		DocType:    formDocType,
	}
	if !wk.Enqueue(task) {
		if s3Key != nil {
			if err := s3Client.Delete(r.Context(), *s3Key); err != nil {
				log.Printf("ingest: rollback upload: %v", err)
			}
		}
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
