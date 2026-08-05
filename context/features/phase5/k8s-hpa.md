# Kubernetes — `HorizontalPodAutoscaler`

## Overview

`project-overview.md` §7 lists `hpa.yaml` as a "strong plus" item, and
`phase-5-plan.md` explicitly scoped it out of the original five specs
pending its own follow-up: "if pursued at all, document the intended
metric... without necessarily wiring a working Prometheus Adapter/KEDA
setup." This spec is that follow-up.

Decided with the user up front: target **CPU utilization**, not
`aviron_connections_active`-style connection count. Connection count is
the metric §9 actually cares about for this workload, but it's a
materially bigger lift than it first looks — see "Why not connection
count (yet)" below — and CPU-based scaling is the standard
`autoscaling/v2` `HorizontalPodAutoscaler` behavior, needing nothing
beyond the Kubernetes metrics-server already expected on a real cluster.
This keeps the spec's own stopping point honest: practice the K8s
mechanism (`HorizontalPodAutoscaler`, metrics-server, `kubectl top`),
not build a production autoscaling signal for a laptop demo.

**Revised — both `ws-gateway` and `race-service` are now real HPA
targets.** This spec originally scoped `race-service` out, below. That
constraint is resolved — see "`race-service` autoscaling: resolved" —
and this revision adds its `hpa.yaml` alongside `ws-gateway`'s.

## `race-service` autoscaling: resolved

**Originally documented here as blocked; no longer true.**
`k8s-race-service-deploy.md` fixed `race-service` at `replicas: 2`
specifically because `ws-gateway`'s own `RACE_SERVICE_INSTANCES` env var
was a **static**, comma-separated list of the two StatefulSet pod DNS
names, read once at `ws-gateway` startup. An `HorizontalPodAutoscaler`
scaling `race-service` up would have spawned `race-service-2`,
`race-service-3`, ... that `ws-gateway` never learned about — any room
`Claim`ed on one of those pods would be unreachable from every gateway,
a silent routing dead end, not a loud failure. Scaling down was worse:
`race-service-1` could be terminated out from under a room `ws-gateway`
was still actively routing to.

**`dynamic-backend-discovery.md` (Phase 4 `horizontal-scaling/`,
shipped 2026-08-05) closes exactly this gap.** `ws-gateway` now watches
`EndpointSlice` objects for the `race-service` Service via a `client-go`
`SharedIndexInformer` instead of reading a static list — the live,
`Ready`-filtered pod pool updates automatically as `race-service` scales
either direction, with no `ws-gateway` restart or manifest edit needed.
Two things worth being precise about, both already true before this
spec, not new guarantees this spec had to add:

- **Room-*scoped*** routing (`/races/{id}/...`) was never actually
  blocked by the static-list problem in the first place — it resolves
  live via `roomlocator.Owner` (Redis), which already reflects whatever
  `INSTANCE_ID` a newly-scaled pod registers for itself, regardless of
  replica count. `dynamic-backend-discovery.md`'s own Overview found
  this while scoping that spec — the original constraint description
  above (still left intact for the historical record) overstated the
  blast radius.
- **A scale-down is graceful, not abrupt**, for the same reason a
  rolling update already is: `race-service`'s `terminationGracePeriodSeconds:
  150` and `cmd/server`'s `waitForRoomsToDrain` (`graceful-shutdown.md`)
  let any room a to-be-removed pod still owns finish naturally before
  the pod actually terminates — a `HorizontalPodAutoscaler`-triggered
  scale-down uses the exact same Pod deletion mechanism `kubectl delete
  pod` does, so this protection applies unconditionally, not only to
  manually-triggered changes.

`race-service` is now this spec's second real HPA target, alongside
`ws-gateway`.

## `hpa.yaml` — `ws-gateway`

```yaml
# deploy/k8s/ws-gateway/hpa.yaml
apiVersion: autoscaling/v2
kind: HorizontalPodAutoscaler
metadata:
  name: ws-gateway
  namespace: aviron
spec:
  scaleTargetRef:
    apiVersion: apps/v1
    kind: Deployment
    name: ws-gateway
  minReplicas: 2
  maxReplicas: 5
  metrics:
    - type: Resource
      resource:
        name: cpu
        target:
          type: Utilization
          averageUtilization: 70
```

- `minReplicas: 2` matches the existing `deployment.yaml` replica count —
  the HPA takes over managing `spec.replicas` once applied, so this
  floor is what keeps the two-gateway topology
  `multi-instance-k8s-verification.md` already verified as a permanent
  baseline, not a transient state the HPA could scale below.
- `maxReplicas: 5` is an arbitrary, generous ceiling for a laptop `kind`
  cluster — there's no real traffic volume in this project that would
  ever approach it; it exists so the HPA has genuine headroom to
  demonstrate scaling up during verification.
- `averageUtilization: 70` against `ws-gateway/deployment.yaml`'s
  existing `resources.requests.cpu: 100m` — a scale-up trigger at
  ~70m/pod average. `Resource`/`cpu` HPA metrics require `requests.cpu`
  to be set on the target's containers, which `deployment.yaml` already
  has; no manifest change needed there.

## `hpa.yaml` — `race-service`

Same shape, targeting the `StatefulSet` instead of the `Deployment` —
`autoscaling/v2` scales anything exposing a `/scale` subresource, which
both kinds provide:

```yaml
# deploy/k8s/race-service/hpa.yaml
apiVersion: autoscaling/v2
kind: HorizontalPodAutoscaler
metadata:
  name: race-service
  namespace: aviron
