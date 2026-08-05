# Phase 5 — Kubernetes for Local Development

## Overview

Per `context/project-overview.md` §7: move the whole stack (Postgres,
Redis, Kafka, NATS, `race-service`, `ws-gateway`, and the Kafka
`consumer`) onto local Kubernetes (`kind` or `minikube`), to genuinely
practice the "exposure to... Kubernetes" line in the JD — not to run a
production cluster.

**This is a full recreation, not an edit.** The original `context/
features/phase5/` (`phase-5-plan.md`, `dockerize.md`, `k8s-core-infra.md`,
`k8s-race-service-deploy.md`) was deleted in the same commit that
redesigned Phase 4 around a WS Gateway + NATS message bus
(`phase4/phase-4-plan.md`'s "Explicitly out of scope" section explains
why: recreating Phase 5 before `ws-gateway` existed would have meant
speccing Kubernetes manifests for a service that hadn't been designed
yet). That WS Gateway work is now done — confirmed by reading the
codebase directly: `cmd/ws-gateway`, `internal/wsgateway`,
`internal/roomrelay` (NATS), `internal/roomlocator` (Redis) all exist,
and `context/feature-history.md`'s "Multi-Instance Dev Setup &
Verification" entry (2026-07-27) records `load/multi-instance-check.sh`
passing against two `ws-gateway` processes and two `race-service`
processes, each test race's two participants deliberately connected
through *different* gateways. The hard dependency this phase always had
on Phase 4's horizontal-scaling design being proven first (per the
original, deleted `phase-5-plan.md`) is satisfied — this phase's job is
to prove the same cross-instance consistency again, orchestrated by
`kind` instead of two hand-run `go run` processes, exactly what §7 says
is the actual point of doing this at all.

## What's different from the original (deleted) Phase 5 docs

| Piece | Status | Notes |
| --- | --- | --- |
| `dockerize.md` | **Superseded — already shipped** | `backend/Dockerfile` exists today: one multi-stage `golang:1.26-alpine` → `alpine:latest` image building all three binaries (`server`, `ws-gateway`, `consumer`) and copying `migrations/`. Not the build-arg-parameterized, distroless design the deleted spec sketched, but functionally equivalent and already proven — every service in `docker-compose.yml` builds from it. No new numbered spec for this; `k8s-core-infra.md` below only needs to confirm it `kind load docker-image`s cleanly and note whether the single shared image is worth splitting later. |
| "No `api-gateway/` folder" | **Reversed** | The original Phase 5 docs (and `project-overview.md` §7 itself, not yet updated) assumed `race-service` was the only backend and `Ingress` routed straight to it. `ws-gateway` now exists specifically to terminate both REST and WebSocket traffic — the same reversal `phase4/phase-4-plan.md` already flagged for §2/§8, extended here to §7's manifest layout and its "`Ingress` routes straight to its `Service`" line. |
| `k8s-core-infra.md` | **Revised** | Gains a `nats/` manifest folder (new infrastructure since the original plan). Postgres, Redis, Kafka sections carry over unchanged in design. |
| `k8s-race-service-deploy.md` | **Split into two** | The original bundled `race-service` and `consumer` into one spec because `race-service` was the only pool-scaled binary. There are now two internet-facing, independently-scaled binaries (`race-service`, `ws-gateway`) with genuinely different Kubernetes shapes — see the StatefulSet-vs-Deployment gap below — so each gets its own spec. |

## Three real, disclosed gaps this phase must close — not just write manifests around

