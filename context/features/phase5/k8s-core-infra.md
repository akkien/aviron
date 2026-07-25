# Kubernetes — Core Infrastructure

## Overview

Per `context/project-overview.md` §7's suggested layout, minus
ClickHouse and minus `api-gateway/` (this project never built a separate
gateway binary — see `phase-5-plan.md`'s "Explicitly out of scope"). This
spec stands up everything race-service depends on; it runs none of this
project's own code yet (`k8s-race-service-deploy.md` is next). Uses
**kind** (per §7's recommendation over minikube — confirm at `start` if a
preference exists, §7 lists both as acceptable).

Depends on `dockerize.md` only for the images it will eventually need
(Postgres/Redis/Kafka all use existing published images, not anything this
project builds) — this spec could technically be built in parallel with
`dockerize.md`, but sequenced after it in `phase-5-plan.md` since a kind
cluster with nothing loadable into it yet is a less satisfying place to
stop and verify.

## Requirements

### Layout

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
  kafka/
    # via an existing chart (Strimzi or Bitnami), not hand-written brokers
  race-service/       # k8s-race-service-deploy.md, not this spec
```

- No `api-gateway/` folder — see `phase-5-plan.md`'s explicitly-out-of-
  scope note for why.

### `namespace.yaml`

- A single `aviron` namespace; every other manifest in this phase targets
  it explicitly (`metadata.namespace: aviron`), not the `default`
  namespace.

### `configmap.yaml` / `secret.yaml`

- `ConfigMap` for non-sensitive config: `CORS_ALLOWED_ORIGIN`,
  `PPROF_ENABLED`, `KAFKA_BROKERS`, ports — mirrors
  `internal/config.Config`'s existing env-var names exactly, so no
  translation layer is needed between this project's existing `getEnv`
  calls and what Kubernetes injects.
- `Secret` for `JWT_SECRET`, `DATABASE_URL` (or its component parts —
  confirm at `start` whether to compose it from a templated
  `postgres://$(POSTGRES_USER):$(POSTGRES_PASSWORD)@postgres:5432/aviron`
  inside the Deployment env, or store the whole DSN as one secret value;
  the component-parts approach is more idiomatic Kubernetes and avoids
  duplicating the password into two places). Plain `Secret` (base64, not
  encrypted at rest by default) is proportionate here — this project's
  local Postgres/Redis/pgAdmin credentials are already `aviron`/`aviron`
  and `admin`/`admin`, this is a local kind cluster, not a real deployment
  target.

### Postgres

- `StatefulSet` + `PersistentVolumeClaim` + headless `Service` — §7
  explicitly allows "or use the Bitnami Helm chart" as an alternative.
  Leaning toward hand-writing a minimal `StatefulSet` rather than pulling
  in a chart, since this project already has a working
  `docker-compose.yml` Postgres service to translate 1:1 (same image,
  `postgres:18-alpine`, same env vars) — a chart adds configuration
  surface this project doesn't need. Confirm at `start`.
- Migrations: resolve the open question `dockerize.md` flagged (bake into
  `cmd/server`'s own startup, or a separate `Job`/`initContainer`) — if
  the answer is a separate step, its manifest belongs in this file
  (`postgres/migrate-job.yaml` or similar), run once against the
  StatefulSet before `k8s-race-service-deploy.md`'s Deployment is ever
  applied.

### Redis

- Plain `Deployment` (not a `StatefulSet` — Redis here is only ever used
  as a cache/pub-sub/registry, Phase 4's `redis-room-registry.md`'s
  ownership keys are already self-healing via TTL, so losing Redis's
  in-memory state on a pod restart is an accepted, bounded-impact event,
  not data loss the way losing Postgres would be) + `Service`, single
  replica. No persistence volume needed for the same reason.

### Kafka

- Per §7 and §11's explicit steer: "use an existing chart (Strimzi or
  Bitnami) rather than writing your own" / "avoid manually managing
  Zookeeper/brokers yourself." Strimzi (the Kafka-native Kubernetes
  operator, KRaft-mode-capable, no Zookeeper) is the more current choice —
  confirm at `start` against whatever's easiest to get running in a local
  kind cluster with minimal resource overhead, since this is a side
  project's laptop, not a production cluster sized for Kafka's usual
  footprint. A single-broker, minimal-resource-request configuration is
  the target either way, not a production-shaped multi-broker setup.

### Resource requests/limits

- Every manifest in this spec gets a `resources.requests`/`limits` block,
  deliberately small (this is a laptop-hosted kind cluster) — Postgres and
  Kafka are the two that most need an explicit memory limit to avoid
  either one starving the others on a constrained local machine.

## Verification

- `kind create cluster` (fresh), apply every manifest in this spec in
  dependency order (`namespace` → `configmap`/`secret` → `postgres` →
  `redis` → `kafka`), confirm every pod reaches `Running`/`Ready` via
  `kubectl get pods -n aviron -w`.
- `kubectl exec` into a throwaway pod (or `kubectl port-forward`) to
  confirm Postgres is actually reachable and migrated, Redis responds to
  `PING`, and Kafka's broker is listable via whatever CLI the chosen chart
  ships (`kafka-topics.sh --list` equivalent) — this spec's "done" bar is
  "the dependencies work," not just "the pods exist," since a
  `CrashLoopBackOff`-free pod can still be misconfigured in a way that
  only shows up once something tries to actually use it.

## Notes

- This spec produces zero manifests for this project's own binaries
  (`race-service`, `consumer`) — that's `k8s-race-service-deploy.md`,
  kept separate so a Postgres/Redis/Kafka
  chart misconfiguration is never tangled up with debugging this
  project's own Deployment/probes/graceful-shutdown logic in the same
  troubleshooting session.
- If Strimzi/Bitnami's Kafka chart turns out to be too heavy for a
  reasonable local kind cluster (a real risk — Kafka's operator-managed
  setups are not lightweight even in single-broker form), document that
  finding here and consider scoping Kafka out of the Kubernetes phase
  specifically (keep Phase 4's `event-pipeline/` verified only against
  `docker-compose`, per `kafka-producer.md`/`kafka-consumer-postgres-
  sink.md`'s own scope) rather than forcing it in — note this as a
  legitimate, expected judgment call to make once the real resource cost
  is observed, not a failure if it happens.
