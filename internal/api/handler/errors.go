package handler

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/Victoroide/quarry-rag/internal/pipeline"
	"github.com/aws/smithy-go"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var errQueryOverloaded = errors.New("query admission queue timed out")

func acquireQuerySlot(ctx context.Context, slots chan struct{}, timeout time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case slots <- struct{}{}:
		return nil
	default:
	}
	if timeout <= 0 {
		return errQueryOverloaded
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case slots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return errQueryOverloaded
	}
}

func writeQueryError(w http.ResponseWriter, r *http.Request, err error) {
	slog.ErrorContext(r.Context(), "query processing failed", "error", err)
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		writeJSONError(w, http.StatusGatewayTimeout, "timeout", "query processing timed out")
	case errors.Is(err, context.Canceled):
		return
	case errors.Is(err, errQueryOverloaded):
		w.Header().Set("Retry-After", "1")
		writeJSONError(w, http.StatusServiceUnavailable, "overloaded", "too many concurrent queries")
	case isUpstreamThrottled(err):
		w.Header().Set("Retry-After", "2")
		writeJSONError(w, http.StatusServiceUnavailable, "upstream_throttled", "upstream service is busy")
	case errors.Is(err, pipeline.ErrSynthesisEmpty):
		writeJSONError(w, http.StatusBadGateway, "synthesis", "answer synthesis returned no text")
	default:
		writeJSONError(w, http.StatusInternalServerError, "pipeline", "query processing failed")
	}
}

func isUpstreamThrottled(err error) bool {
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.ErrorCode() {
		case "ThrottlingException", "ServiceQuotaExceededException", "TooManyRequestsException":
			return true
		}
	}
	return status.Code(err) == codes.ResourceExhausted
}
