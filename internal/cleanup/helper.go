// Package cleanup provides utilities to clean up test documents from Pinecone and Neon.
package cleanup

import (
	"context"
	"fmt"
	"log"

	"github.com/Victoroide/go-ms-rag/internal/config"
	pcstore "github.com/Victoroide/go-ms-rag/internal/store/pinecone"
	pgstore "github.com/Victoroide/go-ms-rag/internal/store/postgres"
	"github.com/jackc/pgx/v5"
)

// CleanupDocument deletes a document and all its data from both Pinecone and Postgres.
func CleanupDocument(ctx context.Context, cfg *config.Config, docID string) error {
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

	log.Printf("Cleaning up document: %s", docID)

	// Delete from Pinecone (candidate namespace)
	if delErr := pcClient.Delete(ctx, "candidate", []string{docID}); delErr != nil {
		log.Printf("  WARNING: pinecone delete candidate namespace: %v", delErr)
	} else {
		log.Printf("  Deleted from Pinecone candidate namespace")
	}

	// Delete from Pinecone (default namespace)
	if delErr := pcClient.Delete(ctx, "", []string{docID}); delErr != nil {
		log.Printf("  WARNING: pinecone delete default namespace: %v", delErr)
	} else {
		log.Printf("  Deleted from Pinecone default namespace")
	}

	// Delete the document's namespace (contains all chunk vectors)
	if delErr := pcClient.DeleteAllVectorsInNamespace(ctx, docID); delErr != nil {
		log.Printf("  WARNING: pinecone delete namespace %q: %v", docID, delErr)
	} else {
		log.Printf("  Deleted Pinecone namespace %q", docID)
	}

	// Delete from Postgres
	conn, err := pgClient.Pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire connection: %w", err)
	}
	defer conn.Release()

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

	log.Printf("  Deleted from Postgres")
	return nil
}
