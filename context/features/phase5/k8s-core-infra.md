# Kubernetes — Core Infrastructure

## Overview

Stands up everything `race-service`, `ws-gateway`, and `consumer` depend
on: Postgres, Redis, Kafka, and NATS. Runs none of this project's own
binaries yet — `k8s-race-service-deploy.md`, `k8s-ws-gateway-deploy.md`,
and `k8s-consumer-deploy.md` are next. Uses **kind** (per
`project-overview.md` §7's own preference ordering; confirm at `start` if
that's still the intended choice, §7 accepts `minikube` too).

No `dockerize` step is needed here the way `phase-5-plan.md`'s "What's
different" table already notes: `backend/Dockerfile` exists today and
already builds all three binaries (`server`, `ws-gateway`, `consumer`)
into one `alpine`-based image, the same image `docker-compose.yml`
already tags `aviron-backend:local` and reuses across `server-a`,
`server-b`, `ws-gateway`, and `consumer`. This spec's only job regarding
that image is confirming it loads into the cluster:

```sh
docker build -t aviron-backend:local ./backend
kind load docker-image aviron-backend:local --name aviron
```

## Layout

```text
deploy/k8s/
  namespace.yaml
  configmap.yaml
  secret.yaml
  postgres/
    statefulset.yaml   # includes volumeClaimTemplates — no separate pvc.yaml, see "Postgres" below
    service.yaml
  redis/
    deployment.yaml
    service.yaml
  nats/
    deployment.yaml
    service.yaml
  kafka/
    values.yaml   # Bitnami Helm chart values — see "Kafka" below; the
                  # release itself is a `helm install`, not an `apply`d manifest
  race-service/     # k8s-race-service-deploy.md, not this spec
  ws-gateway/        # k8s-ws-gateway-deploy.md, not this spec
  consumer/          # k8s-consumer-deploy.md, not this spec
```

No `api-gateway/` folder collision to worry about here — `ws-gateway/`
above is this project's real gateway binary, not a stand-in for one; see
`phase-5-plan.md`'s reversal note for why that's a deliberate departure
from `project-overview.md` §7's original wording.

## `namespace.yaml`

A single `aviron` namespace. Every manifest in this phase targets it
explicitly (`metadata.namespace: aviron`), not `default`.

## `configmap.yaml` / `secret.yaml`

- `ConfigMap` for non-sensitive, shared config: `CORS_ALLOWED_ORIGIN`,
  `PPROF_ENABLED`, `KAFKA_BROKERS`, `REDIS_URL`, `NATS_URL` — names
  mirroring `internal/config.Config`'s and `internal/wsgateway.Config`'s
  existing `getEnv` keys exactly (confirmed by reading both files
  directly), so nothing above the manifest layer needs to change to read
  Kubernetes-injected env vars instead of `docker-compose.yml`'s.
- `Secret` for `JWT_SECRET` and the Postgres credentials
  (`POSTGRES_USER`/`POSTGRES_PASSWORD`, composed into `DATABASE_URL` via
  the Deployment/StatefulSet env, same `postgres://$(POSTGRES_USER):
  $(POSTGRES_PASSWORD)@postgres:5432/aviron?sslmode=disable` shape
  `docker-compose.yml` already uses literally today). Plain `Secret`
  (base64, not encrypted at rest) is proportionate — this project's local
  credentials are already `aviron`/`aviron` and `admin`/`admin` for
  pgAdmin, this is a local `kind` cluster, not a real deployment target.
- Per-binary env vars that differ per pod (`INSTANCE_ID`,
  `RACE_SERVICE_INSTANCES`, `PORT`, `WS_GATEWAY_LISTEN_ADDR`) are **not**
  in this `ConfigMap`/`Secret` — they belong to the Deployment/StatefulSet
  specs that actually need pod-specific values, per
  `k8s-race-service-deploy.md`/`k8s-ws-gateway-deploy.md`.

## Postgres

- `StatefulSet` (single replica — this project runs one Postgres, same as
  `docker-compose.yml`) + headless `Service`, same `postgres:18-alpine`
  image `docker-compose.yml` already uses. **Correction from this spec's
  original layout**: no separate, hand-written `pvc.yaml` — a
  `StatefulSet`'s idiomatic storage mechanism is an inline
  `volumeClaimTemplates` block, which provisions and binds one PVC per
  replica automatically (`pgdata-postgres-0`); a manually created static
  PVC isn't how a `StatefulSet` normally consumes storage, and `kind`'s
  default `standard` `StorageClass` (`rancher.io/local-path`) supports the
  dynamic provisioning this needs out of the box. Confirmed working
  end to end against a real cluster.
- **Migrations need no separate `Job`.** `cmd/server/run.go` already
  calls `db.Migrate(cfg.DatabaseURL)` unconditionally at startup, before
  serving — the same "migrations run automatically on startup, no
  separate step needed" property `README.md`'s Install and Run section
  already documents for `docker-compose up`. This carries over unchanged.
  The one real question this raises for Kubernetes specifically — two
  `race-service` StatefulSet pods (`k8s-race-service-deploy.md`'s
  `replicas: 2`) both calling `db.Migrate` concurrently on a cold
  cluster — is already answered by `golang-migrate`'s own documented
  behavior, not new code this project needs to write: its Postgres driver
  takes an advisory lock (`pg_advisory_lock`) for the duration of `Up()`,
  so the second pod's call simply blocks until the first's completes,
  then finds nothing pending (`migrate.ErrNoChange`, already handled as a
  non-error in `Migrate`). No changes to `internal/db/migrate.go` needed.

## Redis

- Plain `Deployment` (not `StatefulSet`), single replica, + `Service`,
  `redis:7-alpine` matching `docker-compose.yml`. No `PersistentVolume` —
  Redis here is only ever the room-ownership registry and evicted-user
  set (`redis-room-registry.md`), both self-healing via TTL, so losing
  in-memory state on a pod restart is an accepted, bounded-impact event
  (the same stance the original, deleted `k8s-core-infra.md` already
  took), not data loss the way losing Postgres would be.

## NATS

New relative to the original (deleted) Phase 5 docs, which predate the
WS Gateway pivot — `internal/roomrelay`'s transport (`room-message-bus.md`)
now needs standing up here too.

- Plain `Deployment`, single replica, + `Service`. NATS Core only (no
  JetStream — `room-message-bus.md` accepts at-most-once delivery), so no
  `PersistentVolume` either.
- **Image**: `nats:2-alpine`, not `nats:latest`. `docker-compose.yml`
  already carries a comment recording exactly why, and
  `load/multi-instance-check.md`'s own "Real bugs" section confirms it the
  hard way: `nats:latest` is distroless-style with no shell, so nothing
  `exec`-based (a `CMD`-exec healthcheck, `docker compose exec ... sh`)
  can run against it — the same reasoning applies to a Kubernetes
  `exec`-based probe, so use `nats:2-alpine` here too.
- Readiness probe: `httpGet` against `:8222/healthz` (the monitoring
  endpoint `docker-compose.yml`'s `command: ["-m", "8222"]` already
  enables) — a real, native `httpGet` probe, strictly better than
  `docker-compose.yml`'s own `wget`-inside-`exec` healthcheck.

## Kafka

Per `project-overview.md` §7/§11's explicit steer: "use an existing chart
(Strimzi or Bitnami) rather than writing your own." **Decided: the
Bitnami Kafka Helm chart, not Strimzi** — a plain Helm release rather
than an operator + custom-resource model, lighter to reason about and to
run on a laptop-hosted `kind` cluster, which is what this project
actually needs rather than Strimzi's fuller operator lifecycle (rolling
upgrades, multi-cluster management) this project has no use for.

