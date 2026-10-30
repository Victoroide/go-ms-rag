package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"github.com/Victoroide/quarry-rag/internal/config"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	brtypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
)

// ConverseRoutedResult carries the output of a routed converse call,
// including routing metadata needed for fallback decisions.
type ConverseRoutedResult struct {
	Output       *bedrockruntime.ConverseOutput
	Text         string
	ModelID      string
	NeedsReroute bool
	RoutingReq   RoutingRequest
}

// Ambassador wraps a Converser with the three-attempt correction pattern:
//
//	attempt 1: primary model
//	attempt 2: JSON correction prompt + primary model
//	attempt 3: fallback model
type Ambassador struct {
	client Converser
	cfg    *config.Config
}

// NewAmbassador creates an Ambassador from the given configuration.
func NewAmbassador(cfg *config.Config) *Ambassador {
	return &Ambassador{cfg: cfg}
}

// SetClient injects the underlying Converser implementation.
func (a *Ambassador) SetClient(c Converser) {
	a.client = c
}

// Pinger is an optional interface for health checks.
type Pinger interface {
	Ping(ctx context.Context) error
}

// Ping verifies that the Bedrock endpoint is reachable.
func (a *Ambassador) Ping(ctx context.Context) error {
	if p, ok := a.client.(Pinger); ok {
		return p.Ping(ctx)
	}
	return nil
}

// Converse executes one call; the client's SDK retryer owns transport retries.
func (a *Ambassador) Converse(ctx context.Context, messages []brtypes.Message) (*bedrockruntime.ConverseOutput, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("ambassador: context canceled: %w", err)
	}
	output, err := a.client.Converse(ctx, messages)
	if err != nil {
		return nil, fmt.Errorf("ambassador: converse: %w", err)
	}
	return output, nil
}

// ConverseRouted sends a request to the specified model with token
// estimation, enforcement, and JSON retry for PrimaryModelID calls.
func (a *Ambassador) ConverseRouted(ctx context.Context, modelID string, messages []brtypes.Message, temperature float32, maxTokens int) (*ConverseRoutedResult, error) {
	return a.ConverseRoutedWithSystem(ctx, modelID, "", messages, temperature, maxTokens)
}

// ConverseRoutedPlain is a system-prompt-aware Converse call that does
// NOT enter the JSON-correction retry loop. It is the right entry point
// for stages whose output is free-form text (synthesis answers), where
// JSON validation would always fail and trigger a wasted re-call. The
// client SDK owns transient-error retries.
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

// ConverseRoutedWithSystem is ConverseRouted plus an optional system prompt
// routed through the Bedrock Converse System field. Used by the synthesis
// stage to enforce non-negotiable rules (output language, output shape).
func (a *Ambassador) ConverseRoutedWithSystem(ctx context.Context, modelID, systemPrompt string, messages []brtypes.Message, temperature float32, maxTokens int) (*ConverseRoutedResult, error) {
	var combined strings.Builder
	for _, msg := range messages {
		for _, block := range msg.Content {
			if text, ok := block.(*brtypes.ContentBlockMemberText); ok {
				combined.WriteString(text.Value)
			}
		}
	}
	tokenEstimate := EstimateTokens(combined.String())

	// Token count enforcement for primary model.
	if a.cfg != nil && modelID == a.cfg.BedrockPrimaryModel && tokenEstimate >= NovaMicroSafeContextLimit {
		log.Printf("ambassador: token estimate %d exceeds safe limit %d for primary model, needs reroute",
			tokenEstimate, NovaMicroSafeContextLimit)
		return &ConverseRoutedResult{
			NeedsReroute: true,
			RoutingReq: RoutingRequest{
				ContextTokens: tokenEstimate,
				FailureReason: FailureContextLimit,
			},
		}, nil
	}

	// For PrimaryModelID calls, use retry-with-correction for JSON output.
	if a.cfg != nil && modelID == a.cfg.BedrockPrimaryModel {
		return a.retryWithCorrection(ctx, modelID, systemPrompt, messages, temperature, maxTokens)
	}

	// FallbackModelID calls: direct call, no retry.
	output, err := a.converseWithModel(ctx, modelID, systemPrompt, messages, temperature, maxTokens)
	if err != nil {
		return nil, err
	}
	text := ExtractText(output)
	return &ConverseRoutedResult{
		Output:  output,
		Text:    text,
		ModelID: modelID,
	}, nil
}

