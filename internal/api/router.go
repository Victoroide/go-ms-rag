// Package api assembles the HTTP router with all handlers and middleware.
package api

import (
	"net/http"

	"github.com/Victoroide/go-ms-rag/internal/api/handler"
	"github.com/Victoroide/go-ms-rag/internal/api/middleware"
	"github.com/Victoroide/go-ms-rag/internal/config"
	"github.com/Victoroide/go-ms-rag/internal/domain"
	"github.com/Victoroide/go-ms-rag/internal/llm"
	"github.com/Victoroide/go-ms-rag/internal/retriever"
	"github.com/Victoroide/go-ms-rag/internal/worker"
)

// RouterDeps groups the dependencies required to construct the HTTP router.
type RouterDeps struct {
	VectorStore domain.VectorStore
	ChunkStore  domain.ChunkStore
	Retriever   *retriever.Retriever
	Ambassador  *llm.Ambassador
	Config      *config.Config
	Worker      *worker.Worker
}

// NewRouter wires all HTTP routes, handlers, and middleware.
func NewRouter(deps *RouterDeps) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", handler.HealthHandler(deps.VectorStore, deps.ChunkStore, deps.Ambassador))
	mux.HandleFunc("POST /ingest", handler.IngestHandler(deps.Worker, deps.ChunkStore))
	mux.HandleFunc("POST /query", handler.QueryHandler(deps.Retriever, deps.Ambassador, deps.Config))
	var h http.Handler = mux
	h = middleware.Logging(h)
	h = middleware.Recovery(h)
	return h
}
