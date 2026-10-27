package llm

import (
	"context"
	"fmt"
	"math"
	"strings"

	"github.com/Victoroide/quarry-rag/internal/config"
	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	brtypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
)

const (
	extractionTopP float32 = 0.9
)

// Converser abstracts the Converse call for testability.
type Converser interface {
	Converse(ctx context.Context, messages []brtypes.Message) (*bedrockruntime.ConverseOutput, error)
}

// ModelConverser extends Converser with per-call model selection.
type ModelConverser interface {
	Converser
	ConverseWithModel(ctx context.Context, modelID string, messages []brtypes.Message, temperature float32, maxTokens int) (*bedrockruntime.ConverseOutput, error)
	ConverseWithModelAndSystem(ctx context.Context, modelID, systemPrompt string, messages []brtypes.Message, temperature float32, maxTokens int) (*bedrockruntime.ConverseOutput, error)
}

// Client wraps the AWS Bedrock Runtime client.
type Client struct {
	rt      *bedrockruntime.Client
	modelID string
	maxToks int32
}

// NewClient creates a Bedrock client from the service configuration.
func NewClient(ctx context.Context, cfg *config.Config) (*Client, error) {
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion(cfg.AWSRegion),
		awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(
				cfg.AWSAccessKeyID,
				cfg.AWSSecretKey,
				"",
			),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("llm: load aws config: %w", err)
	}
	rt := bedrockruntime.NewFromConfig(awsCfg)
	return &Client{
		rt:      rt,
		modelID: cfg.BedrockFallbackModel,
		maxToks: 4096,
	}, nil
}

// Converse sends a synchronous Converse API call to the configured model.
func (c *Client) Converse(ctx context.Context, messages []brtypes.Message) (*bedrockruntime.ConverseOutput, error) {
	var temp float32
	topP := extractionTopP
	maxToks := c.maxToks
	input := &bedrockruntime.ConverseInput{
		ModelId:  aws.String(c.modelID),
		Messages: messages,
		InferenceConfig: &brtypes.InferenceConfiguration{
			Temperature: &temp,
			TopP:        &topP,
			MaxTokens:   &maxToks,
		},
	}
	output, err := c.rt.Converse(ctx, input)
	if err != nil {
		return nil, fmt.Errorf("llm: converse: %w", err)
	}
	return output, nil
}

// ConverseWithModel sends a synchronous Converse API call to the specified model.
func (c *Client) ConverseWithModel(ctx context.Context, modelID string, messages []brtypes.Message, temperature float32, maxTokens int) (*bedrockruntime.ConverseOutput, error) {
	return c.ConverseWithModelAndSystem(ctx, modelID, "", messages, temperature, maxTokens)
}

// ConverseWithModelAndSystem is like ConverseWithModel but also accepts a
// system prompt routed through the Converse API's dedicated System field.
// This is the reliable way to enforce hard rules (language, format) across
// providers without polluting the user message and is far stronger than an
// in-prompt instruction at the bottom of a long context.
func (c *Client) ConverseWithModelAndSystem(ctx context.Context, modelID, systemPrompt string, messages []brtypes.Message, temperature float32, maxTokens int) (*bedrockruntime.ConverseOutput, error) {
	topP := extractionTopP
	var maxToks int32
	switch {
	case maxTokens <= 0:
		maxToks = c.maxToks
	case maxTokens > math.MaxInt32:
		return nil, fmt.Errorf("llm: output token limit exceeds %d", math.MaxInt32)
	default:
		maxToks = int32(maxTokens)
	}

	input := &bedrockruntime.ConverseInput{
		ModelId:  aws.String(modelID),
		Messages: messages,
		InferenceConfig: &brtypes.InferenceConfiguration{
			Temperature: &temperature,
			TopP:        &topP,
			MaxTokens:   &maxToks,
		},
	}
	if systemPrompt != "" {
		input.System = []brtypes.SystemContentBlock{
			&brtypes.SystemContentBlockMemberText{Value: systemPrompt},
		}
	}
	output, err := c.rt.Converse(ctx, input)
	if err != nil {
		return nil, fmt.Errorf("llm: converse (model=%s): %w", modelID, err)
	}
	return output, nil
}

// Ping verifies connectivity to the AWS Bedrock service.
func (c *Client) Ping(ctx context.Context) error {
	_, err := c.rt.Converse(ctx, &bedrockruntime.ConverseInput{
		ModelId:  aws.String(c.modelID),
		Messages: []brtypes.Message{},
	})
	if err != nil {
		if strings.Contains(err.Error(), "ValidationException") {
			return nil
		}
		return fmt.Errorf("llm: ping: %w", err)
	}
	return nil
}

// ExtractText pulls text from a ConverseOutput.
func ExtractText(output *bedrockruntime.ConverseOutput) string {
	if output == nil || output.Output == nil {
		return ""
	}
	msg, ok := output.Output.(*brtypes.ConverseOutputMemberMessage)
	if !ok {
		return ""
	}
	var b strings.Builder
	for _, block := range msg.Value.Content {
		if text, ok := block.(*brtypes.ContentBlockMemberText); ok {
			b.WriteString(text.Value)
		}
	}
	return b.String()
}
