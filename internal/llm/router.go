// Package llm provides LLM routing, the Bedrock Converse client, and
// the ambassador retry/correction pattern.
package llm

import (
	"log"

	"github.com/Victoroide/quarry-rag/internal/config"
)

// TaskType classifies the purpose of a Bedrock call for routing decisions.
type TaskType string

// Task types select the appropriate model route.
const (
	TaskChunkExtraction    TaskType = "chunk_extraction"
	TaskQueryDecomposition TaskType = "query_decomposition"
	TaskSubQueryAnswer     TaskType = "sub_query_answer"
	TaskSynthesis          TaskType = "synthesis"
)

// FailureReason describes why a previous attempt failed, driving fallback routing.
type FailureReason string

// Failure reasons distinguish structured-output and context-limit failures.
const (
	FailureNone         FailureReason = ""
	FailureJSONInvalid  FailureReason = "json_invalid"
	FailureEmptyResult  FailureReason = "empty_result"
	FailureContextLimit FailureReason = "context_limit"
)

// NovaMicroSafeContextLimit is the safe token ceiling for Nova Micro.
const NovaMicroSafeContextLimit = 60_000

// RoutingRequest carries the inputs needed for a routing decision.
type RoutingRequest struct {
	TaskType       TaskType
	ContextTokens  int
	PreviousFailed bool
	FailureReason  FailureReason

	// MultiNeed signals that the query asks for aggregation, comparison
	// or listing across multiple entities. Used by the "hybrid"
	// synthesis model selector to send complex queries to the larger
	// model while routing simple lookups to the faster primary.
	MultiNeed bool
}

// Route selects the model ID for a given request based on the routing
// decision matrix. Returns the chosen model ID and a human-readable reason.
//
// Routing table:
//
//	chunk_extraction      -> nova-micro-v1:0
//	query_decomposition   -> nova-micro-v1:0
//	sub_query_answer      -> nova-micro-v1:0
//	synthesis             -> configured primary, fallback or hybrid policy
//	fallback (any fail)   -> llama4-maverick
func Route(cfg *config.Config, req RoutingRequest) (modelID, reason string) {
	// Rule 1: Context exceeds the safe limit for Nova Micro.
	if req.ContextTokens >= NovaMicroSafeContextLimit {
		log.Printf("router: task=%s tokens=%d -> %s (context_limit)", req.TaskType, req.ContextTokens, cfg.BedrockFallbackModel)
		return cfg.BedrockFallbackModel, "context_limit"
	}

	// Rule 2: Synthesis model selection. Three modes:
	//   "fallback" (default) -> Llama 4 Maverick: best quality for
	//       aggregation/comparison, higher TTFT.
	//   "primary"            -> Nova Micro: lowest TTFT, terser output,
	//       weaker on multi-chunk aggregation.
	//   "hybrid"             -> Nova Micro for single-need queries
	//       (cheap fast lookups), Llama 4 for multi-need ones. The
	//       caller sets RoutingRequest.MultiNeed so the router stays
	//       a pure function and is unit-testable.
	if req.TaskType == TaskSynthesis {
		switch cfg.SynthesisModel {
		case "primary":
			log.Printf("router: task=%s -> %s (synthesis_via_primary)", req.TaskType, cfg.BedrockPrimaryModel)
			return cfg.BedrockPrimaryModel, "synthesis_via_primary"
		case "hybrid":
			if req.MultiNeed {
				log.Printf("router: task=%s -> %s (synthesis_hybrid_multi_need)", req.TaskType, cfg.BedrockFallbackModel)
				return cfg.BedrockFallbackModel, "synthesis_hybrid_multi_need"
			}
			log.Printf("router: task=%s -> %s (synthesis_hybrid_simple)", req.TaskType, cfg.BedrockPrimaryModel)
			return cfg.BedrockPrimaryModel, "synthesis_hybrid_simple"
		default: // "fallback"
			log.Printf("router: task=%s -> %s (synthesis_via_fallback)", req.TaskType, cfg.BedrockFallbackModel)
			return cfg.BedrockFallbackModel, "synthesis_via_fallback"
		}
	}

	// Rule 3: Previous structured-output failure triggers fallback.
	if req.PreviousFailed && (req.FailureReason == FailureJSONInvalid || req.FailureReason == FailureEmptyResult) {
		log.Printf("router: task=%s -> %s (structured_output_failure, reason=%s)", req.TaskType, cfg.BedrockFallbackModel, req.FailureReason)
		return cfg.BedrockFallbackModel, "structured_output_failure"
	}

	// Default: use the primary model (Nova Micro).
	log.Printf("router: task=%s tokens=%d -> %s", req.TaskType, req.ContextTokens, cfg.BedrockPrimaryModel)
	return cfg.BedrockPrimaryModel, ""
}

// EstimateTokens estimates the token count for a string using the
// rune-count / 4 heuristic.
func EstimateTokens(s string) int {
	return len([]rune(s)) / 4
}
