// Package postgres provides chunk metadata and document fingerprint storage.
package postgres

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"log"

	"github.com/Victoroide/quarry-rag/internal/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/001_init.sql
var migrationSQL string

//go:embed migrations/002_add_file_storage.sql
var migration002SQL string

//go:embed migrations/003_add_content_preview.sql
var migration003SQL string

//go:embed migrations/004_add_doc_type.sql
var migration004SQL string

//go:embed migrations/005_document_default.sql
var migration005SQL string

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
	if _, err := c.Pool.Exec(ctx, migrationSQL); err != nil {
		return err
	}
	if _, err := c.Pool.Exec(ctx, migration002SQL); err != nil {
		return err
	}
	if _, err := c.Pool.Exec(ctx, migration003SQL); err != nil {
		return err
	}
	if _, err := c.Pool.Exec(ctx, migration004SQL); err != nil {
		return err
	}
	_, err := c.Pool.Exec(ctx, migration005SQL)
	return err
}

// InsertDocument inserts a document record.
func (c *Client) InsertDocument(ctx context.Context, doc domain.Document) error {
	metadataJSON, err := json.Marshal(doc.Metadata)
	if err != nil {
		return fmt.Errorf("postgres: encode document metadata: %w", err)
	}
	// Extract doc_type from metadata for the dedicated column.
	docType := domain.DefaultDocType
	if doc.Metadata != nil {
		if dt, ok := doc.Metadata["doc_type"]; ok {
			if s, ok := dt.(string); ok && s != "" {
				docType = s
			}
		}
	}
	_, err = c.Pool.Exec(ctx,
		`INSERT INTO documents (id, content, content_preview, metadata, chunk_count, file_name, file_type, s3_key, doc_type)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		 ON CONFLICT (id) DO UPDATE SET content = $2, content_preview = $3, metadata = $4, chunk_count = $5, file_name = $6, file_type = $7, s3_key = $8, doc_type = $9`,
		string(doc.ID), doc.Content, doc.ContentPreview, metadataJSON, doc.ChunkCount, doc.FileName, doc.FileType, doc.S3Key, docType,
	)
	if err != nil {
		return fmt.Errorf("postgres: insert document %s: %w", doc.ID, err)
	}
	return nil
}

// InsertChunks inserts chunk metadata records.
func (c *Client) InsertChunks(ctx context.Context, chunks []domain.Chunk) error {
	if len(chunks) == 0 {
		return nil
	}
	tx, err := c.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("postgres: begin chunk batch: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	for _, ch := range chunks {
		_, err := tx.Exec(ctx,
			`INSERT INTO chunks (id, document_id, chunk_index, text, token_count)
			 VALUES ($1, $2, $3, $4, $5)
			 ON CONFLICT (id) DO UPDATE SET text=EXCLUDED.text, token_count=EXCLUDED.token_count, chunk_index=EXCLUDED.chunk_index`,
			string(ch.ID), string(ch.DocumentID), ch.Index, ch.Text, ch.TokenCount,
		)
		if err != nil {
			return fmt.Errorf("postgres: insert chunk %s: %w", ch.ID, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("postgres: commit chunk batch: %w", err)
	}
	return nil
}

// UpdateDocumentStatus sets the status of a document (e.g. "completed", "failed").
func (c *Client) UpdateDocumentStatus(ctx context.Context, id domain.DocumentID, status string) error {
	_, err := c.Pool.Exec(ctx,
		`UPDATE documents SET status = $1 WHERE id = $2`,
		status, string(id),
	)
	if err != nil {
		return fmt.Errorf("postgres: update document status %s: %w", id, err)
	}
	return nil
}

// GetDocument retrieves a document by ID.
func (c *Client) GetDocument(ctx context.Context, id domain.DocumentID) (*domain.Document, error) {
	var doc domain.Document
	var docID string
	var metadataJSON []byte
	err := c.Pool.QueryRow(ctx,
		`SELECT id, content, metadata, chunk_count, s3_key FROM documents WHERE id = $1`,
		string(id),
	).Scan(&docID, &doc.Content, &metadataJSON, &doc.ChunkCount, &doc.S3Key)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("postgres: get document %s: %w", id, err)
	}
	doc.ID = domain.DocumentID(docID)
	if len(metadataJSON) > 0 {
		if err := json.Unmarshal(metadataJSON, &doc.Metadata); err != nil {
			return nil, fmt.Errorf("postgres: decode document metadata: %w", err)
		}
	}
	return &doc, nil
}

// GetDocumentS3Key retrieves the s3_key for a document. Returns nil if no file is stored.
func (c *Client) GetDocumentS3Key(ctx context.Context, id domain.DocumentID) (*string, error) {
	var s3Key *string
	err := c.Pool.QueryRow(ctx,
		`SELECT s3_key FROM documents WHERE id = $1`,
		string(id),
	).Scan(&s3Key)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("postgres: get document s3 key %s: %w", id, err)
	}
	return s3Key, nil
}

// ListDocuments returns a lightweight reference for every stored document, used
// for reconciliation with caller-owned records.
func (c *Client) ListDocuments(ctx context.Context) ([]domain.DocumentRef, error) {
	rows, err := c.Pool.Query(ctx, `SELECT id, doc_type, s3_key FROM documents`)
	if err != nil {
		return nil, fmt.Errorf("postgres: list documents: %w", err)
	}
	defer rows.Close()
	var out []domain.DocumentRef
	for rows.Next() {
		var ref domain.DocumentRef
		if err := rows.Scan(&ref.ID, &ref.DocType, &ref.S3Key); err != nil {
			return nil, fmt.Errorf("postgres: scan document ref: %w", err)
		}
		out = append(out, ref)
	}
	return out, rows.Err()
}

// DeleteDocument deletes a document by ID. Chunks and fingerprints cascade via FK.
func (c *Client) DeleteDocument(ctx context.Context, id domain.DocumentID) error {
	_, err := c.Pool.Exec(ctx, `DELETE FROM documents WHERE id = $1`, string(id))
	if err != nil {
		return fmt.Errorf("postgres: delete document %s: %w", id, err)
	}
	return nil
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
