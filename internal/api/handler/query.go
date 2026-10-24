package handler

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/Victoroide/go-ms-rag/internal/config"
	"github.com/Victoroide/go-ms-rag/internal/domain"
	"github.com/Victoroide/go-ms-rag/internal/llm"
	"github.com/Victoroide/go-ms-rag/internal/pipeline"
	"github.com/Victoroide/go-ms-rag/internal/retriever"
	"github.com/jackc/pgx/v5/pgxpool"
)

// QueryRequest is the POST /query request body.
//
// AllowedDocumentIDs is an optional tenant/role allowlist. When the caller
// supplies it (non-null), retrieval is restricted to those document ids BEFORE
// synthesis, so the LLM can never read — let alone summarize — documents the
// caller is not permitted to see. A null value means "no restriction"; an empty
// array matches nothing. The trusted caller supplies the authorization scope.
type QueryRequest struct {
	Query              string   `json:"query"`
	Mode               string   `json:"mode,omitempty"`
	FilterDocType      string   `json:"filter_doc_type,omitempty"`
	MaxChunks          int      `json:"max_chunks,omitempty"`
	AllowedDocumentIDs []string `json:"allowed_document_ids,omitempty"`
}

// ScoredChunkResponse is a single scored chunk in the query response.
// “kind“ and “quality“ are populated for chunks ingested with the
// classifier (body/contact/header × high/low) so downstream consumers
// can de-prioritize low-quality evidence without re-implementing the
// heuristics. Older chunks ingested before the classifier was added
// will leave these fields empty; callers must treat that as "unknown".
type ScoredChunkResponse struct {
	DocID      string  `json:"doc_id"`
	DocType    string  `json:"doc_type"`
	ChunkIndex int     `json:"chunk_index"`
	Score      float64 `json:"score"`
	Text       string  `json:"text"`
	Kind       string  `json:"kind,omitempty"`
	Quality    string  `json:"quality,omitempty"`
}

// QueryResponse is the POST /query response body.
type QueryResponse struct {
	Answer        string                `json:"answer"`
	Mode          string                `json:"mode"`
	ChunksUsed    int                   `json:"chunks_used"`
	ContextChunks int                   `json:"context_chunks,omitempty"`
	ContextTokens int                   `json:"context_tokens,omitempty"`
	EarlyExit     bool                  `json:"early_exit,omitempty"`
	StageTimings  map[string]int64      `json:"stage_timings_ms,omitempty"`
	TokenUsage    domain.TokenUsage     `json:"token_usage"`
	ScoredChunks  []ScoredChunkResponse `json:"scored_chunks"`
}

// APIError is a structured JSON error response.
type APIError struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}

func validateQueryRequest(req *QueryRequest) *APIError {
	if strings.TrimSpace(req.Query) == "" {
		return &APIError{Error: "validation", Message: "query is required"}
	}
	if len(req.Query) > 10000 {
		return &APIError{Error: "validation", Message: "query exceeds maximum length of 10000 characters"}
	}
	switch strings.ToLower(req.Mode) {
	case "", "auto", "local", "global":
	default:
		return &APIError{Error: "validation", Message: "mode must be auto, local or global"}
	}
	if req.MaxChunks < 0 || req.MaxChunks > 100 {
		return &APIError{Error: "validation", Message: "max_chunks must be between 0 and 100"}
	}
	return nil
}

// QueryHandler returns an HTTP handler for POST /query.
func QueryHandler(ret *retriever.Retriever, amb *llm.Ambassador, cfg *config.Config, pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !isJSONRequest(r) {
			writeJSONError(w, http.StatusUnsupportedMediaType, "validation", "Content-Type must be application/json")
			return
		}
		start := time.Now()
		var req QueryRequest
		if !decodeJSON(w, r, &req, 1<<20) {
			return
		}

		if apiErr := validateQueryRequest(&req); apiErr != nil {
			writeJSONError(w, http.StatusBadRequest, apiErr.Error, apiErr.Message)
			return
		}
		ctx := r.Context()

		qc := &pipeline.QueryContext{
			OriginalQuery:      req.Query,
			MaxChunks:          req.MaxChunks,
			RequestedMode:      req.Mode,
			FilterDocType:      req.FilterDocType,
			AllowedDocumentIDs: req.AllowedDocumentIDs,
		}

		stages := []pipeline.QueryStage{
			pipeline.NewDecomposeStage(pool),
			pipeline.NewRoutingStage(ret),
			pipeline.NewRetrievalStage(ret),
			pipeline.NewSynthesisStage(amb, cfg),
		}

		_, err := pipeline.RunRetrievalPipeline(ctx, qc, stages)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "pipeline", err.Error())
			return
		}

		scoredChunks := make([]ScoredChunkResponse, 0, len(qc.RetrievedChunks))
		for _, sc := range qc.RetrievedChunks {
			docType, kind, quality := "", "", ""
			if sc.Chunk.Metadata != nil {
				if dt, ok := sc.Chunk.Metadata["doc_type"]; ok {
					docType, _ = dt.(string)
				}
				if k, ok := sc.Chunk.Metadata["kind"]; ok {
					kind, _ = k.(string)
				}
				if q, ok := sc.Chunk.Metadata["quality"]; ok {
					quality, _ = q.(string)
				}
			}
			// Bound response snippets without splitting UTF-8 sequences.
			text := sc.Chunk.Text
			if runes := []rune(text); len(runes) > 600 {
				text = string(runes[:600])
			}

			scoredChunks = append(scoredChunks, ScoredChunkResponse{
				DocID:      string(sc.Chunk.DocumentID),
				DocType:    docType,
				ChunkIndex: sc.Chunk.Index,
				Score:      sc.Score,
				Text:       text,
				Kind:       kind,
				Quality:    quality,
			})
		}

		resp := QueryResponse{
			Answer:        qc.FinalAnswer,
			Mode:          qc.RetrievalMode,
			ChunksUsed:    len(qc.RetrievedChunks),
			ContextChunks: qc.ContextChunks,
			ContextTokens: qc.ContextTokens,
			EarlyExit:     qc.EarlyExit,
			StageTimings:  qc.StageTimingsMS,
			TokenUsage:    qc.TokenUsage,
			ScoredChunks:  scoredChunks,
		}

		log.Printf("query: mode=%s chunks=%d duration=%s", qc.RetrievalMode, len(qc.RetrievedChunks), time.Since(start))

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			log.Printf("query: encode response: %v", err)
		}
	}
}

func writeJSONError(w http.ResponseWriter, code int, errType, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	resp := APIError{Error: errType, Message: message}
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		log.Printf("error: encode error response: %v", err)
	}
}
