// Package main is the entry point for the Quarry microservice.
// It wires all dependencies and starts the HTTP server.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/Victoroide/quarry-rag/internal/api"
	"github.com/Victoroide/quarry-rag/internal/api/handler"
	"github.com/Victoroide/quarry-rag/internal/chunker"
	"github.com/Victoroide/quarry-rag/internal/config"
	"github.com/Victoroide/quarry-rag/internal/domain"
	"github.com/Victoroide/quarry-rag/internal/embedder"
	"github.com/Victoroide/quarry-rag/internal/llm"
	"github.com/Victoroide/quarry-rag/internal/logctx"
	"github.com/Victoroide/quarry-rag/internal/metrics"
	"github.com/Victoroide/quarry-rag/internal/pipeline"
	"github.com/Victoroide/quarry-rag/internal/retriever"
	memstore "github.com/Victoroide/quarry-rag/internal/store/memory"
	pcstore "github.com/Victoroide/quarry-rag/internal/store/pinecone"
	pgstore "github.com/Victoroide/quarry-rag/internal/store/postgres"
	s3store "github.com/Victoroide/quarry-rag/internal/store/s3"
	"github.com/Victoroide/quarry-rag/internal/worker"
	"github.com/google/uuid"
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
	slog.SetDefault(slog.New(logctx.NewHandler(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: cfg.LogLevel}))))
	for _, warning := range cfg.Warnings {
		slog.Warn(warning)
	}
	logger := slog.Default()
	startupCtx, startupCancel := context.WithTimeout(ctx, 30*time.Second)
	defer startupCancel()
	pgClient, err := pgstore.NewClient(startupCtx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("startup: postgres: %w", err)
	}
	defer func() {
		if closeErr := pgClient.Close(context.Background()); closeErr != nil {
			slog.Error("close postgres", "error", closeErr)
		}
	}()
	if migrationErr := pgClient.RunMigrations(startupCtx); migrationErr != nil {
		return fmt.Errorf("startup: migrations: %w", migrationErr)
	}
	logger.Info("postgres connected")

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
		pcClient, pcErr := pcstore.NewPineconeClient(startupCtx, cfg)
		if pcErr != nil {
			return fmt.Errorf("startup: pinecone: %w", pcErr)
		}
		defer func() {
			if closeErr := pcClient.Close(); closeErr != nil {
				slog.Error("close pinecone", "error", closeErr)
			}
		}()
		pcDriver := pcstore.NewDriver(pcClient)
		vectorStore = pcDriver
		vecDeleter = pcDriver

		bedrockEmb, embErr := embedder.NewBedrockEmbedder(startupCtx, cfg)
		if embErr != nil {
			return fmt.Errorf("startup: embedder: %w", embErr)
		}
		emb = bedrockEmb

		llmClient, llmErr := llm.NewClient(startupCtx, cfg)
		if llmErr != nil {
			return fmt.Errorf("startup: llm client: %w", llmErr)
		}
		amb = llm.NewAmbassador(cfg)
		amb.SetClient(llmClient)

		if cfg.S3Bucket == "" {
			logger.Warn("S3_BUCKET is not set; original files are not stored")
		} else {
			s3Ctx, s3Cancel := context.WithTimeout(ctx, 10*time.Second)
			s3c, s3Err := s3store.New(s3Ctx, cfg)
			if s3Err == nil {
				s3Err = s3c.Verify(s3Ctx)
			}
			s3Cancel()
			if errors.Is(s3Err, s3store.ErrBucketUnavailable) {
				return fmt.Errorf("startup: s3: %w", s3Err)
			}
			if s3Err != nil {
				logger.Warn("S3 initialization or bucket verification failed", "error", s3Err)
			}
			s3Client = s3c
		}
	}

	// Initialize chunker.
	chk := chunker.NewChunker(cfg.ChunkTokenSize, cfg.ChunkOverlap)

	// Initialize retriever.
	ret := retriever.NewRetriever(vectorStore, emb, cfg.LocalRetrievalThreshold, cfg.MaxChunksPerQuery)

	// Build ingestion pipeline stages.
	ingestStages := []pipeline.Stage{
		pipeline.NewChunkStage(chk),
		pipeline.NewEmbedStage(emb, cfg.EmbedConcurrency),
		pipeline.NewPersistStage(vectorStore),
	}

	serviceMetrics := metrics.NewService(metrics.CachedGauge(10*time.Second, func() (float64, error) {
		gaugeCtx, gaugeCancel := context.WithTimeout(ctx, 5*time.Second)
		defer gaugeCancel()
		count, countErr := pgClient.PendingIngestJobs(gaugeCtx)
		if countErr != nil {
			slog.ErrorContext(gaugeCtx, "count pending ingestion jobs", "error", countErr)
		}
		return float64(count), countErr
	}))

	// Initialize worker.
	hostname, err := os.Hostname()
	if err != nil {
		return fmt.Errorf("startup: hostname: %w", err)
	}
	w := worker.New(pgClient, ingestStages, vecDeleter, worker.Options{Metrics: serviceMetrics, Workers: cfg.IngestWorkers, MaxAttempts: cfg.IngestMaxAttempts, Lease: cfg.IngestLease, JobTimeout: cfg.IngestJobTimeout, PollInterval: cfg.IngestPollInterval, Owner: hostname + "-" + uuid.NewString()})
	w.Start(ctx)
	defer func() {
		shutdownCtx, stop := context.WithTimeout(context.Background(), 15*time.Second)
		defer stop()
		if shutdownErr := w.ShutdownContext(shutdownCtx); shutdownErr != nil {
			slog.Error("worker shutdown", "error", shutdownErr)
		}
	}()

	// Build HTTP router.
	probes := handler.NewProbes(vectorStore, pgClient, s3Client, cfg.HealthCacheTTL)
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
		Probes:         probes,
		Metrics:        serviceMetrics,
	})

	srv := &http.Server{
		Addr:              fmt.Sprintf(":%s", cfg.ServerPort),
		Handler:           router,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       cfg.HTTPIdleTimeout,
		ReadTimeout:       cfg.HTTPReadTimeout,
		WriteTimeout:      cfg.HTTPWriteTimeout,
	}

	signalCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	listener, err := (&net.ListenConfig{}).Listen(signalCtx, "tcp", srv.Addr)
	if err != nil {
		return fmt.Errorf("server: listen: %w", err)
	}
	var additional []httpEndpoint
	if cfg.MetricsAddr != "" {
		metricsSrv := metricsServer(cfg.MetricsAddr, serviceMetrics.Registry)
		metricsListener, listenErr := (&net.ListenConfig{}).Listen(signalCtx, "tcp", metricsSrv.Addr)
		if listenErr != nil {
			return errors.Join(fmt.Errorf("metrics: listen: %w", listenErr), listener.Close())
		}
		additional = append(additional, httpEndpoint{metricsSrv, metricsListener})
		logger.Info("starting metrics server", "address", metricsSrv.Addr)
	}
	logger.Info("starting server", "address", srv.Addr)
	return serve(signalCtx, srv, listener, probes, cfg.ShutdownDrainDelay, additional...)
}

