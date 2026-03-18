package handler

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"

	"github.com/Victoroide/go-ms-rag/internal/domain"
	s3store "github.com/Victoroide/go-ms-rag/internal/store/s3"
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
		// Extract doc_id from path: /documents/{doc_id}/file
		pathParts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		if len(pathParts) < 3 {
			writeJSONError(w, http.StatusBadRequest, "validation", "invalid path")
			return
		}
		docIDStr := pathParts[1]

		if _, err := uuid.Parse(docIDStr); err != nil {
			writeJSONError(w, http.StatusBadRequest, "validation", "doc_id must be a valid UUID")
			return
		}

		docID := domain.DocumentID(docIDStr)
		s3Key, err := cs.GetDocumentS3Key(r.Context(), docID)
		if err != nil {
			log.Printf("file: get s3 key for %s: %v", docID, err)
			writeJSONError(w, http.StatusInternalServerError, "internal", "failed to look up document")
			return
		}
		if s3Key == nil {
			writeJSONError(w, http.StatusNotFound, "not_found", "no file stored for this document")
			return
		}

		presignedURL, err := s3Client.PresignGet(r.Context(), *s3Key)
		if err != nil {
			log.Printf("file: presign for %s: %v", *s3Key, err)
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
			log.Printf("file: encode response: %v", err)
		}
	}
}
