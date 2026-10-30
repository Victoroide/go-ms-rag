// Package cleanup removes documents and their stored data.
package cleanup

import (
	"context"
	"errors"
	"fmt"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/Victoroide/quarry-rag/internal/config"
	"github.com/Victoroide/quarry-rag/internal/domain"
	pcstore "github.com/Victoroide/quarry-rag/internal/store/pinecone"
	pgstore "github.com/Victoroide/quarry-rag/internal/store/postgres"
	s3store "github.com/Victoroide/quarry-rag/internal/store/s3"
)

// ObjectDeleter removes a stored source file.
type ObjectDeleter interface {
	Delete(context.Context, string) error
}

// Remove deletes external data before metadata so failed operations remain retryable.
func Remove(ctx context.Context, cs domain.ChunkStore, vectors domain.VectorDeleter, objects ObjectDeleter, doc *domain.Document) error {
	if doc.S3Key != nil && objects == nil {
		return ErrFileStorageUnavailable
	}
	if vectors != nil {
		if err := vectors.DeleteAllVectorsInNamespace(ctx, string(doc.ID)); err != nil && status.Code(err) != codes.NotFound {
			return fmt.Errorf("delete document namespace: %w", err)
		}
		if err := vectors.DeleteByDocID(ctx, string(doc.ID)); err != nil && status.Code(err) != codes.NotFound {
			return fmt.Errorf("delete global document vectors: %w", err)
		}
	}
	if objects != nil && doc.S3Key != nil {
		if err := objects.Delete(ctx, *doc.S3Key); err != nil {
			return fmt.Errorf("delete source file: %w", err)
		}
	}
	if err := cs.DeleteDocument(ctx, doc.ID); err != nil {
		return fmt.Errorf("delete document metadata: %w", err)
	}
	if vectors != nil {
		if err := vectors.DeleteByDocID(ctx, string(doc.ID)); err != nil && status.Code(err) != codes.NotFound {
			return fmt.Errorf("delete document vectors after metadata: %w", err)
		}
	}
	return nil
}

// ErrDocumentNotFound indicates that metadata is not yet present or was removed.
var ErrDocumentNotFound = errors.New("document not found")

// ErrFileStorageUnavailable indicates a stored file cannot be removed without object storage.
var ErrFileStorageUnavailable = errors.New("file storage is not configured")

// Cleaner reuses store connections for a sequence of document deletions.
type Cleaner struct {
	metadata *pgstore.Client
	vectors  *pcstore.Client
	objects  ObjectDeleter
}

// New connects the stores required for document deletion.
func New(ctx context.Context, cfg *config.Config) (*Cleaner, error) {
	pg, err := pgstore.NewClient(ctx, cfg.DatabaseURL)
	if err != nil {
		return nil, fmt.Errorf("postgres connect: %w", err)
	}
	pc, err := pcstore.NewPineconeClient(ctx, cfg)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("pinecone connect: %w", err), pg.Close(ctx))
	}
	cleaner := &Cleaner{metadata: pg, vectors: pc}
	if cfg.S3Bucket != "" {
		cleaner.objects, err = s3store.New(ctx, cfg)
		if err != nil {
			return nil, errors.Join(fmt.Errorf("s3 connect: %w", err), cleaner.Close(ctx))
		}
	}
	return cleaner, nil
}

// Delete removes one stored document and its external data.
func (c *Cleaner) Delete(ctx context.Context, docID string) error {
	doc, err := c.metadata.GetDocument(ctx, domain.DocumentID(docID))
	if err != nil {
		return err
	}
	if doc == nil {
		return fmt.Errorf("%w: %s", ErrDocumentNotFound, docID)
	}
	return Remove(ctx, c.metadata, c.vectors, c.objects, doc)
}

// Close releases the shared store connections.
func (c *Cleaner) Close(ctx context.Context) error {
	return errors.Join(c.vectors.Close(), c.metadata.Close(ctx))
}
