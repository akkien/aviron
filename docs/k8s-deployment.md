# Kubernetes Deployment

How to stand up this project's dependencies on a local `kind` cluster,
verify they actually work, keep them running day to day, and tear
everything down. This is the practical "how do I actually run this"
companion to `context/features/phase5/phase-5-plan.md` (the roadmap) and
`context/features/phase5/k8s-core-infra.md` (the spec this first slice
was built from) — those two explain *why* each choice was made; this doc
is just the commands.

**Current scope**: the whole stack — Postgres, Redis, NATS, Kafka, and
this project's own three binaries (`race-service`, `ws-gateway`,
`consumer`) — is deployed, autoscaled, and verified against a real
cluster. Every step below is what actually stood it up, in the order
that actually works (each binary depends on the one before it being
reachable).

## What's running today

```text
                        Namespace: aviron
+---------------------------------------------------------------+
| Ingress: ws-gateway (nginx)  ->  http://localhost/            |
|    |                                                          |
| ws-gateway x2 (Deployment, HPA 2-5)                           |
|    Service: ws-gateway                                        |
|    ServiceAccount + Role (EndpointSlice watch)                |
|    |                                                          |
|    | discovers dynamically                                    |
|    v                                                          |
| race-service x2 (StatefulSet, HPA 2-5)                        |
|    Service: race-service (headless)                           |
|    |                                                          |
|    +---------+---------+----------------------+               |
|    v         v         v                      v               |
| postgres-0  redis     nats     aviron-kafka-controller-0      |
| (StatefulSet (Deploy-  (Deploy-  (Helm: Bitnami chart,        |
|  Set, PVC)    ment)     ment)     StatefulSet under the hood) |
|                                                               |
| consumer (Deployment, 1 replica)                              |
|    Service: consumer                                          |
|    reads Kafka, writes Postgres                               |
|                                                               |
| prometheus (Deployment, 1 replica)                            |
|    Service: prometheus | ServiceAccount + Role (pod watch)   |
|    scrapes race-service/ws-gateway/consumer's /metrics via    |
|    kubernetes_sd_configs (role: pod), not a static list       |
|                                                               |
| otel-collector (Deployment, 1 replica)                        |
|    Service: otel-collector (OTLP gRPC :4317)                  |
|    receives OTLP from all 3 binaries once instrumentation.md  |
|    lands; today, verified with one manual test span only      |
|    |                                                          |
|    v                                                          |
| tempo (Deployment, 1 replica)                                 |
|    Service: tempo (query API :3200, OTLP :4317)               |
|                                                                |
| elasticsearch-0 (StatefulSet, 1 replica, emptyDir)             |
|    Service: elasticsearch (index: aviron-logs)                |
|    ^                                                          |
|    | ships parsed JSON log lines                              |
| fluent-bit (DaemonSet, 1 per node)                             |
|    ServiceAccount + Role (pod watch, for k8s metadata)        |
|                                                                |
| kibana (Deployment, 1 replica)                                 |
|    Service: kibana — no Ingress, port-forward only            |
+---------------------------------------------------------------+
```

See `docs/observation-architeture.md` for the full observability-plane
picture (metrics/traces/logs/alerting, shipped and planned) with
diagrams — this doc stays focused on the deploy-and-verify commands.

## Prerequisites

