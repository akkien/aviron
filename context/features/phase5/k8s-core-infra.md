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
    statefulset.yaml
    service.yaml
    pvc.yaml
  redis/
    deployment.yaml
    service.yaml
  nats/
    deployment.yaml
    service.yaml
  kafka/
    # via an existing chart (Strimzi or Bitnami) — see "Kafka" below
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
  `docker-compose.yml`) + `PersistentVolumeClaim` + headless `Service`,
  same `postgres:18-alpine` image `docker-compose.yml` already uses.
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
(Strimzi or Bitnami) rather than writing your own." Strimzi's own KRaft
mode (no ZooKeeper) is the natural fit — it's the same broker topology
`docker-compose.yml`'s `kafka` service already runs
(`KAFKA_PROCESS_ROLES: broker,controller`, no ZooKeeper container),
translated into an operator-managed single-node `Kafka` custom resource
rather than a hand-written broker Deployment.

- Single broker, minimal resource requests — this is a laptop-hosted
  `kind` cluster, not a production-sized Kafka footprint.
- `KAFKA_BROKERS` in the shared `ConfigMap` points `consumer`'s and
  `race-service`'s producer/consumer at whatever Service name the chosen
  chart exposes (confirm the exact bootstrap-service DNS name at `start`
  — it's chart-specific).

## Resource requests/limits

Every manifest in this spec gets an explicit `resources.requests`/
`limits` block, deliberately small — this is a laptop-hosted `kind`
cluster. Postgres and Kafka are the two most likely to need a real
memory limit to avoid starving Redis/NATS on a constrained machine.

## Verification

- `kind create cluster --name aviron` (fresh), apply every manifest in
  this spec in dependency order (`namespace` → `configmap`/`secret` →
  `postgres` → `redis` → `nats` → `kafka`), confirm every pod reaches
  `Running`/`Ready` via `kubectl get pods -n aviron -w`.
- `kubectl exec`/`kubectl port-forward` to confirm each dependency
  actually works, not just that its pod exists: Postgres is reachable and
  migrated (nothing to check yet — no binary has run `Migrate` until
  `k8s-race-service-deploy.md` deploys `race-service`, so defer the "is it
  actually migrated" check to that spec's own verification), Redis
  responds to `PING`, NATS's monitoring endpoint answers, Kafka's broker
  is listable via the chosen chart's own CLI tooling.

## Notes

- This spec produces zero manifests for this project's own binaries —
  kept separate so a Postgres/Redis/Kafka/NATS chart misconfiguration is
  never tangled up with debugging this project's own
  Deployment/StatefulSet/probe/graceful-shutdown logic in the same
  troubleshooting session, same separation the original, deleted
  `k8s-core-infra.md` already established.
- If Strimzi's chart turns out too heavy for a reasonable local `kind`
  cluster (a real risk the original plan already flagged — Kafka's
  operator-managed setups are not lightweight even single-broker),
  document that finding here and consider scoping Kafka out of the
  Kubernetes phase specifically, keeping Phase 4's `event-pipeline/`
  verified only against `docker-compose` — a legitimate judgment call to
  make once the real resource cost is observed, not a failure if it
  happens.
