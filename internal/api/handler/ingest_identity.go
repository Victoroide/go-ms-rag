package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/Victoroide/quarry-rag/internal/chunker"
	"github.com/Victoroide/quarry-rag/internal/domain"
	"github.com/Victoroide/quarry-rag/internal/worker"
	"github.com/google/uuid"
)

func ingestDocumentID(rw http.ResponseWriter, supplied string) (string, bool) {
	if supplied == "" {
		return uuid.NewString(), true
	}
	parsed, err := uuid.Parse(supplied)
	if err != nil {
		writeJSONError(rw, http.StatusBadRequest, "validation", "document_id must be a UUID")
		return "", false
	}
	return parsed.String(), true
}

func acceptIngest(rw http.ResponseWriter, r *http.Request, wk *worker.Worker, cs domain.ChunkStore, req domain.AcceptRequest) (domain.AcceptOutcome, error) {
	acceptor, ok := cs.(domain.DocumentAcceptor)
	if !ok {
		writeJSONError(rw, http.StatusServiceUnavailable, "unavailable", "document store unavailable")
		return domain.AcceptConflict, errors.New("durable acceptance is unavailable")
	}
	req.Fingerprint = chunker.Fingerprint(req.Document.Content)
	acceptCtx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 10*time.Second)
	defer cancel()
	result, err := acceptor.AcceptDocument(acceptCtx, req)
	if err != nil {
		slog.ErrorContext(r.Context(), "accept document", "error", err)
		writeJSONError(rw, http.StatusServiceUnavailable, "unavailable", "document store unavailable")
		return domain.AcceptConflict, err
	}
	code, state := http.StatusAccepted, "processing"
	switch result.Outcome {
	case domain.AcceptAccepted, domain.AcceptInProgress, domain.AcceptRequeued:
		if wk != nil {
			wk.Notify()
		}
	case domain.AcceptDuplicate:
		code, state = http.StatusOK, "already_ingested"
	case domain.AcceptConflict:
		writeJSONError(rw, http.StatusConflict, "conflict", "document_id already exists with different content")
		return result.Outcome, nil
	case domain.AcceptQueueFull:
		rw.Header().Set("Retry-After", "30")
		writeJSONError(rw, http.StatusServiceUnavailable, "queue", "ingest queue is full")
		return result.Outcome, nil
	default:
		writeJSONError(rw, http.StatusServiceUnavailable, "unavailable", "document store unavailable")
		return result.Outcome, nil
	}
	rw.Header().Set("Content-Type", "application/json")
	rw.WriteHeader(code)
	if err = json.NewEncoder(rw).Encode(IngestResponse{DocumentID: string(result.DocumentID), Status: state}); err != nil {
		slog.ErrorContext(r.Context(), "encode ingestion response", "error", err)
	}
	return result.Outcome, nil
}

func metadataHasNUL(value any) bool {
	switch v := value.(type) {
	case string:
		return strings.ContainsRune(v, 0)
	case map[string]any:
		for key, item := range v {
			if strings.ContainsRune(key, 0) || metadataHasNUL(item) {
				return true
			}
		}
	case []any:
		for _, item := range v {
			if metadataHasNUL(item) {
				return true
			}
		}
	}
	return false
}
