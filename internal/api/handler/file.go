package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/Victoroide/quarry-rag/internal/domain"
	s3store "github.com/Victoroide/quarry-rag/internal/store/s3"
	"github.com/google/uuid"
)

// FileResponse is the GET /documents/{doc_id}/file response body.
type FileResponse struct {
	URL       string `json:"url"`
	ExpiresIn int    `json:"expires_in"`
}

// FileHandler returns an HTTP handler for GET /documents/{doc_id}/file.
func FileHandler(cs domain.ChunkStore, s3Client *s3store.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		docIDStr := r.PathValue("doc_id")

		if _, err := uuid.Parse(docIDStr); err != nil {
			writeJSONError(w, http.StatusBadRequest, "validation", "doc_id must be a valid UUID")
			return
		}

		if s3Client == nil {
			writeJSONError(w, http.StatusNotFound, "not_found", "file storage is not configured")
			return
		}

		docID := domain.DocumentID(docIDStr)
		s3Key, err := cs.GetDocumentS3Key(r.Context(), docID)
		if err != nil {
			slog.ErrorContext(r.Context(), "get document file", "document_id", docID, "error", err)
			writeJSONError(w, http.StatusInternalServerError, "internal", "failed to look up document")
			return
		}
		if s3Key == nil {
			writeJSONError(w, http.StatusNotFound, "not_found", "no file stored for this document")
			return
		}

		presignedURL, err := s3Client.PresignGet(r.Context(), *s3Key)
		if err != nil {
			slog.ErrorContext(r.Context(), "presign document file", "s3_key", *s3Key, "error", err)
			writeJSONError(w, http.StatusInternalServerError, "internal", "failed to generate download URL")
			return
		}

		resp := FileResponse{
			URL:       presignedURL,
			ExpiresIn: 900,
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			slog.ErrorContext(r.Context(), "encode file response", "error", err)
		}
	}
}
