# Trace-Based Alert Rule — Tempo Metrics-Generator -> Prometheus -> Alertmanager

## Overview

Unlike logs, traces don't get their own alerting engine here. There's no
natural "count of matching traces over a threshold" query against Tempo
the way Elasticsearch supports for logs — Tempo is built for trace
search/retrieval, not aggregate querying. The standard way to alert on
trace data is Tempo's own **metrics-generator**: it derives
Prometheus-shaped RED metrics (call counts, latencies, error rates) from
the spans flowing through it and remote-writes them into Prometheus.
That turns "alert on traces" into one more Prometheus rule, so this rule
stays on the existing Alertmanager pipeline (`alerting/alert-rules.md`)
instead of adding a third alerting path alongside Grafana Alerting
(`alerting/log-alert-rules.md`).

## Requirements

### Tempo gains a `metrics_generator` block

Amends `tracing/otel-collector-tempo-deploy.md`'s `tempo.yaml` — the
span-metrics processor derives `traces_spanmetrics_calls_total` (a
counter, labeled `service`, `span_name`, `span_kind`, `status_code`)
from every span Tempo already receives, no new instrumentation needed in
`tracing/instrumentation.md`'s app code:

```yaml
# addition to deploy/k8s/tempo/configmap.yaml (tempo.yaml key)
overrides:
  defaults:
    metrics_generator:
      processors: [span-metrics]
metrics_generator:
  storage:
    path: /var/tempo/generator-wal
  registry:
    external_labels:
      cluster: aviron-kind
  remote_write:
    - url: http://prometheus.aviron.svc.cluster.local:9090/api/v1/write
      send_exemplars: true
```

### Prometheus gains `--web.enable-remote-write-receiver`

Amends `metrics/prometheus-deploy.md`'s Deployment `args` — the one
deliberate exception to this project's pull-only metrics stance
(`phase-6-plan.md`'s Decisions #5), scoped narrowly to this single push
path from Tempo's metrics-generator, not a general policy change:

```yaml
# addition to deploy/k8s/prometheus/deployment.yaml args
args:
  - --config.file=/etc/prometheus/prometheus.yml
  - --storage.tsdb.path=/prometheus
  - --web.enable-remote-write-receiver
```

### One rule: `SpanErrorRateHigh`, appended to the existing rule file

Appends to `deploy/k8s/prometheus/configmap-rules.yaml` (the same
`aviron` rule group `alerting/alert-rules.md` already owns) — a 9th
rule, not a second `ConfigMap`/rule group, keeping one Alertmanager
pipeline:

```yaml
- alert: SpanErrorRateHigh
  expr: |
    sum by (service) (rate(traces_spanmetrics_calls_total{status_code="STATUS_CODE_ERROR"}[5m]))
      / sum by (service) (rate(traces_spanmetrics_calls_total[5m])) > 0.1
  for: 2m
  labels: { severity: warning }
  annotations:
    summary: "{{ $labels.service }} span error rate above 10% over 5m"
```

Relies on `otelhttp`'s own automatic behavior (already true once
`tracing/instrumentation.md` lands): a REST entry-point span's status is
set to `STATUS_CODE_ERROR` whenever the handler returns a 5xx response
— no app-code change this spec needs to make, purely infra (Tempo
config + Prometheus flag + one more rule).

Routed through `alerting/alert-rules.md`'s existing `route`/`receivers`
block unchanged — same `telegram-relay` receiver, no new Alertmanager
config.

## Verification

- Once Tempo's metrics-generator is live and a few real requests have
  flowed through (any REST traffic, `tracing/instrumentation.md` already
  produces entry-point spans for all of it), confirm
  `traces_spanmetrics_calls_total` appears as a real series in
  Prometheus's own `/graph`/`/api/v1/query` — proof the remote-write hop
  actually works before trusting the alert rule on top of it.
- **Trigger it on demand**: briefly take Postgres down
  (`kubectl scale statefulset postgres --replicas=0 -n aviron`) and send
  a burst of requests against an endpoint that depends on it (e.g.
  `POST /races` repeatedly) — the resulting 500s tag their REST
  entry-point spans `STATUS_CODE_ERROR`, pushing `race-service`'s span
  error ratio past 10% within the 2m `for` window almost immediately.
  Scale Postgres back up afterward.
- Confirm: fires in Prometheus's `/alerts`, reaches Alertmanager, reaches
  `telegram-relay`, and a real message lands in the configured Telegram
  chat — the same chain `alerting/alert-rules.md`'s own verification
  already exercises for its 8 metric rules, now proven for a
  trace-derived one too.

## Notes

- Depends on `tracing/otel-collector-tempo-deploy.md` and
  `tracing/instrumentation.md` (needs real spans flowing through Tempo,
  and `otelhttp`'s span-status-on-5xx behavior) and `metrics/prometheus-
  deploy.md` (the remote-write-receiver flag) all already live, plus
  `alerting/alert-rules.md` (the rule file/Alertmanager pipeline this
  appends to).
- Exact metric/label names (`traces_spanmetrics_calls_total`,
  `status_code`) are Tempo's own metrics-generator convention as of the
  version pinned in `otel-collector-tempo-deploy.md` — confirm against
  the actually-deployed version at `start` rather than trusting this
  spec's naming from memory alone.
- Only one rule, deliberately, for the same reason `log-alert-rules.md`
  stops at one — proving the mechanism (Tempo metrics-generator ->
  Prometheus -> Alertmanager -> Telegram) is the point here, not rule
  coverage. `service-graphs` (the other half of Tempo's
  metrics-generator, deriving request-graph metrics between services) is
  left unconfigured — no rule needs it yet, and enabling it unused would
  just be scope creep.
- No changes to `alerting/telegram-relay.md` or `internal/telegramrelay`
  — this rule flows through the exact same Alertmanager webhook receiver
  every other rule in `alerting/alert-rules.md` already uses.
</content>
