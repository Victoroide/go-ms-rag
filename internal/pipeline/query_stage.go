package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"github.com/Victoroide/go-ms-rag/internal/config"
	"github.com/Victoroide/go-ms-rag/internal/domain"
	"github.com/Victoroide/go-ms-rag/internal/llm"
	"github.com/Victoroide/go-ms-rag/internal/retriever"
	brtypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
)

const (
	queryDecompositionMaxOutput = 1024
	subQueryAnswerMaxOutput     = 2048
	answerSynthesisMaxOutput    = 4096
)

// DecomposeStage decomposes the original query into sub-queries using the LLM.
type DecomposeStage struct {
	ambassador *llm.Ambassador
	cfg        *config.Config
}

// NewDecomposeStage creates a DecomposeStage.
func NewDecomposeStage(amb *llm.Ambassador, cfg *config.Config) *DecomposeStage {
	return &DecomposeStage{ambassador: amb, cfg: cfg}
}

// ID returns the stage identifier.
func (s *DecomposeStage) ID() StageID { return StageQueryDecomposition }

// Run decomposes the original query into sub-queries.
func (s *DecomposeStage) Run(ctx context.Context, qc *QueryContext) error {
	queryLower := strings.ToLower(qc.OriginalQuery)

	// Check if query mentions a known named entity from DB.
	// We record the entity but always let the LLM decompose — the LLM
	// is authoritative for deciding single-entity vs multi-entity queries.
	namedEntities, err := FetchNamedEntitiesFromDB(ctx, s.cfg)
	if err != nil {
		log.Printf("WARNING: failed to fetch named entities: %v", err)
	} else {
		for entity := range namedEntities {
			if strings.Contains(queryLower, entity) {
				qc.NamedEntity = entity
				break
			}
		}
	}

	prompt := fmt.Sprintf(`You are a query decomposition engine. Decide whether to keep a query as-is or split it into sub-queries.

Rules:
1. If the query asks about ONE specific person, role, or document → keep as one sub-query.
2. If the query asks to compare, match, or evaluate MULTIPLE entities → split into sub-queries.
3. Queries that ask "which candidates fit X" or "match candidates to X" require retrieving BOTH the role AND all candidates → always split.

Examples:
- "What are Laura's skills?" → {"sub_queries": ["What are Laura's skills?"]}
- "Tell me about the Senior Backend Engineer role" → {"sub_queries": ["Tell me about the Senior Backend Engineer role"]}
- "Which candidates have Python AND AWS experience?" → {"sub_queries": ["Which candidates have Python experience?", "Which candidates have AWS experience?"]}
- "Which candidates are suitable for the DataFlow position?" → {"sub_queries": ["What are the requirements for the DataFlow position?", "Which candidates match those requirements?"]}
- "Match candidates to the backend role" → {"sub_queries": ["What does the backend role require?", "Which candidates have matching skills?"]}

Return valid JSON only: {"sub_queries": ["q1", "q2", ...]}.

Query: %s`, qc.OriginalQuery)

	modelID, _ := llm.Route(s.cfg, llm.RoutingRequest{
		TaskType:      llm.TaskQueryDecomposition,
		ContextTokens: llm.EstimateTokens(prompt),
	})

	messages := []brtypes.Message{
		{
			Role: brtypes.ConversationRoleUser,
			Content: []brtypes.ContentBlock{
				&brtypes.ContentBlockMemberText{Value: prompt},
			},
		},
	}

	result, err := s.ambassador.ConverseRouted(ctx, modelID, messages, 0, queryDecompositionMaxOutput)
	if err != nil {
		return fmt.Errorf("query_decomposition: %w", err)
	}

	if result.NeedsReroute {
		fallbackID, _ := llm.Route(s.cfg, llm.RoutingRequest{
			TaskType:       llm.TaskQueryDecomposition,
			ContextTokens:  result.RoutingReq.ContextTokens,
			PreviousFailed: true,
			FailureReason:  result.RoutingReq.FailureReason,
		})
		result, err = s.ambassador.ConverseRouted(ctx, fallbackID, messages, 0, queryDecompositionMaxOutput)
		if err != nil {
			return fmt.Errorf("query_decomposition fallback: %w", err)
		}
	}

	text := llm.ExtractText(result.Output)
	var parsed struct {
		SubQueries []string `json:"sub_queries"`
	}
	cleaned := strings.TrimSpace(text)
	cleaned = strings.TrimPrefix(cleaned, "```json")
	cleaned = strings.TrimPrefix(cleaned, "```")
	cleaned = strings.TrimSuffix(cleaned, "```")
	cleaned = strings.TrimSpace(cleaned)

	if err := json.Unmarshal([]byte(cleaned), &parsed); err != nil {
		return fmt.Errorf("query_decomposition: parse JSON: %w", err)
	}

	if len(parsed.SubQueries) == 0 {
		qc.SubQueries = []string{qc.OriginalQuery}
		return nil
	}
	qc.SubQueries = parsed.SubQueries
	log.Printf("decompose_stage: query=%q sub_queries=%d", qc.OriginalQuery, len(qc.SubQueries))
	return nil
}

