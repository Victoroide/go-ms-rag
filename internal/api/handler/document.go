package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/Victoroide/quarry-rag/internal/cleanup"

	"github.com/Victoroide/quarry-rag/internal/domain"
	s3store "github.com/Victoroide/quarry-rag/internal/store/s3"
	"github.com/google/uuid"
)

// DeleteResponse is the DELETE /documents/{doc_id} response body.
type DeleteResponse struct {
	Deleted bool   `json:"deleted"`
	DocID   string `json:"doc_id"`
}

// DeleteHandler returns an HTTP handler for DELETE /documents/{doc_id}.
func DeleteHandler(cs domain.ChunkStore, pcDriver domain.VectorDeleter, s3Client *s3store.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		docIDStr := r.PathValue("doc_id")

		if _, err := uuid.Parse(docIDStr); err != nil {
			writeJSONError(w, http.StatusBadRequest, "validation", "doc_id must be a valid UUID")
			return
		}

		ctx := r.Context()
		docID := domain.DocumentID(docIDStr)

		// 1. Check document exists and get s3_key.
		doc, err := cs.GetDocument(ctx, docID)
		if err != nil {
			slog.ErrorContext(ctx, "look up document", "document_id", docIDStr, "error", err)
			writeJSONError(w, http.StatusInternalServerError, "internal", "failed to look up document")
			return
		}
		if doc == nil {
			writeJSONError(w, http.StatusNotFound, "not_found", "document not found")
			return
		}

		var objects cleanup.ObjectDeleter
		if s3Client != nil {
			objects = s3Client
		}
		if err := cleanup.Remove(ctx, cs, pcDriver, objects, doc); err != nil {
			slog.ErrorContext(ctx, "delete document", "document_id", docIDStr, "error", err)
			writeJSONError(w, http.StatusInternalServerError, "internal", "failed to delete document")
			return
		}

		resp := DeleteResponse{
			Deleted: true,
			DocID:   docIDStr,
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			slog.ErrorContext(ctx, "encode delete response", "error", err)
		}
	}
}

// ListDocumentsHandler returns every stored document reference (id, doc_type,
// s3_key) for reconciliation with caller-owned records. Service-token protected;
// emits no document content.
func ListDocumentsHandler(cs domain.ChunkStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		docs, err := cs.ListDocuments(r.Context())
		if err != nil {
			slog.ErrorContext(r.Context(), "list documents", "error", err)
			writeJSONError(w, http.StatusInternalServerError, "internal", "failed to list documents")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{"documents": docs, "count": len(docs)}); err != nil {
			slog.ErrorContext(r.Context(), "encode document list", "error", err)
		}
	}
}
