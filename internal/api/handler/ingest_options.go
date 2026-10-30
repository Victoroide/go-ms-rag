package handler

import (
	"context"

	"github.com/Victoroide/quarry-rag/internal/extractor"
)

type ingestOptions struct {
	maxPending int
	extract    func(context.Context, extractor.FileType, []byte) (string, error)
	limiter    *extractor.Limiter
}

// IngestOption configures file extraction and upload admission.
type IngestOption func(*ingestOptions)

// WithExtractor selects the function used to extract uploaded document text.
func WithExtractor(extract func(context.Context, extractor.FileType, []byte) (string, error)) IngestOption {
	return func(opts *ingestOptions) { opts.extract = extract }
}

// WithUploadLimiter reserves an upload slot before reading multipart data.
func WithUploadLimiter(limiter *extractor.Limiter) IngestOption {
	return func(opts *ingestOptions) { opts.limiter = limiter }
}

// WithMaxPending sets the maximum admitted ingestion jobs across replicas.
func WithMaxPending(limit int) IngestOption {
	return func(opts *ingestOptions) {
		if limit > 0 {
			opts.maxPending = limit
		}
	}
}
