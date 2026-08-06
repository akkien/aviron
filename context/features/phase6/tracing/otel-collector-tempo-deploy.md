# OpenTelemetry Collector + Tempo Deployment

## Overview

Pure infrastructure, no application code — this spec stands up the two
new pieces `tracing/instrumentation.md` needs to actually push spans
somewhere: a single **OpenTelemetry Collector** every binary sends OTLP
to, and **Tempo** as the trace storage/query backend behind it
(`phase-6-plan.md`'s Decisions #1). Sequenced before `instrumentation.md`
deliberately — `otel.SetTracerProvider` with no real exporter reachable
produces spans nobody can ever look at, the exact trap
`phase3/observability/opentelemetry-tracing.md` already flagged once for
this project's much smaller original tracing scope.

Plain manifests, same stance as every other piece of infra this phase
deploys — both the Collector and Tempo (in its single-binary "monolithic
mode") are simple enough to hand-write without an operator.

## Requirements

### OpenTelemetry Collector

Receives OTLP (gRPC, port 4317) from all three binaries, forwards to
Tempo. No processors beyond the standard `batch` (reduces the number of
distinct export calls under this system's per-telemetry-message span
volume — `phase-6-plan.md`'s Decisions #3) and `memory_limiter` (a
misbehaving instrumented binary must not OOM the Collector).

```yaml
# deploy/k8s/otel-collector/configmap.yaml (otel-collector-config.yaml key)
receivers:
  otlp:
    protocols:
      grpc:
        endpoint: 0.0.0.0:4317
processors:
  memory_limiter:
    check_interval: 1s
    limit_mib: 200
  batch:
    timeout: 5s
exporters:
  otlp:
    endpoint: tempo.aviron.svc.cluster.local:4317
    tls:
      insecure: true
service:
  pipelines:
    traces:
      receivers: [otlp]
      processors: [memory_limiter, batch]
      exporters: [otlp]
```

`tls.insecure: true` on the Collector -> Tempo hop is the same "no
internal TLS" stance this whole local cluster already takes for
Postgres/Redis/Kafka/NATS — not a new exception.

### Tempo

Single-binary "monolithic mode" (`tempo -config.file=...`), local
filesystem backend for both the WAL and block storage — Tempo's own
Helm chart supports far more (S3/GCS backends, distributed
microservices mode), none of which this project needs at laptop-`kind`
scale:

```yaml
# deploy/k8s/tempo/configmap.yaml (tempo.yaml key, sketch)
server:
  http_listen_port: 3200
distributor:
  receivers:
    otlp:
      protocols:
        grpc:
          endpoint: 0.0.0.0:4317
storage:
  trace:
    backend: local
    local:
      path: /var/tempo/traces
    wal:
      path: /var/tempo/wal
```

Tempo receives OTLP directly too (its own `distributor.receivers.otlp`)
— the Collector could point at Tempo's OTLP endpoint just like a
binary could in principle, but the whole reason the Collector exists in
this architecture (`phase-6-plan.md`'s "Proposed architecture") is a
single fan-out point every binary talks to, not per-service knowledge of
where traces ultimately land.

### Storage

`emptyDir` for both Tempo's WAL and block storage — same "no PVC, data
doesn't survive a pod restart" stance already accepted for Prometheus
(`metrics/prometheus-deploy.md`) and Kafka. A restarted Tempo pod losing
trace history is an acceptable loss for a laptop demo cluster, not a
regression from anything this project already promises elsewhere.

### Services

- `otel-collector` — `ClusterIP`, port `4317` (OTLP gRPC), the address
  every binary's `OTEL_EXPORTER_OTLP_ENDPOINT` points at.
- `tempo` — `ClusterIP`, ports `3200` (Tempo's own query API, what
  Grafana's Tempo data source reads from in `dashboards/grafana-
  deploy.md`) and `4317` (OTLP, Collector's target).

## Verification

- `kubectl apply` both; `kubectl get pods -n aviron -l app=otel-collector`
  and `-l app=tempo` reach `Running`/`Ready`.
- Send a manual test span (e.g. a throwaway `curl` against the
  Collector's OTLP HTTP receiver, or a one-line Go program using
  `go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc`) and
  confirm it's queryable via Tempo's own API (`GET /api/search`) before
  `tracing/instrumentation.md` starts sending real application spans —
  proves the pipeline works end to end independent of any app-code
  change.

## Notes

- No dependency on any other Phase 6 spec — pure infra, can build in
  parallel with `metrics/prometheus-deploy.md` per `phase-6-plan.md`'s
  "Dependency order".
- `tracing/instrumentation.md` depends on this spec being live to verify
  against.
- Grafana's Tempo data source (`dashboards/grafana-deploy.md`) points at
  `tempo.aviron.svc.cluster.local:3200`, not the Collector — Grafana
  queries Tempo directly, never through the Collector.
