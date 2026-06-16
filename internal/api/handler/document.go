package handler

import (
	"encoding/json"
	"log"
	"net/http"

	"github.com/Victoroide/go-ms-rag/internal/domain"
	s3store "github.com/Victoroide/go-ms-rag/internal/store/s3"
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
		if err != nil || doc == nil {
			writeJSONError(w, http.StatusNotFound, "not_found", "document not found")
			return
		}

		// 2. Delete all vectors in Pinecone namespace = doc_id.
		if pcDriver != nil {
			if delErr := pcDriver.DeleteAllVectorsInNamespace(ctx, docIDStr); delErr != nil {
				log.Printf("delete: pinecone namespace %s: %v", docIDStr, delErr)
			}
		}

		// 3. Delete vectors from default namespace where metadata doc_id = docIDStr.
		if pcDriver != nil {
			if delErr := pcDriver.DeleteByDocID(ctx, docIDStr); delErr != nil {
				log.Printf("delete: pinecone default namespace doc_id=%s: %v", docIDStr, delErr)
			}
		}

		// 4. Delete from S3 if s3_key is not null.
		if s3Client != nil && doc.S3Key != nil {
			if delErr := s3Client.Delete(ctx, *doc.S3Key); delErr != nil {
				log.Printf("delete: s3 key %s: %v", *doc.S3Key, delErr)
			}
		}

		// 5. Delete from Postgres (cascades to chunks and fingerprints).
		if err := cs.DeleteDocument(ctx, docID); err != nil {
			log.Printf("delete: postgres document %s: %v", docIDStr, err)
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
			log.Printf("delete: encode response: %v", err)
		}
	}
}

// ListDocumentsHandler returns every stored document reference (id, doc_type,
// s3_key). The backend reconciliation command diffs this against its active rows
// to find orphaned vectors. Service-token protected; emits no content/PII.
func ListDocumentsHandler(cs domain.ChunkStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		docs, err := cs.ListDocuments(r.Context())
		if err != nil {
			log.Printf("list documents: %v", err)
			writeJSONError(w, http.StatusInternalServerError, "internal", "failed to list documents")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{"documents": docs, "count": len(docs)}); err != nil {
			log.Printf("list documents: encode response: %v", err)
		}
	}
}