spec:
  scaleTargetRef:
    apiVersion: apps/v1
    kind: StatefulSet
    name: race-service
  minReplicas: 2
  maxReplicas: 5
  metrics:
    - type: Resource
      resource:
        name: cpu
        target:
          type: Utilization
          averageUtilization: 70
```

- `minReplicas: 2` / `maxReplicas: 5` / `averageUtilization: 70` mirror
  `ws-gateway`'s own values exactly, for the same two reasons: `2` keeps
  `multi-instance-k8s-verification.md`'s already-proven baseline as a
  floor, not something the HPA could scale below, and `70` is measured
  against `race-service/statefulset.yaml`'s own `resources.requests.cpu:
  100m`, which is already set — no manifest change needed there either.
- A `StatefulSet` scaling up adds `race-service-2`, `race-service-3`,
  ... in ordinal order; scaling down removes the highest ordinal first
  (`race-service-N-1`, then `race-service-N-2`, ...) — standard
  `StatefulSet` behavior, unrelated to anything this spec adds. Each new
  pod gets a real, dialable `INSTANCE_ID` automatically (the existing
  downward-API + DNS-suffix composition in `statefulset.yaml`), and
  `ws-gateway`'s `EndpointSlice` watch (`dynamic-backend-discovery.md`)
  picks it up without any code or config change on either side.

## Prerequisite: `metrics-server` on `kind`

Confirmed by checking `deploy/kind-config.yaml` and every existing
`context/features/phase5/*.md`: **no metrics-server is installed
anywhere in this project's cluster setup today.** A CPU-based HPA has a
hard runtime dependency on it (`kubectl top`/the `metrics.k8s.io` API
group) that doesn't exist without it — this is new to this spec, not an
oversight in an earlier one.

`kind` needs one extra flag past the stock metrics-server manifest,
since kubelet's serving certs on a `kind` node aren't signed by a CA the
metrics-server trusts by default:

```sh
kubectl apply -f https://github.com/kubernetes-sigs/metrics-server/releases/latest/download/components.yaml
kubectl patch deployment metrics-server -n kube-system --type='json' \
  -p='[{"op":"add","path":"/spec/template/spec/containers/0/args/-","value":"--kubelet-insecure-tls"}]'
```

`--kubelet-insecure-tls` is an accepted local-dev-only stance — never
appropriate on a real cluster, fine here for the same reason this whole
phase already runs Kafka/Postgres without TLS internally.

## Why not connection count (yet)

`project-overview.md` §9 lists "connection count" as one of this
project's core Prometheus metrics, and `phase-5-plan.md`'s own
"Explicitly out of scope" note assumed a metric like
`aviron_connections_active` already existed to point an HPA at. It
doesn't. Confirmed by reading `internal/metrics/metrics.go`'s own
comment directly: a connection-count gauge existed once, wired against
`internal/ws.WSHandler`, and was **removed** when
`room-service-adapter.md` moved connection-holding to
`internal/wsgateway` — its own comment says the gauge's rebuild is
`ws-gateway.md`'s job, which never happened; `ws-gateway`'s package has
no Prometheus wiring of any kind today (confirmed: no `prometheus`
import anywhere under `internal/wsgateway` or `cmd/ws-gateway`).

Getting from here to a working connection-count-based HPA is three
separate, real pieces of work, not one:

1. Build the gauge itself in `internal/wsgateway` (straightforward —
   the removed one already establishes the pattern).
2. Stand up Prometheus itself in the cluster to scrape it — this phase
   never deployed one; `GET /metrics` exists but nothing currently
   polls it in-cluster.
3. Bridge that custom metric into the `autoscaling/v2` API the HPA
   controller reads from, via either the Prometheus Adapter or KEDA —
   neither is installed, and both are meaningfully heavier than
   metrics-server.

CPU utilization needs none of the above and demonstrates the same
Kubernetes mechanism end to end. Revisit connection-count scaling as its
own future spec if this project's scope grows to actually need it.

## What about Redis, Postgres, Kafka?

Deliberately not autoscaled — and not because it's out of scope to
think about, but because a plain `HorizontalPodAutoscaler` is the wrong
mechanism for any of them. This is also how real systems actually treat
this split, not a simplification unique to this project: **stateless
tiers autoscale via replica count; stateful data infrastructure scales
by a different mechanism entirely**, because a data-owning pod can't be
freely created or destroyed the way a `race-service`/`ws-gateway` pod
can — each one owns something (a shard, a partition, a role) that a
fresh replica doesn't automatically inherit.

| Component | Why plain HPA doesn't fit | What real systems use instead |
| --- | --- | --- |
| Postgres | A second identical pod isn't a second primary — it can't take writes without a distributed-SQL layer (Citus, CockroachDB, Vitess) this project doesn't run. `race-service`'s own writes need exactly one primary. | Vertical scaling (bigger instance) for writes; read replicas fanned out for read-heavy workloads; often a managed service (RDS/Cloud SQL) handling both operationally |
| Redis | This project runs one instance already, a disclosed simplification (`docs/knowledge-summary.md`'s "Horizontally Scaling" section). Real Redis scaling is sharding (Redis Cluster, hash-slot-owning nodes) or HA failover (Sentinel) — neither is "add an interchangeable pod," each node owns a distinct slot range or replica role | A dedicated cluster/sentinel operator managing resharding and failover as its own concern, not tied to a CPU threshold |
| Kafka **brokers** | Each broker owns specific partition replicas; adding/removing one triggers partition reassignment — a heavyweight, usually operator-orchestrated rebalance, not something a CPU spike should trigger automatically | Deliberate capacity planning, or an operator with rebalancing built in (Strimzi + Cruise Control) |

**One real exception, worth naming since it's the legitimate version of
what this section is about**: Kafka *consumers* — `cmd/consumer` in this
project — scale safely and horizontally, because Kafka's own
partition-assignment protocol already handles rebalancing consumer-group
members automatically. This is a genuinely standard real-world pattern
(KEDA has a first-class Kafka scaler built specifically for it),
typically triggered on **consumer lag**, not CPU. `cmd/consumer` stays
at `replicas: 1` in this project today (`k8s-consumer-deploy.md`'s own
reasoning: "no horizontal-scaling story needed here... nothing about
this phase requires exercising that") — scaling it for real would need
a lag metric this project doesn't currently expose, the same category of
gap `k8s-hpa.md`'s "Why not connection count" section already describes
for `ws-gateway`. Left as a genuine future spec, not built here.

## Verification

- `kubectl apply -f deploy/k8s/ws-gateway/hpa.yaml -f
  deploy/k8s/race-service/hpa.yaml` shows `TARGETS` reporting a real
  percentage (not `<unknown>`) for both, within a few scrape intervals
  of `metrics-server` being ready — `<unknown>` staying stuck means
  `metrics-server` itself isn't serving yet, not an HPA misconfiguration.
- Generate load (`load/`'s existing k6 scenarios, pointed at the
  in-cluster `Ingress`) at a volume high enough to push CPU past the 70%
  target on both; `kubectl get hpa -n aviron -w` shows `REPLICAS` climb
  above 2 for both `ws-gateway` and `race-service`, and `kubectl get
  pods -n aviron` shows the new pods (`ws-gateway-*`, `race-service-2`,
  ...) reach `Running`/`Ready`.
- **`race-service`-specific**: while replicas are elevated, create a new
  race and confirm it can be `Claim`ed on a newly-scaled pod
  (`redis-cli GET room:<id>` shows an instance beyond the original two)
  — proof the new pod is genuinely receiving traffic, not just idling.
- Stop the load and confirm `REPLICAS` scales back down to `minReplicas:
  2` for both, after the default stabilization window (5 minutes) — slow
  by design (`autoscaling/v2`'s own default
  `scaleDown.stabilizationWindowSeconds`), not a bug in either manifest.
- A scale-down event during a k6 run must not fail any in-progress race
  on **either** target: for `ws-gateway`, the existing rolling-update
  assertion (clean disconnect within the flush window, not a hang); for
  `race-service`, confirm a room already `Claim`ed on the pod being
  scaled down finishes naturally before that pod terminates (the same
  assertion `multi-instance-k8s-verification.md` already established for
  a `race-service` rolling update — a scale-down uses the identical Pod
  deletion path).

## Notes

- No Helm chart, no `VerticalPodAutoscaler` — same "plain manifests,
  don't over-build infra for a side project" stance this whole phase has
  already taken.
- **A real, disclosed risk worth naming now that `race-service` can
  actually scale up in practice**: each additional pod opens its own
  Postgres connection pool against the same single, non-pooled Postgres
  instance this project already runs (`docs/knowledge-summary.md`'s
  disclosed single-instance simplification). `maxReplicas: 5` was not
  chosen against a measured `max_connections` budget — if this is ever
  pushed past a demo/verification run, checking Postgres's actual
  connection headroom (or introducing `PgBouncer`) becomes a real
  prerequisite, not just a nice-to-have.
- `docs/k8s-deployment.md`'s runbook will need a short new section once
  this is actually implemented (how to install metrics-server, how to
  read `kubectl get hpa` for both targets) — noted here so it isn't
  lost, not written yet since this spec is planning-only per the user's
  own request.
