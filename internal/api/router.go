// Package api assembles the HTTP router with all handlers and middleware.
package api

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/Victoroide/quarry-rag/internal/api/handler"
	"github.com/Victoroide/quarry-rag/internal/api/middleware"
	"github.com/Victoroide/quarry-rag/internal/config"
	"github.com/Victoroide/quarry-rag/internal/domain"
	"github.com/Victoroide/quarry-rag/internal/extractor"
	"github.com/Victoroide/quarry-rag/internal/llm"
	"github.com/Victoroide/quarry-rag/internal/retriever"
	s3store "github.com/Victoroide/quarry-rag/internal/store/s3"
	"github.com/Victoroide/quarry-rag/internal/worker"
	"github.com/jackc/pgx/v5/pgxpool"
)

// RouterDeps groups the dependencies required to construct the HTTP router.
type RouterDeps struct {
	VectorStore    domain.VectorStore
	ChunkStore     domain.ChunkStore
	Retriever      *retriever.Retriever
	Ambassador     *llm.Ambassador
	Config         *config.Config
	Worker         *worker.Worker
	S3Client       *s3store.Client
	PineconeDriver domain.VectorDeleter
	PGPool         *pgxpool.Pool
	Probes         *handler.Probes
}

// NewRouter wires all HTTP routes, handlers, and middleware.
func NewRouter(deps *RouterDeps) http.Handler {
	mux := http.NewServeMux()
	// Service-to-service auth: when service tokens are set, every route except the
	// public probes requires a matching X-Service-Token header. The presigned
	// file GET is included because it mints S3 download URLs for any document id
	// and must never be reachable without the trusted caller in front of it.
	tokens := deps.Config.ServiceTokens
	if len(tokens) == 0 && deps.Config.ServiceToken != "" {
		tokens = []string{deps.Config.ServiceToken}
	}
	middleware.LogServiceAuthState(tokens)
	ingestOpts := []handler.IngestOption{handler.WithMaxPending(deps.Config.WorkerQueueSize), handler.WithUploadLimiter(extractor.NewLimiter(deps.Config.ExtractConcurrency, deps.Config.ExtractQueueTimeout))}
	if deps.Config.ExtractIsolation {
		sb, err := extractor.NewSandbox(extractor.SandboxConfig{Timeout: deps.Config.ExtractTimeout, MemoryLimitMB: deps.Config.ExtractMemoryLimitMB})
		if err != nil {
			slog.Error("initialize document extraction", "error", err)
			ingestOpts = append(ingestOpts, handler.WithExtractor(func(context.Context, extractor.FileType, []byte) (string, error) {
				return "", extractor.ErrExtractionUnavailable
			}))
		} else {
			ingestOpts = append(ingestOpts, handler.WithExtractor(sb.Extract))
		}
	}
	probes := deps.Probes
	if probes == nil {
		probes = handler.NewProbes(deps.VectorStore, deps.ChunkStore, deps.S3Client, deps.Config.HealthCacheTTL)
	}
	mux.HandleFunc("GET /health", handler.HealthHandler(probes))
	mux.HandleFunc("GET /livez", handler.LivenessHandler())
	mux.HandleFunc("GET /readyz", handler.ReadinessHandler(probes))
	var objects handler.ObjectStore
	if deps.S3Client != nil {
		objects = deps.S3Client
	}
	mux.HandleFunc("POST /ingest", middleware.RequireServiceTokens(tokens, handler.IngestHandler(deps.Worker, deps.ChunkStore, objects, deps.Config.MaxUploadBytes, ingestOpts...)))
	mux.HandleFunc("POST /query", middleware.RequireServiceTokens(tokens, handler.QueryHandler(deps.Retriever, deps.Ambassador, deps.Config, deps.PGPool)))
	mux.HandleFunc("GET /documents", middleware.RequireServiceTokens(tokens, handler.ListDocumentsHandler(deps.ChunkStore)))
	mux.HandleFunc("GET /documents/{doc_id}", middleware.RequireServiceTokens(tokens, handler.DocumentStatusHandler(deps.ChunkStore)))
	mux.HandleFunc("GET /documents/{doc_id}/file", middleware.RequireServiceTokens(tokens, handler.FileHandler(deps.ChunkStore, deps.S3Client)))
	mux.HandleFunc("DELETE /documents/{doc_id}", middleware.RequireServiceTokens(tokens, handler.DeleteHandler(deps.ChunkStore, deps.PineconeDriver, deps.S3Client)))
	return middleware.RequestID(middleware.Logging(middleware.Recovery(mux)))
}
