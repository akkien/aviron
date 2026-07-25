# Phase 5 — Kubernetes for Local Development

## Overview

Per `context/project-overview.md` §7: move the whole stack (Postgres,
Redis, Kafka, the Race Service, and — new since this project has grown an
extra binary — the Kafka consumer) onto local Kubernetes (`kind` or
`minikube`), to genuinely practice the "exposure to... Kubernetes" line in
the JD, not to run a production cluster.

**Split out of `phase4/` on purpose.** Everything in `context/features/
phase4/` is application logic — new code paths inside this codebase
(`internal/roomlocator`, `internal/roomrelay`, `internal/kafka`,
`internal/consumer`). Everything in this phase is deployment
orchestration — manifests, Dockerfiles, probes — that changes *how*
already-built code runs, not what it does. `phase-4-plan.md`'s own "A note
on scope discipline" already separated these once implicitly (Kubernetes
was sequenced last, "least design risk... most value in being done
last"); this split makes that separation explicit rather than leaving
deployment concerns bundled into the same plan as the Redis/Kafka design
work.

## Hard dependency on Phase 4's `horizontal-scaling/`

This phase cannot start meaningfully before `phase4/horizontal-scaling/`
(`redis-room-registry.md`, `cross-instance-relay.md`,
`multi-instance-dev-setup.md`) is done and its own verification script
passes. `k8s-race-service-deploy.md`'s `replicas: 2` is the exact same
scenario `multi-instance-dev-setup.md` already proved by hand with two
`go run` processes — running it in Kubernetes before that manual proof
exists would mean debugging a possible Redis pub/sub relay bug and
possible Kubernetes networking/DNS issues at the same time, unable to tell
which layer a failure came from. `project-overview.md` §7 warns against
exactly this ("doing them earlier tends to turn into infrastructure
overhead rather than Go practice").

`phase4/event-pipeline/` (Kafka) is not a hard dependency the same way —
this phase can containerize and deploy `cmd/server`/`cmd/consumer`
whichever of the two actually exist by the time it starts, per
`dockerize.md`'s own "confirm both exist at `start`" note. There is no
`cmd/analytics` to account for either way — Phase 4 dropped the internal
gRPC service entirely (see `phase4/phase-4-plan.md`'s "Explicitly out of
scope").

## Specs, in build order

1. `dockerize.md` — a `Dockerfile` for the backend binaries. Confirmed by
   `find` across the whole repo before this plan was written: **no
   Dockerfile exists anywhere in this project today.** Every prior phase
   ran the backend via `go run`/`make start`; Kubernetes cannot start a
   single pod without an image to load, so this is a real prerequisite,
   not busywork.
2. `k8s-core-infra.md` — namespace, ConfigMap/Secret, Postgres
   (StatefulSet+PVC or the Bitnami chart), Redis (Deployment+Service),
   Kafka (Strimzi or Bitnami chart) — standing up the dependencies
   race-service needs. Runs none of this project's own code yet.
3. `k8s-race-service-deploy.md` — the race-service Deployment itself:
   `replicas: 2` (the actual point of this whole phase), readiness probe
   separate from liveness, graceful `SIGTERM` shutdown (a real, currently-
   missing gap in `internal/app.go`, not just a manifest concern — see
   that spec's Overview), resource limits, an Ingress. Also where
   `consumer` gets its own (simpler) Deployment if it exists.

## Dependency order

```text
(phase4/horizontal-scaling/ proven first — hard dependency, see above)
        |
        v
    dockerize
        |
        v
  k8s-core-infra
        |
        v
k8s-race-service-deploy   (replicas: 2 — the real proof horizontal-scaling works)
```

Strictly linear — each spec in this phase depends on the one before it,
unlike `phase4/`'s three parallel-ish sub-areas.

## Explicitly out of scope

- **A separate `api-gateway` binary/deployment.** `project-overview.md`'s
  architecture diagram and §7's suggested manifest layout both mention
  one, but this project never built a separate gateway service —
  `cmd/server` has always served REST and WebSocket directly (confirmed
  by reading `cmd/server/main.go` and this project's entire feature
  history: no gateway feature ever shipped). `k8s-race-service-deploy.md`
  flags this drift explicitly and drops the `api-gateway/` manifest
  folder from §7's suggested layout rather than building a service that
  doesn't exist to satisfy a doc's suggested directory tree.
- **CI/CD for the Kubernetes deploy.** §7 itself says this isn't needed
  for a side project — `kind load docker-image` is enough.
- **Helm chart packaging of this project's own manifests.** §7 offers it
  as an alternative to a plain `deploy/k8s/` tree, not a requirement;
  plain manifests are simpler to review spec-by-spec across 3 specs and
  are what this plan assumes throughout. Revisit only if the
  plain-manifest tree becomes unwieldy in practice.
- **ClickHouse's Kubernetes manifest** — moot, `phase4/` already dropped
  ClickHouse from this project entirely; nothing here stands one up.
- **Fixing whatever `k8s-race-service-deploy.md`'s real `replicas: 2` run
  surfaces about `phase4/horizontal-scaling/`'s correctness.** Same
  convention Phase 3's plan established for `k6-load-test.md`'s findings:
  don't pre-write that spec, scope it to whatever a real run actually
  shows — and per that spec's own Notes, treat a failure here as a signal
  to revisit `cross-instance-relay.md`'s design in `phase4/`, not just
  this phase's manifests.