- [Docker](https://www.docker.com/), running.
- `kind`, `kubectl`, `helm` — install the first two via Homebrew if you
  don't have them:

  ```sh
  brew install kind helm
  ```

## Creating the cluster

Use `deploy/kind-config.yaml`, not a plain `kind create cluster` — it
maps host ports 80/443 onto the node and labels it `ingress-ready=true`,
both required for `ws-gateway`'s real `Ingress` to be reachable from the
host at all (a plain `kind create cluster` has no port mapping for
either):

```sh
kind create cluster --name aviron --config deploy/kind-config.yaml
```

Install the `nginx` ingress controller itself — it's a cluster addon,
not something `kubectl apply -f deploy/k8s/` provisions:

```sh
kubectl apply -f https://raw.githubusercontent.com/kubernetes/ingress-nginx/main/deploy/static/provider/kind/deploy.yaml
kubectl wait --namespace ingress-nginx \
  --for=condition=ready pod \
  --selector=app.kubernetes.io/component=controller \
  --timeout=180s
```

`backend/Dockerfile` already builds all three of this project's binaries
into one shared image (used by `docker-compose.yml` today) — build it and
load it into the cluster so it's available without needing a real
registry:

```sh
docker build -t aviron-backend:local ./backend
kind load docker-image aviron-backend:local --name aviron
```

Whenever the backend code changes, both of those commands need to be
re-run, followed by a rollout restart of whatever's already running the
old image (see "A rebuilt backend image doesn't seem to take effect" in
Troubleshooting) — `kind` doesn't share the host's Docker build cache or
watch for rebuilds automatically.

## Deploying core infrastructure

Everything below lives under `deploy/k8s/` and targets the `aviron`
namespace. Apply in this order — each step's manifest doesn't reference
anything from a later one, but pods will crash-loop waiting on a
dependency that isn't there yet:

```sh
kubectl apply -f deploy/k8s/namespace.yaml
kubectl apply -f deploy/k8s/configmap.yaml -f deploy/k8s/secret.yaml
kubectl apply -f deploy/k8s/postgres/
kubectl apply -f deploy/k8s/redis/
kubectl apply -f deploy/k8s/nats/
```

Then confirm each is up before moving to Kafka (a `StatefulSet` and two
`Deployment`s, so `Running` is a real signal, not close enough):

```sh
kubectl get pods -n aviron
kubectl wait --for=condition=Ready pod -l app=redis -n aviron --timeout=60s
kubectl wait --for=condition=Ready pod -l app=nats -n aviron --timeout=60s
```

Kafka is the one piece not `kubectl apply`d directly — it's a Helm
release (the Bitnami chart), configured by `deploy/k8s/kafka/values.yaml`
rather than a raw manifest:

```sh
helm repo add bitnami https://charts.bitnami.com/bitnami
helm repo update
helm install aviron-kafka bitnami/kafka --version 32.4.3 \
  --namespace aviron -f deploy/k8s/kafka/values.yaml
kubectl wait --for=condition=Ready pod -l app.kubernetes.io/instance=aviron-kafka -n aviron --timeout=180s
```

Three non-obvious things baked into these manifests, worth knowing before
you go changing them:

- **Postgres is a `StatefulSet`, not a `Deployment`**, with an inline
  `volumeClaimTemplates` block (no separate `pvc.yaml` file) — a database
  needs stable identity and durable per-pod storage across restarts;
  `kind`'s default `standard` `StorageClass` provisions the PVC
  automatically.
- **Kafka's `values.yaml` overrides `image.repository` to
  `bitnamilegacy/kafka`.** The chart's own default image
  (`docker.io/bitnami/kafka:...`) is unpullable for free since Broadcom's
  August 2025 licensing change — confirmed directly, not a
  precaution. `bitnamilegacy` is a community mirror of the old free
  images. `helm install`/`upgrade` will print a security warning about
  "substituted" containers when this override is active — expected, not a
  misconfiguration.
- **Kafka's listeners are forced to `PLAINTEXT`** (the chart defaults
  every listener to `SASL_PLAINTEXT`). This project's Kafka client code
  (`internal/kafka`, `internal/consumer`) has never done SASL, matching
  `docker-compose.yml`'s own plain, unauthenticated broker — leaving the
  chart's default in place would make `race-service`/`consumer` fail to
  authenticate.

## Deploying `race-service`

Needs Postgres/Redis/NATS/Kafka already up (above) — apply the
`StatefulSet` and its headless `Service`:

```sh
kubectl apply -f deploy/k8s/race-service/statefulset.yaml -f deploy/k8s/race-service/service.yaml
kubectl wait --for=condition=Ready pod -l app=race-service -n aviron --timeout=120s
```

`replicas: 2` is fixed in the manifest itself, matching the two-instance
topology `multi-instance-k8s-verification.md` proved — the
`HorizontalPodAutoscaler` in "Autoscaling" below takes over managing the
actual replica count once applied, this is just the starting point.

## Deploying `ws-gateway`

