// Package llm provides LLM routing, the Bedrock Converse client, and
// the ambassador retry/correction pattern.
package llm

import (
	"log"

	"github.com/Victoroide/go-ms-rag/internal/config"
)

// TaskType classifies the purpose of a Bedrock call for routing decisions.
type TaskType string

const (
	TaskChunkExtraction    TaskType = "chunk_extraction"
	TaskQueryDecomposition TaskType = "query_decomposition"
	TaskSubQueryAnswer     TaskType = "sub_query_answer"
	TaskSynthesis          TaskType = "synthesis"
)

// FailureReason describes why a previous attempt failed, driving fallback routing.
type FailureReason string

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
}

// Route selects the model ID for a given request based on the routing
// decision matrix. Returns the chosen model ID and a human-readable reason.
//
// Routing table:
//
//	chunk_extraction      -> nova-micro-v1:0
//	query_decomposition   -> nova-micro-v1:0
//	sub_query_answer      -> nova-micro-v1:0
//	synthesis             -> llama4-maverick (always)
//	fallback (any fail)   -> llama4-maverick
func Route(cfg *config.Config, req RoutingRequest) (modelID, reason string) {
	// Rule 1: Context exceeds the safe limit for Nova Micro.
	if req.ContextTokens >= NovaMicroSafeContextLimit {
		log.Printf("router: task=%s tokens=%d -> %s (context_limit)", req.TaskType, req.ContextTokens, cfg.BedrockFallbackModel)
		return cfg.BedrockFallbackModel, "context_limit"
	}

	// Rule 2: Synthesis always uses the fallback model (Llama4 Maverick).
	if req.TaskType == TaskSynthesis {
		log.Printf("router: task=%s -> %s (synthesis_always_maverick)", req.TaskType, cfg.BedrockFallbackModel)
		return cfg.BedrockFallbackModel, "synthesis_always_maverick"
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
