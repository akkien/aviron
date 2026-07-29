# Current Feature: Phase 5 — Kubernetes Core Infrastructure

## Status

In Progress

## Goals

- A fresh `kind` cluster (`kind create cluster --name aviron`) reaches a
  state where `namespace`, `configmap`/`secret`, Postgres, Redis, NATS,
  and Kafka are all applied and every pod is `Running`/`Ready` under
  `kubectl get pods -n aviron`.
- `backend/Dockerfile`'s existing image (`aviron-backend:local`, already
  built by `docker-compose.yml` today) loads cleanly into the cluster via
  `kind load docker-image`.
- Each dependency is confirmed actually reachable, not just "pod exists":
  Postgres via `kubectl exec`, Redis responds to `PING`, NATS's `:8222`
  monitoring endpoint answers, Kafka's broker is listable via the chosen
  chart's own CLI.
- Zero manifests produced for this project's own binaries
  (`race-service`/`ws-gateway`/`consumer`) — deliberately out of scope for
  this spec, so a chart misconfiguration here is never tangled up with
  debugging this project's own Deployment/probe logic later.

## Explain

- This is spec 1 of 6 in `context/features/phase5/phase-5-plan.md`'s
  build order — the first spec in Phase 5 (Kubernetes for local
  development), with no dependency on anything else in this phase; every
  later spec (`graceful-shutdown.md`,
  `k8s-race-service-deploy.md`, `k8s-ws-gateway-deploy.md`,
  `k8s-consumer-deploy.md`, `multi-instance-k8s-verification.md`) depends
  on this one.
- No separate "dockerize" step is needed: `backend/Dockerfile` already
  exists and already builds all three binaries (`server`, `ws-gateway`,
  `consumer`) into one shared `alpine`-based image — this spec's only job
  regarding it is confirming it loads into `kind`.
- Layout: `deploy/k8s/{namespace,configmap,secret}.yaml` plus
  `postgres/`, `redis/`, `nats/`, `kafka/` subfolders. `race-service/`,
  `ws-gateway/`, `consumer/` are reserved names for later specs, not built
  here.
- Postgres: `StatefulSet` (single replica) + `PersistentVolumeClaim` +
  headless `Service`, `postgres:18-alpine` — matches `docker-compose.yml`
  exactly. Migrations still run automatically via `cmd/server`'s existing
  `db.Migrate` call at startup — no separate `Job` needed.
- Redis: plain `Deployment`, single replica, no PVC — room-registry data
  is self-healing via TTL, so losing it on restart is an accepted,
  bounded-impact event, not real data loss.
- NATS: genuinely new relative to the original (deleted) Phase 5 docs,
  which predate the WS Gateway pivot. Must use `nats:2-alpine`, not
  `nats:latest` — the latter is distroless-style with no shell, which
  already broke `docker-compose.yml`'s own healthcheck and
  `load/multi-instance-check.md`'s NATS readiness wait the same way once
  before.
- Kafka: via the **Bitnami Helm chart** in KRaft mode (no ZooKeeper) —
  decided over Strimzi's operator model, since a plain Helm release is
  lighter to run and reason about on a laptop-hosted `kind` cluster than
  Strimzi's fuller operator lifecycle, which this project has no use for.
  Single combined broker+controller node, `persistence.enabled: false` —
  matching `docker-compose.yml`'s own single-node, no-ZooKeeper,
  no-persistent-volume `KAFKA_PROCESS_ROLES: broker,controller` setup.

## Plan

1. `docker build -t aviron-backend:local ./backend` then
   `kind load docker-image aviron-backend:local --name aviron`.
2. Apply `namespace.yaml` (a single `aviron` namespace; every later
   manifest targets it explicitly).
3. Apply `configmap.yaml`/`secret.yaml` — `ConfigMap` for shared,
   non-sensitive config (`CORS_ALLOWED_ORIGIN`, `PPROF_ENABLED`,
   `KAFKA_BROKERS`, `REDIS_URL`, `NATS_URL`, named to match
   `internal/config.Config`'s/`internal/wsgateway.Config`'s existing
   `getEnv` keys exactly); `Secret` for `JWT_SECRET` and Postgres
   credentials. Deliberately **excludes** per-pod values
   (`INSTANCE_ID`, `RACE_SERVICE_INSTANCES`, `PORT`,
   `WS_GATEWAY_LISTEN_ADDR`) — those belong to the Deployment/StatefulSet
   specs that actually need pod-specific values.
4. Apply `postgres/` (`statefulset.yaml`, `service.yaml`, `pvc.yaml`).
5. Apply `redis/` (`deployment.yaml`, `service.yaml`).
6. Apply `nats/` (`deployment.yaml`, `service.yaml`), readiness probe as a
   native `httpGet` against `:8222/healthz` — strictly better than
   `docker-compose.yml`'s own `wget`-inside-`exec` healthcheck, since a
   real `httpGet` probe doesn't depend on a shell existing in the image.
7. Apply `kafka/` via the **Bitnami Helm chart** (KRaft mode, single
   combined broker+controller node, `persistence.enabled: false`,
   minimal resource requests). Confirm the exact bootstrap Service DNS
   name it exposes at implementation time — versioned by the chart, not
   knowable in advance — and point `KAFKA_BROKERS` at it.
8. Resource requests/limits on every manifest, deliberately small — this
   is a laptop-hosted `kind` cluster, not a production footprint.

**Divergence from `project-overview.md` §7, called out deliberately, not
silently:** §7's own suggested manifest layout has no `nats/` folder —
NATS postdates that section, added only once Phase 4's WS Gateway pivot
(`context/features/phase4/phase-4-plan.md`) replaced Redis pub/sub with a
dedicated NATS message bus for room-scoped realtime traffic. This spec
adds `nats/` as a genuine, necessary addition to §7's layout rather than
treating it as optional.

**Real risk to watch, not pre-solved:** even the Bitnami chart's
single-broker Kafka setup may be too heavy for a reasonable local `kind`
cluster — Kafka generally isn't a lightweight footprint, chart-managed or
not. If so, the accepted fallback (per
`k8s-core-infra.md`'s own Notes) is scoping Kafka out of the Kubernetes
phase specifically and keeping Phase 4's `event-pipeline/` verified only
against `docker-compose` — a legitimate judgment call to make once the
real resource cost is observed at `start`, not something to force through
if it clearly doesn't fit.

## Notes

- Full spec: `context/features/phase5/k8s-core-infra.md`. Phase roadmap:
  `context/features/phase5/phase-5-plan.md`.
- Migration-concurrency question (two `race-service` StatefulSet pods
  both calling `db.Migrate` on a cold cluster, once
  `k8s-race-service-deploy.md` exists) is already resolved by
  `golang-migrate`'s own documented advisory-lock behavior during `Up()`
  — no code change needed in `internal/db/migrate.go`.
- This spec's own "done" bar is "the dependencies work," not just "the
  pods exist" — a `CrashLoopBackOff`-free pod can still be misconfigured
  in a way that only shows up once something actually tries to use it, so
  the `kubectl exec`/`port-forward` checks in Goals above are not
  optional busywork.
