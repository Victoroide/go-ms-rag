// Package main is the entry point for the go-ms-rag microservice.
// It wires all dependencies and starts the HTTP server.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Victoroide/go-ms-rag/internal/api"
	"github.com/Victoroide/go-ms-rag/internal/chunker"
	"github.com/Victoroide/go-ms-rag/internal/config"
	"github.com/Victoroide/go-ms-rag/internal/domain"
	"github.com/Victoroide/go-ms-rag/internal/embedder"
	"github.com/Victoroide/go-ms-rag/internal/llm"
	"github.com/Victoroide/go-ms-rag/internal/pipeline"
	"github.com/Victoroide/go-ms-rag/internal/retriever"
	memstore "github.com/Victoroide/go-ms-rag/internal/store/memory"
	pcstore "github.com/Victoroide/go-ms-rag/internal/store/pinecone"
	pgstore "github.com/Victoroide/go-ms-rag/internal/store/postgres"
	s3store "github.com/Victoroide/go-ms-rag/internal/store/s3"
	"github.com/Victoroide/go-ms-rag/internal/worker"
	"github.com/jackc/pgx/v5"
)

func main() {
	if err := run(); err != nil {
		log.Fatalf("server error: %v", err)
	}
}

func run() error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("startup: configuration error: %w", err)
	}
	logger := slog.Default()

	// Initialize Neon/PostgreSQL.
	pgClient, err := pgstore.NewClient(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("startup: neon: %w", err)
	}
	defer func() { _ = pgClient.Close(ctx) }()

	if migErr := pgClient.RunMigrations(ctx); migErr != nil {
		return fmt.Errorf("startup: migrations: %w", migErr)
	}

	var smokeDocID string
	smokeErr := pgClient.Pool.QueryRow(ctx, `SELECT id::text FROM documents LIMIT 1`).Scan(&smokeDocID)
	if smokeErr != nil && !errors.Is(smokeErr, pgx.ErrNoRows) {
		return fmt.Errorf("startup: neon smoke test: %w", smokeErr)
	}
	logger.Info("neon connected")

	// Select vector store, embedder, and LLM. FAKE MODE (GO_RAG_FAKE_MODE) wires
	// deterministic offline implementations so the full HTTP API can be exercised
	// in local QA without Bedrock/Pinecone/S3 cost. Postgres is still the
	// metadata store. Never enable FAKE MODE in production.
	var (
		emb         domain.EmbeddingProvider
		vectorStore domain.VectorStore
		vecDeleter  domain.VectorDeleter
		amb         *llm.Ambassador
		s3Client    *s3store.Client
	)

	if cfg.FakeMode {
		logger.Warn("FAKE MODE ACTIVE: deterministic in-memory vector store, embedder, and LLM. No AWS/Pinecone/S3 calls will be made.")
		mem := memstore.NewFakeVectorStore()
		vectorStore = mem
		vecDeleter = mem
		emb = embedder.NewFakeEmbedder(cfg.EmbeddingDimensions)
		amb = llm.NewAmbassador(cfg)
		amb.SetClient(llm.NewFakeConverser())
	} else {
		pcClient, pcErr := pcstore.NewPineconeClient(ctx, cfg)
		if pcErr != nil {
			return fmt.Errorf("startup: pinecone: %w", pcErr)
		}
		pcDriver := pcstore.NewDriver(ctx, pcClient)
		vectorStore = pcDriver
		vecDeleter = pcDriver

		bedrockEmb, embErr := embedder.NewBedrockEmbedder(ctx, cfg)
		if embErr != nil {
			return fmt.Errorf("startup: embedder: %w", embErr)
		}
		emb = bedrockEmb

		llmClient, llmErr := llm.NewClient(ctx, cfg)
		if llmErr != nil {
			return fmt.Errorf("startup: llm client: %w", llmErr)
		}
		amb = llm.NewAmbassador(cfg)
		amb.SetClient(llmClient)

		// Initialize S3 client (non-fatal if it fails). Skipped in FAKE MODE.
		if cfg.S3Bucket != "" {
			s3c, s3Err := s3store.New(ctx, cfg)
			if s3Err != nil {
				log.Printf("WARNING: s3 init failed (file uploads disabled): %v", s3Err)
			} else {
				s3Client = s3c
			}
		}
	}

	// Initialize chunker.
	chk := chunker.NewChunker(cfg.ChunkTokenSize, cfg.ChunkOverlap)

	// Initialize retriever.
	ret := retriever.NewRetriever(vectorStore, emb, cfg.LocalRetrievalThreshold, cfg.MaxChunksPerQuery)

	// Build ingestion pipeline stages.
	ingestStages := []pipeline.Stage{
		pipeline.NewChunkStage(chk),
		pipeline.NewEmbedStage(emb),
		pipeline.NewPersistStage(vectorStore, pgClient),
	}

	// Initialize worker.
	w := worker.NewWorker(cfg.WorkerQueueSize, ingestStages, pgClient)
	w.Start(ctx)

	// Build HTTP router.
	router := api.NewRouter(&api.RouterDeps{
		VectorStore:    vectorStore,
		ChunkStore:     pgClient,
		Retriever:      ret,
		Ambassador:     amb,
		Config:         cfg,
		Worker:         w,
		S3Client:       s3Client,
		PineconeDriver: vecDeleter,
		PGPool:         pgClient.Pool,
	})

	srv := &http.Server{
		Addr:         fmt.Sprintf(":%s", cfg.ServerPort),
		Handler:      router,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 120 * time.Second,
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		log.Printf("received signal %s, shutting down", sig)
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer shutdownCancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.Printf("server shutdown: %v", err)
		}
		cancel()
		w.Shutdown()
	}()

	log.Printf("starting server on :%s", cfg.ServerPort)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("server: %w", err)
	}
	return nil
}
