// Package embedder provides embedding generation using AWS Bedrock Titan Embed V2.
package embedder

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"time"

	"github.com/Victoroide/quarry-rag/internal/awsconf"
	"github.com/Victoroide/quarry-rag/internal/config"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"golang.org/x/sync/semaphore"
)

// titanEmbedRequest is the JSON payload for Titan Embeddings V2.
type titanEmbedRequest struct {
	InputText  string `json:"inputText"`
	Dimensions int    `json:"dimensions,omitempty"`
	Normalize  bool   `json:"normalize"`
}

// titanEmbedResponse is the JSON response from Titan Embeddings V2.
type titanEmbedResponse struct {
	Embedding []float32 `json:"embedding"`
}

// ErrInvalidEmbedding indicates an embedding with invalid dimensions or values.
var ErrInvalidEmbedding = errors.New("embedder: invalid embedding")

// BedrockEmbedder generates vector embeddings using AWS Bedrock Titan
// Embeddings V2 with bounded concurrency via semaphore.
type BedrockEmbedder struct {
	rt         *bedrockruntime.Client
	modelID    string
	dimensions int
	sem        *semaphore.Weighted
	timeout    time.Duration
}

// NewBedrockEmbedder creates an embedder from the service configuration.
func NewBedrockEmbedder(ctx context.Context, cfg *config.Config) (*BedrockEmbedder, error) {
	awsCfg, err := awsconf.Load(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("embedder: load aws config: %w", err)
	}
	rt := bedrockruntime.NewFromConfig(awsCfg)
	return &BedrockEmbedder{
		rt:         rt,
		modelID:    cfg.BedrockEmbedModel,
		dimensions: cfg.EmbeddingDimensions,
		sem:        semaphore.NewWeighted(int64(cfg.EmbedConcurrency)),
		timeout:    cfg.EmbedTimeout,
	}, nil
}

// Embed generates a single embedding vector for the given text.
// Satisfies domain.EmbeddingProvider.
func (e *BedrockEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	if err := e.sem.Acquire(ctx, 1); err != nil {
		return nil, fmt.Errorf("embedder: acquire semaphore: %w", err)
	}
	defer e.sem.Release(1)
	// The timeout bounds the model call, not the wait for a concurrency slot.
	ctx, cancel := context.WithTimeout(ctx, e.timeout)
	defer cancel()
	return e.embedSingle(ctx, text)
}

// Runtime returns the underlying Bedrock runtime client.
func (e *BedrockEmbedder) Runtime() *bedrockruntime.Client {
	return e.rt
}

func (e *BedrockEmbedder) embedSingle(ctx context.Context, text string) ([]float32, error) {
	payload, err := json.Marshal(titanEmbedRequest{
		InputText:  text,
		Dimensions: e.dimensions,
		Normalize:  true,
	})
	if err != nil {
		return nil, fmt.Errorf("embedder: marshal request: %w", err)
	}
	out, err := e.rt.InvokeModel(ctx, &bedrockruntime.InvokeModelInput{
		ModelId:     aws.String(e.modelID),
		ContentType: aws.String("application/json"),
		Accept:      aws.String("application/json"),
		Body:        payload,
	})
	if err != nil {
		return nil, fmt.Errorf("embedder: invoke model: %w", err)
	}
	var resp titanEmbedResponse
	if err := json.Unmarshal(out.Body, &resp); err != nil {
		return nil, fmt.Errorf("embedder: unmarshal response: %w", err)
	}
	if len(resp.Embedding) != e.dimensions {
		return nil, fmt.Errorf("%w: got %d dimensions, want %d", ErrInvalidEmbedding, len(resp.Embedding), e.dimensions)
	}
	for _, value := range resp.Embedding {
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return nil, fmt.Errorf("%w: non-finite value", ErrInvalidEmbedding)
		}
	}
	slog.DebugContext(ctx, "generated embedding", "dimensions", len(resp.Embedding), "characters", len(text))
	return resp.Embedding, nil
}
