# Current Feature: Kubernetes — `HorizontalPodAutoscaler`

## Status

In Progress

## Goals

- `deploy/k8s/ws-gateway/hpa.yaml` and `deploy/k8s/race-service/hpa.yaml`
  exist: CPU-based `HorizontalPodAutoscaler`s for both, `minReplicas: 2`
  / `maxReplicas: 5` / `averageUtilization: 70`
- `metrics-server` installed and working on the `kind` cluster (a hard
  prerequisite that doesn't exist today)
- Both HPAs verified live: scale up under real load, scale back down
  once load stops, and neither drops an in-progress race/connection
  during a scale-down

## Explain

- Target **CPU utilization**, not connection count — decided directly
  with the user. `aviron_connections_active`-style metric doesn't
  actually exist (`internal/wsgateway` has no Prometheus wiring at all,
  confirmed by grep), and getting there needs three separate pieces of
  new infrastructure (the gauge itself, a Prometheus deployment, a
  Prometheus Adapter/KEDA bridge) — CPU needs only `metrics-server`,
  already the standard `autoscaling/v2` mechanism.
- **`race-service` is now a real target — previously blocked, now
  unblocked.** `k8s-race-service-deploy.md` fixed it at `replicas: 2`
  because `ws-gateway`'s `RACE_SERVICE_INSTANCES` was a static list;
  scaling would have spawned pods `ws-gateway` never learned about.
  `dynamic-backend-discovery.md` (shipped 2026-08-05) closed this via a
  `client-go` `EndpointSlice` watch — no code changes needed here, the
  blocker is just gone.
- **A correction folded into this spec while resolving the above**:
  room-*scoped* routing (`/races/{id}/...`) was never actually blocked
  by the static-list problem — it already resolves live via
  `roomlocator.Owner` (Redis). Only the room-*less* round-robin path was
  ever at risk. The original "constraint" writeup overstated the blast
  radius; left intact in the spec for the historical record, not
  deleted.
- **Scale-down is graceful for `race-service`**, not just `ws-gateway` —
  `terminationGracePeriodSeconds: 150` + `waitForRoomsToDrain`
  (`graceful-shutdown.md`) already let an in-progress room finish before
  its pod terminates, and a `HorizontalPodAutoscaler`-triggered
  scale-down uses the exact same Pod-deletion path a manual
  `kubectl delete pod` does — no new protection needed, just relying on
  what already exists.
- Two nearly-identical manifests: `ws-gateway`'s targets a `Deployment`,
  `race-service`'s targets the `StatefulSet` — `autoscaling/v2` scales
  either, since both expose a `/scale` subresource. Same
  `minReplicas`/`maxReplicas`/`averageUtilization` values for both, by
  design (§"why" already in the spec — floor matches the already-proven
  2-instance baseline, ceiling is generous headroom for a laptop demo).
- `metrics-server` isn't installed anywhere in this project's cluster
  setup today — confirmed by checking `deploy/kind-config.yaml` and
  every existing `phase5/*.md`. `kind` specifically needs
  `--kubelet-insecure-tls` patched onto it (kubelet's serving certs on a
  `kind` node aren't signed by a CA `metrics-server` trusts by default)
  — an accepted local-dev-only stance.
- **Redis/Postgres/Kafka are deliberately not autoscaled** — a plain HPA
  is the wrong mechanism for stateful data infrastructure (a fresh pod
  doesn't inherit a shard/partition/primary role the way a fresh
  `race-service`/`ws-gateway` pod inherits nothing special). Real
  systems scale these via vertical scaling, read replicas, or dedicated
  sharding/failover operators instead — spec has a full table. The one
  legitimate real-world exception is Kafka *consumer* scaling
  (`cmd/consumer`) on consumer lag, which this project doesn't build —
  flagged as a genuine future spec, not in scope here.

## Plan

1. Install `metrics-server` on the running `kind` cluster: `kubectl
   apply -f https://github.com/kubernetes-sigs/metrics-server/releases/latest/download/components.yaml`,
   then patch in `--kubelet-insecure-tls` via `kubectl patch deployment
   metrics-server -n kube-system ...` (exact command already in the
   spec). Confirm `kubectl top nodes`/`kubectl top pods -n aviron` return
   real numbers before moving on — no point writing HPA manifests
   against a metrics source that isn't actually serving yet.
2. Write `deploy/k8s/ws-gateway/hpa.yaml` exactly as specced.
3. Write `deploy/k8s/race-service/hpa.yaml` exactly as specced.
4. `kubectl apply -f` both; confirm `kubectl get hpa -n aviron` shows
   `TARGETS` as a real percentage (not `<unknown>`) for both within a
   few scrape intervals.
5. Generate load via `load/`'s existing k6 scenarios against the
   in-cluster `Ingress`, enough to push CPU past 70% on both targets;
   watch `kubectl get hpa -n aviron -w` for `REPLICAS` climbing above 2
   on both, and confirm the new pods reach `Running`/`Ready`.
6. `race-service`-specific check: while replicas are elevated, create a
   new race and confirm (`redis-cli GET room:<id>`) it gets `Claim`ed on
   one of the newly-scaled pods, not just the original two — proof the
   new pod is actually receiving traffic.
7. Stop load, confirm both scale back down to `minReplicas: 2` after the
   default 5-minute stabilization window.
8. Scale-down-safety check on both: trigger (or wait for) a scale-down
   mid-race and confirm no in-progress race/connection is dropped —
   `ws-gateway`'s existing rolling-update assertion for the gateway
   side, the equivalent room-finishes-before-pod-terminates check
   (already proven for rolling updates in
   `multi-instance-k8s-verification.md`) for `race-service`.
9. `go build ./...`/`go test ./... -race` — expected to be a no-op since
   this spec is manifests-only, no Go code changes; run once anyway per
   this workflow's own convention.
10. Update `docs/k8s-deployment.md` with a short new section (how to
    install `metrics-server`, how to read `kubectl get hpa` for both
    targets) — the spec's own Notes flag this as still owed.

**Divergence from `project-overview.md`, called out explicitly**: §9's
Prometheus-metrics list (and `phase-5-plan.md`'s own original note)
implicitly assumed a connection-count metric would be what an HPA
eventually targets. This spec targets CPU instead — decided directly
with the user, for the reasons in Explain above — not a rejection of
§9's metric, just not what this particular autoscaling decision ended
up using. Connection-count-based scaling stays a valid future spec if
this project's scope grows to need it.

## Notes

- **An investigation was in progress and got interrupted, worth picking
  back up before/during implementation**: verifying the real numbers
  behind the spec's own disclosed Postgres-connection-pool risk (each
  additional `race-service` pod opens its own `pgxpool` against the
  single, non-pooled Postgres instance this project runs; `maxReplicas:
  5` was chosen without checking real `max_connections` headroom).
  Confirmed so far: no `max_connections` override anywhere in
  `deploy/k8s/postgres/statefulset.yaml` (so it's running on
  `postgres:18-alpine`'s own default), and `internal/db/db.go` calls
  `pgxpool.New(ctx, dsn)` with no `pool_max_conns` in the DSN (so pgx's
  own default pool-sizing formula applies, not an explicit value this
  codebase set). Actually computing whether `maxReplicas: 5` could
  realistically exceed Postgres's connection budget — and whether
  `PgBouncer` becomes a real prerequisite — was not finished.
- Why not Redis/Postgres/Kafka autoscaling, and the one real exception
  (Kafka consumer lag): full reasoning + table already in the spec's
  "What about Redis, Postgres, Kafka?" section — don't re-derive it,
  it's already written.
- No Helm chart, no `VerticalPodAutoscaler` — consistent with this
  phase's existing "plain manifests" stance.
- Full spec: `context/features/phase5/k8s-hpa.md`.