// RoutingStage determines LOCAL or GLOBAL retrieval mode.
type RoutingStage struct {
	ret *retriever.Retriever
	cfg *config.Config
}

// NewRoutingStage creates a RoutingStage.
func NewRoutingStage(ret *retriever.Retriever, cfg *config.Config) *RoutingStage {
	return &RoutingStage{ret: ret, cfg: cfg}
}

// ID returns the stage identifier.
func (s *RoutingStage) ID() StageID { return StageRetrievalRouting }

// Run evaluates query signals and sets RetrievalMode on the QueryContext.
func (s *RoutingStage) Run(ctx context.Context, qc *QueryContext) error {
	embedding, topScore, err := s.ret.EmbedQuery(ctx, qc.OriginalQuery)
	if err != nil {
		return fmt.Errorf("routing_stage: %w", err)
	}
	qc.QueryEmbedding = embedding
	qc.TopScore = topScore

	// If mode was explicitly requested (and not auto), honor it.
	if qc.RequestedMode != "" && strings.ToLower(qc.RequestedMode) != "auto" {
		qc.RetrievalMode = strings.ToUpper(qc.RequestedMode)
		log.Printf("retrieval: mode=%s (explicit) similarity=%.4f", qc.RetrievalMode, topScore)
		return nil
	}

	mode := s.ret.DetermineModeWithQuery(qc.OriginalQuery, len(qc.SubQueries), topScore, qc.NamedEntity)
	qc.RetrievalMode = string(mode)
	log.Printf("retrieval: mode=%s sub_queries=%d similarity=%.4f named_entity=%s", qc.RetrievalMode, len(qc.SubQueries), topScore, qc.NamedEntity)
	return nil
}

// RetrievalStage retrieves chunks from Pinecone using the determined mode.
type RetrievalStage struct {
	ret *retriever.Retriever
}

// NewRetrievalStage creates a RetrievalStage.
func NewRetrievalStage(ret *retriever.Retriever) *RetrievalStage {
	return &RetrievalStage{ret: ret}
}

// ID returns the stage identifier.
func (s *RetrievalStage) ID() StageID { return StageRetrieval }

// Run retrieves chunks based on the routing decision.
func (s *RetrievalStage) Run(ctx context.Context, qc *QueryContext) error {
	mode := retriever.RetrievalMode(qc.RetrievalMode)
	result, err := s.ret.Retrieve(ctx, qc.OriginalQuery, mode, "", qc.MaxChunks)
	if err != nil {
		return fmt.Errorf("retrieval_stage: %w", err)
	}
	qc.RetrievedChunks = result.Chunks
	qc.TopScore = result.TopScore
	return nil
}

