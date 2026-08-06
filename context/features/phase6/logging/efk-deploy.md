# EFK Deployment — Elasticsearch + Fluent Bit + Kibana

## Overview

Centralizes the structured `slog` JSON every binary already writes to
stdout — today readable only via `kubectl logs` against whichever of a
service's 2-5 replicas happened to handle a given request. Per
`phase-6-plan.md`'s Decisions #2: **EFK, not Loki** — heavier to run on a
laptop `kind` cluster, but closer to what a large share of real
companies actually run in production, with real full-text search via
Elasticsearch. Plain manifests, same stance as every other piece of
infra in this phase; a single-node Elasticsearch doesn't need the ECK
operator at this scale.

No application code changes — Fluent Bit tails container log files
directly via the node's `kubelet`, the standard Kubernetes-native
mechanism, so nothing about how `race-service`/`ws-gateway`/`consumer`
already log to stdout needs to change.

## Requirements

### Elasticsearch — single node

```yaml
# deploy/k8s/elasticsearch/statefulset.yaml (sketch)
apiVersion: apps/v1
kind: StatefulSet
metadata:
  name: elasticsearch
  namespace: aviron
spec:
  serviceName: elasticsearch
  replicas: 1
  selector:
    matchLabels: { app: elasticsearch }
  template:
    metadata:
      labels: { app: elasticsearch }
    spec:
      containers:
        - name: elasticsearch
          image: docker.elastic.co/elasticsearch/elasticsearch:8.15.0
          env:
            # Single-node "quorum-less" mode — no cluster formation, no
            # multi-node discovery needed at this project's scale.
            - name: discovery.type
              value: single-node
            # Same "no internal TLS/auth" stance this whole local cluster
            # already takes for Postgres/Redis/Kafka/NATS — not a new
            # exception, an extension of it. Never appropriate on a real
            # cluster, accepted here for the same reasons already
            # documented elsewhere (kind-specific, laptop-demo-only).
            - name: xpack.security.enabled
              value: "false"
            - name: ES_JAVA_OPTS
              value: "-Xms512m -Xmx512m"
          ports:
            - containerPort: 9200
          resources:
            requests: { cpu: 250m, memory: 1Gi }
            limits: { cpu: 1, memory: 1.5Gi }
```

No `PersistentVolumeClaim` — `emptyDir`, same "data doesn't survive a
pod restart" acceptance already made for Prometheus and Tempo. Losing
log history on a restart is a real, disclosed tradeoff for a laptop demo
cluster, not silently glossed over.

### Fluent Bit — `DaemonSet`

One pod per node, `hostPath`-mounting `/var/log/containers` (the
standard path `kubelet` writes every container's stdout/stderr to,
already symlinked to the real per-container log file) — this is why a
`DaemonSet`, not a `Deployment`: log files live on each node, not
somewhere network-reachable from a single pod.

```yaml
# deploy/k8s/fluent-bit/configmap.yaml (fluent-bit.conf key, sketch)
[INPUT]
    Name              tail
    Path              /var/log/containers/*_aviron_*.log
    Parser            docker
    Tag               kube.*

[FILTER]
    Name              kubernetes
    Match             kube.*
    Kube_Tag_Prefix   kube.var.log.containers.
    Merge_Log         On

[OUTPUT]
    Name              es
    Match             *
    Host              elasticsearch.aviron.svc.cluster.local
    Port              9200
    Index             aviron-logs
```

- `Path` filtered to `*_aviron_*.log` — Fluent Bit as a cluster-wide
  `DaemonSet` sees every namespace's logs by default; this project's own
  logs are the only ones worth shipping, consistent with `metrics/
  prometheus-deploy.md`'s own annotation-based opt-in for the same
  reason (avoid noise from infra pods that were never meant to be
  scraped/shipped).
- `Merge_Log On` parses each line's own JSON body (the `slog.
  NewJSONHandler` output) into real, individually queryable/filterable
  fields in Elasticsearch (`race_id`, `user_id`, `request_id`,
  `trace_id`/`span_id` once `logging/log-trace-correlation.md` lands) —
  without it, Kibana would only ever see one opaque `log` string field
  per line.
- `Kubernetes` filter also attaches pod/namespace/container metadata —
  the `pod`/`app` labels needed for `dashboards/grafana-deploy.md`'s
  pod-aware panels to have a logs-side equivalent.
- `DaemonSet`'s own `serviceAccountName` needs `get`/`list`/`watch` on
  `pods` (same shape as `metrics/prometheus-deploy.md`'s RBAC, needed for
  the `kubernetes` filter's own pod-metadata lookups) via `fluent-bit`'s
  own `Role`/`RoleBinding`.

### Kibana

```yaml
# deploy/k8s/kibana/deployment.yaml (sketch)
apiVersion: apps/v1
kind: Deployment
metadata:
  name: kibana
  namespace: aviron
spec:
  replicas: 1
  selector:
    matchLabels: { app: kibana }
  template:
    metadata:
      labels: { app: kibana }
    spec:
      containers:
        - name: kibana
          image: docker.elastic.co/kibana/kibana:8.15.0
          env:
            - name: ELASTICSEARCH_HOSTS
              value: http://elasticsearch.aviron.svc.cluster.local:9200
          ports:
            - containerPort: 5601
```

No `Ingress` for Kibana — `kubectl port-forward` is enough for occasional
manual log search during development/verification, matching this
project's existing "no need for external exposure of internal tooling"
stance (pgAdmin's own `docker-compose.yml` entry is the closest
precedent, exposed only via a host port, never an `Ingress`).

## Verification

- `kubectl get pods -n aviron -l app=elasticsearch` /
  `-l app=fluent-bit` / `-l app=kibana` all reach `Running`/`Ready`.
- `curl elasticsearch.aviron.svc.cluster.local:9200/aviron-logs/_count`
  (from inside the cluster) returns a growing, non-zero count once
  `race-service`/`ws-gateway`/`consumer` are producing any log traffic.
- Port-forward Kibana, create the `aviron-logs` index pattern, and
  confirm a log line's `race_id`/`user_id`/`request_id` fields are
  individually filterable — not just full-text-searchable inside one
  opaque blob — proving `Merge_Log On` actually parsed the JSON body.

## Notes

- No dependency on `tracing/instrumentation.md` or `logging/log-trace-
  correlation.md` — this spec ships whatever JSON already exists today;
  `trace_id`/`span_id` fields simply start appearing once that later
  spec lands, no config change needed here to pick them up.
- No ECK (Elastic Cloud on Kubernetes) operator — single-node,
  hand-written manifests, consistent with this phase's stance everywhere
  except Kafka.
- `xpack.security.enabled: false` disables Elasticsearch's own auth —
  same accepted local-dev-only posture as every other piece of this
  cluster's internal traffic; would be a real problem on any cluster
  this isn't.
