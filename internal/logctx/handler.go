// Package logctx correlates structured logs with HTTP requests.
package logctx

import (
	"context"
	"log/slog"
)

type requestIDKey struct{}

// WithRequestID returns a context carrying the request ID.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey{}, id)
}

// RequestID returns the request ID, or an empty string when none is present.
func RequestID(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

type handler struct{ slog.Handler }

// NewHandler adds the context's request ID to records handled by next.
func NewHandler(next slog.Handler) slog.Handler {
	return &handler{Handler: next}
}

func (h *handler) Handle(ctx context.Context, record slog.Record) error {
	if id := RequestID(ctx); id != "" {
		record.AddAttrs(slog.String("request_id", id))
	}
	return h.Handler.Handle(ctx, record)
}

func (h *handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return NewHandler(h.Handler.WithAttrs(attrs))
}

func (h *handler) WithGroup(name string) slog.Handler {
	return NewHandler(h.Handler.WithGroup(name))
}