// ConverseWithRouting handles routing automatically.
func (a *Ambassador) ConverseWithRouting(ctx context.Context, taskType TaskType, messages []brtypes.Message, temperature float32, maxTokens int) (*ConverseRoutedResult, error) {
	var combined strings.Builder
	for _, msg := range messages {
		for _, block := range msg.Content {
			if text, ok := block.(*brtypes.ContentBlockMemberText); ok {
				combined.WriteString(text.Value)
				combined.WriteString(" ")
			}
		}
	}
	tokenEstimate := EstimateTokens(combined.String())

	modelID, _ := Route(a.cfg, RoutingRequest{
		TaskType:      taskType,
		ContextTokens: tokenEstimate,
	})

	result, err := a.ConverseRouted(ctx, modelID, messages, temperature, maxTokens)
	if err != nil {
		return nil, err
	}

	if result.NeedsReroute {
		fallbackID, _ := Route(a.cfg, RoutingRequest{
			TaskType:       taskType,
			ContextTokens:  result.RoutingReq.ContextTokens,
			PreviousFailed: true,
			FailureReason:  result.RoutingReq.FailureReason,
		})
		result, err = a.ConverseRouted(ctx, fallbackID, messages, temperature, maxTokens)
		if err != nil {
			return nil, err
		}
	}

	return result, nil
}

// retryWithCorrection implements the JSON retry loop for PrimaryModelID calls.
func (a *Ambassador) retryWithCorrection(ctx context.Context, modelID, systemPrompt string, messages []brtypes.Message, temperature float32, maxTokens int) (*ConverseRoutedResult, error) {
	// Attempt 1: primary model.
	log.Printf("ambassador: attempt=1 model=%s", modelID)
	output, err := a.converseWithModel(ctx, modelID, systemPrompt, messages, temperature, maxTokens)
	if err != nil {
		return nil, err
	}
	text := ExtractText(output)

	parseErr := validateJSON(text)
	if parseErr == nil {
		if isEmptyJSONResult(text) {
			log.Printf("ambassador: attempt=1 model=%s empty result, signaling reroute", modelID)
			return &ConverseRoutedResult{
				NeedsReroute: true,
				RoutingReq: RoutingRequest{
					PreviousFailed: true,
					FailureReason:  FailureEmptyResult,
				},
			}, nil
		}
		return &ConverseRoutedResult{
			Output:  output,
			Text:    text,
			ModelID: modelID,
		}, nil
	}

	// Attempt 2: JSON correction prompt + primary model.
	log.Printf("ambassador: attempt=2 model=%s (JSON correction)", modelID)
	correctionMsg := fmt.Sprintf(
		"The previous response failed JSON validation with error: %s. Output only valid JSON matching the required schema. No explanation.",
		parseErr.Error(),
	)
	correctedMessages := make([]brtypes.Message, len(messages))
	copy(correctedMessages, messages)
	correctedMessages = append(correctedMessages,
		brtypes.Message{
			Role: brtypes.ConversationRoleAssistant,
			Content: []brtypes.ContentBlock{
				&brtypes.ContentBlockMemberText{Value: text},
			},
		},
		brtypes.Message{
			Role: brtypes.ConversationRoleUser,
			Content: []brtypes.ContentBlock{
				&brtypes.ContentBlockMemberText{Value: correctionMsg},
			},
		},
	)

	output2, err := a.converseWithModel(ctx, modelID, systemPrompt, correctedMessages, temperature, maxTokens)
	if err != nil {
		return nil, err
	}
	text2 := ExtractText(output2)

	parseErr2 := validateJSON(text2)
	if parseErr2 != nil {
		// Attempt 3: signal reroute to fallback model.
		log.Printf("ambassador: attempt=3 model=%s -> fallback (JSON retry failed)", modelID)
		//nolint:nilerr // Invalid structured output is a routing signal, not a transport failure.
		return &ConverseRoutedResult{
			NeedsReroute: true,
			RoutingReq: RoutingRequest{
				PreviousFailed: true,
				FailureReason:  FailureJSONInvalid,
			},
		}, nil
	}
	if isEmptyJSONResult(text2) {
		log.Printf("ambassador: attempt=2 model=%s empty result, signaling reroute", modelID)
		return &ConverseRoutedResult{
			NeedsReroute: true,
			RoutingReq: RoutingRequest{
				PreviousFailed: true,
				FailureReason:  FailureEmptyResult,
			},
		}, nil
	}

	return &ConverseRoutedResult{
		Output:  output2,
		Text:    text2,
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
	return a.Converse(ctx, messages)
}

func validateJSON(text string) error {
	cleaned := stripFences(text)
	if !json.Valid([]byte(cleaned)) {
		return fmt.Errorf("invalid JSON")
	}
	return nil
}

func isEmptyJSONResult(text string) bool {
	cleaned := strings.TrimSpace(stripFences(text))
	return cleaned == "{}" || cleaned == "[]"
}

func stripFences(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```json") {
		s = s[7:]
	} else if strings.HasPrefix(s, "```") {
		s = s[3:]
	}
	s = strings.TrimSuffix(s, "```")
	return strings.TrimSpace(s)
}