type httpEndpoint struct {
	server   *http.Server
	listener net.Listener
}

func metricsServer(addr string, registry *metrics.Registry) *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		if err := registry.WriteText(w); err != nil {
			slog.ErrorContext(r.Context(), "write metrics", "error", err)
		}
	})
	return &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: time.Minute}
}

func serve(ctx context.Context, srv *http.Server, listener net.Listener, probes *handler.Probes, drain time.Duration, additional ...httpEndpoint) error {
	endpoints := append([]httpEndpoint{{srv, listener}}, additional...)
	results := make(chan error, len(endpoints))
	for _, endpoint := range endpoints {
		go func() { results <- endpoint.server.Serve(endpoint.listener) }()
	}
	remaining := len(endpoints)
	var result error
	select {
	case err := <-results:
		remaining--
		if !errors.Is(err, http.ErrServerClosed) {
			result = fmt.Errorf("server: %w", err)
		}
	case <-ctx.Done():
		secondSignal := make(chan os.Signal, 1)
		signal.Notify(secondSignal, syscall.SIGINT, syscall.SIGTERM)
		defer signal.Stop(secondSignal)
		probes.SetDraining(true)
		timer := time.NewTimer(drain)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-secondSignal:
		}
	}
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	shutdownErrors := make(chan error, len(endpoints))
	var wg sync.WaitGroup
	for _, endpoint := range endpoints {
		wg.Go(func() {
			if err := endpoint.server.Shutdown(shutdownCtx); err != nil {
				shutdownErrors <- errors.Join(fmt.Errorf("server shutdown: %w", err), endpoint.server.Close())
			}
		})
	}
	wg.Wait()
	close(shutdownErrors)
	for err := range shutdownErrors {
		result = errors.Join(result, err)
	}
	for range remaining {
		if err := <-results; !errors.Is(err, http.ErrServerClosed) {
			result = errors.Join(result, fmt.Errorf("server: %w", err))
		}
	}
	return result
}
