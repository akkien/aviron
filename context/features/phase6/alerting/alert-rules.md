# Prometheus Alert Rules + Alertmanager Deployment

## Overview

Turns "the data exists" into "someone gets told before they have to go
looking" (`phase-6-plan.md`'s Overview) — Prometheus alert rules tied to
this system's real failure modes, evaluated against `metrics/prometheus-
deploy.md`'s already-running Prometheus, routed through a new
Alertmanager to `alerting/telegram-relay.md`'s webhook. Per `phase-6-
plan.md`'s Decisions #4, the rule list is deliberately not a generic
textbook set — every rule below ties to a real component this project
actually runs.

## Requirements

### Two small metric additions this spec depends on

Two of the rules below need a metric `metrics/metrics-parity.md` doesn't
already cover — noted there as out of scope for that spec, added here
since they exist specifically to feed alert rules, not general
dashboards:

- **`aviron_nats_reconnects_total`** (`Counter`) — wired via `nats.
  Connect`'s own `nats.DisconnectErrHandler`/`nats.ReconnectHandler`
  options, in both `cmd/server/run.go` and `cmd/ws-gateway/run.go`'s
  `nats.Connect(cfg.NATSURL)` call sites. `nats.go` already retries
  reconnection internally (this project's NATS Core setup, no JetStream)
  — this counter makes a *pattern* of reconnects (not one transient blip)
  visible.
- **`aviron_pg_pool_acquired_conns`/`aviron_pg_pool_max_conns`**
  (`GaugeFunc` pair) — `race-service`-only, wired via `pgxpool.Pool.
  Stat()` (`AcquiredConns()`/`MaxConns()`) in `internal/metrics.Metrics`
  (not the `GatewayMetrics`/`ConsumerMetrics` types `metrics/metrics-
  parity.md` adds — `race-service` is the one binary running `pgxpool`).
  Exists specifically to give the Postgres connection-pool rule below a
  real signal, closing the risk `k8s-hpa.md`'s own Notes left as an
  un-computed number.

### The rules

```yaml
# deploy/k8s/prometheus/configmap-rules.yaml (aviron-alerts.yml key)
groups:
  - name: aviron
    rules:
      - alert: ElevatedErrorRate
        expr: |
          sum by (app) (rate(aviron_roomlocator_errors_total[5m]))
            / sum by (app) (rate(aviron_roomrelay_publish_total[5m])) > 0.05
        for: 5m
        labels: { severity: warning }
        annotations:
          summary: "{{ $labels.app }} error rate above 5% over 5m"

      - alert: TickLatencySLOBurn
        expr: |
          histogram_quantile(0.99, rate(aviron_tick_latency_seconds_bucket[5m])) > 0.2
        for: 10m
        labels: { severity: warning }
        annotations:
          summary: "race-service tick latency p99 above 200ms for 10m"

      - alert: GoroutineCountTrendingUp
        expr: |
          predict_linear(go_goroutines{namespace="aviron"}[30m], 3600) > 100000
        for: 15m
        labels: { severity: warning }
        annotations:
          summary: "{{ $labels.app }} goroutine count on a leak trajectory"

      - alert: PodRestartLooping
        expr: |
          increase(kube_pod_container_status_restarts_total{namespace="aviron"}[15m]) > 3
        labels: { severity: critical }
        annotations:
          summary: "{{ $labels.pod }} restarted more than 3 times in 15m"

      - alert: HPAStuckAtMaxReplicas
        expr: |
          kube_horizontalpodautoscaler_status_current_replicas{namespace="aviron"}
            == kube_horizontalpodautoscaler_spec_max_replicas{namespace="aviron"}
        for: 15m
        labels: { severity: warning }
        annotations:
          summary: "{{ $labels.horizontalpodautoscaler }} pinned at maxReplicas for 15m — real capacity ceiling, not a transient burst"

      - alert: KafkaConsumerLagHigh
        expr: aviron_kafka_consumer_lag > 2000
        for: 10m
        labels: { severity: warning }
        annotations:
          summary: "consumer lag on {{ $labels.topic }} exceeds 2000 messages (10x flushBatchSize) for 10m"

      - alert: NATSReconnectStorm
        expr: increase(aviron_nats_reconnects_total[15m]) > 3
        labels: { severity: warning }
        annotations:
          summary: "{{ $labels.app }} reconnected to NATS more than 3 times in 15m"

      - alert: PostgresPoolSaturation
        expr: |
          aviron_pg_pool_acquired_conns / aviron_pg_pool_max_conns > 0.8
        for: 5m
        labels: { severity: critical }
        annotations:
          summary: "race-service Postgres pool above 80% acquired for 5m — the k8s-hpa.md maxReplicas:5 risk, now with a real signal"
