// Package config handles environment-based configuration loading and validation.
package config

import (
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"time"
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

	// SynthesisModel selects primary, fallback or hybrid answer routing.
	SynthesisModel string

	// FakeMode (GO_RAG_FAKE_MODE) runs the service fully offline: deterministic
	// in-memory vector store, embedder, and LLM. No AWS/Pinecone credentials
	// required. DATABASE_URL is still used for metadata (local Postgres in QA).
	// QA/local only — never enable in production.
	FakeMode bool

	// ServiceToken protects every route except the public health probe.
	ServiceToken string

	S3ExpectedBucketOwner string

	PineconeUpsertBatchSize int
	PineconeTimeout         time.Duration
	PineconeMaxAttempts     int

	ExtractIsolation         bool
	ExtractTimeout           time.Duration
	ExtractConcurrency       int
	ExtractMemoryLimitMB     int
	ExtractQueueTimeout      time.Duration
	MaxAllowedDocumentIDs    int
	RequireDocumentAllowlist bool

	IngestWorkers      int
	IngestMaxAttempts  int
	IngestLease        time.Duration
	IngestJobTimeout   time.Duration
	IngestPollInterval time.Duration

	AWSSessionToken string
	AWSMaxAttempts  int
	EmbedTimeout    time.Duration
}

