# Observability Architecture (Phase 6)

This is the project-specific companion to `docs/observability.md` (general
industry background, in Vietnamese) and the source of truth is
`context/features/phase6/phase-6-plan.md` — this doc exists to show **the
whole picture in one place, with diagrams**, of what Phase 6 actually
builds for *this* system: `race-service`, `ws-gateway`, and `consumer`,
running as 2-5 replicas each under a `HorizontalPodAutoscaler`, where a
single player action already crosses process, broker, and machine
boundaries (WebSocket → NATS → Redis → Kafka → Postgres).

## Status at a glance

| Pillar | Tool | Status | Spec |
| --- | --- | --- | --- |
| Metrics | Prometheus (pull-based) | **Shipped** | `metrics/metrics-parity.md`, `metrics/prometheus-deploy.md` |
| Traces — infra | OTel Collector + Tempo | **Shipped** | `tracing/otel-collector-tempo-deploy.md` |
| Traces — app code | OpenTelemetry SDK, full depth | **Shipped** | `tracing/instrumentation.md` |
| Logs — correlation | `trace_id` in `slog` output | **Shipped** | `logging/log-trace-correlation.md` |
| Logs — backend | EFK (Elasticsearch, Fluent Bit, Kibana) | **Shipped** | `logging/efk-deploy.md` |
| Dashboards | Grafana (RED + USE, pod-aware) | **Shipped** | `dashboards/grafana-deploy.md` |
| Alerting — rules | Prometheus alert rules + Alertmanager | **Shipped** | `alerting/alert-rules.md` |
| Alerting — delivery | `telegram-relay` (4th binary) → Telegram | **Shipped** | `alerting/telegram-relay.md` |
| Alerting — logs | Grafana Unified Alerting on Elasticsearch | **Shipped** | `alerting/log-alert-rules.md` |
| Alerting — traces | Tempo metrics-generator → Prometheus rule | **Shipped** | `alerting/trace-alert-rules.md` |
| End-to-end proof | Full walkthrough of every piece together | **Shipped** | `verification/phase-6-verification.md` |

Phase 6 is complete — every pillar above is deployed, instrumented, and
verified end to end against the live cluster by
`verification/phase-6-verification.md`'s own 12-step pass: a real k6 race
followed through metrics, traces, and logs, plus all three alert types
(metrics-based, log-based, trace-based) each confirmed reaching a real
Telegram message. That pass also surfaced two real capacity bugs under
combined heavy load — `otel-collector`'s batch size and Tempo's memory
limit — both fixed and re-verified; see "Two real capacity lessons" under
Pillar 4 below and `docs/k8s-deployment.md`'s Observability section for
the operational detail.

## The whole picture

Every one of this project's binaries feeds all four pillars simultaneously
— one `/metrics` scrape, one OTLP export, one stdout log line can all
describe the same event. Grafana is the single pane of glass that ties
them back together via `trace_id`.

```mermaid
flowchart TB
    subgraph apps["This project's binaries — 2-5 replicas each, under an HPA"]
        RS["race-service"]
        WG["ws-gateway"]
        CO["consumer"]
    end

    subgraph metrics["Metrics — pull-based — SHIPPED"]
        PROM["Prometheus<br/>kubernetes_sd_configs: role: pod<br/>(no static target list)"]
    end

    subgraph tracing["Traces — push-based OTLP — SHIPPED"]
        OTEL["OTel Collector<br/>otlp receiver :4317<br/>memory_limiter + batch"]
        TEMPO["Tempo<br/>monolithic mode<br/>local filesystem backend"]
    end

    subgraph logging["Logs — SHIPPED"]
        FB["Fluent Bit<br/>DaemonSet, tails pod stdout"]
        ES["Elasticsearch<br/>index: aviron-logs"]
        KIB["Kibana<br/>full-text log search"]
    end

    subgraph dash["Dashboards — SHIPPED"]
        GRAF["Grafana<br/>single pane of glass<br/>RED + USE, per-pod"]
    end

    subgraph alert["Alerting — SHIPPED — two independent engines"]
        AM["Alertmanager<br/>9 rules: 8 metric + 1 trace-derived"]
        GA["Grafana Unified Alerting<br/>1 rule: LogErrorRateHigh"]
        TG["telegram-relay<br/>4th binary — POST /alert"]
        TGAPI["Telegram Bot API"]
    end

    RS -->|"GET /metrics scrape"| PROM
    WG -->|"GET /metrics scrape"| PROM
    CO -->|"GET /metrics scrape"| PROM

    RS -->|"OTLP gRPC :4317"| OTEL
    WG -->|"OTLP gRPC :4317"| OTEL
    CO -->|"OTLP gRPC :4317"| OTEL
    OTEL --> TEMPO
    TEMPO -->|"metrics-generator<br/>remote_write (spanmetrics)"| PROM

    RS -.->|"stdout JSON (slog)"| FB
    WG -.->|"stdout JSON"| FB
    CO -.->|"stdout JSON"| FB
    FB --> ES
    ES --> KIB

    PROM --> GRAF
    TEMPO -->|"tracesToLogsV2"| GRAF
    ES -->|"derivedFields: trace_id"| GRAF

    PROM -->|"alert rules firing"| AM
    AM -->|"webhook POST /alert"| TG
    ES -->|"queried directly<br/>(Alertmanager can't)"| GA
    GA -->|"webhook POST /alert"| TG
    TG -->|"sendMessage"| TGAPI
```

