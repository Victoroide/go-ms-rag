package unit

import (
	"testing"

	"github.com/Victoroide/quarry-rag/internal/config"
	"github.com/Victoroide/quarry-rag/internal/llm"
	"github.com/stretchr/testify/assert"
)

func newTestConfig() *config.Config {
	return &config.Config{
		BedrockPrimaryModel:  "amazon.nova-micro-v1:0",
		BedrockFallbackModel: "us.meta.llama4-maverick-17b-instruct-v1:0",
	}
}

func TestRouteByTask(t *testing.T) {
	cfg := newTestConfig()

	tests := []struct {
		name     string
		task     llm.TaskType
		tokens   int
		expected string
	}{
		{
			name:     "chunk_extraction routes to primary",
			task:     llm.TaskChunkExtraction,
			tokens:   100,
			expected: cfg.BedrockPrimaryModel,
		},
		{
			name:     "query_decomposition routes to primary",
			task:     llm.TaskQueryDecomposition,
			tokens:   100,
			expected: cfg.BedrockPrimaryModel,
		},
		{
			name:     "sub_query_answer routes to primary",
			task:     llm.TaskSubQueryAnswer,
			tokens:   100,
			expected: cfg.BedrockPrimaryModel,
		},
		{
			name:     "synthesis always routes to fallback",
			task:     llm.TaskSynthesis,
			tokens:   100,
			expected: cfg.BedrockFallbackModel,
		},
		{
			name:     "context limit routes to fallback",
			task:     llm.TaskChunkExtraction,
			tokens:   70000,
			expected: cfg.BedrockFallbackModel,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			modelID, _ := llm.Route(cfg, llm.RoutingRequest{
				TaskType:      tt.task,
				ContextTokens: tt.tokens,
			})
			assert.Equal(t, tt.expected, modelID)
		})
	}
}

func TestFallbackOnParseError(t *testing.T) {
	cfg := newTestConfig()

	// Simulate JSON invalid failure -> should route to fallback.
	modelID, _ := llm.Route(cfg, llm.RoutingRequest{
		TaskType:       llm.TaskChunkExtraction,
		ContextTokens:  100,
		PreviousFailed: true,
		FailureReason:  llm.FailureJSONInvalid,
	})
	assert.Equal(t, cfg.BedrockFallbackModel, modelID,
		"JSON parse error should escalate to fallback model")

	// Simulate empty result failure -> should route to fallback.
	modelID2, _ := llm.Route(cfg, llm.RoutingRequest{
		TaskType:       llm.TaskSubQueryAnswer,
		ContextTokens:  100,
		PreviousFailed: true,
		FailureReason:  llm.FailureEmptyResult,
	})
	assert.Equal(t, cfg.BedrockFallbackModel, modelID2,
		"empty result should escalate to fallback model")
}
