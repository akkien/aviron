# Grafana Deployment

## Overview

Grafana is the correlation layer `phase-6-plan.md`'s Overview names as
the actual point of this phase — one place to pivot from a metric spike
to the logs and traces that explain it. This spec deploys Grafana itself,
provisions all three data sources (Prometheus, Tempo, Elasticsearch), and
builds the RED/USE dashboards `phase-6-plan.md`'s pillars table commits
to. Depends on `metrics/prometheus-deploy.md`, `tracing/otel-collector-
tempo-deploy.md`, and `logging/efk-deploy.md` all already being live —
there's nothing to wire a data source to otherwise.

## Requirements

### Deployment

Plain `Deployment` + `Service`, same shape as every other piece of this
phase's infra — Grafana OSS needs no operator at this scale:

```yaml
# deploy/k8s/grafana/deployment.yaml (sketch)
apiVersion: apps/v1
kind: Deployment
metadata:
  name: grafana
  namespace: aviron
spec:
  replicas: 1
  selector:
    matchLabels: { app: grafana }
  template:
    metadata:
      labels: { app: grafana }
    spec:
      containers:
        - name: grafana
          image: grafana/grafana:latest
          ports:
            - containerPort: 3000
          volumeMounts:
            - name: datasources
              mountPath: /etc/grafana/provisioning/datasources
            - name: dashboards-config
              mountPath: /etc/grafana/provisioning/dashboards
            - name: dashboards
              mountPath: /var/lib/grafana/dashboards
      volumes:
        - name: datasources
          configMap: { name: grafana-datasources }
        - name: dashboards-config
          configMap: { name: grafana-dashboards-config }
        - name: dashboards
          configMap: { name: grafana-dashboards }
```

No `PersistentVolumeClaim` — dashboards/data sources are entirely
provisioned from `ConfigMap`s (below), not created by hand through the
UI, so there's nothing stateful worth persisting across a pod restart.

### Data source provisioning — all three, plus the correlation wiring

```yaml
# deploy/k8s/grafana/configmap-datasources.yaml (datasources.yaml key)
apiVersion: 1
datasources:
  - name: Prometheus
    type: prometheus
    url: http://prometheus.aviron.svc.cluster.local:9090
    isDefault: true
  - name: Tempo
    type: tempo
    url: http://tempo.aviron.svc.cluster.local:3200
    jsonData:
      # "Trace to logs": clicking a span in Tempo jumps straight to its
      # matching log lines in Elasticsearch, filtered by trace_id — the
      # concrete feature phase-6-plan.md's Overview means by "pivot from
      # a metric spike to the logs and traces that explain it."
      tracesToLogsV2:
        datasourceUid: elasticsearch
        filterByTraceID: true
        tags: ["trace_id"]
  - name: Elasticsearch
    type: elasticsearch
    url: http://elasticsearch.aviron.svc.cluster.local:9200
    jsonData:
      index: aviron-logs
      timeField: "@timestamp"
      # "Derived field": a trace_id value found in a log line renders as
      # a clickable link straight into the matching Tempo trace — the
      # reverse direction of Tempo's tracesToLogsV2 above, so correlation
      # works starting from either side.
      derivedFields:
        - name: trace_id
          matcherRegex: '"trace_id":"(\w+)"'
          datasourceUid: tempo
          url: "$${__value.raw}"
```

This is the one part of this spec with a real dependency on `logging/
log-trace-correlation.md`: the derived-field link only works once log
lines actually carry a `trace_id` field to match against — configured
here regardless (inert until that spec lands), not blocked on it.

### Dashboards — RED per service, USE for goroutines/channels, pod-aware

Per `phase-6-plan.md`'s Decisions #7: every panel aggregates with
`sum by (pod, ...)`/`avg by (pod, ...)`, never a bare average across all
2-5 replicas of an autoscaled service — a dashboard that silently
averages away which specific pod is the outlier defeats the point of
building one.

- **RED, one dashboard per service** (`race-service`, `ws-gateway`,
  `consumer`): Rate (`rate(aviron_..._total[5m])` or `http_requests`-
  equivalent), Errors (error-tagged counter rate), Duration (`aviron_
  tick_latency_seconds`/`aviron_roomrelay_publish_duration_seconds`/
  `aviron_consumer_batch_insert_duration_seconds` histograms' own
  quantiles, all from `metrics/metrics-parity.md`).
- **USE, one shared dashboard**: goroutine count (`go_goroutines`, the
  standard collector's own metric — no custom gauge duplicates it, same
  conclusion `prometheus-metrics.md` already reached), channel buffer
  usage (`aviron_channel_buffer_used{channel}`), Kafka consumer lag
  (`aviron_kafka_consumer_lag{topic}`) — all per-pod, all three binaries
  on one dashboard for a fleet-wide comparison.
- **HPA panel**: `kube_horizontalpodautoscaler_status_current_replicas`
  vs. `..._desired_replicas` (needs `kube-state-metrics` — see Notes)
  overlaid with CPU utilization, so a scale event is visible alongside
  the load that triggered it.

Dashboards are provisioned as JSON in a `ConfigMap`
(`grafana-dashboards`), not clicked together by hand — reproducible from
a fresh `kubectl apply`, matching this phase's "plain manifests, nothing
that only lives in one person's browser session" stance.

## Verification

- `kubectl port-forward` (or a future `Ingress`, out of scope here) into
  Grafana; confirm all three data sources show green "Test" results.
- Generate real traffic (a k6 race, per `load/`'s existing scenarios),
  confirm the RED dashboards populate with non-zero series for the
  correct `pod` labels.
- Click a slow span in Tempo's Explore view; confirm "Trace to logs"
  actually opens the matching Elasticsearch log lines (requires
  `logging/log-trace-correlation.md` already shipped) — the concrete
  proof this phase's "single pane of glass" claim is real, not aspirational.

## Notes

- `kube-state-metrics` isn't deployed anywhere in this project today —
  a real, additional prerequisite for the HPA panel specifically (`kube_
  horizontalpodautoscaler_*` metrics come from it, not from `metrics/
  prometheus-deploy.md`'s own app-level scrape targets). Small, standard,
  well-known manifest set — flagged here as an actual new dependency,
  not assumed to already exist.
- No Grafana Cloud, no managed Grafana — self-hosted only, consistent
  with `phase-6-plan.md`'s "Explicitly still out of scope" section.
- Depends on `metrics/prometheus-deploy.md`, `tracing/otel-collector-
  tempo-deploy.md`, and `logging/efk-deploy.md` all already deployed —
  the last spec in the "wire up the four pillars" chain before alerting.
- `alerting/log-alert-rules.md` later mounts a fourth provisioning
  `ConfigMap` onto this same `Deployment`
  (`/etc/grafana/provisioning/alerting`) — not this spec's concern, just
  flagged here since it touches the `Deployment` this spec owns.
