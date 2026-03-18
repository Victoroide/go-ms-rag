// Package main provides a cleanup utility to delete test documents from Pinecone and Neon.
package main

import (
	"context"
	"fmt"
	"log"

	"github.com/Victoroide/go-ms-rag/internal/config"
	pcstore "github.com/Victoroide/go-ms-rag/internal/store/pinecone"
	pgstore "github.com/Victoroide/go-ms-rag/internal/store/postgres"
	"github.com/jackc/pgx/v5"
)

func main() {
	if err := run(); err != nil {
		log.Fatalf("cleanup error: %v", err)
	}
}

func run() error {
	ctx := context.Background()

	// Load config
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	// Test document IDs to delete
	testDocIDs := []string{
		"a15fc9bc-d80d-4fbe-979f-0e60b5ae0032", // Large file test
		"97d7d355-6eef-4559-8f49-dd0ff3e4f8a3", // Smoke test Maria Garcia
		"f9fa6418-6b0d-43a0-b73c-24b641ff4a17", // JSON ingest test
	}

	// Connect to Postgres
	pgClient, err := pgstore.NewClient(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("postgres connect: %w", err)
	}
	defer pgClient.Close(ctx)

	// Connect to Pinecone
	pcClient, err := pcstore.NewPineconeClient(ctx, cfg)
	if err != nil {
		return fmt.Errorf("pinecone connect: %w", err)
	}

	log.Printf("Deleting %d test documents...", len(testDocIDs))

	for _, docID := range testDocIDs {
		log.Printf("Processing document: %s", docID)

		// Delete from Pinecone (candidate namespace)
		if err := pcClient.Delete(ctx, "candidate", []string{docID}); err != nil {
			log.Printf("  WARNING: pinecone delete candidate namespace: %v", err)
		} else {
			log.Printf("  Deleted from Pinecone candidate namespace")
		}

		// Delete from Pinecone (default namespace)
		if err := pcClient.Delete(ctx, "", []string{docID}); err != nil {
			log.Printf("  WARNING: pinecone delete default namespace: %v", err)
		} else {
			log.Printf("  Deleted from Pinecone default namespace")
		}

		// Delete from Postgres
		conn, err := pgClient.Pool.Acquire(ctx)
		if err != nil {
			log.Printf("  WARNING: acquire connection: %v", err)
			continue
		}

		batch := &pgx.Batch{}
		batch.Queue("DELETE FROM chunks WHERE document_id = $1", docID)
		batch.Queue("DELETE FROM document_fingerprints WHERE document_id = $1", docID)
		batch.Queue("DELETE FROM documents WHERE id = $1", docID)

		results := conn.SendBatch(ctx, batch)
		for i := 0; i < 3; i++ {
			if _, err := results.Exec(); err != nil {
				log.Printf("  WARNING: postgres delete batch %d: %v", i, err)
			}
		}
		results.Close()
		conn.Release()

		log.Printf("  Deleted from Postgres")
	}

	log.Printf("Cleanup complete. Deleted %d test documents.", len(testDocIDs))
	return nil
}