Depends on `race-service` already being up — `ws-gateway` discovers its
pods dynamically via a Kubernetes `EndpointSlice` watch
(`dynamic-backend-discovery.md`), which needs its own RBAC applied
first:

```sh
kubectl apply -f deploy/k8s/ws-gateway/rbac.yaml
kubectl apply -f deploy/k8s/ws-gateway/deployment.yaml -f deploy/k8s/ws-gateway/service.yaml -f deploy/k8s/ws-gateway/ingress.yaml
kubectl wait --for=condition=Ready pod -l app=ws-gateway -n aviron --timeout=120s
```

Confirm the `Ingress` actually resolves (needs `deploy/kind-config.yaml`
and the `nginx` controller from "Creating the cluster" above — without
both, this hangs or connection-refuses):

```sh
curl -i http://localhost/healthz
```

`ingress.yaml`'s two annotations
(`nginx.ingress.kubernetes.io/proxy-read-timeout`/`-send-timeout`,
both `"3600"`) aren't optional tuning — without them, `nginx` applies
its own default proxy timeout to an idle-but-open connection and kills
a live WebSocket mid-race; `ws-gateway`'s own `WSHandler` already does
the upgrade itself, `nginx` just has to get out of the way for as long
as a race can run.

## Deploying `consumer`

No dependency on `race-service`/`ws-gateway` — only needs Postgres/Kafka.
It gained a small `Service` (`deploy/k8s/consumer/service.yaml`) once
`metrics/metrics-parity.md` gave it a real HTTP surface — `/metrics` and
`/debug/pprof/*` only, nothing app-facing routes through it:

```sh
kubectl apply -f deploy/k8s/consumer/deployment.yaml -f deploy/k8s/consumer/service.yaml
kubectl wait --for=condition=Ready pod -l app=consumer -n aviron --timeout=60s
```

## Deploying Prometheus

Scrapes all three binaries' `/metrics` via `kubernetes_sd_configs`
(`role: pod`), not a static target list — see
`context/features/phase6/metrics/prometheus-deploy.md` for why:

```sh
kubectl apply -f deploy/k8s/prometheus/rbac.yaml -f deploy/k8s/prometheus/configmap.yaml \
  -f deploy/k8s/prometheus/deployment.yaml -f deploy/k8s/prometheus/service.yaml
kubectl wait --for=condition=Ready pod -l app=prometheus -n aviron --timeout=60s

# Check discovered targets
kubectl port-forward -n aviron svc/prometheus 9090:9090
# then open http://localhost:9090/targets — every race-service/ws-gateway/
# consumer pod should show up, discovered automatically.
```

## Deploying the OTel Collector + Tempo

Pure infra, no application code — stands up the single OTLP fan-out point
(`otel-collector`) and the trace backend behind it (`tempo`, monolithic
mode) that `tracing/instrumentation.md` needs to exist before it can send
real spans. See
`context/features/phase6/tracing/otel-collector-tempo-deploy.md`:

```sh
kubectl apply -f deploy/k8s/otel-collector/configmap.yaml -f deploy/k8s/otel-collector/deployment.yaml \
  -f deploy/k8s/otel-collector/service.yaml
kubectl apply -f deploy/k8s/tempo/configmap.yaml -f deploy/k8s/tempo/deployment.yaml -f deploy/k8s/tempo/service.yaml
kubectl wait --for=condition=Ready pod -l 'app in (otel-collector,tempo)' -n aviron --timeout=60s
```

Verify the pipeline works before any application code sends a real span
— send one manual test span (a tiny Go program using
`otlptracegrpc.New(..., otlptracegrpc.WithEndpoint("localhost:4317"))`
against a port-forwarded Collector) and confirm it's queryable via
Tempo's own search API:

```sh
kubectl port-forward -n aviron svc/tempo 3200:3200
curl -s 'http://localhost:3200/api/search?tags=service.name%3D<your-test-service-name>' | jq
```

## Deploying EFK (Elasticsearch, Fluent Bit, Kibana)

Centralizes the structured `slog` JSON every binary already writes to
stdout — today readable only via `kubectl logs` against whichever replica
happened to handle a given request. See
`context/features/phase6/logging/efk-deploy.md`:

