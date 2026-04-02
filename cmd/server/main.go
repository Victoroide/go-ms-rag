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
	"github.com/Victoroide/go-ms-rag/internal/embedder"
	"github.com/Victoroide/go-ms-rag/internal/llm"
	"github.com/Victoroide/go-ms-rag/internal/pipeline"
	"github.com/Victoroide/go-ms-rag/internal/retriever"
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
	defer pgClient.Close(ctx)

	if migErr := pgClient.RunMigrations(ctx); migErr != nil {
		return fmt.Errorf("startup: migrations: %w", migErr)
	}

	var smokeDocID string
	smokeErr := pgClient.Pool.QueryRow(ctx, `SELECT id::text FROM documents LIMIT 1`).Scan(&smokeDocID)
	if smokeErr != nil && !errors.Is(smokeErr, pgx.ErrNoRows) {
		return fmt.Errorf("startup: neon smoke test: %w", smokeErr)
	}
	logger.Info("neon connected")

	// Initialize Pinecone.
	pcClient, err := pcstore.NewPineconeClient(ctx, cfg)
	if err != nil {
		return fmt.Errorf("startup: pinecone: %w", err)
	}
	pcDriver := pcstore.NewDriver(ctx, pcClient)

	// Initialize Bedrock embedder.
	emb, err := embedder.NewBedrockEmbedder(ctx, cfg)
	if err != nil {
		return fmt.Errorf("startup: embedder: %w", err)
	}

	// Initialize LLM client and ambassador.
	llmClient, err := llm.NewClient(ctx, cfg)
	if err != nil {
		return fmt.Errorf("startup: llm client: %w", err)
	}
	amb := llm.NewAmbassador(cfg)
	amb.SetClient(llmClient)

	// Initialize chunker.
	chk := chunker.NewChunker(cfg.ChunkTokenSize, cfg.ChunkOverlap)

	// Initialize retriever.
	ret := retriever.NewRetriever(pcDriver, emb, cfg.LocalRetrievalThreshold, cfg.MaxChunksPerQuery)

	// Initialize S3 client (non-fatal if it fails).
	var s3Client *s3store.Client
	if cfg.S3Bucket != "" {
		s3c, s3Err := s3store.New(ctx, cfg)
		if s3Err != nil {
			log.Printf("WARNING: s3 init failed (file uploads disabled): %v", s3Err)
		} else {
			s3Client = s3c
		}
	}

	// Build ingestion pipeline stages.
	ingestStages := []pipeline.Stage{
		pipeline.NewChunkStage(chk),
		pipeline.NewEmbedStage(emb),
		pipeline.NewPersistStage(pcDriver, pgClient),
	}

	// Initialize worker.
	w := worker.NewWorker(cfg.WorkerQueueSize, ingestStages, pgClient)
	w.Start(ctx)

	// Build HTTP router.
	router := api.NewRouter(&api.RouterDeps{
		VectorStore:    pcDriver,
		ChunkStore:     pgClient,
		Retriever:      ret,
		Ambassador:     amb,
		Config:         cfg,
		Worker:         w,
		S3Client:       s3Client,
		PineconeDriver: pcDriver,
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
		cancel()
		w.Shutdown()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer shutdownCancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.Printf("server shutdown: %v", err)
		}
	}()

	log.Printf("starting server on :%s", cfg.ServerPort)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("server: %w", err)
	}
	return nil
}
