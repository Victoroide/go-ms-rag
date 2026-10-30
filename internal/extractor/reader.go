package extractor

import (
	"context"
	"fmt"
	"io"

	"github.com/Victoroide/quarry-rag/internal/domain"
)

const (
	maxSourceBytes      = 100 << 20
	maxDocumentXMLBytes = 16 << 20
)

// ErrContentTooLarge indicates that extracted text exceeds the content byte limit.
var ErrContentTooLarge = fmt.Errorf("extracted text exceeds %d bytes", domain.MaxContentBytes)

func checkTextLimit(n int) error {
	if n > domain.MaxContentBytes {
		return ErrContentTooLarge
	}
	return nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

func readSource(ctx context.Context, r io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(contextReader{ctx: ctx, reader: r}, maxSourceBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxSourceBytes {
		return nil, fmt.Errorf("source exceeds maximum size of %d bytes", maxSourceBytes)
	}
	return data, nil
}