// Load reads configuration from environment variables, applies defaults,
// and validates required fields.
func Load() (*Config, error) {
	cfg := &Config{
		ServerPort:            envOrDefault("SERVER_PORT", "8081"),
		AWSRegion:             os.Getenv("AWS_REGION"),
		AWSAccessKeyID:        os.Getenv("AWS_ACCESS_KEY_ID"),
		AWSSecretKey:          os.Getenv("AWS_SECRET_ACCESS_KEY"),
		BedrockEmbedModel:     envOrDefault("BEDROCK_EMBED_MODEL", "amazon.titan-embed-text-v2:0"),
		BedrockPrimaryModel:   envOrDefault("BEDROCK_PRIMARY_MODEL", "amazon.nova-micro-v1:0"),
		BedrockFallbackModel:  envOrDefault("BEDROCK_FALLBACK_MODEL", "us.meta.llama4-maverick-17b-instruct-v1:0"),
		PineconeAPIKey:        os.Getenv("PINECONE_API_KEY"),
		PineconeIndexName:     envOrDefault("PINECONE_INDEX_NAME", "go-ms-rag"),
		PineconeEnvironment:   os.Getenv("PINECONE_ENVIRONMENT"),
		DatabaseURL:           os.Getenv("DATABASE_URL"),
		S3Bucket:              os.Getenv("S3_BUCKET"),
		SynthesisModel:        envOrDefault("SYNTHESIS_MODEL", "fallback"),
		ServiceToken:          os.Getenv("SERVICE_TOKEN"),
		S3ExpectedBucketOwner: os.Getenv("S3_EXPECTED_BUCKET_OWNER"),
		AWSSessionToken:       os.Getenv("AWS_SESSION_TOKEN"),
	}
	for _, setting := range []struct {
		key      string
		dst      *int
		fallback int
	}{
		{"WORKER_QUEUE_SIZE", &cfg.WorkerQueueSize, 1000}, {"EMBED_CONCURRENCY", &cfg.EmbedConcurrency, 10},
		{"MAX_CHUNKS_PER_QUERY", &cfg.MaxChunksPerQuery, 10}, {"CHUNK_TOKEN_SIZE", &cfg.ChunkTokenSize, 512},
		{"CHUNK_OVERLAP", &cfg.ChunkOverlap, 64}, {"EMBEDDING_DIMENSIONS", &cfg.EmbeddingDimensions, 1024},
		{"PINECONE_UPSERT_BATCH_SIZE", &cfg.PineconeUpsertBatchSize, 100},
		{"PINECONE_MAX_ATTEMPTS", &cfg.PineconeMaxAttempts, 5},
		{"INGEST_WORKERS", &cfg.IngestWorkers, 2}, {"INGEST_MAX_ATTEMPTS", &cfg.IngestMaxAttempts, 5},
		{"EXTRACT_CONCURRENCY", &cfg.ExtractConcurrency, 2}, {"EXTRACT_MEMORY_LIMIT_MB", &cfg.ExtractMemoryLimitMB, 512},
		{"MAX_ALLOWED_DOCUMENT_IDS", &cfg.MaxAllowedDocumentIDs, 25000},
		{"AWS_MAX_ATTEMPTS", &cfg.AWSMaxAttempts, 4},
	} {
		value, err := envIntOrDefault(setting.key, setting.fallback)
		if err != nil {
			return nil, err
		}
		*setting.dst = value
	}
	var err error
	if cfg.PineconeTimeout, err = envDurationOrDefault("PINECONE_TIMEOUT", 10*time.Second); err != nil {
		return nil, err
	}
	if cfg.LocalRetrievalThreshold, err = envFloat64OrDefault("LOCAL_RETRIEVAL_THRESHOLD", 0.70); err != nil {
		return nil, err
	}
	if cfg.MaxUploadBytes, err = envInt64OrDefault("MAX_UPLOAD_BYTES", 20971520); err != nil {
		return nil, err
	}
	if cfg.FakeMode, err = envBoolOrDefault("GO_RAG_FAKE_MODE", false); err != nil {
		return nil, err
	}
	if cfg.ExtractIsolation, err = envBoolOrDefault("EXTRACT_ISOLATION", true); err != nil {
		return nil, err
	}
	if cfg.ExtractTimeout, err = envDurationOrDefault("EXTRACT_TIMEOUT", 30*time.Second); err != nil {
		return nil, err
	}
	if cfg.ExtractQueueTimeout, err = envDurationOrDefault("EXTRACT_QUEUE_TIMEOUT", 5*time.Second); err != nil {
		return nil, err
	}

	if cfg.RequireDocumentAllowlist, err = envBoolOrDefault("REQUIRE_DOCUMENT_ALLOWLIST", false); err != nil {
		return nil, err
	}

	for _, setting := range []struct {
		key      string
		dst      *time.Duration
		fallback time.Duration
	}{
		{"INGEST_LEASE", &cfg.IngestLease, 2 * time.Minute},
		{"INGEST_JOB_TIMEOUT", &cfg.IngestJobTimeout, 15 * time.Minute},
		{"INGEST_POLL_INTERVAL", &cfg.IngestPollInterval, time.Second},
		{"EMBED_TIMEOUT", &cfg.EmbedTimeout, 15 * time.Second},
	} {
		value, parseErr := envDurationOrDefault(setting.key, setting.fallback)
		if parseErr != nil {
			return nil, parseErr
		}
		*setting.dst = value
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
	// DATABASE_URL is always required. FakeMode runs without AWS or Pinecone.
	required := []requirement{
		{"DATABASE_URL", c.DatabaseURL},
	}
	if !c.FakeMode {
		required = append(required,
			requirement{"AWS_REGION", c.AWSRegion},
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
	if (c.AWSAccessKeyID == "") != (c.AWSSecretKey == "") {
		return fmt.Errorf("config: AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY must be both set or both empty")
	}
	if c.AWSMaxAttempts < 1 || c.AWSMaxAttempts > 10 {
		return fmt.Errorf("config: AWS_MAX_ATTEMPTS must be between 1 and 10")
	}
	if c.EmbedTimeout < time.Second || c.EmbedTimeout > 2*time.Minute {
		return fmt.Errorf("config: EMBED_TIMEOUT must be between 1s and 2m")
	}
	if c.WorkerQueueSize < 1 || c.WorkerQueueSize > 100000 {
		return fmt.Errorf("config: WORKER_QUEUE_SIZE must be between 1 and 100000, got %d", c.WorkerQueueSize)
	}
	if c.EmbedConcurrency < 1 || c.EmbedConcurrency > 50 {
		return fmt.Errorf("config: EMBED_CONCURRENCY must be between 1 and 50, got %d", c.EmbedConcurrency)
	}
	port, err := strconv.Atoi(c.ServerPort)
	if err != nil || port < 1 || port > 65535 {
		return fmt.Errorf("config: SERVER_PORT must be between 1 and 65535")
	}
	if c.ChunkTokenSize < 1 || c.ChunkTokenSize > 8192 {
		return fmt.Errorf("config: CHUNK_TOKEN_SIZE must be between 1 and 8192")
	}
	if c.ChunkOverlap < 0 || c.ChunkOverlap >= c.ChunkTokenSize {
		return fmt.Errorf("config: CHUNK_OVERLAP must be nonnegative and smaller than CHUNK_TOKEN_SIZE")
	}
	if math.IsNaN(c.LocalRetrievalThreshold) || math.IsInf(c.LocalRetrievalThreshold, 0) || c.LocalRetrievalThreshold < 0 || c.LocalRetrievalThreshold > 1 {
		return fmt.Errorf("config: LOCAL_RETRIEVAL_THRESHOLD must be between 0 and 1")
	}
	if c.MaxChunksPerQuery < 1 || c.MaxChunksPerQuery > 100 {
		return fmt.Errorf("config: MAX_CHUNKS_PER_QUERY must be between 1 and 100")
	}
	if c.MaxUploadBytes < 1 || c.MaxUploadBytes > 100<<20 {
		return fmt.Errorf("config: MAX_UPLOAD_BYTES must be between 1 and 104857600")
	}
	if c.EmbeddingDimensions != 256 && c.EmbeddingDimensions != 512 && c.EmbeddingDimensions != 1024 {
		return fmt.Errorf("config: EMBEDDING_DIMENSIONS must be 256, 512 or 1024")
	}
	if c.PineconeUpsertBatchSize < 1 || c.PineconeUpsertBatchSize > 1000 {
		return fmt.Errorf("config: PINECONE_UPSERT_BATCH_SIZE must be between 1 and 1000")
	}
	if c.PineconeTimeout < time.Second || c.PineconeTimeout > 2*time.Minute {
		return fmt.Errorf("config: PINECONE_TIMEOUT must be between 1s and 2m")
	}
	if c.PineconeMaxAttempts < 1 || c.PineconeMaxAttempts > 10 {
		return fmt.Errorf("config: PINECONE_MAX_ATTEMPTS must be between 1 and 10")
	}
	switch c.SynthesisModel {
	case "primary", "fallback", "hybrid":
	default:
		return fmt.Errorf("config: SYNTHESIS_MODEL must be primary, fallback or hybrid")
	}

	// Fail closed: a real (non-fake) deployment must set SERVICE_TOKEN so the
	// mutating/file routes are authenticated. An empty token disables service
	// auth, which is acceptable only for offline fake mode (dev/CI/QA).
	if !c.FakeMode && strings.TrimSpace(c.ServiceToken) == "" {
		return fmt.Errorf("config: SERVICE_TOKEN is required when GO_RAG_FAKE_MODE is off")
	}
	if c.S3ExpectedBucketOwner != "" {
		if len(c.S3ExpectedBucketOwner) != 12 || strings.IndexFunc(c.S3ExpectedBucketOwner, func(r rune) bool { return r < '0' || r > '9' }) >= 0 {
			return fmt.Errorf("config: S3_EXPECTED_BUCKET_OWNER must contain exactly 12 digits")
		}
	}
	if c.ExtractTimeout < time.Second || c.ExtractTimeout > 10*time.Minute {
		return fmt.Errorf("config: EXTRACT_TIMEOUT must be between 1s and 10m")
	}
	if c.ExtractConcurrency < 1 || c.ExtractConcurrency > 64 {
		return fmt.Errorf("config: EXTRACT_CONCURRENCY must be between 1 and 64")
	}
	if c.ExtractMemoryLimitMB < 64 || c.ExtractMemoryLimitMB > 8192 {
		return fmt.Errorf("config: EXTRACT_MEMORY_LIMIT_MB must be between 64 and 8192")
	}
	if c.ExtractQueueTimeout < 0 || c.ExtractQueueTimeout > time.Minute {
		return fmt.Errorf("config: EXTRACT_QUEUE_TIMEOUT must be between 0s and 60s")
	}
	if c.MaxAllowedDocumentIDs < 1 || c.MaxAllowedDocumentIDs > 100000 {
		return fmt.Errorf("config: MAX_ALLOWED_DOCUMENT_IDS must be between 1 and 100000")
	}
	if c.IngestWorkers < 1 || c.IngestWorkers > 32 {
		return fmt.Errorf("config: INGEST_WORKERS must be between 1 and 32")
	}
	if c.IngestMaxAttempts < 1 || c.IngestMaxAttempts > 20 {
		return fmt.Errorf("config: INGEST_MAX_ATTEMPTS must be between 1 and 20")
	}
	if c.IngestLease < 30*time.Second || c.IngestLease > 30*time.Minute {
		return fmt.Errorf("config: INGEST_LEASE must be between 30s and 30m")
	}
	if c.IngestJobTimeout < time.Minute || c.IngestJobTimeout > 2*time.Hour || c.IngestJobTimeout <= c.IngestLease {
		return fmt.Errorf("config: INGEST_JOB_TIMEOUT must be between 1m and 2h and exceed INGEST_LEASE")
	}
	if c.IngestPollInterval < 100*time.Millisecond || c.IngestPollInterval > time.Minute {
		return fmt.Errorf("config: INGEST_POLL_INTERVAL must be between 100ms and 1m")
	}

	return nil
}

func envBoolOrDefault(key string, fallback bool) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "":
		return fallback, nil
	case "1", "true", "yes", "y", "on":
		return true, nil
	case "0", "false", "no", "n", "off":
		return false, nil
	default:
		return false, fmt.Errorf("config: invalid %s", key)
	}
}
func envOrDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
func parseEnv[T any](key string, fallback T, parse func(string) (T, error)) (T, error) {
	value := os.Getenv(key)
	if value == "" {
		return fallback, nil
	}
	parsed, err := parse(value)
	if err != nil {
		return parsed, fmt.Errorf("config: invalid %s: %w", key, err)
	}
	return parsed, nil
}
func envIntOrDefault(key string, fallback int) (int, error) {
	return parseEnv(key, fallback, strconv.Atoi)
}
func envInt64OrDefault(key string, fallback int64) (int64, error) {
	return parseEnv(key, fallback, func(value string) (int64, error) { return strconv.ParseInt(value, 10, 64) })
}
func envFloat64OrDefault(key string, fallback float64) (float64, error) {
	return parseEnv(key, fallback, func(value string) (float64, error) { return strconv.ParseFloat(value, 64) })
}

func envDurationOrDefault(key string, fallback time.Duration) (time.Duration, error) {
	return parseEnv(key, fallback, time.ParseDuration)
}
