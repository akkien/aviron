# Phase 6 — Full Observability Stack (Metrics, Logs, Traces, Alerting)

## Overview

**Scope decided directly with the user, overriding `project-overview.md`'s
usual "practice the mechanism, don't run a production stack" stance for
this phase specifically** — a deliberate choice, not an oversight. The
goal isn't "give each binary a `/metrics` endpoint"; it's to build the
same kind of observability stack a real distributed system at a company
like Grab, Uber, or Netflix runs — metrics, logs, and traces unified
behind one pane of glass, correlated with each other, with real alerting
on top — scaled down to fit a laptop `kind` cluster, not scaled down in
ambition.

This isn't one of `project-overview.md` §12's original five phases:
Phase 4's WS Gateway revision split one binary into three
(`race-service`, `ws-gateway`, `consumer`), and the JD this project
targets explicitly calls out "investigate and resolve production
issues" — a full observability stack is a more direct, higher-fidelity
way to practice exactly that than three isolated `/metrics` endpoints
would be. `project-overview.md` itself is out of this plan's scope to
edit directly — noted here so it isn't lost.

This system's actual shape today is the thing that makes each pillar
worth building, not an abstract "good practice." A single user action
now genuinely crosses process and machine boundaries: `ws-gateway`
receives a WebSocket message and hands it to `race-service` over NATS
(`internal/roomrelay`), room ownership is resolved through Redis
(`internal/roomlocator`), and a finished race fans out through Kafka
(`internal/kafka`) to `consumer`, which writes Postgres independently —
all of it running as 2-5 replicas per service under a
`HorizontalPodAutoscaler`, so "which pod" is itself part of debugging
any incident. Distributed tracing exists specifically to make that kind
of multi-hop, multi-replica request legible — logs and metrics alone
can't reconstruct "where did this one request's time actually go" once
it's crossed three processes and two brokers. Grafana is the actual
correlation layer that ties the three pillars together — one place to
pivot from a metric spike to the logs and traces that explain it, which
is the entire reason real observability stacks centralize on one query
UI instead of three separate tools. And the structured `slog` output
every binary already produces is sitting in per-pod stdout today,
readable only via `kubectl logs` against whichever of the 2-5 replicas
happened to handle the request; centralizing it (correlated with
`trace_id`) plus real Alertmanager rules is what turns "the data exists"
into "someone gets told before they have to go looking."

## The four pillars this phase builds

See "Decisions" below for the reasoning behind each tool choice.

| Pillar | Tool |
| --- | --- |
| Metrics | Prometheus, pull-based |
| Dashboards | Grafana, RED method (rate/errors/duration) per service + USE method (utilization/saturation/errors) for goroutines/channel buffers |
| Traces | OpenTelemetry SDK in all three binaries, full depth (REST/WS entry points incl. per-message, NATS, Redis, Kafka, `pgx`), backend: **Tempo** |
| Logs | Structured `slog`, already tagged `race_id`/`user_id`/`request_id`; backend: **EFK/ELK** (Elasticsearch + Fluent Bit + Kibana) |
| Alerting | Alertmanager, real SLO-driven rules, routed to **Telegram** via a small custom adapter |

## What already exists and carries into this scope unchanged

- `internal/metrics` on `race-service` (`aviron_rooms_active`,
  `aviron_tick_latency_seconds`, `aviron_channel_buffer_used`) — the
  template every other binary's metrics still follow.
- Structured logging (`slog`) is already wired in all three binaries,
  already tagged with `race_id`/`user_id`/`request_id` per
  `project-overview.md` §9 — the missing piece is centralizing it and
  adding `trace_id` once tracing exists, not building logging from
  scratch.
- `ws-gateway`/`consumer` still have neither `/metrics` nor
  `/debug/pprof/*` today (confirmed by grepping for
  `prometheus/client_golang` — used in exactly one place, `internal/
  metrics`) — closing that gap is in scope here (`metrics/metrics-parity.md`).
- Nothing currently scrapes even `race-service`'s existing `/metrics` —
  still true, still needs a real Prometheus deployment.

## Proposed architecture (the "one real company detail" worth calling out)