**1. No graceful shutdown exists anywhere.** Confirmed by reading all
three `cmd/*/run.go` files directly: none call `signal.NotifyContext` or
`http.Server.Shutdown`; `cmd/ws-gateway/run.go` even says so explicitly
in its own comment ("No SIGTERM/graceful-shutdown handling, consistent
with every other binary in this codebase"). Kubernetes sends `SIGTERM` on
every pod termination — rolling update, scale-down, eviction — followed
by `SIGKILL` after a default 30s `terminationGracePeriodSeconds`; without
handling it, an in-progress WebSocket connection or a mid-batch Kafka
consumer read gets hard-killed. `project-overview.md` §7 names this
outcome directly ("without cutting off the WebSocket of someone
mid-race"). This is Go application code, not YAML — sequenced as its own
spec (`graceful-shutdown.md`, below) before any Deployment/StatefulSet
spec, so `terminationGracePeriodSeconds` has real behavior underneath it
to configure against, not a number chosen in a vacuum.

**2. `ws-gateway`'s backend list is static and load-once — the wrong
shape for Kubernetes' dynamic pod addresses.** `internal/wsgateway/
config.go`'s `RACE_SERVICE_INSTANCES` is a fixed, comma-separated
`host:port` list, fine for `docker-compose.yml`'s permanent `server-a`/
`server-b` container names but wrong once `race-service` pods have IPs
that change on every reschedule. This isn't a new discovery — it's
earmarked twice already in this codebase's own history:
`ws-gateway.md`'s Notes flag "the same headless-Service question
resurfaces once Kubernetes work resumes," and `context/
feature-history.md`'s multi-instance writeup records the question being
asked and deliberately deferred ("DNS-based discovery against a
Kubernetes headless Service is the already-earmarked Phase 5 answer").
This phase pays that off: `race-service` becomes a **StatefulSet**, not a
Deployment, fronted by a headless `Service`, giving each pod a stable DNS
name (`race-service-0.race-service.aviron.svc.cluster.local`,
`race-service-1...`). Replica count stays small and fixed (2, matching
`docker-compose.yml` today), so a static list of those stable names is
still consistent with `Config`'s own existing comment ("no dynamic
service discovery at this project's scale") — this closes the gap
without abandoning that stance or building real watch-based discovery.

**3. The existing `/healthz` endpoints are dependency checks, deliberately
shared between what Kubernetes wants as two separate probes.**
`internal/wsgateway/healthz.go`'s own comment is explicit about this
being intentional ("No readiness/liveness split: this process holds no
background state whose liveness could plausibly diverge from its
readiness") — correct for a single combined health signal, but wrong to
wire unmodified into both `readinessProbe` and `livenessProbe`: a
transient Redis or NATS blip would then make `kubelet` kill and restart
an otherwise-healthy pod via the liveness probe, instead of just pulling
it out of Service rotation via readiness — turning a brief dependency
hiccup into a cascading pod-restart storm. `project-overview.md` §7 asks
for the split explicitly. This is a real design decision, not a rename:
resolved in `graceful-shutdown.md` below since it touches the same
handler code, ahead of the manifests that reference `httpGet` probes
against it.

## Specs, in build order

1. `k8s-core-infra.md` — namespace, `ConfigMap`/`Secret`, Postgres
   (StatefulSet + PVC), Redis (Deployment), Kafka (Bitnami Helm chart,
   KRaft mode), NATS (new vs. the original plan). Runs none of this
   project's own binaries yet. Confirms `backend/Dockerfile`'s existing image loads
   cleanly via `kind load docker-image`.
2. `graceful-shutdown.md` — `SIGTERM` handling across `cmd/server`,
   `cmd/ws-gateway`, `cmd/consumer`: `http.Server.Shutdown`, the
   room-actor-draining product decision (let in-flight races finish vs.
   cancel them immediately on shutdown — flagged but left unresolved by
   the original, deleted `k8s-race-service-deploy.md`; must be decided
   here, not silently), `ws-gateway`'s own local-connection draining
   (mirroring what `internal/ws/hub.go` already does when a room ends),
   the consumer's reader-loop draining, and the readiness/liveness probe
   split from gap 3 above. Pure Go application code, no manifests —
   sequenced before every Deployment/StatefulSet spec so
   `terminationGracePeriodSeconds` and `readinessProbe`/`livenessProbe`
   configure something real.
3. `k8s-race-service-deploy.md` — `race-service` as a **StatefulSet**,
   `replicas: 2`, headless `Service` for stable per-pod DNS (closing gap
   2), readiness/liveness probes wired to spec 2's split, resource
   limits, `INSTANCE_ID` sourced from the downward API (pod name) instead
   of the `crypto/rand` fallback `internal/config.getEnvInstanceID` uses
   today.
4. `k8s-ws-gateway-deploy.md` — `ws-gateway` as a plain **Deployment**
   (stateless, no need for stable identity), `replicas: 2`,
   `RACE_SERVICE_INSTANCES` populated with `race-service`'s stable
   StatefulSet pod DNS names from spec 3, `Ingress` terminates here — the
   concrete reversal of §7's original "no `api-gateway/` folder" framing,
   since this binary now does exactly that job for both REST and
   WebSocket traffic.
5. `k8s-consumer-deploy.md` — small Deployment, `replicas: 1`, no
   `Service` (the consumer is never called into, only reads Kafka and
   writes Postgres).
6. `multi-instance-k8s-verification.md` — the real acceptance test: apply
   everything above to a fresh `kind` cluster, rerun `load/
   multi-instance-check.md`'s cross-gateway scenario against the cluster
   (through the `Ingress`, or via `kubectl port-forward` to each
   `ws-gateway` pod) instead of `docker-compose`, plus a rolling update
   (`kubectl rollout restart`) performed mid-race to prove spec 2's
   graceful-shutdown decision actually holds under real Kubernetes pod
   churn, not just in a unit test.

## Dependency order

```text
(Phase 4 — done, verified 2026-07-27: cmd/ws-gateway, internal/roomrelay,
 internal/roomlocator, load/multi-instance-check.sh all passing on two
 plain processes)
        |
        v
  k8s-core-infra          (namespace, Postgres, Redis, Kafka, NATS — no app code yet)
        |
        v
  graceful-shutdown        (Go changes only, no manifests — all three cmd/* binaries)
        |
        v
  k8s-race-service-deploy  (StatefulSet + headless Service — the stable identity ws-gateway needs)
        |
        v
  k8s-ws-gateway-deploy    (Deployment + Ingress — needs race-service's stable DNS names to exist)
        |
        v
  k8s-consumer-deploy      (independent of the gateway chain — could build in parallel with 3/4,
                            sequenced last only for scope reasons)
        |
        v
  multi-instance-k8s-verification   (the real proof — everything above, together, under kubectl)
```

Mostly linear, same as the original plan's own "strictly linear" note.
`k8s-consumer-deploy.md` is the one real exception — it has no dependency
on `race-service`'s StatefulSet conversion or `ws-gateway`'s Deployment,
and could be built any time after `k8s-core-infra.md`.

## A note this plan can't act on: `project-overview.md` §7 is now stale

Same situation `phase4/phase-4-plan.md` disclosed for §2/§8, one level
down: §7's suggested manifest layout has no `ws-gateway/` folder and its
own text says "no `Ingress` — routes straight to `race-service`'s
`Service`." Both are now wrong per this plan's reversal above.
`project-overview.md` is this project's own top-level source of truth,
not a `context/features/` spec, so editing it is out of this plan's
scope — noted here so it isn't lost, the same way `phase4/phase-4-plan.md`
left its own §2/§8 correction as a pending follow-up.

## Explicitly out of scope

- **HPA.** §7 offers it as optional ("strong plus"). **No longer open —
  see `k8s-hpa.md`**: `aviron_connections_active` doesn't actually exist
  (removed from `race-service` when connections moved to `ws-gateway`,
  never rebuilt there — that spec confirms it by reading
  `internal/metrics/metrics.go` directly), so it targets CPU utilization
  via the standard Kubernetes metrics-server instead, and only against
  `ws-gateway` — `race-service` turns out to be unsafe to autoscale under
  today's static `RACE_SERVICE_INSTANCES` discovery, a real constraint
  that spec found and left as its own open question.
- **Helm chart packaging.** Plain `deploy/k8s/` manifests, same call the
  original plan made — revisit only if the tree becomes unwieldy in
  practice.
- **CI/CD for the Kubernetes deploy.** §7 itself says `kind load
  docker-image` is enough for a side project.
- **ClickHouse's manifest.** Moot — dropped from this project entirely in
  Phase 4.
- **Splitting `backend/Dockerfile` into three per-binary images, or a
  distroless base.** The existing single shared `alpine` image (already
  used by every service in `docker-compose.yml` today) is reused as-is;
  revisit only if `k8s-core-infra.md`'s real `kind load` run finds a
  concrete problem with it — size, a missing CA-cert-bundle-style
  distroless gotcha — not preemptively.
- **Fixing whatever `multi-instance-k8s-verification.md`'s real run
  surfaces about Phase 4's design.** Same convention this project already
  uses for `k6-load-test.md`'s and `multi-instance-dev-setup.md`'s own
  findings: don't pre-write that spec, scope it to whatever a real run
  actually shows, and treat a failure as a signal to revisit the relevant
  Phase 4 spec's design, not just this phase's manifests.
