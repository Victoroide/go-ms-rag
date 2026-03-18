// Package postgres provides chunk metadata and document fingerprint storage.
package postgres

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"log"

	"github.com/Victoroide/go-ms-rag/internal/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/001_init.sql
var migrationSQL string

// Client manages the PostgreSQL connection pool and provides
// chunk metadata and document fingerprint operations.
type Client struct {
	Pool *pgxpool.Pool
}

// NewClient creates a PostgreSQL client from DATABASE_URL.
func NewClient(ctx context.Context, databaseURL string) (*Client, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("postgres: connect: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("postgres: ping: %w", err)
	}
	log.Printf("postgres: connected")
	return &Client{Pool: pool}, nil
}

// RunMigrations executes the embedded SQL migration.
func (c *Client) RunMigrations(ctx context.Context) error {
	if err := c.runMigrations(ctx); err != nil {
		return fmt.Errorf("postgres: run migrations: %w", err)
	}
	log.Printf("postgres: migrations applied")
	return nil
}

func (c *Client) runMigrations(ctx context.Context) error {
	_, err := c.Pool.Exec(ctx, migrationSQL)
	return err
}

// InsertDocument inserts a document record.
func (c *Client) InsertDocument(ctx context.Context, doc domain.Document) error {
	metadataJSON, err := json.Marshal(doc.Metadata)
	if err != nil {
		metadataJSON = []byte("{}")
	}
	_, err = c.Pool.Exec(ctx,
		`INSERT INTO documents (id, content, metadata, chunk_count)
		 VALUES ($1, $2, $3, $4)
		 ON CONFLICT (id) DO UPDATE SET content = $2, metadata = $3, chunk_count = $4`,
		string(doc.ID), doc.Content, metadataJSON, doc.ChunkCount,
	)
	if err != nil {
		return fmt.Errorf("postgres: insert document %s: %w", doc.ID, err)
	}
	return nil
}

// InsertChunks inserts chunk metadata records.
func (c *Client) InsertChunks(ctx context.Context, chunks []domain.Chunk) error {
	for _, ch := range chunks {
		_, err := c.Pool.Exec(ctx,
			`INSERT INTO chunks (id, document_id, chunk_index, text, token_count)
			 VALUES ($1, $2, $3, $4, $5)
			 ON CONFLICT (id) DO NOTHING`,
			string(ch.ID), string(ch.DocumentID), ch.Index, ch.Text, ch.TokenCount,
		)
		if err != nil {
			return fmt.Errorf("postgres: insert chunk %s: %w", ch.ID, err)
		}
	}
	return nil
}

// GetDocument retrieves a document by ID.
func (c *Client) GetDocument(ctx context.Context, id domain.DocumentID) (*domain.Document, error) {
	var doc domain.Document
	var docID string
	var metadataJSON []byte
	err := c.Pool.QueryRow(ctx,
		`SELECT id, content, metadata, chunk_count FROM documents WHERE id = $1`,
		string(id),
	).Scan(&docID, &doc.Content, &metadataJSON, &doc.ChunkCount)
	if err != nil {
		return nil, fmt.Errorf("postgres: get document %s: %w", id, err)
	}
	doc.ID = domain.DocumentID(docID)
	if len(metadataJSON) > 0 {
		_ = json.Unmarshal(metadataJSON, &doc.Metadata)
	}
	return &doc, nil
}

// CheckFingerprint checks whether a document fingerprint already exists.
func (c *Client) CheckFingerprint(ctx context.Context, fingerprint string) (domain.DocumentID, bool, error) {
	var docID string
	err := c.Pool.QueryRow(ctx,
		`SELECT document_id FROM document_fingerprints WHERE fingerprint = $1`,
		fingerprint,
	).Scan(&docID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("postgres: check fingerprint: %w", err)
	}
	return domain.DocumentID(docID), true, nil
}

// StoreFingerprint records a document fingerprint.
func (c *Client) StoreFingerprint(ctx context.Context, fingerprint, docID string) error {
	_, err := c.Pool.Exec(ctx,
		`INSERT INTO document_fingerprints (fingerprint, document_id)
		 VALUES ($1, $2)
		 ON CONFLICT (fingerprint) DO NOTHING`,
		fingerprint, docID,
	)
	if err != nil {
		return fmt.Errorf("postgres: store fingerprint: %w", err)
	}
	return nil
}

// Ping verifies the database connection.
func (c *Client) Ping(ctx context.Context) error {
	return c.Pool.Ping(ctx)
}

// Close shuts down the connection pool.
func (c *Client) Close(_ context.Context) error {
	c.Pool.Close()
	return nil
}
