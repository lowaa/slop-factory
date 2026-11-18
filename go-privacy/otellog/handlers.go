package otellog

import (
	"context"
	"log/slog"

	"go.opentelemetry.io/otel/trace"
)

// minLevel holds a handler to a level: the OTel bridge would otherwise
// export debug records that the local copy never shows.
type minLevel struct {
	slog.Handler
	level slog.Level
}

func (h *minLevel) Enabled(ctx context.Context, level slog.Level) bool {
	return level >= h.level && h.Handler.Enabled(ctx, level)
}

func (h *minLevel) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &minLevel{Handler: h.Handler.WithAttrs(attrs), level: h.level}
}

func (h *minLevel) WithGroup(name string) slog.Handler {
	return &minLevel{Handler: h.Handler.WithGroup(name), level: h.level}
}

// traceHandler adds the trace and span ids of the record's context to what it
// writes. The OTLP handler does this by itself, as the record's own trace
// fields; the local copy has no such field, and the ids are what ties a
// frontend error to the server's logs for the request.
type traceHandler struct{ slog.Handler }

func (h *traceHandler) Handle(ctx context.Context, record slog.Record) error {
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		record.AddAttrs(slog.String("trace_id", sc.TraceID().String()), slog.String("span_id", sc.SpanID().String()))
	}
	return h.Handler.Handle(ctx, record)
}

func (h *traceHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &traceHandler{Handler: h.Handler.WithAttrs(attrs)}
}

func (h *traceHandler) WithGroup(name string) slog.Handler {
	return &traceHandler{Handler: h.Handler.WithGroup(name)}
}

// fanout hands every record to each handler that wants its level.
type fanout struct {
	handlers []slog.Handler
}

func (h *fanout) Enabled(ctx context.Context, level slog.Level) bool {
	for _, handler := range h.handlers {
		if handler.Enabled(ctx, level) {
			return true
		}
	}
	return false
}

// Handle gives each handler its own clone: a Record's attributes share
// backing storage, and one handler's use of them must not show in the next's.
// Every handler is tried whatever an earlier one returned, so a failing
// export never costs the local copy, nor the reverse.
func (h *fanout) Handle(ctx context.Context, record slog.Record) error {
	var first error
	for _, handler := range h.handlers {
		if !handler.Enabled(ctx, record.Level) {
			continue
		}
		if err := handler.Handle(ctx, record.Clone()); err != nil && first == nil {
			first = err
		}
	}
	return first
}

func (h *fanout) WithAttrs(attrs []slog.Attr) slog.Handler {
	next := make([]slog.Handler, len(h.handlers))
	for i, handler := range h.handlers {
		next[i] = handler.WithAttrs(attrs)
	}
	return &fanout{handlers: next}
}

func (h *fanout) WithGroup(name string) slog.Handler {
	next := make([]slog.Handler, len(h.handlers))
	for i, handler := range h.handlers {
		next[i] = handler.WithGroup(name)
	}
	return &fanout{handlers: next}
}