The realistic way companies like this actually wire multi-language,
multi-service tracing/logging isn't "each backend implements its own
per-language ingestion protocol" — it's a single **OpenTelemetry
Collector** deployed once, that every service pushes to over OTLP, which
then fans out to whichever backends are actually storing the data
(traces to Tempo/Jaeger, and optionally logs/metrics through the same
pipeline). That's the shape this phase should build, not three binaries
each hand-rolling their own exporter wiring per backend:

```text
race-service, ws-gateway, consumer
        |  (OTLP: traces)
        v
  OpenTelemetry Collector  (new, single deployment)
        |
        v
      Tempo  (traces)

race-service, ws-gateway, consumer
        |  (stdout, structured slog + trace_id)
        v
  Fluent Bit (DaemonSet, one per node)
        |
        v
  Elasticsearch  ---\
        |             \
        v              v
     Kibana          Grafana  (single pane of glass: metrics + logs + traces,
   (full-text                  correlated via trace_id/exemplars)
    log search)                   ^
                                   |
race-service, ws-gateway, consumer
        |  (Prometheus scrape, pull-based)
        v
    Prometheus  ---------------> Grafana
        |
        v
  Alertmanager  --(webhook)-->  telegram-relay  --(Bot API)-->  Telegram
   (alert rules)                 (new, tiny adapter)
```

Metrics stay pull-based (Prometheus scraping `/metrics` directly) rather
than also routing through the Collector — mixing a pull model for
metrics with a push model (OTLP) for traces is itself the realistic
detail; most real stacks don't force everything through one transport
just for architectural purity. Logs go straight from each pod's stdout
into Fluent Bit rather than through the Collector too — Fluent Bit
already is the standard Kubernetes-native log shipper (tails container
log files directly via the node's `kubelet`, no application-side change
needed), so routing logs through OTLP as well would just be a second,
redundant path to the same data.

## Decisions

Decided directly with the user:

1. **Tracing backend: Tempo.** Native Grafana integration (same vendor,
   first-class data source, simpler object-storage model) fits the
   single-pane-of-glass architecture this phase is built around.
2. **Log backend: EFK/ELK**, not Loki. Heavier to run on a laptop `kind`
   cluster, but closer to what a large share of real companies actually
   run in production, and gives real full-text search via Elasticsearch
   — chosen over Loki's lighter footprint because "practice the stack
   most likely to show up at a job" won out over "lightest path to a
   working correlated stack."
3. **Tracing depth: full depth**, including the WS hot path. REST/
   WebSocket entry-point spans, NATS publish/consume spans
   (`internal/roomrelay`), Redis room-ownership lookup spans
   (`internal/roomlocator` — on the critical path for every room-scoped
   route), Kafka produce/consume spans (`internal/kafka`,
   `internal/consumer`), and `pgx` query spans. Explicitly **including a
   span per `telemetry` message**, not just per join/reconnect/finish
   event: each word-typed message gets one end-to-end trace
   (`ws-gateway` receive → NATS publish → `race-service` apply →
   broadcast). Volume is naturally bounded by human typing speed
   (~0.4-2s/message per player, per `project-overview.md` §13), and this
   is exactly the hot path — the room actor's real-time tick/broadcast
   loop — that the whole project exists to practice, so tracing it at
   full resolution is worth the higher span volume rather than trading
   it away.
4. **Alert routing: Telegram**, via a new small adapter service this
   phase builds (working name `telegram-relay`, exact package name TBD
   when specced), not an off-the-shelf bridge container. Alertmanager's
   generic webhook receiver POSTs to it; it forwards to the Telegram Bot
   API's `sendMessage`. Chosen over "log-only" (proves the mechanism but
   nobody actually gets notified) and a real Slack webhook (needs a
   Slack workspace) — a Telegram bot is free, needs only a token from
   `@BotFather` and a chat ID, and writing the adapter ourselves (rather
   than reusing an existing `alertmanager-telegram-bridge` container)
   keeps this phase's "practice building the mechanism" value instead of
   trading it away for less Go code. Bot token + chat ID are stored as a
   Kubernetes `Secret`, never committed.

   `alert-rules.md`'s actual rules should be tied to this system's real
   failure modes, not a generic textbook list: elevated error rate and
   p99 latency SLO burn per service, goroutine count trending up (leak
   signal), pod restart-looping, `HorizontalPodAutoscaler` stuck at
   `maxReplicas` under sustained load, Kafka consumer lag on `consumer`,
   NATS connection drops/reconnects on `ws-gateway`/`race-service`,
   elevated `internal/roomlocator` (Redis) error rate, and — picking up
   the one genuinely open risk `k8s-hpa.md`'s Notes left unresolved —
   Postgres connection-pool saturation as `race-service` scales toward
   `maxReplicas: 5`, each replica opening its own `pgxpool` against a
   single non-pooled Postgres instance. That last rule doubles as the
   cheapest way to finally get a real signal on whether `PgBouncer`
   becomes a genuine prerequisite, instead of leaving it as an
   un-computed risk.
