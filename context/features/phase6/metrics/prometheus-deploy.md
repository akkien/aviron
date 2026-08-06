# Prometheus Deployment

## Overview

Nothing scrapes even `race-service`'s existing `/metrics` today — this
spec stands up a real Prometheus, scraping all three binaries once
`metrics/metrics-parity.md` closes the `ws-gateway`/`consumer` gap. Plain
manifests (`Deployment` + `ConfigMap` + `Service`), the same "plain
manifests over an operator's lifecycle" stance `k8s-core-infra.md` already
took for Redis/NATS — a single-node Prometheus with a static
`scrape_configs` is genuinely simple to hand-write, unlike Kafka, this
project's one deliberate Helm-chart exception.

## Requirements

### Scrape discovery, not a static target list

`race-service` and `ws-gateway` both run under an `HorizontalPodAutoscaler`
(`k8s-hpa.md`, 2-5 replicas) — a static `scrape_configs` target list would
go stale the same way `ws-gateway`'s old `RACE_SERVICE_INSTANCES` did
before `dynamic-backend-discovery.md`. Prometheus's own answer to this is
`kubernetes_sd_configs` with `role: pod`, filtered via pod annotations —
the standard pre-Operator, pre-`ServiceMonitor`-CRD pattern, and a natural
fit for this project's "plain manifests" stance:

```yaml
# deploy/k8s/prometheus/configmap.yaml (prometheus.yml key)
scrape_configs:
  - job_name: aviron-pods
    kubernetes_sd_configs:
      - role: pod
        namespaces:
          names: ["aviron"]
    relabel_configs:
      # Only scrape pods that opt in — every one of this project's own
      # pods will, but this keeps Postgres/Redis/NATS/Kafka pods (which
      # expose no /metrics today) from generating scrape-failure noise.
      - source_labels: [__meta_kubernetes_pod_annotation_prometheus_io_scrape]
        action: keep
        regex: "true"
      - source_labels: [__meta_kubernetes_pod_annotation_prometheus_io_path]
        action: replace
        target_label: __metrics_path__
        regex: (.+)
      - source_labels: [__address__, __meta_kubernetes_pod_annotation_prometheus_io_port]
        action: replace
        regex: ([^:]+)(?::\d+)?;(\d+)
        replacement: $1:$2
        target_label: __address__
      # pod/replica-aware labels for dashboards/grafana-deploy.md's
      # pod-level panels (phase-6-plan.md's Decisions #7) — without these,
      # every scrape target collapses to indistinguishable series.
      - source_labels: [__meta_kubernetes_pod_name]
        target_label: pod
      - source_labels: [__meta_kubernetes_pod_label_app]
        target_label: app
```

### Pod annotations, one per Deployment/StatefulSet

`race-service/statefulset.yaml`, `ws-gateway/deployment.yaml`, and a new
`consumer/deployment.yaml` Service (below) all gain, on their pod
template's `metadata.annotations`:

```yaml
prometheus.io/scrape: "true"
prometheus.io/port: "8080"
prometheus.io/path: "/metrics"
```

### RBAC

Prometheus needs `get`/`list`/`watch` on `pods` in the `aviron` namespace
to run its own `kubernetes_sd_configs` discovery — same shape as `ws-
gateway/rbac.yaml`'s `EndpointSlice` `Role` (`dynamic-backend-
discovery.md`), a namespaced `Role`, not a `ClusterRole`:

```yaml
# deploy/k8s/prometheus/rbac.yaml
apiVersion: v1
kind: ServiceAccount
metadata:
  name: prometheus
  namespace: aviron
---
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: prometheus
  namespace: aviron
rules:
  - apiGroups: [""]
    resources: ["pods"]
    verbs: ["get", "list", "watch"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: prometheus
  namespace: aviron
subjects:
  - kind: ServiceAccount
    name: prometheus
roleRef:
  kind: Role
  name: prometheus
  apiGroup: rbac.authorization.k8s.io
```

### `consumer` gains a real `Service`

`consumer` has none today (`docs/k8s-deployment.md`'s own "no Service"
note — it exposed no HTTP surface). `metrics/metrics-parity.md` gives it
one purely to serve `/metrics`/`/debug/pprof/*`; this spec is what makes
that `Service` (and the pod annotations above) actually necessary — a
`ClusterIP: None` headless `Service` isn't needed here (nothing looks up
`consumer` by stable DNS name the way `race-service` is), a plain
`ClusterIP` `Service` is enough, matching `redis`/`nats`'s own shape.

### Storage

No `PersistentVolumeClaim` — `emptyDir` for Prometheus's TSDB, same
"data doesn't survive a pod restart" stance this whole local cluster
already takes for Kafka (`persistence.enabled: false`) and NATS (no
JetStream). Acceptable here for the same reason: this is a laptop `kind`
cluster for demoing/verifying the mechanism, not a real production
retention target.

### Deployment shape

```yaml
# deploy/k8s/prometheus/deployment.yaml (sketch)
apiVersion: apps/v1
kind: Deployment
metadata:
  name: prometheus
  namespace: aviron
spec:
  replicas: 1
  selector:
    matchLabels: { app: prometheus }
  template:
    metadata:
      labels: { app: prometheus }
    spec:
      serviceAccountName: prometheus
      containers:
        - name: prometheus
          image: prom/prometheus:latest
          args:
            - --config.file=/etc/prometheus/prometheus.yml
            - --storage.tsdb.path=/prometheus
          ports:
            - containerPort: 9090
          volumeMounts:
            - name: config
              mountPath: /etc/prometheus
            - name: data
              mountPath: /prometheus
      volumes:
        - name: config
          configMap: { name: prometheus-config }
        - name: data
          emptyDir: {}
```

## Verification

- `kubectl exec` into the Prometheus pod (or port-forward `:9090`) and
  check `/targets` — every `race-service`/`ws-gateway`/`consumer` pod
  should show `UP`, discovered automatically, not hand-listed.
- Scale `race-service`/`ws-gateway` (manually or via the existing HPAs)
  and confirm the target list grows/shrinks within one `kubernetes_sd_
  configs` refresh interval, with no Prometheus restart or config
  edit — the actual point of using `role: pod` discovery instead of a
  static list.
- `count(up{app="race-service"})` and `count(up{app="ws-gateway"})`
  queries in Prometheus's own UI match `kubectl get pods -n aviron -l
  app=... --field-selector=status.phase=Running` at the same moment.

## Notes

- No Prometheus Operator, no `ServiceMonitor` CRD — consistent with this
  phase's "plain manifests" stance; annotation-based discovery is the
  pre-Operator pattern real companies used for years before the CRD
  approach became standard, still fully legitimate here.
- Depends on `metrics/metrics-parity.md` (needs all 3 binaries actually
  exposing `/metrics` to scrape something real) — sequenced after it per
  `phase-6-plan.md`'s "Dependency order".
- Alertmanager wiring (`alerting/alert-rules.md`) is a separate spec, not
  bundled here — this spec is scrape + storage only.