Three deliberate asymmetries worth noticing in that diagram, all decided
directly with the user (`phase-6-plan.md`'s "Decisions"):

- **Metrics stay pull-based** (Prometheus scrapes `/metrics` directly)
  while **traces are push-based** (OTLP to the Collector). Real stacks
  mix transports like this rather than forcing everything through one
  pipe for architectural purity.
- **Logs skip the Collector entirely** — Fluent Bit tails each pod's
  stdout directly via the node's `kubelet`, which is the standard
  Kubernetes-native path and needs no application-side change. Routing
  logs through OTLP too would just be a second path to the same data.
- **Alerting is two independent engines, not one.** Alertmanager only
  speaks PromQL against Prometheus — it structurally can't query
  Elasticsearch, so `LogErrorRateHigh` (log *content*) lives in Grafana's
  own built-in Unified Alerting instead, which can alert on any
  provisioned data source. Trace data gets the opposite treatment: rather
  than a third alerting engine, Tempo's metrics-generator derives
  Prometheus-shaped RED metrics from real spans and remote-writes them
  into Prometheus, so `SpanErrorRateHigh` is an ordinary Alertmanager
  rule. Both engines terminate at the same `telegram-relay` webhook.

## Build order

Almost linear — only a few pairs could build in parallel, both because
neither had a real code/data dependency on the other. All 13 specs are
now shipped, in this order:

```mermaid
flowchart TD
    A["metrics-parity<br/>DONE"] --> B["prometheus-deploy<br/>DONE"]
    B --> D["otel-collector-tempo-deploy<br/>DONE"]
    C["efk-deploy<br/>DONE"] -.parallel with tracing track.-> D
    D --> E["instrumentation<br/>DONE"]
    E --> F["log-trace-correlation<br/>DONE"]
    F --> C
    C --> G["grafana-deploy<br/>DONE"]
    G --> H["alert-rules<br/>DONE"]
    H --> I["telegram-relay<br/>DONE"]
    I --> K["log-alert-rules<br/>DONE"]
    I --> L["trace-alert-rules<br/>DONE"]
    K --> J["phase-6-verification<br/>DONE"]
    L --> J
```

`prometheus-deploy` and `otel-collector-tempo-deploy` had no dependency
on each other either (both are pure infra) — they just happen to be drawn
sequentially above because `prometheus-deploy` shipped first in practice.
`log-alert-rules` and `trace-alert-rules` likewise had no dependency on
each other — both only needed `telegram-relay`'s webhook live, and each
plugs into a different alerting engine (Grafana's own vs. Alertmanager's).

## Pillar 1 — Metrics (shipped)

Pull-based: Prometheus discovers scrape targets dynamically via
`kubernetes_sd_configs` (`role: pod`), filtered to pods annotated
`prometheus.io/scrape: "true"` — not a static list, because
`race-service`/`ws-gateway` both scale 2-5 replicas under their own HPA
and a fixed list would go stale the same way `ws-gateway`'s old
`RACE_SERVICE_INSTANCES` did before dynamic backend discovery.

```mermaid
flowchart LR
    RS["race-service pods<br/>:8080/metrics"]
    WG["ws-gateway pods<br/>:8080/metrics"]
    CO["consumer pod<br/>:8091/metrics"]
    API["kube-apiserver<br/>Pod list/watch"]
    PROM["Prometheus"]

    PROM -->|"role: pod discovery"| API
    API -.->|"pod IPs + annotations"| PROM
    PROM -->|"scrape every 15s (default)"| RS
    PROM -->|"scrape"| WG
    PROM -->|"scrape"| CO
```

Each binary's own metric surface (`internal/metrics.Metrics` /
`GatewayMetrics` / `ConsumerMetrics`) instruments the infrastructure it
actually sits on top of, not just generic process stats:

