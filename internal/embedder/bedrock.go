// Package embedder provides embedding generation using AWS Bedrock Titan Embed V2.
package embedder

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	"github.com/Victoroide/go-ms-rag/internal/config"
	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
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

// BedrockEmbedder generates vector embeddings using AWS Bedrock Titan
// Embeddings V2 with bounded concurrency via semaphore.
type BedrockEmbedder struct {
	rt         *bedrockruntime.Client
	modelID    string
	dimensions int
	sem        *semaphore.Weighted
}

// NewBedrockEmbedder creates an embedder from the service configuration.
func NewBedrockEmbedder(ctx context.Context, cfg *config.Config) (*BedrockEmbedder, error) {
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
		return nil, fmt.Errorf("embedder: load aws config: %w", err)
	}
	rt := bedrockruntime.NewFromConfig(awsCfg)
	return &BedrockEmbedder{
		rt:         rt,
		modelID:    cfg.BedrockEmbedModel,
		dimensions: cfg.EmbeddingDimensions,
		sem:        semaphore.NewWeighted(int64(cfg.EmbedConcurrency)),
	}, nil
}

// Embed generates a single embedding vector for the given text.
// Satisfies domain.EmbeddingProvider.
func (e *BedrockEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	if err := e.sem.Acquire(ctx, 1); err != nil {
		return nil, fmt.Errorf("embedder: acquire semaphore: %w", err)
	}
	defer e.sem.Release(1)
	return e.embedSingle(ctx, text)
}

// EmbedBatch generates embeddings for multiple texts with bounded concurrency.
func (e *BedrockEmbedder) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	results := make([][]float32, len(texts))
	errCh := make(chan error, len(texts))

	for i, text := range texts {
		i, text := i, text
		go func() {
			emb, err := e.Embed(ctx, text)
			if err != nil {
				errCh <- fmt.Errorf("embedder: batch item %d: %w", i, err)
				return
			}
			results[i] = emb
			errCh <- nil
		}()
	}

	for range texts {
		if err := <-errCh; err != nil {
			return nil, err
		}
	}
	return results, nil
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
	log.Printf("embedder: generated %d-dim embedding for %d chars", len(resp.Embedding), len(text))
	return resp.Embedding, nil
}
