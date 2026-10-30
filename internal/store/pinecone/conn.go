package pinecone

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"time"

	"github.com/Victoroide/quarry-rag/internal/config"
	"github.com/pinecone-io/go-pinecone/v5/pinecone"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"
)

// ErrMetadataTooLarge marks metadata that exceeds the per-record limit.
var ErrMetadataTooLarge = errors.New("pinecone: record metadata exceeds 40960 bytes")

type indexConn interface {
	UpsertVectors(context.Context, []*pinecone.Vector) (uint32, error)
	QueryByVectorValues(context.Context, *pinecone.QueryByVectorValuesRequest) (*pinecone.QueryVectorsResponse, error)
	DeleteVectorsByFilter(context.Context, *structpb.Struct) error
	DeleteVectorsById(context.Context, []string) error
	DeleteAllVectorsInNamespace(context.Context) error
	Close() error
}

var _ indexConn = (*pinecone.IndexConnection)(nil)

func (p *Client) do(ctx context.Context, attempts int, op func(context.Context) error) error {
	if attempts < 1 {
		return errors.New("pinecone: attempts must be positive")
	}
	backoff := 200 * time.Millisecond
	for attempt := 0; attempt < attempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		attemptCtx, cancel := context.WithTimeout(ctx, p.timeout)
		err := op(attemptCtx)
		cancel()
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !retryable(err) || attempt == attempts-1 {
			return err
		}
		// Retry timing does not require cryptographic randomness.
		timer := time.NewTimer(time.Duration(rand.Int64N(int64(backoff) + 1))) // #nosec G404
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		backoff = min(5*time.Second, 2*backoff)
	}
	return nil
}

func retryable(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	switch status.Code(err) {
	case codes.Unavailable, codes.ResourceExhausted, codes.Aborted, codes.Internal, codes.DeadlineExceeded:
		return true
	default:
		return false
	}
}

func validateIndex(idx *pinecone.Index, cfg *config.Config) error {
	if idx == nil {
		return errors.New("pinecone: missing index description")
	}
	if idx.Dimension == nil {
		return errors.New("pinecone: missing index dimension")
	}
	if int64(*idx.Dimension) != int64(cfg.EmbeddingDimensions) {
		return fmt.Errorf("pinecone: index dimension %d does not match EMBEDDING_DIMENSIONS %d", *idx.Dimension, cfg.EmbeddingDimensions)
	}
	if idx.Metric != pinecone.Cosine {
		return fmt.Errorf("pinecone: index metric must be cosine, got %q", idx.Metric)
	}
	if idx.Status == nil || !idx.Status.Ready {
		return errors.New("pinecone: index is not ready")
	}
	return nil
}