| Binary | Notable metrics |
| --- | --- |
| `race-service` | `aviron_rooms_active`, `aviron_tick_latency_seconds`, `aviron_channel_buffer_used` |
| `race-service` + `ws-gateway` (shared: `internal/roomrelay`, `internal/roomlocator`) | `aviron_roomrelay_publish_*`, `aviron_roomlocator_lookup_duration_seconds` |
| `ws-gateway` | `aviron_ws_connections_active` |
| `consumer` | `aviron_kafka_consumer_lag`, `aviron_consumer_batch_insert_*`, `aviron_consumer_dlq_total` |

## Pillar 2 — Traces (shipped)

Full depth, deliberately including a span per `telemetry` message (one
per correctly-typed word) — the hot path this whole project exists to
practice, naturally bounded by human typing speed (~0.4-2s/message per
player). Real span names, confirmed against the code and against a live
trace pulled from Tempo during `phase-6-verification.md`'s own pass
(`04d554ee6224cb9d5675c0e421f0c3f1`):

```mermaid
sequenceDiagram
    participant FE as Browser
    participant WG as ws-gateway
    participant NATS
    participant RS as race-service (RoomActor)
    participant OTel as OTel Collector

    Note over FE,RS: One correctly-typed word → one telemetry message — a real, connected trace, verified live
    FE->>WG: WS frame: telemetry, seq=N
    WG->>WG: span ws.frame (root)
    WG->>NATS: publish room.<id>.in (traceparent header)
    WG--)OTel: export span ws.frame
    NATS->>RS: deliver
    RS->>RS: span roomrelay.receive (child of ws.frame)
    RS->>RS: RoomActor.applyEvent mutates WordsCorrect/LastSeq — no span of its own
    RS--)OTel: export span roomrelay.receive — trace ends here
```

**The broadcast leg back to the browser is a *separate*, disconnected
trace — this is a real architectural characteristic, not a missing
span.** `RoomActor`'s tick fires every 250ms regardless of how many
telemetry messages arrived since the last one (§4.2's own ingest/
broadcast decoupling), so there is no single inbound message a broadcast
could unambiguously parent to: `RoomEvent` implementations carry no
`context.Context` at all, `RoomActor.applyEvent`/`broadcastSnapshot` have
zero spans, and `broadcastSnapshot` doesn't even call the NATS publish
itself — it writes to an in-process channel a separate goroutine drains
on the tick:

```mermaid
sequenceDiagram
    participant RS as race-service (RoomActor)
    participant NATS
    participant WG as ws-gateway (raceHub)
    participant FE as Browser
    participant OTel as OTel Collector

    Note over RS,FE: Every 250ms tick — its own fresh trace, not a continuation of any inbound message
    RS->>RS: ticker fires — broadcastSnapshot() batches whatever accumulated since the last tick
    RS->>NATS: publish room.<id>.out
    RS--)OTel: export span roomrelay.publish (new root — no parent)
    NATS->>WG: deliver
    WG->>FE: WS frame: race_state broadcast — raceHub.run has no span at all
```

Note what's *not* in either diagram: Prometheus doesn't scrape per
message — it polls `/metrics` independently every ~15s and reads
whatever `aviron_tick_latency_seconds` accumulated since the last scrape.
Traces are per-event; metrics are aggregate. Same event, two different
resolutions.

The other real cross-process flow this project has — a race finishing —
goes through Kafka instead of NATS, and is asynchronous rather than
request/response:

