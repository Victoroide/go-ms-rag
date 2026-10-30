package handler

import (
	"encoding/json"
	"log"
	"log/slog"
	"net/http"

	"github.com/Victoroide/quarry-rag/internal/chunker"
	"github.com/Victoroide/quarry-rag/internal/domain"
	"github.com/google/uuid"
)

func checkIngestIdentity(rw http.ResponseWriter, r *http.Request, cs domain.ChunkStore, supplied, content string) (string, bool) {
	if supplied != "" {
		parsed, err := uuid.Parse(supplied)
		if err != nil {
			writeJSONError(rw, http.StatusBadRequest, "validation", "document_id must be a UUID")
			return "", false
		}
		id := parsed.String()
		doc, err := cs.GetDocument(r.Context(), domain.DocumentID(id))
		if err != nil {
			slog.ErrorContext(r.Context(), "ingest document lookup", "error", err)
			writeJSONError(rw, http.StatusServiceUnavailable, "unavailable", "document store unavailable")
			return "", false
		}
		if doc == nil {
			return id, true
		}
		if chunker.Fingerprint(doc.Content) != chunker.Fingerprint(content) {
			writeJSONError(rw, http.StatusConflict, "conflict", "document_id already exists with different content")
			return "", false
		}
		writeAlreadyIngested(rw, id)
		return "", false
	}

	id, exists, err := cs.CheckFingerprint(r.Context(), chunker.Fingerprint(content))
	if err != nil {
		slog.ErrorContext(r.Context(), "ingest fingerprint lookup", "error", err)
		writeJSONError(rw, http.StatusServiceUnavailable, "unavailable", "document store unavailable")
		return "", false
	}
	if exists {
		writeAlreadyIngested(rw, string(id))
		return "", false
	}
	return uuid.NewString(), true
}

func writeAlreadyIngested(rw http.ResponseWriter, id string) {
	rw.Header().Set("Content-Type", "application/json")
	rw.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(rw).Encode(IngestResponse{DocumentID: id, Status: "already_ingested"}); err != nil {
		log.Printf("ingest: encode duplicate response: %v", err)
	}
}