// SynthesisStage synthesizes a final answer from retrieved chunks.
// Always uses Llama4 Maverick.
type SynthesisStage struct {
	ambassador *llm.Ambassador
	cfg        *config.Config
}

// NewSynthesisStage creates a SynthesisStage.
func NewSynthesisStage(amb *llm.Ambassador, cfg *config.Config) *SynthesisStage {
	return &SynthesisStage{ambassador: amb, cfg: cfg}
}

// ID returns the stage identifier.
func (s *SynthesisStage) ID() StageID { return StageSynthesis }

// Run synthesizes a final answer from retrieved chunks.
func (s *SynthesisStage) Run(ctx context.Context, qc *QueryContext) error {
	if len(qc.RetrievedChunks) == 0 {
		qc.FinalAnswer = "No relevant information found to answer this query."
		return nil
	}

	// Build context from retrieved chunks.
	var contextBlock strings.Builder
	for i, sc := range qc.RetrievedChunks {
		fmt.Fprintf(&contextBlock, "--- Chunk %d (score: %.4f, doc: %s) ---\n%s\n\n",
			i+1, sc.Score, sc.Chunk.DocumentID, sc.Chunk.Text)
	}

	prompt := fmt.Sprintf(`You are a precise and thorough RAG answer synthesis engine. Use ONLY the provided context chunks to answer the query. If the context does not contain enough information, state what is missing.

Be comprehensive. Include specific details, numbers, and names from the context. Structure your answer clearly.

Context:
%s

Query: %s

Provide a complete, well-structured answer based solely on the context above.`, contextBlock.String(), qc.OriginalQuery)

	// Synthesis always routes to fallback model (Llama4 Maverick).
	modelID, _ := llm.Route(s.cfg, llm.RoutingRequest{
		TaskType:      llm.TaskSynthesis,
		ContextTokens: llm.EstimateTokens(prompt),
	})

	messages := []brtypes.Message{
		{
			Role: brtypes.ConversationRoleUser,
			Content: []brtypes.ContentBlock{
				&brtypes.ContentBlockMemberText{Value: prompt},
			},
		},
	}

	result, err := s.ambassador.ConverseRouted(ctx, modelID, messages, 0.3, answerSynthesisMaxOutput)
	if err != nil {
		return fmt.Errorf("synthesis_stage: %w", err)
	}

	qc.FinalAnswer = llm.ExtractText(result.Output)

	// Track token usage.
	if result.Output != nil && result.Output.Usage != nil {
		if result.Output.Usage.InputTokens != nil {
			qc.TokenUsage.InputTokens = *result.Output.Usage.InputTokens
		}
		if result.Output.Usage.OutputTokens != nil {
			qc.TokenUsage.OutputTokens = *result.Output.Usage.OutputTokens
		}
		qc.TokenUsage.TotalTokens = qc.TokenUsage.InputTokens + qc.TokenUsage.OutputTokens
	}

	log.Printf("synthesis_stage: mode=%s chunks=%d answer_len=%d", qc.RetrievalMode, len(qc.RetrievedChunks), len(qc.FinalAnswer))
	return nil
}

// BuildQueryMessages creates a single context+query message for direct Converse.
func BuildQueryMessages(query string, chunks []domain.ScoredChunk) []brtypes.Message {
	var contextBlock strings.Builder
	for i, sc := range chunks {
		fmt.Fprintf(&contextBlock, "--- Chunk %d (score: %.4f) ---\n%s\n\n", i+1, sc.Score, sc.Chunk.Text)
	}
	prompt := fmt.Sprintf("Context:\n%s\nQuery: %s\nAnswer based on the context.", contextBlock.String(), query)
	return []brtypes.Message{
		{
			Role: brtypes.ConversationRoleUser,
			Content: []brtypes.ContentBlock{
				&brtypes.ContentBlockMemberText{Value: prompt},
			},
		},
	}
}