```mermaid
sequenceDiagram
    participant RS as race-service (RoomActor)
    participant PG as Postgres
    participant Kafka
    participant CO as consumer
    participant DLQ as *.dlq topics

    Note over RS,PG: Race finishes — synchronous, one transaction
    RS->>PG: UPDATE races / race_participants / leaderboard_alltime
    RS->>Kafka: publish race.finished (key = race_id)

    Note over Kafka,CO: Async, decoupled — separate consumer groups per topic
    Kafka->>CO: race.finished
    CO->>PG: ReconcileParticipantResults (idempotent)
    Kafka->>CO: workout.sample × N (batched)
    CO->>PG: InsertBatch (every ~2-5s or 200 messages)
    alt malformed or permanent write failure
        CO->>DLQ: PublishRaw, commit offset anyway
    end
```

Every hop in both diagrams above (NATS publish/consume via
`roomrelay.publish`/`roomrelay.receive`, Redis `roomlocator` lookups via
`roomlocator.<op>`, Kafka produce/consume via `kafka.produce`/
`kafka.consume`, `pgx` queries via `otelpgx`'s auto-instrumented tracer)
gets its own span — `internal/roomlocator` in particular sits on the
critical path of every room-scoped request, so its spans matter even
though Redis itself has no cross-process trace propagation to carry.

## Pillar 3 — Logs (shipped)

No application code changes beyond adding `trace_id`/`span_id` to
existing `slog` JSON lines — every binary already logged structured JSON
tagged with `race_id`/`user_id`/`request_id` (Phase 3), so this pillar
was "centralize what already exists," not "build logging from scratch."
Confirmed live end to end in `phase-6-verification.md`'s own pass: a real
log line's `trace_id`/`span_id` matched exactly the `ws.frame` span's IDs
from Pillar 2 above, found both by direct Elasticsearch query and by
clicking "Trace to logs" in Grafana's UI.

```mermaid
flowchart LR
    RS["race-service<br/>stdout JSON"]
    WG["ws-gateway<br/>stdout JSON"]
    CO["consumer<br/>stdout JSON"]
    Kubelet["kubelet<br/>writes container log files"]
    FB["Fluent Bit<br/>DaemonSet, one per node<br/>tail + kubernetes filter"]
    ES["Elasticsearch<br/>index: aviron-logs"]
    KIB["Kibana"]

    RS --> Kubelet
    WG --> Kubelet
    CO --> Kubelet
    Kubelet --> FB
    FB --> ES
    ES --> KIB
```

## Pillar 4 — Alerting (shipped)

Real SLO-driven rules, not a generic textbook list — each one is tied to
a failure mode this exact system can actually hit (goroutine leak, HPA
stuck at `maxReplicas`, Kafka consumer lag, Postgres pool saturation as
`race-service` scales toward 5 replicas each opening its own `pgxpool`).
Two independent engines, both terminating at the same `telegram-relay`
webhook — see the third asymmetry noted earlier for why alerting isn't
one pipeline:

```mermaid
flowchart LR
    PROM["Prometheus<br/>9 rules: 8 metric-based<br/>+ SpanErrorRateHigh (trace-derived)"]
    AM["Alertmanager<br/>group_by: alertname, app<br/>group_wait: 30s"]
    GA["Grafana Unified Alerting<br/>1 rule: LogErrorRateHigh<br/>group_by: alertname"]
    ES["Elasticsearch<br/>aviron-logs index"]
    TG["telegram-relay<br/>4th binary<br/>POST /alert"]
    API["Telegram Bot API<br/>sendMessage"]
    Phone["Your phone"]

    PROM -->|"rule firing"| AM
    AM -->|"webhook JSON"| TG
    ES -->|"queried directly<br/>Alertmanager can't"| GA
    GA -->|"webhook JSON"| TG
    TG -->|"sendMessage"| API
    API --> Phone
```

`SpanErrorRateHigh` is the one Alertmanager rule that isn't
Prometheus-native — see Pillar 1's flowchart for how Tempo's
metrics-generator bridges `traces_spanmetrics_calls_total` into
Prometheus first, turning "alert on traces" into an ordinary rule rather
than a third alerting engine.

**`telegram-relay`'s handler always responds `200` and only logs on
failure** (`internal/telegramrelay.NewAlertHandler`) — so from either
engine, silence in its logs plus a flat `aviron_telegram_relay_errors_total`
*is* the success signal, not an absence of proof. Confirmed live for all
9 + 1 rules during `phase-6-verification.md`'s own pass, including
closing a gap `alert-rules.md`'s own verification had left deliberately
deferred: `PodRestartLooping` had never actually fired anywhere before
this — forced 4 real container restarts via `crictl stop` on the `kind`
node (`kubectl exec ... kill -9 1` doesn't work; a container's PID 1 is
immune to signals originating from inside its own PID namespace) and
confirmed it fired, reached Alertmanager, and reached Telegram for real.

## Correlation — the actual payoff

The reason all four pillars exist together, not as separate tools: an
operator can start anywhere and pivot to the other two without
re-deriving context by hand. This mirrors `docs/observability.md`'s
"game bị lag" walkthrough, but wired to this project's own Grafana
datasources instead of a generic example.

```mermaid
sequenceDiagram
    participant Op as Operator
    participant Graf as Grafana
    participant Prom as Prometheus
    participant Tempo
    participant Kib as Kibana / Elasticsearch

    Op->>Graf: notices RED panel — race-service p99 latency spike
    Graf->>Prom: PromQL (already the open dashboard)
    Op->>Graf: click exemplar → pivot to trace
    Graf->>Tempo: fetch trace by trace_id
    Tempo-->>Graf: span tree — ws-gateway → NATS → race-service (see Pillar 2 for the real shape)
    Op->>Graf: click "Trace to logs"
    Graf->>Kib: query Elasticsearch for trace_id
    Kib-->>Graf: matching log lines, across every pod/replica that touched the request
```

## Component reference

| Component | Pillar | K8s resource | Port(s) | Image | Status |
| --- | --- | --- | --- | --- | --- |
| Prometheus | Metrics | `Deployment` | 9090 | `prom/prometheus` | Shipped |
| `race-service`/`ws-gateway`/`consumer` `/metrics` | Metrics | (existing) | 8080 / 8080 / 8091 | `aviron-backend:local` | Shipped |
| OTel Collector | Traces | `Deployment` | 4317 (OTLP gRPC), 13133 (health) | `otel/opentelemetry-collector:latest` (core, not `-contrib`) | Shipped |
| Tempo | Traces + trace-derived alerting | `Deployment` | 3200 (query), 4317 (OTLP) | `grafana/tempo:latest` | Shipped — metrics-generator (span-metrics processor) feeds `SpanErrorRateHigh` |
| OTel SDK instrumentation | Traces | (app code) | n/a | `go.opentelemetry.io/otel` | Shipped |
| `trace_id` in `slog` | Logs | (app code) | n/a | n/a | Shipped |
| Elasticsearch | Logs | `StatefulSet` | 9200 | `docker.elastic.co/elasticsearch/elasticsearch:8.15.0` | Shipped |
| Fluent Bit | Logs | `DaemonSet` | n/a (tails `hostPath`) | `fluent/fluent-bit` | Shipped |
| Kibana | Logs | `Deployment` | 5601 | `docker.elastic.co/kibana/kibana:8.15.0` | Shipped |
| Grafana | Dashboards + log-based alerting | `Deployment` | 3000 | `grafana/grafana:latest` | Shipped — Unified Alerting provisioned with `LogErrorRateHigh` |
| `kube-state-metrics` | Dashboards (HPA panel) | `Deployment` | 8080 (metrics), 8081 (telemetry) | `registry.k8s.io/kube-state-metrics/kube-state-metrics:v2.19.1` | Shipped |
| Alertmanager | Alerting | `Deployment` | 9093 | `prom/alertmanager` | Shipped |
| `telegram-relay` (4th binary) | Alerting | `Deployment` | 8080 (`/alert`, `/metrics`) | `aviron-backend:local` (`cmd/telegram-relay`) | Shipped |

## Further reading

- `context/features/phase6/phase-6-plan.md` — the full design and every
  decision's reasoning.
- `context/features/phase6/*/*.md` — one spec per component above.
- `docs/observability.md` — general industry background on how large
  companies run observability platforms (Uber, Netflix, Google, Meta).
- `docs/k8s-deployment.md` — the application-plane deployment this
  observability plane sits alongside; its `## Observability` section has
  the actual `kubectl port-forward` commands for every UI here, plus how
  to read the alert-to-Telegram chain for both engines.
- `docs/knowledge-summary.md` — Phase 3's single-instance observability
  (`slog` + Prometheus + `pprof` + k6), the smaller-scale predecessor this
  phase replaced.
