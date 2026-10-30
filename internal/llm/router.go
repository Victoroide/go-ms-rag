// Package llm provides synthesis routing and Bedrock Converse calls.
package llm

import (
	"log/slog"

	"github.com/Victoroide/quarry-rag/internal/config"
)

// TaskType classifies the purpose of a Bedrock call for routing decisions.
type TaskType string

// Task types select the appropriate model route.
const (
	TaskSynthesis TaskType = "synthesis"
)

// NovaMicroSafeContextLimit is the safe token ceiling for Nova Micro.
const NovaMicroSafeContextLimit = 60_000

// RoutingRequest carries the inputs needed for a routing decision.
type RoutingRequest struct {
	TaskType      TaskType
	ContextTokens int

	// MultiNeed signals that the query asks for aggregation, comparison
	// or listing across multiple entities. Used by the "hybrid"
	// synthesis model selector to send complex queries to the larger
	// model while routing simple lookups to the faster primary.
	MultiNeed bool
}

// Route selects the synthesis destination using the configured policy and context limit.
func Route(cfg *config.Config, req RoutingRequest) (modelID, reason string) {
	defer func() {
		slog.Debug("synthesis route selected", "model_id", modelID, "reason", reason)
	}()
	if req.ContextTokens >= NovaMicroSafeContextLimit {
		return cfg.BedrockFallbackModel, "context_limit"
	}
	switch cfg.SynthesisModel {
	case "primary":
		return cfg.BedrockPrimaryModel, "synthesis_via_primary"
	case "hybrid":
		if req.MultiNeed {
			return cfg.BedrockFallbackModel, "synthesis_hybrid_multi_need"
		}
		return cfg.BedrockPrimaryModel, "synthesis_hybrid_simple"
	default:
		return cfg.BedrockFallbackModel, "synthesis_via_fallback"
	}
}

// EstimateTokens estimates the token count for a string using the
// rune-count / 4 heuristic.
func EstimateTokens(s string) int {
	return len([]rune(s)) / 4
}
