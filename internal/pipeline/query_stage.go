package pipeline

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/Victoroide/go-ms-rag/internal/config"
	"github.com/Victoroide/go-ms-rag/internal/domain"
	"github.com/Victoroide/go-ms-rag/internal/llm"
	"github.com/Victoroide/go-ms-rag/internal/retriever"
	brtypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Synthesis budget knobs. The numbers below come from measuring p95 on
// the slow-query subset. Cutting the output cap from 4096 → 1024 alone
// saves ~1.5 s on Llama 4 Maverick's worst case because Bedrock token
// budget reservation correlates with stream length. The cap is well
// above the 95th-percentile observed output (~750 tokens) so quality
// is preserved.
const (
	answerSynthesisMaxOutput = 1024 // was 4096

	// Chunk count caps. Simple lookups can answer from 5 chunks; multi-
	// need aggregation/compare queries need a wider view, otherwise the
	// model refuses because no single chunk has the full picture.
	synthesisMaxChunksSimple    = 5
	synthesisMaxChunksMultiNeed = 8

	// synthesisRelScoreFloor prunes chunks whose score is less than this
	// fraction of the top chunk's score. Keeps strong matches even when
	// fewer than the cap survive; drops near-noise chunks.
	synthesisRelScoreFloor = 0.55

	// chunkBodyMaxRunes truncates over-long chunk text. The chunker
	// produces ~512-token windows (≈2000 runes); we keep enough room for
	// the salient facts at the start without padding the prompt with
	// trailing boilerplate.
	chunkBodyMaxRunes = 1200
)

// synthesisSystemPrompt is the non-negotiable contract for the synthesis
// model. Compressed from the prior 4-rule version. Routed through the
// Bedrock Converse System field where it gets the highest salience.
//
// Two design notes:
//   - Rule 1 (Spanish) is restated multiple ways. Llama 4 and Nova both
//     occasionally drift on a single-mention rule; restatement is cheap.
//   - The "use what you have" framing is deliberate — saying "lista lo
//     que aparezca" instead of describing a refusal branch keeps the
//     model from over-refusing. Empirically, models that are told a
//     literal refusal sentence will overuse it.
const synthesisSystemPrompt = `Eres un asistente RAG para una app boliviana.
Reglas:
1. RESPONDE SIEMPRE EN ESPAÑOL neutro. Aunque la pregunta o los fragmentos vengan en inglés, la respuesta final debe estar 100% en español. Traduce o parafrasea al español cualquier título, descripción o cita que aparezca en inglés en los fragmentos; nombres propios y tecnologías (p. ej. Python, AWS, React) se mantienen.
2. Usa solo los fragmentos del contexto. Si hay información parcial, agrégala (lista vacantes, describe candidatos, compara datos disponibles). Solo si los fragmentos no contienen NADA pertinente, indica brevemente que la información no aparece.
3. Si la pregunta es vacía, ambigua o incomprensible (p. ej. "?", "Cuéntame más", cadenas sin sentido), pide en una frase corta que el usuario formule una pregunta clara, en español.
4. Sé claro y específico: incluye nombres, años, tecnologías, salarios y demás datos concretos. Estructura con listas cuando ayude.
5. No repitas la pregunta, no expliques tu razonamiento, no agregues notas en inglés.`

// answerLanguageReminder is appended to the user prompt as a short
// belt-and-suspenders layer in case the System block is down-weighted.
const answerLanguageReminder = "\n\nResponde en español, conciso y al grano."

// DecomposeStage classifies the query as single-need or multi-need and
// resolves any known named entity from the DB cache.
//
// History: this stage used to call an LLM for true decomposition into
// sub-queries. In practice, downstream stages only consume the COUNT
// (>1 vs ==1) — the actual sub-queries were never re-embedded or
// queried independently. The LLM hop therefore added ~300–800 ms and
// burned tokens for zero retrieval benefit.
//
// The new implementation runs a deterministic, multilingual heuristic
// over conjunctions and comparison verbs (Spanish + English) plus the
// named-entity cache. It is two-to-three orders of magnitude faster
// than a Bedrock call and produces the same routing signal.
type DecomposeStage struct {
	ambassador *llm.Ambassador
	cfg        *config.Config
	pool       *pgxpool.Pool
}