```sh
kubectl apply -f deploy/k8s/elasticsearch/statefulset.yaml -f deploy/k8s/elasticsearch/service.yaml
kubectl apply -f deploy/k8s/fluent-bit/rbac.yaml -f deploy/k8s/fluent-bit/configmap.yaml \
  -f deploy/k8s/fluent-bit/daemonset.yaml
kubectl apply -f deploy/k8s/kibana/deployment.yaml -f deploy/k8s/kibana/service.yaml
kubectl wait --for=condition=Ready pod -l 'app in (elasticsearch,fluent-bit,kibana)' -n aviron --timeout=300s
```

Verify logs are actually landing and their structured fields are
individually queryable, not just one opaque blob:

```sh
kubectl exec -n aviron elasticsearch-0 -c elasticsearch -- \
  curl -s 'http://localhost:9200/aviron-logs/_count'

# a field-only filter (no full-text match) proving Merge_Log actually
# parsed the JSON body into real fields
kubectl exec -n aviron elasticsearch-0 -c elasticsearch -- \
  curl -s -X POST 'http://localhost:9200/aviron-logs/_search?pretty' \
  -H 'Content-Type: application/json' \
  -d '{"size":0,"query":{"term":{"path.keyword":"/healthz"}}}'
```

Kibana has no `Ingress` — reach it via port-forward, then create the
`aviron-logs` data view (Stack Management → Data Views, timestamp field
`@timestamp`) before using Discover:

```sh
kubectl port-forward -n aviron svc/kibana 5601:5601
# then open http://localhost:5601
```

## Components

Quick reference — especially the DNS names, which are exactly what you'd
need when debugging a connection issue from inside the cluster:

| Component | Kind | Image | Ports | In-cluster DNS | Persistence |
| --- | --- | --- | --- | --- | --- |
| Postgres | `StatefulSet` | `postgres:18-alpine` | 5432 | `postgres.aviron.svc.cluster.local` (headless) | PVC via `volumeClaimTemplates`, 1Gi |
| Redis | `Deployment` | `redis:7-alpine` | 6379 | `redis.aviron.svc.cluster.local` | None — self-healing TTL data |
| NATS | `Deployment` | `nats:2-alpine` | 4222 (client), 8222 (monitor) | `nats.aviron.svc.cluster.local` | None — NATS Core, no JetStream |
| Kafka | Helm release (`bitnami/kafka`, `StatefulSet` under the hood) | `bitnamilegacy/kafka:4.0.0-debian-12-r10` | 9092 | `aviron-kafka.aviron.svc.cluster.local` | None — `persistence.enabled: false` |
| `race-service` | `StatefulSet` (2-5, HPA) | `aviron-backend:local` (`/app/server`) | 8080 | `race-service-<N>.race-service.aviron.svc.cluster.local` (headless) | None — no room state survives a restart by design |
| `ws-gateway` | `Deployment` (2-5, HPA) | `aviron-backend:local` (`/app/ws-gateway`) | 8080 | `ws-gateway.aviron.svc.cluster.local`; externally `http://localhost/` via `Ingress` | None — stateless |
| `consumer` | `Deployment` (1) | `aviron-backend:local` (`/app/consumer`) | 8091 (`/metrics`, `/debug/pprof/*` only) | `consumer.aviron.svc.cluster.local` | None |
| Prometheus | `Deployment` (1) | `prom/prometheus:latest` | 9090 | `prometheus.aviron.svc.cluster.local` | None — `emptyDir`, TSDB doesn't survive a pod restart |
| OTel Collector | `Deployment` (1) | `otel/opentelemetry-collector:latest` (core, not `-contrib`) | 4317 (OTLP gRPC), 13133 (health) | `otel-collector.aviron.svc.cluster.local` | None — stateless fan-out, nothing to persist |
| Tempo | `Deployment` (1) | `grafana/tempo:latest` | 3200 (query API), 4317 (OTLP) | `tempo.aviron.svc.cluster.local` | None — `emptyDir` for WAL + blocks, doesn't survive a pod restart |
| Elasticsearch | `StatefulSet` (1) | `docker.elastic.co/elasticsearch/elasticsearch:8.15.0` | 9200 | `elasticsearch.aviron.svc.cluster.local` | None — `emptyDir`, index data doesn't survive a pod restart |
| Fluent Bit | `DaemonSet` (1 per node) | `fluent/fluent-bit:latest` | n/a (tails `hostPath`) | n/a — no Service, ships to Elasticsearch | None — stateless log shipper |
| Kibana | `Deployment` (1) | `docker.elastic.co/kibana/kibana:8.15.0` | 5601 | `kibana.aviron.svc.cluster.local`; reached via `kubectl port-forward` only, no `Ingress` | None — stateless |

