package tracing

import (
	"context"
	"log/slog"

	"go.opentelemetry.io/otel/trace"
)

// LogAttrs returns trace_id/span_id slog attributes for ctx's active span
// (logging/log-trace-correlation.md) — nil if ctx carries no valid span
// context, so callers can unconditionally
// append(fields, tracing.LogAttrs(ctx)...) the same way they already
// conditionally append request_id/user_id.
func LogAttrs(ctx context.Context) []any {
	sc := trace.SpanContextFromContext(ctx)
	if !sc.IsValid() {
		return nil
	}
	return []any{
		slog.String("trace_id", sc.TraceID().String()),
		slog.String("span_id", sc.SpanID().String()),
	}
}