// NewDecomposeStage creates a DecomposeStage.
func NewDecomposeStage(amb *llm.Ambassador, cfg *config.Config, pool *pgxpool.Pool) *DecomposeStage {
	return &DecomposeStage{ambassador: amb, cfg: cfg, pool: pool}
}

// ID returns the stage identifier.
func (s *DecomposeStage) ID() StageID { return StageQueryDecomposition }

// multiNeedSignals are surface-level cues that a single query is asking
// the system to retrieve and compare more than one logical entity. Both
// Spanish and English variants are listed because the consumer is a
// Bolivian app but queries occasionally arrive in English.
var multiNeedSignals = []string{
	" y ", " e ", " and ", " or ", " o ",
	"compar", "comparar",
	"match", "empareja", "afín", "afin",
	"cumpl", "fit", "suitable",
	"vs ", " versus ",
	"todos", "todas", "all candidates", "all jobs", "todas las",
	"cuáles candidatos", "cuales candidatos", "which candidates",
	"qué candidatos", "que candidatos",
	"gap", "brecha", "diferencia",
	"requisitos", "requirements",
}

// hasMultiNeedSignal returns true when the query exhibits at least one
// of the multi-need surface cues above. False positives are acceptable:
// the only downstream effect is using GLOBAL retrieval instead of LOCAL,
// which is slightly broader but never wrong.
func hasMultiNeedSignal(queryLower string) bool {
	for _, sig := range multiNeedSignals {
		if strings.Contains(queryLower, sig) {
			return true
		}
	}
	return false
}