Installed as chart version `32.4.3` (app version `4.0.0` — Kafka 4.0,
which dropped ZooKeeper entirely, so this chart version has no
`zookeeper.enabled` toggle at all; KRaft is simply the only mode):

```sh
helm repo add bitnami https://charts.bitnami.com/bitnami
helm install aviron-kafka bitnami/kafka --version 32.4.3 \
  --namespace aviron -f deploy/k8s/kafka/values.yaml
```

- `controller.replicaCount: 1`, `controller.controllerOnly` left at its
  chart default (`false`) — a single combined broker+controller node,
  matching `docker-compose.yml`'s own single-process
  `KAFKA_PROCESS_ROLES: broker,controller` topology, not a multi-broker
  production shape. `broker.replicaCount` stays at its chart default
  (`0`) — no separate broker-only pool.
- `controller.persistence.enabled: false` — matching `docker-compose.yml`'s
  own `kafka` service, which mounts no volume today (only `pgdata`/
  `pgadmin_data` are named volumes in that file); topics/messages not
  surviving a broker pod restart is already this project's accepted
  local-dev stance, not a new regression introduced by moving to
  Kubernetes.
- **`listeners.{client,controller,interbroker}.protocol: PLAINTEXT`,
  overriding the chart's own default (`SASL_PLAINTEXT` on every
  listener).** Real finding, not anticipated when this spec was first
  written: this project's Kafka client code (`internal/kafka.Producer`,
  `internal/consumer`, both plain `segmentio/kafka-go` with no SASL
  mechanism ever configured) has never done SASL, matching
  `docker-compose.yml`'s own plain-`PLAINTEXT` broker — leaving the
  chart's SASL defaults in place would make `race-service`/`consumer`
  fail to authenticate against it. Same "local kind cluster, not a real
  deployment target" stance already taken for the Postgres/JWT
  credentials in `secret.yaml`.