5. **Prometheus stays pull-based**, not routed through the OTel
   Collector — confirmed as the final architecture, not just the
   sketch's default. Scraping `/metrics` directly is the standard
   Prometheus model and this project's existing pattern
   (`internal/metrics` on `race-service` already works this way); mixing
   a pull model for metrics with a push model (OTLP) for traces is
   itself the realistic detail worth keeping, not a gap to close.
6. **`metrics-parity.md` isn't just "give `ws-gateway`/`consumer` the
   default `client_golang` process collectors."** Both binaries sit
   directly on top of the cross-process infrastructure this system
   actually depends on, and that's exactly what needs its own gauges/
   counters/histograms: `ws-gateway` gets NATS publish/consume
   counters+latency and an `internal/roomlocator` lookup
   latency/error-rate metric (both on its hot path to `race-service`);
   `consumer` gets Kafka consume lag and batch-insert latency/error-rate
   into `workout_samples`. Generic process metrics (goroutines, memory)
   are still in scope too, just not the whole of it.
7. **Grafana dashboards are pod/replica-aware, not single-instance.**
   `race-service` and `ws-gateway` both run 2-5 replicas under their
   `HorizontalPodAutoscaler`s (`k8s-hpa.md`) — RED/USE panels use
   Prometheus label aggregation (`sum by (pod, ...)` / `avg by
   (pod, ...)`) so a dashboard can show both the fleet-wide picture and
   which specific replica is the outlier, not just a number that
   silently averages 2-5 pods together.
8. **Log-based alerting: Grafana's own Unified Alerting, not
   Alertmanager. Trace-based alerting stays on the existing Alertmanager
   pipeline instead of a third alerting path.** Alertmanager only
   evaluates PromQL against Prometheus — it has no way to query
   Elasticsearch, so a rule that alerts on log *content*
   (`alerting/log-alert-rules.md`) lives in Grafana's built-in Unified
   Alerting engine, which can alert on any provisioned datasource
   (including the Elasticsearch datasource `dashboards/grafana-
   deploy.md` already wires up) and route through a webhook contact
   point — no new Deployment, just provisioning config on the Grafana
   pod that already exists. Traces get the opposite treatment: Tempo
   isn't a "count over a threshold" query source the way Elasticsearch
   is, so the standard mechanism is Tempo's own metrics-generator
   deriving RED metrics from spans and remote-writing them into
   Prometheus (`alerting/trace-alert-rules.md`) — turning "alert on
   traces" into one more Prometheus rule rather than standing up a
   second alerting engine for it. Net result: two alerting engines
   total (Alertmanager for metrics and trace-derived metrics, Grafana
   Alerting for logs), both terminating at the same `telegram-relay`
   webhook.

## Explicitly still out of scope, even at this ambition level

- **Metrics federation at scale** (Thanos, Cortex, M3) — solves a
  multi-cluster/long-retention problem this single local cluster
  doesn't have.
- **A managed/SaaS backend** (Datadog, New Relic, Honeycomb, Grafana
  Cloud) — self-hosted only, consistent with this whole project running
  on a laptop `kind` cluster with no external dependencies.
- **Real paging integration** (PagerDuty, Opsgenie) — "Decisions" above
  covers how far alert routing goes (Telegram); a real paging vendor is
  past what this project needs to prove the concept.