## Verifying it's actually working

A pod reaching `Running` isn't proof it works — confirm each dependency
actually answers:

```sh
# Postgres
kubectl exec -n aviron postgres-0 -- pg_isready -U aviron
# -> accepting connections

# Redis
kubectl exec -n aviron deploy/redis -- redis-cli ping
# -> PONG

# NATS (from a throwaway pod, since nothing in-cluster has curl by default)
kubectl run curltest --rm -i --restart=Never --image=curlimages/curl -n aviron \
  --command -- curl -s http://nats:8222/healthz
# -> {"status":"ok"}

# Kafka
kubectl exec -n aviron aviron-kafka-controller-0 -c kafka -- \
  kafka-broker-api-versions.sh --bootstrap-server localhost:9092
# -> lists broker id: 0 with its supported API version ranges
```

Postgres migrations run automatically on `race-service`'s own startup
(`db.Migrate`, `cmd/server/run.go`) — confirmed by their presence in
`workout_samples`/`race_participants` etc. once a real race finishes,
not checked separately here.

This project's own three binaries, plus one real end-to-end flow through
all of them:

```sh
# race-service / ws-gateway readiness (dependency-checking) and liveness
# (dependency-free) endpoints — both should be 200
curl -i http://localhost/healthz
curl -i http://localhost/livez

# consumer exposes only /metrics + /debug/pprof/* (no app-facing routes) —
# confirm it's actually consuming by checking its own logs for a batch
# flush after a race finishes
kubectl logs -n aviron -l app=consumer --tail=20

# The real proof: register, create, start, and finish a race entirely
# through the Ingress, then confirm the row landed in Postgres
kubectl exec -n aviron postgres-0 -- psql -U aviron -d aviron \
  -c "select count(*) from race_participants;"
```

## Autoscaling (`HorizontalPodAutoscaler`)

`race-service` and `ws-gateway` both scale on CPU utilization
(`k8s-hpa.md` — `race-service`'s own manifest, `deploy/k8s/race-service/hpa.yaml`,
targets its `StatefulSet`; `ws-gateway`'s, `deploy/k8s/ws-gateway/hpa.yaml`,
targets its `Deployment`). Redis/Postgres/Kafka are deliberately not
autoscaled — see that spec's "What about Redis, Postgres, Kafka?" section
for why.

**One-time prerequisite**: `metrics-server` isn't part of a plain `kind`
cluster and has to be installed once per cluster:

```sh
kubectl apply -f https://github.com/kubernetes-sigs/metrics-server/releases/latest/download/components.yaml
kubectl patch deployment metrics-server -n kube-system --type='json' \
  -p='[{"op":"add","path":"/spec/template/spec/containers/0/args/-","value":"--kubelet-insecure-tls"}]'
```

The patch is `kind`-specific: kubelet's serving certs on a `kind` node
aren't signed by a CA `metrics-server` trusts by default, so without
`--kubelet-insecure-tls` it stays permanently unable to scrape. Confirm
it's actually serving before relying on it for anything —
`kubectl top nodes`/`kubectl top pods -n aviron` should return real
numbers, not an error.

**Applying the HPAs** — not automatic, and not part of "Deploying
`race-service`"/"Deploying `ws-gateway`" above on purpose: they're
useless without `metrics-server` already serving, so apply them only
once the prerequisite above is confirmed working:

```sh
kubectl apply -f deploy/k8s/race-service/hpa.yaml -f deploy/k8s/ws-gateway/hpa.yaml
```