```

- `HPAStuckAtMaxReplicas` and `PodRestartLooping` both need `kube-state-
  metrics` (`kube_horizontalpodautoscaler_*`/`kube_pod_container_status_
  restarts_total`) — same new prerequisite `dashboards/grafana-deploy.
  md`'s Notes already flags for its own HPA panel, not a second,
  separate gap.
- Thresholds above are starting points, not tuned SLOs — `verification/
  phase-6-verification.md` is where these get exercised against real
  load and adjusted if they fire too eagerly or not at all.

### Alertmanager

```yaml
# deploy/k8s/alertmanager/configmap.yaml (alertmanager.yml key)
route:
  receiver: telegram-relay
  group_by: ["alertname", "app"]
  group_wait: 30s
  group_interval: 5m
  repeat_interval: 4h
receivers:
  - name: telegram-relay
    webhook_configs:
      - url: http://telegram-relay.aviron.svc.cluster.local:8080/alert
        send_resolved: true
```

Plain `Deployment` + `ConfigMap` + `Service`, same shape as every other
piece of this phase's infra. Prometheus itself needs
`--alertmanager.url`/`alerting.alertmanagers` config pointing at this
Service, and `--rule-files` picking up the `ConfigMap` above — both small
additions to `metrics/prometheus-deploy.md`'s existing `prometheus.yml`,
not a new Prometheus deployment.

## Verification

- `kubectl exec` into Prometheus (or port-forward `:9090`) and check
  `/rules` — all 8 rules load without a YAML/PromQL syntax error.
- Force at least one rule to fire deliberately (e.g. `kubectl delete pod`
  on a `race-service` pod repeatedly to trip `PodRestartLooping`, or stop
  `consumer` briefly under load to trip `KafkaConsumerLagHigh`) and
  confirm it reaches `firing` state in Prometheus's `/alerts` view, then
  appears in Alertmanager's own UI.
- Confirm Alertmanager's webhook actually reaches `alerting/telegram-
  relay.md`'s service once that spec is also live — this spec's own
  verification stops at "Alertmanager attempted the POST," full
  end-to-end proof is `verification/phase-6-verification.md`'s job.

## Notes

- Depends on `metrics/prometheus-deploy.md` (needs Prometheus already
  scraping and running) and, for two rules, `kube-state-metrics` (also
  flagged in `dashboards/grafana-deploy.md`'s Notes — install once, both
  specs benefit).
- Depends on `alerting/telegram-relay.md` for the webhook receiver to
  actually exist at the URL above — this spec's `alertmanager.yml`
  references it by its eventual Service DNS name regardless of build
  order, since Kubernetes DNS resolves lazily (no error until an alert
  actually tries to fire against a not-yet-existing Service).
- No PagerDuty/Opsgenie receiver — `phase-6-plan.md`'s "Explicitly still
  out of scope" already covers why.
- `alerting/trace-alert-rules.md` later appends a 9th rule
  (`SpanErrorRateHigh`) to this same rule `ConfigMap`/group and reuses
  this spec's Alertmanager `route`/`receivers` block unchanged — no
  second Alertmanager, no second `ConfigMap`.