- **A service mesh** (Istio, Linkerd) for automatic mTLS/traffic
  metrics — a genuinely different, much larger scope than observability
  instrumentation, and this system's service-to-service traffic (NATS,
  Kafka) isn't mesh-proxyable the way HTTP is anyway.

## Concrete spec breakdown

Grouped into subfolders the way `phase4/` split `horizontal-scaling/`
from `event-pipeline/`.

```text
context/features/phase6/
  metrics/
    metrics-parity.md          # ws-gateway + consumer get /metrics, /debug/pprof/*,
                                # plus NATS/roomlocator/Kafka-specific gauges (see Decisions #6)
    prometheus-deploy.md       # Prometheus Deployment + scrape config, all 3 binaries
  tracing/
    otel-collector-tempo-deploy.md   # Collector + Tempo, pure infra, no app code
    instrumentation.md               # OTel SDK in all 3 binaries, full depth incl. per-message
                                      # WS spans and internal/roomlocator (see Decisions #3)
  logging/
    log-trace-correlation.md   # inject trace_id into slog output
    efk-deploy.md               # Elasticsearch + Fluent Bit (DaemonSet) + Kibana
  dashboards/
    grafana-deploy.md          # Grafana + datasources (Prometheus, Tempo, Elasticsearch)
                                # + pod-aware RED/USE dashboards (see Decisions #7)
  alerting/
    alert-rules.md              # Prometheus alert rules (see Decisions #4 for the concrete
                                 # rule list) + Alertmanager deployment
    telegram-relay.md           # new adapter service + Alertmanager webhook wiring
    log-alert-rules.md          # Grafana Alerting rule on Elasticsearch logs (Decisions #8)
    trace-alert-rules.md        # Tempo metrics-generator -> Prometheus rule (Decisions #8)
  verification/
    phase-6-verification.md     # full walkthrough, see "Next" below
```

## Dependency order

```text
metrics/metrics-parity            (no dependency — closes the /metrics gap)
        |
        v
metrics/prometheus-deploy         (needs all 3 binaries exposing /metrics)
        |
        v
tracing/otel-collector-tempo-deploy   (pure infra, no app-code dependency —
        |                              could build in parallel with the above)
        v
tracing/instrumentation           (needs the Collector+Tempo reachable to verify against;
        |                          this is the spec that touches internal/roomrelay,
        |                          internal/roomlocator, internal/kafka, internal/consumer,
        |                          and pgx call sites, including a span per telemetry message)
        v
logging/log-trace-correlation     (needs trace context available from instrumentation)
        |
        v
logging/efk-deploy                (independent infra — could build in parallel with the
        |                          tracing track, sequenced after only for the
        |                          correlation view to have trace_id already in log lines)
        v
dashboards/grafana-deploy         (needs Prometheus, Tempo, and Elasticsearch/Kibana
        |                          all already deployed to wire as datasources)
        v
alerting/alert-rules              (needs Prometheus already deployed and scraping)
        |
        v
alerting/telegram-relay           (needs Alertmanager already deployed to webhook into)
        |
        v
alerting/log-alert-rules           (needs dashboards/grafana-deploy's Elasticsearch
        |                          datasource and alerting/telegram-relay's webhook live)
        v
alerting/trace-alert-rules         (needs tracing/instrumentation's spans flowing,
        |                          metrics/prometheus-deploy to add the remote-write-receiver
        |                          flag, and alerting/alert-rules' rule file to append to —
        |                          no dependency on log-alert-rules, could build in parallel)
        v
verification/phase-6-verification
```

`metrics/prometheus-deploy` and `tracing/otel-collector-tempo-deploy` have
no dependency on each other and could be built in either order or in
parallel; `logging/efk-deploy` similarly has no hard dependency on the
tracing track beyond wanting `trace_id` already flowing before the
correlation view is worth demoing. `alerting/log-alert-rules` and
`alerting/trace-alert-rules` likewise have no dependency on each other —
shown sequentially here only for readability. The rest is close to
linear.

## Next

Start with `metrics/metrics-parity.md` — no dependencies, smallest
scope, and the same `internal/metrics` pattern `race-service` already
proves out. Load it via `/feature load` when ready to begin
implementation.
