package domain

import (
	"context"
	"encoding/json"
	"time"
)

// DocumentSummary describes a document without its content or metadata.
type DocumentSummary struct {
	ID          string     `json:"document_id"`
	DocType     string     `json:"doc_type"`
	S3Key       *string    `json:"s3_key,omitempty"`
	Status      string     `json:"status"`
	ChunkCount  int        `json:"chunk_count"`
	FileName    *string    `json:"file_name"`
	FileType    *string    `json:"file_type"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	CompletedAt *time.Time `json:"completed_at"`
	LastError   *string    `json:"last_error,omitempty"`
}

// MarshalJSON exposes the last error only for failed documents.
func (d DocumentSummary) MarshalJSON() ([]byte, error) {
	type summary DocumentSummary
	if d.Status != "failed" {
		d.LastError = nil
	}
	return json.Marshal(summary(d))
}

// DocumentLister supports ordered document inventory and status lookups.
type DocumentLister interface {
	StreamDocuments(context.Context, func(DocumentSummary) error) error
	ListDocumentsPage(ctx context.Context, after string, limit int) ([]DocumentSummary, error)
	GetDocumentSummary(ctx context.Context, id DocumentID) (*DocumentSummary, error)
}