- Resources left at the chart's own `resourcesPreset: small` default
  (not overridden) — already proportionate to a laptop-hosted `kind`
  cluster, no need to hand-pick CPU/memory numbers on top of it.
- `KAFKA_BROKERS` in the shared `ConfigMap` points at the chart's real,
  confirmed bootstrap `Service` DNS name for release name `aviron-kafka`:
  `aviron-kafka.aviron.svc.cluster.local:9092` (printed by `helm install`'s
  own `NOTES`, and matches what `k8s-core-infra.md`'s `configmap.yaml`
  already has set).

### Real blocker hit during implementation: the chart's own image is unpullable for free

`helm install` itself deploys, but the controller pod then sits in
`ImagePullBackOff` against the chart's default image,
`docker.io/bitnami/kafka:4.0.0-debian-12-r10` — confirmed directly
against this cluster, not a hypothetical:

```text
Failed to pull image "docker.io/bitnami/kafka:4.0.0-debian-12-r10":
docker.io/bitnami/kafka:4.0.0-debian-12-r10: not found
```

Since August 2025, Broadcom (Bitnami's owner) moved most
`docker.io/bitnami/*` tags behind a paid "Bitnami Secure Images"
subscription and pruned the free ones — `helm install` itself even prints
a warning to this effect on every install of this chart now. This isn't
the resource-weight risk this spec originally anticipated ("too heavy for
local `kind`") — it's that the free chart, as published, references an
image that no longer exists for free pulling, a different failure mode
entirely.

**Resolved by overriding `image.registry`/`image.repository` to
`bitnamilegacy/kafka`** — a community mirror of the pre-August-2025 free
images that Bitnami/Broadcom itself stood up as a stopgap, confirmed to
have the exact same tag (`4.0.0-debian-12-r10`) pullable. `helm
upgrade`/`install` prints its own security warning about substituted,
"unrecognized" containers when this override is in place — expected and
accepted here, not a sign of a misconfiguration; this is a local `kind`
cluster, and `bitnamilegacy` is worth revisiting if it ever stops being
maintained (it has no support guarantee the way a paid subscription
would).

## Resource requests/limits

Postgres, Redis, and NATS each get an explicit, small
`resources.requests`/`limits` block in their hand-written manifests —
proportionate to a laptop-hosted `kind` cluster. Kafka's resources come
from the Bitnami chart's own `resourcesPreset: small` default instead
(left un-overridden — already proportionate, no need to hand-pick
numbers on top of it).

## Verification

Confirmed end to end against a real cluster, not just planned:

- `kind create cluster --name aviron` (fresh), applied every manifest in
  this spec in dependency order (`namespace` → `configmap`/`secret` →
  `postgres` → `redis` → `nats` → `kafka`) — every pod reached
  `Running 1/1 Ready` (`aviron-kafka-controller-0` only after the
  `bitnamilegacy` image override above).
- Each dependency confirmed actually reachable, not just pod-exists:
  - Postgres: `kubectl exec postgres-0 -- pg_isready -U aviron` →
    `accepting connections`. (Not migrated yet — no binary has run
    `Migrate` until `k8s-race-service-deploy.md` deploys `race-service`,
    so that check is deferred to that spec's own verification, as
    originally planned.)
  - Redis: `kubectl exec deploy/redis -- redis-cli ping` → `PONG`.
  - NATS: `curl http://nats:8222/healthz` (from a throwaway in-cluster
    pod) → `{"status":"ok"}`, HTTP 200.
  - Kafka: `kafka-broker-api-versions.sh --bootstrap-server localhost:9092`
    (run inside `aviron-kafka-controller-0`) → broker `id: 0` listed with
    its full supported-API-version range.
- `go build ./...` and `go test ./...` still pass unmodified — this spec
  touches no Go code, only `deploy/k8s/` manifests and a Helm release.

## Notes

- This spec produces zero manifests for this project's own binaries —
  kept separate so a Postgres/Redis/Kafka/NATS chart misconfiguration is
  never tangled up with debugging this project's own
  Deployment/StatefulSet/probe/graceful-shutdown logic in the same
  troubleshooting session, same separation the original, deleted
  `k8s-core-infra.md` already established.
- If the Bitnami chart still turns out too heavy for a reasonable local
  `kind` cluster (a real risk the original plan already flagged for Kafka
  generally — even a single-broker setup isn't a lightweight footprint,
  chart-managed or not), document that finding here and consider scoping
  Kafka out of the Kubernetes phase specifically, keeping Phase 4's
  `event-pipeline/` verified only against `docker-compose` — a legitimate
  judgment call to make once the real resource cost is observed, not a
  failure if it happens.
