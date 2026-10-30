// Package pipeline implements the decomposed ingestion and retrieval
// pipeline as a sequence of discrete, composable stages.
package pipeline

import (
	"context"
	"fmt"
	"time"

	"github.com/Victoroide/quarry-rag/internal/domain"
)

// StageID identifies a pipeline stage.
type StageID string

// Stage identifiers label pipeline execution results.
const (
	StageChunk              StageID = "chunk"
	StageEmbed              StageID = "embed"
	StagePersist            StageID = "persist"
	StageQueryDecomposition StageID = "query_decomposition"
	StageRetrievalRouting   StageID = "retrieval_routing"
	StageRetrieval          StageID = "retrieval"
	StageSynthesis          StageID = "synthesis"
)

// StageResult carries the output of a single pipeline stage and any
// error that terminated execution.
type StageResult struct {
	StageID StageID
	Err     error
}

// IngestContext accumulates state as a document flows through
// the ingestion pipeline stages.
type IngestContext struct {
	DocumentID domain.DocumentID
	RawText    string
	Chunks     []domain.Chunk
	Metadata   map[string]any
	FileName   *string
	FileType   *string
	S3Key      *string
	DocType    string
}

// NewIngestContext creates an initialized IngestContext.
func NewIngestContext(docID domain.DocumentID, text string, metadata map[string]any) *IngestContext {
	return &IngestContext{
		DocumentID: docID,
		RawText:    text,
		Metadata:   metadata,
	}
}

// QueryContext accumulates state as a query flows through
// the retrieval pipeline stages.
type QueryContext struct {
	OriginalQuery string
	SubQueries    []string
	MaxChunks     int
	RequestedMode string
	FilterDocType string
	// AllowedDocumentIDs, when non-nil, restricts retrieved chunks to this
	// allowlist BEFORE synthesis (tenant/role scoping). Nil = no restriction;
	// non-nil empty = matches nothing.
	AllowedDocumentIDs []string

	// Populated by routing stage.
	NamedEntity      string
	EntityDocumentID string
	LocalDocumentID  string
	Candidates       []domain.ScoredChunk
	SkipRetrieval    bool

	// Populated by routing stage.
	RetrievalMode  string
	QueryEmbedding []float32
	TopScore       float64

	// Populated by retrieval stage.
	RetrievedChunks []domain.ScoredChunk

	// Populated by synthesis stage.
	FinishReason  string
	FinalAnswer   string
	TokenUsage    domain.TokenUsage
	ContextTokens int  // estimated synthesis input tokens (after compression)
	ContextChunks int  // chunks actually sent to the LLM (after adaptive trim)
	EarlyExit     bool // true if synthesis skipped (cache hit / no chunks)

	// Per-stage timing in milliseconds — observability for tail-latency
	// analysis. Filled by each stage and surfaced in the query response.
	StageTimingsMS map[string]int64
}

// Stage is the interface for a single ingestion pipeline stage.
type Stage interface {
	ID() StageID
	Run(ctx context.Context, ic *IngestContext) error
}

// QueryStage is the interface for a single retrieval pipeline stage.
type QueryStage interface {
	ID() StageID
	Run(ctx context.Context, qc *QueryContext) error
}

// RunIngestionPipeline executes the ingestion stages in order, short-circuiting
// on the first error.
func RunIngestionPipeline(ctx context.Context, ic *IngestContext, stages []Stage) ([]StageResult, error) {
	results := make([]StageResult, 0, len(stages))
	for _, s := range stages {
		if err := ctx.Err(); err != nil {
			return results, fmt.Errorf("pipeline: context canceled before stage %s: %w", s.ID(), err)
		}
		err := s.Run(ctx, ic)
		results = append(results, StageResult{StageID: s.ID(), Err: err})
		if err != nil {
			return results, fmt.Errorf("pipeline: stage %s failed: %w", s.ID(), err)
		}
	}
	return results, nil
}

// RunRetrievalPipeline executes the retrieval stages in order, short-circuiting
// on the first error. Records per-stage wall-clock time into
// qc.StageTimingsMS so callers can attribute tail-latency costs.
func RunRetrievalPipeline(ctx context.Context, qc *QueryContext, stages []QueryStage) ([]StageResult, error) {
	if qc.StageTimingsMS == nil {
		qc.StageTimingsMS = make(map[string]int64, len(stages))
	}
	results := make([]StageResult, 0, len(stages))
	for _, s := range stages {
		if err := ctx.Err(); err != nil {
			return results, fmt.Errorf("pipeline: context canceled before stage %s: %w", s.ID(), err)
		}
		t0 := time.Now()
		err := s.Run(ctx, qc)
		qc.StageTimingsMS[string(s.ID())] = time.Since(t0).Milliseconds()
		results = append(results, StageResult{StageID: s.ID(), Err: err})
		if err != nil {
			return results, fmt.Errorf("pipeline: stage %s failed: %w", s.ID(), err)
		}
		// Early-exit hook: a stage may set qc.EarlyExit=true to skip the
		// remaining stages (used by retrieval-cache hits).
		if qc.EarlyExit {
			break
		}
	}
	return results, nil
}
