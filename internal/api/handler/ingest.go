package handler

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"log/slog"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/Victoroide/quarry-rag/internal/chunker"
	"github.com/Victoroide/quarry-rag/internal/domain"
	"github.com/Victoroide/quarry-rag/internal/extractor"
	"github.com/Victoroide/quarry-rag/internal/worker"
	"github.com/google/uuid"
)

// ObjectStore stores and removes original uploaded files.
type ObjectStore interface {
	Upload(context.Context, string, string, string, extractor.FileType, io.Reader) (string, error)
	Delete(context.Context, string) error
}

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
	var apiErr *APIError
	req.DocType, apiErr = validateDocType(req.DocType)
	if apiErr != nil {
		return apiErr
	}
	return validateMetadata(req.Metadata)
}

// IngestHandler returns an HTTP handler for POST /ingest.
// Supports two content types:
//   - application/json: existing JSON body with "content" field
//   - multipart/form-data: file upload with text extraction
func IngestHandler(wk *worker.Worker, cs domain.ChunkStore, s3Client ObjectStore, maxUploadBytes int64, opts ...IngestOption) http.HandlerFunc {
	options := ingestOptions{maxPending: 1000, extract: func(ctx context.Context, ft extractor.FileType, data []byte) (string, error) {
		return extractor.New(ft).Extract(ctx, bytes.NewReader(data))
	}}
	for _, opt := range opts {
		opt(&options)
	}
	return func(rw http.ResponseWriter, r *http.Request) {
		mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err == nil && mediaType == "multipart/form-data" {
			handleFileIngest(rw, r, wk, cs, s3Client, maxUploadBytes, options)
			return
		}
		handleJSONIngest(rw, r, wk, cs, options)
	}
}

func handleJSONIngest(rw http.ResponseWriter, r *http.Request, wk *worker.Worker, cs domain.ChunkStore, opts ingestOptions) {
	if !isJSONRequest(r) {
		writeJSONError(rw, http.StatusUnsupportedMediaType, "validation", "Content-Type must be application/json or multipart/form-data")
		return
	}
	var req IngestRequest
	if !decodeJSON(rw, r, &req, ingestJSONBodyLimit) {
		return
	}

	req.Content = strings.ReplaceAll(req.Content, "\x00", "")
	if apiErr := validateIngestRequest(&req); apiErr != nil {
		writeJSONError(rw, http.StatusBadRequest, apiErr.Error, apiErr.Message)
		return
	}

	docID, ok := ingestDocumentID(rw, req.DocumentID)
	if !ok {
		return
	}
	_, _ = acceptIngest(rw, r, wk, cs, domain.AcceptRequest{Document: domain.Document{ID: domain.DocumentID(docID), Content: req.Content, Metadata: req.Metadata}, CallerSupplied: req.DocumentID != "", DocType: req.DocType, MaxPending: opts.maxPending})
}

func handleFileIngest(rw http.ResponseWriter, r *http.Request, wk *worker.Worker, cs domain.ChunkStore, s3Client ObjectStore, maxUploadBytes int64, opts ingestOptions) {
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

	text = strings.ReplaceAll(text, "\x00", "")
	suppliedID := r.FormValue("document_id")
	docID, ok := ingestDocumentID(rw, suppliedID)
	if !ok {
		return
	}
	formDocType := r.FormValue("doc_type")
	ftStr, fileName := string(ft), header.Filename
	metadata := make(map[string]any)
	if values, present := r.MultipartForm.Value["metadata"]; present {
		if len(values) != 1 || decodeJSONObject([]byte(values[0]), &metadata) != nil {
			writeJSONError(rw, http.StatusBadRequest, "validation", "metadata must be a JSON object without duplicate keys or trailing data")
			return
		}
		if apiErr := validateMetadata(metadata); apiErr != nil {
			writeJSONError(rw, http.StatusBadRequest, apiErr.Error, apiErr.Message)
			return
		}
	}
	metadata["source_file"] = fileName
	req := IngestRequest{Content: text, DocType: formDocType, Metadata: metadata}
	if apiErr := validateIngestRequest(&req); apiErr != nil {
		writeJSONError(rw, http.StatusBadRequest, apiErr.Error, apiErr.Message)
		return
	}
	formDocType = req.DocType
	var s3Key *string
	if s3Client != nil {
		existingID, exists, lookupErr := cs.CheckFingerprint(r.Context(), chunker.Fingerprint(text))
		if lookupErr != nil {
			writeJSONError(rw, http.StatusServiceUnavailable, "unavailable", "document store unavailable")
			return
		}
		if !exists || (suppliedID != "" && string(existingID) != docID) {
			docType := formDocType
			if docType == "" {
				docType = domain.DefaultDocType
			}
			// Each attempt owns a distinct object so rollback cannot delete another acceptance's file.
			key, uploadErr := s3Client.Upload(r.Context(), docID+"/"+uuid.NewString(), docType, fileName, ft, bytes.NewReader(fileData))
			if uploadErr != nil {
				slog.ErrorContext(r.Context(), "upload source file", "error", uploadErr)
				rw.Header().Set("Retry-After", "30")
				writeJSONError(rw, http.StatusServiceUnavailable, "storage", "source file storage is unavailable")
				return
			}
			s3Key = &key
		}
	}
	outcome, acceptErr := acceptIngest(rw, r, wk, cs, domain.AcceptRequest{Document: domain.Document{ID: domain.DocumentID(docID), Content: text, Metadata: metadata, FileName: &fileName, FileType: &ftStr, S3Key: s3Key}, CallerSupplied: suppliedID != "", DocType: formDocType, MaxPending: opts.maxPending})
	if s3Key != nil && outcome != domain.AcceptAccepted {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 10*time.Second)
		defer cancel()
		if acceptErr != nil {
			storedKey, lookupErr := cs.GetDocumentS3Key(cleanupCtx, domain.DocumentID(docID))
			if lookupErr != nil || (storedKey != nil && *storedKey == *s3Key) {
				slog.WarnContext(cleanupCtx, "retain source file after uncertain acceptance", "document_id", docID, "error", lookupErr)
				return
			}
		}
		if err := s3Client.Delete(cleanupCtx, *s3Key); err != nil {
			slog.ErrorContext(cleanupCtx, "remove unaccepted source file", "error", err)
		}
	}
}