**Reading HPA status**:

```sh
kubectl get hpa -n aviron
```

```text
NAME           REFERENCE                  TARGETS       MINPODS   MAXPODS   REPLICAS   AGE
race-service   StatefulSet/race-service   cpu: 1%/70%   2         5         2          25m
ws-gateway     Deployment/ws-gateway      cpu: 2%/70%   2         5         5          25m
```

- `TARGETS` showing `<unknown>` instead of a percentage means
  `metrics-server` isn't serving yet (or was never installed) — not an
  HPA misconfiguration, check the prerequisite above first.
- `REPLICAS` only climbs above `MINPODS` under real sustained load; it
  settles back down on its own once load stops, but not instantly —
  `autoscaling/v2`'s default 5-minute `scaleDown.stabilizationWindowSeconds`
  means a replica count can stay elevated for a while after CPU has
  already dropped, by design (avoids flapping on a brief lull).
- `kubectl get hpa -n aviron -w` to watch it live during a load test.

**Generating enough load to see it actually scale**: `race-service` and
`ws-gateway` respond very differently to the same traffic —
`race-service` is the CPU-heavy side under a normal race workload (room
actor ticking + broadcast fan-out), while `ws-gateway` is a thin proxy
that stays cheap under that same workload and needs its own dedicated
REST-proxy burst to cross 70%:

```sh
# race-service: real races via k6 (this project's own documented default —
# NUM_RACES=8 VUS_PER_RACE=8 also works for load, but pushes setup() close
# to k6's own 60s default setup timeout; 5/8 stays comfortably under it)
BASE_URL=http://localhost NUM_RACES=5 VUS_PER_RACE=8 k6 run load/scenarios/race-lifecycle.js

# ws-gateway: a raw REST-proxy burst (room-less path, no simulation cost).
# $TOKEN is any valid JWT — register + log in once to get one:
#   curl -s -X POST http://localhost/auth/register -H "Content-Type: application/json" \
#     -d '{"email":"loadtest@example.com","password":"loadtest-pw-1","display_name":"Load Test"}'
#   TOKEN=$(curl -s -X POST http://localhost/auth/login -H "Content-Type: application/json" \
#     -d '{"email":"loadtest@example.com","password":"loadtest-pw-1"}' | jq -r .token)
ab -n 60000 -c 150 -H "Authorization: Bearer $TOKEN" http://localhost/races
```

**Scale-down safety**: a `HorizontalPodAutoscaler`-triggered scale-down
uses the exact same Pod-deletion path `kubectl delete pod` does — so
`kubectl delete pod race-service-<N>` while it owns an in-progress race
is a faithful, controllable stand-in for testing the real thing without
waiting on the stabilization window. Watch its logs for the sequence
`graceful-shutdown.md` designed: `shutdown signal received` →
`waiting for in-progress races to finish` → (room keeps broadcasting) →
`shutdown complete`, only after the room actually finishes — not cut off
mid-race.

## Maintaining the cluster

- **Status at a glance**: `kubectl get pods -n aviron -o wide`.
- **Logs**: `kubectl logs -n aviron <pod-name>`, or `-l app=<name>` to
  tail a whole `Deployment`/`StatefulSet` (add `-f` to follow, `--previous`
  after a restart to see the crashed instance's last logs).
- **Restarting a stateless piece** (Redis, NATS) after an edit:
  `kubectl rollout restart deployment/<name> -n aviron`.
- **Restarting Postgres**: same idea, but `kubectl rollout restart
  statefulset/postgres -n aviron` — the PVC survives the pod restart, data
  is untouched.
- **Editing a hand-written manifest**: edit the file under `deploy/k8s/`,
  then `kubectl diff -f <file>` to see exactly what would change before
  `kubectl apply -f <file>` for real. `kubectl diff` against every
  manifest in this doc against a healthy cluster should show no output at
  all — that's the expected steady state, not a check that's expected to
  fail.
- **Upgrading Kafka's Helm release** after editing
  `deploy/k8s/kafka/values.yaml`:

  ```sh
  helm upgrade aviron-kafka bitnami/kafka --version 32.4.3 \
    --namespace aviron -f deploy/k8s/kafka/values.yaml
  ```

  Always pass `-f deploy/k8s/kafka/values.yaml` again on every upgrade —
  Helm doesn't remember a prior release's values file, an upgrade without
  it would silently reset every override (including the
  `bitnamilegacy`/`PLAINTEXT` ones above) back to the chart's defaults.
- **Inspecting what Kafka's chart actually generated** (it manages its own
  `StatefulSet`, not one this repo hand-writes):

  ```sh
  helm get manifest aviron-kafka -n aviron
  kubectl get statefulset -n aviron
  ```

