package llm

import (
	"context"
	"fmt"

	"github.com/Victoroide/quarry-rag/internal/config"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	brtypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
)

// ConverseRoutedResult carries the output of a synthesis call.
type ConverseRoutedResult struct {
	Output  *bedrockruntime.ConverseOutput
	Text    string
	ModelID string
}

// Ambassador executes synthesis calls with SDK-owned transport retries.
type Ambassador struct {
	client Converser
}

// NewAmbassador creates a synthesis caller.
func NewAmbassador(_ *config.Config) *Ambassador {
	return &Ambassador{}
}

// SetClient injects the underlying Converser implementation.
func (a *Ambassador) SetClient(c Converser) {
	a.client = c
}

// ConverseRoutedPlain sends a plain-text synthesis request with a system prompt.
func (a *Ambassador) ConverseRoutedPlain(ctx context.Context, modelID, systemPrompt string, messages []brtypes.Message, temperature float32, maxTokens int) (*ConverseRoutedResult, error) {
	output, err := a.converseWithModel(ctx, modelID, systemPrompt, messages, temperature, maxTokens)
	if err != nil {
		return nil, err
	}
	return &ConverseRoutedResult{
		Output:  output,
		Text:    ExtractText(output),
		ModelID: modelID,
	}, nil
}

// converseWithModel dispatches to the ModelConverser interface if available.
func (a *Ambassador) converseWithModel(ctx context.Context, modelID, systemPrompt string, messages []brtypes.Message, temperature float32, maxTokens int) (*bedrockruntime.ConverseOutput, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("ambassador: context canceled: %w", err)
	}
	if mc, ok := a.client.(ModelConverser); ok {
		output, err := mc.ConverseWithModelAndSystem(ctx, modelID, systemPrompt, messages, temperature, maxTokens)
		if err != nil {
			return nil, fmt.Errorf("ambassador: converse: %w", err)
		}
		return output, nil
	}
	output, err := a.client.Converse(ctx, messages)
	if err != nil {
		return nil, fmt.Errorf("ambassador: converse: %w", err)
	}
	return output, nil
}
