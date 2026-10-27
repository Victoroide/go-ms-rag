// Package api assembles the HTTP router with all handlers and middleware.
package api

import (
	"net/http"

	"github.com/Victoroide/quarry-rag/internal/api/handler"
	"github.com/Victoroide/quarry-rag/internal/api/middleware"
	"github.com/Victoroide/quarry-rag/internal/config"
	"github.com/Victoroide/quarry-rag/internal/domain"
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
}

// NewRouter wires all HTTP routes, handlers, and middleware.
func NewRouter(deps *RouterDeps) http.Handler {
	mux := http.NewServeMux()
	// Service-to-service auth: when SERVICE_TOKEN is set, every route except the
	// health probe requires a matching X-Service-Token header. The presigned
	// file GET is included because it mints S3 download URLs for any document id
	// and must never be reachable without the trusted caller in front of it.
	tok := deps.Config.ServiceToken
	middleware.LogServiceAuthState(tok)
	mux.HandleFunc("GET /health", handler.HealthHandler(deps.VectorStore, deps.ChunkStore, deps.S3Client))
	mux.HandleFunc("POST /ingest", middleware.RequireServiceToken(tok, handler.IngestHandler(deps.Worker, deps.ChunkStore, deps.S3Client, deps.Config.MaxUploadBytes)))
	mux.HandleFunc("POST /query", middleware.RequireServiceToken(tok, handler.QueryHandler(deps.Retriever, deps.Ambassador, deps.Config, deps.PGPool)))
	mux.HandleFunc("GET /documents", middleware.RequireServiceToken(tok, handler.ListDocumentsHandler(deps.ChunkStore)))
	mux.HandleFunc("GET /documents/{doc_id}/file", middleware.RequireServiceToken(tok, handler.FileHandler(deps.ChunkStore, deps.S3Client)))
	mux.HandleFunc("DELETE /documents/{doc_id}", middleware.RequireServiceToken(tok, handler.DeleteHandler(deps.ChunkStore, deps.PineconeDriver, deps.S3Client)))
	var h http.Handler = mux
	h = middleware.Logging(h)
	h = middleware.Recovery(h)
	return h
}
