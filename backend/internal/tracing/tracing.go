// Package tracing wires the OpenTelemetry Go SDK into each binary
// (tracing/instrumentation.md): one shared bootstrap so cmd/server,
// cmd/ws-gateway, and cmd/consumer all export spans to the same collector
// the same way, tagged with their own service name.
package tracing

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// Init constructs a TracerProvider exporting spans over OTLP/gRPC to
// endpoint (project-overview.md's OTEL_EXPORTER_OTLP_ENDPOINT, typically
// otel-collector.aviron.svc.cluster.local:4317), sets it as the global
// provider, and installs the W3C traceparent propagator every cross-process
// hop this feature touches (NATS headers, Kafka headers, otelhttp) relies
// on. Returns a shutdown func the caller must invoke during graceful
// shutdown to flush any buffered spans before the process exits — an
// unflushed batch exporter silently drops whatever hasn't been sent yet.
func Init(ctx context.Context, serviceName, endpoint string) (shutdown func(context.Context) error, err error) {
	exporter, err := otlptracegrpc.New(ctx,
		otlptracegrpc.WithEndpoint(endpoint),
		// The collector is only ever reached over the cluster's internal
		// network — no TLS anywhere else on this local stack either
		// (Postgres/Redis/Kafka/NATS all plaintext in-cluster, per
		// tracing/otel-collector-tempo-deploy.md's own Collector->Tempo hop).
		otlptracegrpc.WithInsecure(),
	)
	if err != nil {
		return nil, fmt.Errorf("tracing: create otlp exporter: %w", err)
	}

	res, err := resource.New(ctx,
		resource.WithAttributes(semconv.ServiceNameKey.String(serviceName)),
	)
	if err != nil {
		return nil, fmt.Errorf("tracing: build resource: %w", err)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.TraceContext{})

	return tp.Shutdown, nil
}
