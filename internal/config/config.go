// Package config handles environment-based configuration loading and validation.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Config holds all operational parameters for the service.
// Every field is populated from environment variables at startup.
type Config struct {
	ServerPort string

	AWSRegion      string
	AWSAccessKeyID string
	AWSSecretKey   string

	BedrockEmbedModel    string
	BedrockPrimaryModel  string
	BedrockFallbackModel string

	PineconeAPIKey      string
	PineconeIndexName   string
	PineconeEnvironment string

	DatabaseURL string

	WorkerQueueSize         int
	EmbedConcurrency        int
	LocalRetrievalThreshold float64
	MaxChunksPerQuery       int

	ChunkTokenSize int
	ChunkOverlap   int

	EmbeddingDimensions int

	S3Bucket       string
	MaxUploadBytes int64

	// SynthesisModel selects which Bedrock model handles answer
	// synthesis. Values: "primary" (Nova Micro, faster, lower TTFT),
	// "fallback" (Llama 4 Maverick, more verbose). Default "fallback"
	// preserves historical behavior; "primary" cuts p95 noticeably at
	// the cost of slightly terser answers. Configurable so the choice
	// is a one-line env change, not a redeploy.
	SynthesisModel string

	// FakeMode (GO_RAG_FAKE_MODE) runs the service fully offline: deterministic
	// in-memory vector store, embedder, and LLM. No AWS/Pinecone credentials
	// required. DATABASE_URL is still used for metadata (local Postgres in QA).
	// QA/local only — never enable in production.
	FakeMode bool

	// ServiceToken (SERVICE_TOKEN), when non-empty, is required in the
	// X-Service-Token header on the mutating/expensive routes (POST /ingest,
	// POST /query, DELETE /documents/{id}). Empty disables service auth.
	ServiceToken string
}

// Load reads configuration from environment variables, applies defaults,
// and validates required fields.
func Load() (*Config, error) {
	cfg := &Config{
		ServerPort:              envOrDefault("SERVER_PORT", "8081"),
		AWSRegion:               os.Getenv("AWS_REGION"),
		AWSAccessKeyID:          os.Getenv("AWS_ACCESS_KEY_ID"),
		AWSSecretKey:            os.Getenv("AWS_SECRET_ACCESS_KEY"),
		BedrockEmbedModel:       envOrDefault("BEDROCK_EMBED_MODEL", "amazon.titan-embed-text-v2:0"),
		BedrockPrimaryModel:     envOrDefault("BEDROCK_PRIMARY_MODEL", "amazon.nova-micro-v1:0"),
		BedrockFallbackModel:    envOrDefault("BEDROCK_FALLBACK_MODEL", "us.meta.llama4-maverick-17b-instruct-v1:0"),
		PineconeAPIKey:          os.Getenv("PINECONE_API_KEY"),
		PineconeIndexName:       envOrDefault("PINECONE_INDEX_NAME", "go-ms-rag"),
		PineconeEnvironment:     os.Getenv("PINECONE_ENVIRONMENT"),
		DatabaseURL:             os.Getenv("DATABASE_URL"),
		WorkerQueueSize:         envIntOrDefault("WORKER_QUEUE_SIZE", 100),
		EmbedConcurrency:        envIntOrDefault("EMBED_CONCURRENCY", 10),
		LocalRetrievalThreshold: envFloat64OrDefault("LOCAL_RETRIEVAL_THRESHOLD", 0.70),
		MaxChunksPerQuery:       envIntOrDefault("MAX_CHUNKS_PER_QUERY", 10),
		ChunkTokenSize:          envIntOrDefault("CHUNK_TOKEN_SIZE", 512),
		ChunkOverlap:            envIntOrDefault("CHUNK_OVERLAP", 64),
		EmbeddingDimensions:     envIntOrDefault("EMBEDDING_DIMENSIONS", 1024),
		S3Bucket:                envOrDefault("S3_BUCKET", "ms-go-rag"),
		MaxUploadBytes:          envInt64OrDefault("MAX_UPLOAD_BYTES", 20971520),
		SynthesisModel:          envOrDefault("SYNTHESIS_MODEL", "fallback"),
		FakeMode:                envBoolOrDefault("GO_RAG_FAKE_MODE", false),
		ServiceToken:            os.Getenv("SERVICE_TOKEN"),
	}
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (c *Config) validate() error {
	type requirement struct {
		name  string
		value string
	}
	// DATABASE_URL is always required (metadata store). Cloud creds are only
	// required in real mode; FakeMode runs without AWS/Pinecone.
	required := []requirement{
		{"DATABASE_URL", c.DatabaseURL},
	}
	if !c.FakeMode {
		required = append(required,
			requirement{"AWS_REGION", c.AWSRegion},
			requirement{"AWS_ACCESS_KEY_ID", c.AWSAccessKeyID},
			requirement{"AWS_SECRET_ACCESS_KEY", c.AWSSecretKey},
			requirement{"PINECONE_API_KEY", c.PineconeAPIKey},
		)
	}
	var missing []string
	for _, r := range required {
		if r.value == "" {
			missing = append(missing, r.name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("config: missing required environment variables: %v", missing)
	}
	if c.WorkerQueueSize < 1 || c.WorkerQueueSize > 1000 {
		return fmt.Errorf("config: WORKER_QUEUE_SIZE must be between 1 and 1000, got %d", c.WorkerQueueSize)
	}
	if c.EmbedConcurrency < 1 || c.EmbedConcurrency > 50 {
		return fmt.Errorf("config: EMBED_CONCURRENCY must be between 1 and 50, got %d", c.EmbedConcurrency)
	}
	return nil
}

func envBoolOrDefault(key string, fallback bool) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "1", "true", "yes", "y", "on":
		return true
	case "0", "false", "no", "n", "off":
		return false
	default:
		return fallback
	}
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envIntOrDefault(key string, fallback int) int {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	return n
}

func envInt64OrDefault(key string, fallback int64) int64 {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return fallback
	}
	return n
}

func envFloat64OrDefault(key string, fallback float64) float64 {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return fallback
	}
	return f
}
