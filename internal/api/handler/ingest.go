package handler

import (
	"encoding/json"
	"log"
	"net/http"

	"github.com/Victoroide/go-ms-rag/internal/chunker"
	"github.com/Victoroide/go-ms-rag/internal/domain"
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
// Checks content fingerprint before processing. Returns 200 with
// "already_ingested" status for duplicate content.
func IngestHandler(w *worker.Worker, cs domain.ChunkStore) http.HandlerFunc {
	return func(rw http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Content-Type") != "application/json" {
			writeJSONError(rw, http.StatusUnsupportedMediaType, "validation", "Content-Type must be application/json")
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

		// Check document fingerprint to skip duplicate ingestion.
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

		if !w.Enqueue(task) {
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
}