## Troubleshooting

- **Kafka pod stuck in `ImagePullBackOff` / `Init:ErrImagePull`**: the
  Bitnami chart's own default image (`docker.io/bitnami/kafka:...`) is
  unpullable for free since Broadcom's August 2025 licensing change.
  Confirm `deploy/k8s/kafka/values.yaml` still has the
  `bitnamilegacy/kafka` override in place — see the note below on Helm
  upgrades silently dropping it.
- **A `StatefulSet` pod (Postgres, or Kafka's controller) stuck
  `Pending`**: check `kubectl get storageclass`. A `volumeClaimTemplates`
  block needs a working default `StorageClass` to dynamically provision
  from — `kind` ships one (`standard`, `rancher.io/local-path`) out of the
  box; if it's missing (a custom `kind` config disabled it, or a
  different cluster tool is in use), the PVC sits `Pending` forever and so
  does the pod.
- **A rebuilt backend image doesn't seem to take effect**: `kind`
  clusters don't share the host's `docker build` cache/registry —
  rebuilding `aviron-backend:local` locally does nothing to the cluster
  until you run `kind load docker-image aviron-backend:local --name
  aviron` again, and any pod already running the old image needs a
  restart (`kubectl rollout restart ...`) to actually pick up the
  reloaded one.
- **A Helm upgrade "resets" Kafka's config** (SASL listeners come back,
  or the image reverts to the unpullable default): `-f
  deploy/k8s/kafka/values.yaml` was left off the `helm upgrade` command.
  Helm doesn't remember a prior release's values file — every upgrade
  must pass it again explicitly, or every override in this doc's
  "Deploying core infrastructure" section silently reverts to the
  chart's defaults.
- **The checks in "Verifying it's actually working" fail even though the
  pod shows `Running`**: `Running` only means the container process
  started, not that whatever's inside is actually answering — that gap is
  exactly why that section's checks exist rather than stopping at
  `kubectl get pods`. Kafka's controller in particular can stay `Running`
  for a little while after starting before it actually shows up in the
  broker API.

## Shutting down

Tear down one piece at a time if you just want to reclaim resources but
keep iterating — delete the `HorizontalPodAutoscaler`s first, or a
scale-down attempt can race the rest of the teardown:

```sh
kubectl delete -f deploy/k8s/race-service/hpa.yaml -f deploy/k8s/ws-gateway/hpa.yaml
kubectl delete -f deploy/k8s/consumer/deployment.yaml
kubectl delete -f deploy/k8s/ws-gateway/ingress.yaml -f deploy/k8s/ws-gateway/service.yaml -f deploy/k8s/ws-gateway/deployment.yaml
kubectl delete -f deploy/k8s/ws-gateway/rbac.yaml
kubectl delete -f deploy/k8s/race-service/service.yaml -f deploy/k8s/race-service/statefulset.yaml
helm uninstall aviron-kafka -n aviron
kubectl delete -f deploy/k8s/nats/
kubectl delete -f deploy/k8s/redis/
kubectl delete -f deploy/k8s/postgres/    # also deletes its PVC — data is gone
kubectl delete -f deploy/k8s/configmap.yaml -f deploy/k8s/secret.yaml
kubectl delete -f deploy/k8s/namespace.yaml
```

Or delete the whole cluster in one step — since every `kind` node is
itself a Docker container, this removes the PVC-backed Postgres data too,
no separate cleanup needed:

```sh
kind delete cluster --name aviron
```

Optionally remove the locally built image if you don't need it around:

```sh
docker rmi aviron-backend:local
```
