package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/Victoroide/quarry-rag/internal/domain"
	"github.com/google/uuid"
)

// DocumentStatusHandler returns a document's ingestion summary.
func DocumentStatusHandler(cs domain.ChunkStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := uuid.Parse(r.PathValue("doc_id"))
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "validation", "doc_id must be a valid UUID")
			return
		}
		lister, ok := cs.(domain.DocumentLister)
		if !ok {
			writeJSONError(w, http.StatusServiceUnavailable, "unavailable", "document status is unavailable")
			return
		}
		doc, err := lister.GetDocumentSummary(r.Context(), domain.DocumentID(id.String()))
		if err != nil {
			slog.ErrorContext(r.Context(), "look up document summary", "document_id", id, "error", err)
			writeJSONError(w, http.StatusInternalServerError, "internal", "failed to look up document")
			return
		}
		if doc == nil {
			writeJSONError(w, http.StatusNotFound, "not_found", "document not found")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(doc); err != nil {
			slog.ErrorContext(r.Context(), "encode document summary", "error", err)
		}
	}
}
