# Kubernetes Deployment

How to stand up this project's dependencies on a local `kind` cluster,
verify they actually work, keep them running day to day, and tear
everything down. This is the practical "how do I actually run this"
companion to `context/features/phase5/phase-5-plan.md` (the roadmap) and
`context/features/phase5/k8s-core-infra.md` (the spec this first slice
was built from) — those two explain *why* each choice was made; this doc
is just the commands.

**Current scope**: only core infrastructure (Postgres, Redis, NATS,
Kafka) is deployed today. This project's own binaries
(`race-service`/`ws-gateway`/`consumer`) don't have Kubernetes manifests
yet — that's `k8s-race-service-deploy.md`, `k8s-ws-gateway-deploy.md`, and
`k8s-consumer-deploy.md` in `phase-5-plan.md`'s build order. This doc gets
a "Deploying `race-service`/`ws-gateway`/`consumer`" section once those
land.

## What's running today

```text
                         Namespace: aviron
   ┌───────────────────────────────────────────────────────────┐
   │                                                             │
   │   postgres-0                       redis                  │
   │   (StatefulSet, 1Gi PVC)            (Deployment)            │
   │        │                                │                  │
   │   Service: postgres                 Service: redis         │
   │   (headless)                                                │
   │                                                             │
   │   nats                              aviron-kafka-controller-0 │
   │   (Deployment)                       (Helm: Bitnami chart,   │
   │        │                              StatefulSet under the  │
   │   Service: nats                       hood — see Components) │
   │                                            │                │
   │                                       Service: aviron-kafka  │
   │                                                             │
   └───────────────────────────────────────────────────────────┘

   race-service / ws-gateway / consumer — not deployed yet, see
   "Current scope" above.
```

## Prerequisites

- [Docker](https://www.docker.com/), running.
- `kind`, `kubectl`, `helm` — install the first two via Homebrew if you
  don't have them:

  ```sh
  brew install kind helm
  ```

## Creating the cluster

```sh
kind create cluster --name aviron
```

`backend/Dockerfile` already builds all three of this project's binaries
into one shared image (used by `docker-compose.yml` today) — build it and
load it into the cluster so it's available without needing a real
registry:

```sh
docker build -t aviron-backend:local ./backend
kind load docker-image aviron-backend:local --name aviron
```

Nothing consumes this image yet (see "Current scope" above) — this step
just gets it in place ahead of the specs that will.

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

## Components

Quick reference — especially the DNS names, which are exactly what you'd
need when debugging a connection issue from inside the cluster:

| Component | Kind | Image | Ports | In-cluster DNS | Persistence |
| --- | --- | --- | --- | --- | --- |
| Postgres | `StatefulSet` | `postgres:18-alpine` | 5432 | `postgres.aviron.svc.cluster.local` (headless) | PVC via `volumeClaimTemplates`, 1Gi |
| Redis | `Deployment` | `redis:7-alpine` | 6379 | `redis.aviron.svc.cluster.local` | None — self-healing TTL data |
| NATS | `Deployment` | `nats:2-alpine` | 4222 (client), 8222 (monitor) | `nats.aviron.svc.cluster.local` | None — NATS Core, no JetStream |
| Kafka | Helm release (`bitnami/kafka`, `StatefulSet` under the hood) | `bitnamilegacy/kafka:4.0.0-debian-12-r10` | 9092 | `aviron-kafka.aviron.svc.cluster.local` | None — `persistence.enabled: false` |

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

Postgres migration status isn't checked here — nothing has run
`db.Migrate` yet, since no binary is deployed (see "Current scope"). That
check belongs to whichever spec actually deploys `race-service`.

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
keep iterating:

```sh
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
