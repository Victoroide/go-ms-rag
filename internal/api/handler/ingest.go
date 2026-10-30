package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"log/slog"
	"net/http"
	"strings"

	"github.com/Victoroide/quarry-rag/internal/domain"
	"github.com/Victoroide/quarry-rag/internal/extractor"
	s3store "github.com/Victoroide/quarry-rag/internal/store/s3"
	"github.com/Victoroide/quarry-rag/internal/worker"
)

// JSON escaping can expand each content byte sixfold; reserve room for metadata.
const ingestJSONBodyLimit = 6*domain.MaxContentBytes + 2<<20

// IngestRequest is the POST /ingest request body.
//
// DocumentID is optional. Supplied UUIDs become the persisted document identity;
// malformed values are rejected. Omitted values generate a fresh UUID.
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

// IngestHandler returns an HTTP handler for POST /ingest.
// Supports two content types:
//   - application/json: existing JSON body with "content" field
//   - multipart/form-data: file upload with text extraction
func IngestHandler(wk *worker.Worker, cs domain.ChunkStore, s3Client *s3store.Client, maxUploadBytes int64, opts ...IngestOption) http.HandlerFunc {
	options := ingestOptions{extract: func(ctx context.Context, ft extractor.FileType, data []byte) (string, error) {
		return extractor.New(ft).Extract(ctx, bytes.NewReader(data))
	}}
	for _, opt := range opts {
		opt(&options)
	}
	return func(rw http.ResponseWriter, r *http.Request) {
		ct := r.Header.Get("Content-Type")
		if strings.HasPrefix(ct, "multipart/form-data") {
			handleFileIngest(rw, r, wk, cs, s3Client, maxUploadBytes, options)
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

	docID, proceed := checkIngestIdentity(rw, r, cs, req.DocumentID, req.Content)
	if !proceed {
		return
	}

	task := worker.IngestTask{
		DocumentID:     domain.DocumentID(docID),
		CallerSupplied: req.DocumentID != "",
		Content:        req.Content,
		Metadata:       req.Metadata,
		DocType:        req.DocType,
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

func handleFileIngest(rw http.ResponseWriter, r *http.Request, wk *worker.Worker, cs domain.ChunkStore, s3Client *s3store.Client, maxUploadBytes int64, opts ingestOptions) {
	if opts.limiter != nil {
		release, err := opts.limiter.Acquire(r.Context())
		if err != nil {
			rw.Header().Set("Retry-After", "5")
			writeJSONError(rw, http.StatusServiceUnavailable, "busy", "too many concurrent uploads")
			return
		}
		defer release()
	}
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
	text, extractErr := opts.extract(r.Context(), ft, fileData)
	if extractErr != nil {
		slog.ErrorContext(r.Context(), "extract uploaded text", "file_name", header.Filename, "error", extractErr)
		status, message := http.StatusUnprocessableEntity, extractErr.Error()
		switch {
		case errors.Is(extractErr, extractor.ErrExtractionTimeout):
			message = "document extraction exceeded the time limit"
		case errors.Is(extractErr, extractor.ErrExtractionFailed):
			message = "document could not be parsed"
		case errors.Is(extractErr, extractor.ErrExtractionUnavailable):
			status, message = http.StatusServiceUnavailable, "document extraction is unavailable"
		}
		writeJSONError(rw, status, "extraction", message)
		return
	}

	suppliedID := r.FormValue("document_id")
	docID, proceed := checkIngestIdentity(rw, r, cs, suppliedID, text)
	if !proceed {
		return
	}

	// Read optional doc_type form field.
	formDocType := r.FormValue("doc_type")

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
		DocumentID:     domain.DocumentID(docID),
		CallerSupplied: suppliedID != "",
		Content:        text,
		Metadata:       map[string]any{"source_file": fileName},
		FileName:       &fileName,
		FileType:       &ftStr,
		S3Key:          s3Key,
		DocType:        formDocType,
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
