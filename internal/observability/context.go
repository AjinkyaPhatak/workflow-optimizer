package observability

import (
	"context"
	"log/slog"
)

type requestIDKey struct{}

// WithRequestID stores the request correlation ID (X-Request-ID) in ctx.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey{}, id)
}

// RequestID returns the correlation ID in ctx ("" when none).
func RequestID(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

// ContextHandler adds request_id from the context to every record logged
// with a context (logger.InfoContext, ...), so request and execution logs
// correlate without passing IDs around.
type ContextHandler struct{ slog.Handler }

// NewContextLogger wraps logger's handler.
func NewContextLogger(logger *slog.Logger) *slog.Logger {
	if logger == nil {
		logger = slog.Default()
	}
	if _, ok := logger.Handler().(ContextHandler); ok {
		return logger
	}
	return slog.New(ContextHandler{logger.Handler()})
}

func (h ContextHandler) Handle(ctx context.Context, r slog.Record) error {
	if id := RequestID(ctx); id != "" {
		r.AddAttrs(slog.String("request_id", id))
	}
	return h.Handler.Handle(ctx, r)
}

func (h ContextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return ContextHandler{h.Handler.WithAttrs(attrs)}
}

func (h ContextHandler) WithGroup(name string) slog.Handler {
	return ContextHandler{h.Handler.WithGroup(name)}
}
