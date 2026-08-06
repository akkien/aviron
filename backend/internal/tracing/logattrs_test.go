package tracing_test

import (
	"context"
	"log/slog"
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/akkien/aviron/internal/tracing"
)

func TestLogAttrs_ValidSpan_ReturnsTraceAndSpanID(t *testing.T) {
	tp := sdktrace.NewTracerProvider()
	defer func() { _ = tp.Shutdown(context.Background()) }()

	ctx, span := tp.Tracer("test").Start(context.Background(), "test-span")
	defer span.End()
	sc := span.SpanContext()

	attrs := tracing.LogAttrs(ctx)
	if len(attrs) != 2 {
		t.Fatalf("LogAttrs returned %d attrs, want 2 (trace_id, span_id): %v", len(attrs), attrs)
	}

	traceAttr, ok := attrs[0].(slog.Attr)
	if !ok || traceAttr.Key != "trace_id" || traceAttr.Value.String() != sc.TraceID().String() {
		t.Errorf("attrs[0] = %+v, want trace_id=%s", attrs[0], sc.TraceID().String())
	}
	spanAttr, ok := attrs[1].(slog.Attr)
	if !ok || spanAttr.Key != "span_id" || spanAttr.Value.String() != sc.SpanID().String() {
		t.Errorf("attrs[1] = %+v, want span_id=%s", attrs[1], sc.SpanID().String())
	}
}

func TestLogAttrs_NoSpanInContext_ReturnsNil(t *testing.T) {
	if attrs := tracing.LogAttrs(context.Background()); attrs != nil {
		t.Errorf("LogAttrs(context.Background()) = %v, want nil", attrs)
	}
}

// TestLogAttrs_EndedSpan_StillReturnsValidIDs is a regression test for the
// exact bug workout_sample_loop.go had before logging/log-trace-
// correlation.md: a span's context must keep returning valid trace/span
// IDs after span.End(), since every call site that wants to log-correlate
// a just-finished span's work relies on that (e.g. workout_sample_loop.go
// ends its kafka.consume span immediately, before logging a decode
// failure against that same span's context).
func TestLogAttrs_EndedSpan_StillReturnsValidIDs(t *testing.T) {
	tp := sdktrace.NewTracerProvider()
	defer func() { _ = tp.Shutdown(context.Background()) }()

	ctx, span := tp.Tracer("test").Start(context.Background(), "test-span")
	span.End()

	if attrs := tracing.LogAttrs(ctx); len(attrs) != 2 {
		t.Errorf("LogAttrs after span.End() = %v, want 2 attrs (trace_id, span_id) still present", attrs)
	}
}
