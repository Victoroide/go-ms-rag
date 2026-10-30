package unit

import (
	"bytes"
	"encoding/json"
	"log"
	"log/slog"
	"testing"

	"github.com/Victoroide/quarry-rag/internal/config"
	"github.com/Victoroide/quarry-rag/internal/llm"
	"github.com/stretchr/testify/require"
)

func TestRouteSynthesisPolicies(t *testing.T) {
	for _, tt := range []struct {
		name, policy, modelID, reason string
		multiNeed                     bool
		tokens                        int
	}{
		{"primary", "primary", "primary", "synthesis_via_primary", false, 100},
		{"primary multi need", "primary", "primary", "synthesis_via_primary", true, 100},
		{"fallback", "fallback", "fallback", "synthesis_via_fallback", false, 100},
		{"fallback multi need", "fallback", "fallback", "synthesis_via_fallback", true, 100},
		{"default", "", "fallback", "synthesis_via_fallback", false, 100},
		{"hybrid simple", "hybrid", "primary", "synthesis_hybrid_simple", false, 100},
		{"hybrid multi need", "hybrid", "fallback", "synthesis_hybrid_multi_need", true, 100},
		{"below context limit", "primary", "primary", "synthesis_via_primary", false, 59999},
		{"primary context limit", "primary", "fallback", "context_limit", false, 60000},
		{"hybrid context limit", "hybrid", "fallback", "context_limit", false, 60000},
		{"fallback context limit", "fallback", "fallback", "context_limit", true, 60001},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var logs bytes.Buffer
			old, flags := slog.Default(), log.Flags()
			slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
			t.Cleanup(func() { slog.SetDefault(old); log.SetFlags(flags) })
			cfg := &config.Config{BedrockPrimaryModel: "primary", BedrockFallbackModel: "fallback", SynthesisModel: tt.policy}
			modelID, reason := llm.Route(cfg, llm.RoutingRequest{TaskType: llm.TaskSynthesis, ContextTokens: tt.tokens, MultiNeed: tt.multiNeed})
			require.Equal(t, tt.modelID, modelID)
			require.Equal(t, tt.reason, reason)
			var record map[string]any
			require.NoError(t, json.Unmarshal(logs.Bytes(), &record))
			require.Equal(t, "DEBUG", record["level"])
			require.Equal(t, tt.modelID, record["model_id"])
			require.Equal(t, tt.reason, record["reason"])
		})
	}
}
