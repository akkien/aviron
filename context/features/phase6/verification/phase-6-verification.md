# Phase 6 Verification

## Overview

The real proof this phase exists to deliver: metrics, traces, and logs
for a genuine end-to-end race, correlated with each other in Grafana, with
a real alert reaching Telegram. Every other Phase 6 spec proves its own
piece works in isolation (each has its own "Verification" section) — this
spec is what proves the pieces work *together*, the same role `multi-
instance-k8s-verification.md` played for Phase 5's individual manifests.
Depends on every other Phase 6 spec already being deployed.

## Requirements

Not much new code — this spec is almost entirely verification steps,
plus whatever small fixes those steps surface (same convention this
project already uses for `k6-load-test.md`'s and `multi-instance-dev-
setup.md`'s own findings: don't pre-write fixes, scope them to whatever a
real run actually shows).

## Verification

1. **Everything's up**: `kubectl get pods -n aviron` shows every
   component from all 9 preceding specs `Running`/`Ready` — `prometheus`,
   `otel-collector`, `tempo`, `elasticsearch`, `fluent-bit` (one per
   node), `kibana`, `grafana`, `alertmanager`, `telegram-relay`, plus the
   three application binaries and their existing infra.
2. **Generate a real race**: run `load/`'s existing `race-lifecycle.js`
   k6 scenario (or several concurrent races) against the in-cluster
   `Ingress`, enough players and duration to produce a meaningful volume
   of `telemetry` messages.
3. **Metrics**: Grafana's RED dashboards (`dashboards/grafana-deploy.md`)
   show non-zero series, correctly split by `pod` across whichever
   replicas actually handled traffic — not collapsed into one averaged
   line.
4. **Traces**: pick one player's session in Tempo (via Grafana Explore)
   and follow a single `telemetry` message's trace end to end —
   `ws-gateway` receive -> `roomrelay.publish` (NATS `in`) ->
   `roomrelay.receive` -> `race-service` `RoomActor.applyEvent` ->
   `roomrelay.publish` (NATS `out`) -> `ws-gateway` `raceHub` fan-out —
   confirming `tracing/instrumentation.md`'s full-depth design actually
   produces one connected trace across the process boundary, not two
   disjoint ones.
5. **Span volume, the honesty check `tracing/instrumentation.md` itself
   flagged as unverified**: with several concurrent races' worth of
   real `telemetry` traffic, confirm the OTel Collector and Tempo keep up
   (no dropped spans in the Collector's own logs, no Tempo ingestion
   errors) — the napkin math in that spec (5-25 spans/sec) was never
   checked against a real multi-race run until now.
6. **Logs, correlated**: from the trace in step 4, click "Trace to logs"
   (`dashboards/grafana-deploy.md`'s `tracesToLogsV2` data source config)
   and confirm it lands on the matching log lines in Kibana/Elasticsearch
   — the concrete proof `logging/log-trace-correlation.md`'s `trace_id`
   field actually round-trips through Fluent Bit into a queryable
   Elasticsearch field.
7. **A real alert reaches Telegram**: trigger one rule for real rather
   than trusting `alerting/alert-rules.md`'s own isolated verification —
   e.g. `kubectl delete pod` on a `race-service` pod several times in a
   short window (`PodRestartLooping`), or pause `consumer` while load
   keeps running (`KafkaConsumerLagHigh`). Confirm: fires in Prometheus's
   `/alerts`, reaches Alertmanager, reaches `telegram-relay`, and a real
   message lands in the configured Telegram chat — the full chain, not
   any single hop in isolation.
8. **HPA scale event, visible in context**: push load past 70% CPU on
   `race-service`/`ws-gateway` (same load pattern `k8s-hpa.md`'s own
   verification already used) and confirm Grafana's HPA panel
   (`dashboards/grafana-deploy.md`) shows the replica count climbing
   overlaid with the CPU metric that triggered it — the dashboard
   actually explaining a scale event, not just displaying two unrelated
   numbers.
9. **Scale-down/rolling-update doesn't break the pipeline**: trigger a
   scale-down or rolling update mid-load (same scenario `k8s-hpa.md`'s
   and `graceful-shutdown.md`'s own verification already exercise for
   in-progress races) and confirm the Collector/Fluent Bit/Prometheus
   simply see a terminated source gracefully — no crash-loop, no stuck
   scrape target, an incomplete trace for whatever was mid-flight but
   nothing corrupted.
10. **`go build ./...`/`go test ./... -race`** — expected to be
    non-trivial this time, unlike `k8s-hpa.md`'s own manifests-only spec:
    this phase adds real Go code (`tracing/instrumentation.md`, `logging/
    log-trace-correlation.md`, `metrics/metrics-parity.md`,
    `alerting/telegram-relay.md`'s new binary) — run the full suite, not
    skip it as a formality.

## Notes

- `docs/k8s-deployment.md` gains a new "Observability" section once this
  spec actually runs — how to install/verify `kube-state-metrics`, how to
  reach Grafana/Kibana/Tempo/Prometheus (`kubectl port-forward` commands
  for each), and how to read the alert-to-Telegram chain — mirroring the
  "Autoscaling" section `k8s-hpa.md` added to the same doc. Not written
  yet; this spec is planning-only until the preceding 9 are actually
  implemented.
- If step 5's span-volume check finds the Collector/Tempo genuinely can't
  keep up under a realistic multi-race load, the right response is
  revisiting `tracing/instrumentation.md`'s "trace every telemetry
  message" decision (e.g. sampling), not silently dropping spans and
  calling the phase done — same "a failure here is a signal to revisit
  the relevant spec's design" convention this project already applies to
  `k6-load-test.md`'s own findings.
- This is the last spec in `phase-6-plan.md`'s dependency order — nothing
  in this phase follows it.