// Run resolves the named entity and produces a synthetic sub-query
// count for downstream routing. No LLM call is made.
func (s *DecomposeStage) Run(ctx context.Context, qc *QueryContext) error {
	queryLower := strings.ToLower(qc.OriginalQuery)

	// Named-entity lookup uses the cached map (60 s TTL, app pgx pool).
	namedEntities, err := FetchNamedEntitiesFromDB(ctx, s.pool)
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

	// Treat the original query as the (only) sub-query unless surface
	// cues say otherwise. The list length is what downstream stages
	// inspect; the entries themselves are never re-embedded.
	if hasMultiNeedSignal(queryLower) && qc.NamedEntity == "" {
		qc.SubQueries = []string{qc.OriginalQuery, qc.OriginalQuery}
	} else {
		qc.SubQueries = []string{qc.OriginalQuery}
	}
	log.Printf("decompose_stage: query=%q sub_queries=%d named_entity=%q (heuristic)",
		qc.OriginalQuery, len(qc.SubQueries), qc.NamedEntity)
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

// Run retrieves chunks based on the routing decision. Reuses the
// embedding the routing stage already produced for the same query
// to skip one Bedrock embed call per request.
func (s *RetrievalStage) Run(ctx context.Context, qc *QueryContext) error {
	mode := retriever.RetrievalMode(qc.RetrievalMode)
	var result *retriever.Result
	var err error
	if len(qc.QueryEmbedding) > 0 {
		result, err = s.ret.RetrieveWithEmbedding(ctx, qc.QueryEmbedding, mode, "", qc.MaxChunks, qc.FilterDocType)
	} else {
		result, err = s.ret.Retrieve(ctx, qc.OriginalQuery, mode, "", qc.MaxChunks, qc.FilterDocType)
	}
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

// trimChunksForSynthesis applies adaptive chunk selection to the
// retrieval result: relative-score pruning, top-N cap, and per-chunk
// text truncation. maxChunks lets the caller pick a tighter cap for
// simple lookups and a wider cap for multi-need / aggregation queries
// where the model needs to see more of the corpus to answer.
func trimChunksForSynthesis(in []domain.ScoredChunk, maxChunks int) []domain.ScoredChunk {
	if len(in) == 0 {
		return in
	}
	if maxChunks <= 0 {
		maxChunks = synthesisMaxChunksSimple
	}
	// in is already sorted by score desc from Retriever.Retrieve.
	top := in[0].Score
	floor := top * synthesisRelScoreFloor
	out := make([]domain.ScoredChunk, 0, maxChunks)
	for _, sc := range in {
		if len(out) >= maxChunks {
			break
		}
		if sc.Score < floor {
			break
		}
		// Truncate over-long chunk text at a sentence boundary if one
		// exists in the trailing 1/4 of the kept slice, otherwise hard
		// cut at the rune limit.
		txt := sc.Chunk.Text
		if r := []rune(txt); len(r) > chunkBodyMaxRunes {
			cut := chunkBodyMaxRunes
			// Look for a clean break in the last 25% of the window.
			window := r[chunkBodyMaxRunes*3/4 : chunkBodyMaxRunes]
			for i := len(window) - 1; i >= 0; i-- {
				if window[i] == '.' || window[i] == '\n' {
					cut = chunkBodyMaxRunes*3/4 + i + 1
					break
				}
			}
			sc.Chunk.Text = string(r[:cut])
		} else {
			sc.Chunk.Text = txt
		}
		out = append(out, sc)
	}
	// Always keep at least one chunk if any were retrieved.
	if len(out) == 0 && len(in) > 0 {
		out = append(out, in[0])
	}
	return out
}

// Run synthesizes a final answer from retrieved chunks.
//
// Language enforcement: the synthesisSystemPrompt is delivered through
// the Bedrock Converse System field, which both Nova Micro and Llama 4
// Maverick treat as a higher-priority instruction than any user text.
// We additionally append answerLanguageReminder to the user prompt.
//
// Tail-latency control: chunks are pruned by relative score and capped
// at synthesisMaxChunks; output tokens are capped at
// answerSynthesisMaxOutput; the system prompt is the compressed 3-rule
// version. All quality knobs (relative-score floor, max chunks, output
// cap) are explicit constants so they can be tuned per profile.
func (s *SynthesisStage) Run(ctx context.Context, qc *QueryContext) error {
	if len(qc.RetrievedChunks) == 0 {
		qc.FinalAnswer = "No se encontró información relevante para responder a esta consulta."
		qc.ContextChunks = 0
		qc.ContextTokens = 0
		return nil
	}

	// Multi-need queries get a wider context window — listing/comparing
	// across the corpus needs to see more chunks than a single-entity
	// lookup. The signal is the same one the router uses to pick the
	// stronger synthesis model, so prompt and model scale together.
	maxChunks := synthesisMaxChunksSimple
	if len(qc.SubQueries) > 1 {
		maxChunks = synthesisMaxChunksMultiNeed
	}
	chunks := trimChunksForSynthesis(qc.RetrievedChunks, maxChunks)
	qc.ContextChunks = len(chunks)

	// Build a compact context block. Dropped the per-chunk "doc:" line
	// (the LLM never needed the UUID) — saves ~30 tokens per chunk.
	var contextBlock strings.Builder
	for i, sc := range chunks {
		fmt.Fprintf(&contextBlock, "[%d] %s\n", i+1, sc.Chunk.Text)
	}

	prompt := fmt.Sprintf(`Contexto:
%s
Pregunta: %s

Responde en español usando los fragmentos anteriores. Si la pregunta pide listar, comparar o agregar, hazlo a partir de los fragmentos. Incluye nombres y datos concretos.%s`,
		contextBlock.String(), qc.OriginalQuery, answerLanguageReminder)

	tokenEst := llm.EstimateTokens(prompt) + llm.EstimateTokens(synthesisSystemPrompt)
	qc.ContextTokens = tokenEst

	// SubQueries length > 1 is our cheap multi-need / aggregation
	// signal (set by the decomposition heuristic). The hybrid router
	// uses it to send aggregation queries to the stronger model.
	multiNeed := len(qc.SubQueries) > 1
	modelID, _ := llm.Route(s.cfg, llm.RoutingRequest{
		TaskType:      llm.TaskSynthesis,
		ContextTokens: tokenEst,
		MultiNeed:     multiNeed,
	})

	messages := []brtypes.Message{
		{
			Role: brtypes.ConversationRoleUser,
			Content: []brtypes.ContentBlock{
				&brtypes.ContentBlockMemberText{Value: prompt},
			},
		},
	}

	// Synthesis is free-form text — never JSON. Use the plain path so
	// the ambassador's JSON-correction retry loop is bypassed even when
	// SYNTHESIS_MODEL=primary routes us through Nova Micro.
	result, err := s.ambassador.ConverseRoutedPlain(
		ctx, modelID, synthesisSystemPrompt, messages, 0.2, answerSynthesisMaxOutput,
	)
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
