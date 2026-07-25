# Kubernetes — race-service Deployment (and consumer)

## Overview

The payoff spec for this entire phase: `replicas: 2` on race-service is
what makes Phase 4's `horizontal-scaling/` design matter in practice
rather than in theory, and per `context/project-overview.md` §7, this is
where "investigate and resolve production issues" and "horizontally
scaled... stateful sessions" actually get practiced end to end. Depends on
`dockerize.md` and `k8s-core-infra.md`, and — the real dependency, not
just a sequencing one — depends on Phase 4's `horizontal-scaling/` already
being proven correct via `multi-instance-dev-setup.md`'s manual
two-process verification. `replicas: 2` in Kubernetes is the same scenario
as that spec's two-`go run`-processes setup, just orchestrated instead of
hand-run — if the relay design has a bug, this is where it becomes visible
under real (if light) concurrent load instead of a controlled manual
script.

## A real gap this spec must close first: no graceful shutdown exists today

Confirmed by reading `internal/app.go` directly: `Run` calls
`http.ListenAndServe` and blocks — there is no `signal.Notify`, no
`server.Shutdown(ctx)`, nothing. A `SIGTERM` (what Kubernetes sends on pod
termination — scale-down, rolling update, or eviction) kills the process
immediately today, mid-request or mid-WebSocket-connection, with no
chance to drain. `project-overview.md` §7 calls this out explicitly
("a pod receiving `SIGTERM`... must close its active room actors properly,
without cutting off the WebSocket of someone mid-race — a good place to
practice `context` cancellation at the whole-service level") — this spec
is where that finally gets built, not a pre-existing property of this
codebase being merely exposed to Kubernetes.

### Graceful shutdown design

- `internal/app.go`'s `Run`: replace the blocking `http.ListenAndServe`
  call with an `http.Server` constructed explicitly, started via
  `go server.ListenAndServe()`, and a `signal.NotifyContext(context.
  Background(), syscall.SIGTERM, syscall.SIGINT)` watched on the main
  goroutine.
- On signal: call `server.Shutdown(ctx)` (stdlib's own graceful drain —
  stops accepting new connections, waits for in-flight `ServeHTTP` calls
  to return) with a bounded timeout (e.g. 25s, comfortably inside
  Kubernetes' default 30s `terminationGracePeriodSeconds` — confirm the
  exact numbers align at `start`, the Deployment manifest's
  `terminationGracePeriodSeconds` and this timeout must agree or one will
  cut the other off).
- **The harder part: in-flight WebSocket connections.** `http.Server.
  Shutdown` does *not* forcibly close already-hijacked connections (a WS
  connection, per the `k6-load-test.md` Hijacker fix, is exactly that) —
  it only stops new requests and waits for handlers that haven't returned.
  A `WSHandler.ServeHTTP`'s `serveConn` call blocks for the connection's
  entire lifetime (`wg.Wait()`), so `Shutdown` will already wait for it
  naturally — but there's currently nothing telling those connections
  *why* they should wind down, so they'd just wait out the full grace
  period doing nothing differently. Real design question for `start`:
  should the root `context.Context` threaded through `internal/app.go` →
  `RegisterRoutes` → `room.Registry`/`WSHandler` be the *same* context
  cancelled on `SIGTERM` (so every room actor and connection gets a
  cancellation signal immediately, matching §7's "close its active room
  actors properly" instruction), or should room actors keep running
  independently of the HTTP server's own shutdown (finishing in-progress
  races, only new connections being refused)? Leaning toward the latter —
  cancelling every room actor immediately on `SIGTERM` would forcibly end
  every race in progress on that pod the instant a rolling update starts,
  which is a worse user experience than §7's own wording implies wanting
  ("without cutting off the WebSocket of someone mid-race") — but this is
  a real product decision, not just an implementation detail, confirm
  explicitly rather than picking silently.
- `cmd/consumer` needs the equivalent treatment: its reader loops watching
  the same signal-derived context, Phase 4's
  `kafka-consumer-postgres-sink.md`'s Concurrency section already
  anticipated this ("worth building this consumer's shutdown handling
  with [this spec]'s requirements already in mind").

## Requirements

### `deploy/k8s/race-service/`

```text
race-service/
  deployment.yaml   # replicas: 2, probes, resources, terminationGracePeriodSeconds
  service.yaml       # ClusterIP, selects race-service pods
  hpa.yaml            # optional — see below
  ingress.yaml
```

- No `api-gateway/` folder (see `phase-5-plan.md`) — `Ingress` routes
  directly to `race-service`'s `Service`.

### Readiness vs. liveness (separate, per §7)

- **Liveness**: a lightweight check that the process itself hasn't
  deadlocked — `GET /healthz` is already the right shape for this
  (existing endpoint, unauthenticated, no Cors wrapper — confirmed by
  reading `route.go`), reused as-is for `livenessProbe`.
- **Readiness**: must additionally confirm Postgres/Redis/Kafka are
  actually reachable — §7: "to avoid traffic being routed to a pod that
  isn't ready yet." `GET /healthz`'s current implementation (`internal/
  httpserver`'s `NewHealthzHandler`) already `Ping`s the Postgres pool —
  confirm at `start` whether it needs extending to also check Redis
  (`redisclient`'s client, added in Phase 4's `redis-room-registry.md`)
  and Kafka (a lightweight metadata/broker-list call, not a full
  produce/consume round trip), or whether a **separate** `GET /readyz`
  is cleaner than
  overloading `/healthz`'s existing meaning for both probe types — the
  Kubernetes convention of two distinct endpoints (not two behaviors
  behind one path) is the more idiomatic choice and avoids `/healthz`
  silently changing meaning for whatever already depends on its current
  behavior (nothing does today, confirmed by grep, but a `/readyz` split
  is still the cleaner design going forward).

### `deployment.yaml`

- `replicas: 2` — the actual point of this spec.
- `terminationGracePeriodSeconds` aligned with the graceful-shutdown
  timeout above.
- `resources.requests`/`limits` — proportionate to a laptop kind cluster,
  same reasoning as `k8s-core-infra.md`'s dependencies.
- Env sourced from the `ConfigMap`/`Secret` `k8s-core-infra.md` built,
  plus `INSTANCE_ID` sourced from Kubernetes' own downward API
  (`fieldRef: metadata.name`, i.e. the pod name) rather than this
  project's own `crypto/rand` fallback (Phase 4's
  `redis-room-registry.md`) — a pod's name is already unique and stable
  for its lifetime, a better `INSTANCE_ID` than a random value
  regenerated on every restart, and worth wiring in specifically for the
  Kubernetes deployment even though the random fallback stays correct
  (if less legible in `kubectl logs`) for local `go run` use.

### HPA (optional, per §7's own "or" framing)

- If pursued: scale on a **custom metric** (active connection count, per
  §7's own suggestion — `WSHandler.ConnectionCount()`,
  `prometheus-metrics.md`'s already-exposed `aviron_connections_active`)
  rather than plain CPU, since this service's real bottleneck under load
  (per `k6-load-test.md`'s own findings) is connection/goroutine count,
  not CPU. Requires the Prometheus Adapter (or KEDA) installed in-cluster
  to expose that metric to the HPA controller — real added complexity;
  confirm at `start` whether this is worth the setup cost for a side
  project's local kind cluster, or whether documenting the *intent*
  (which metric, why) without actually wiring a working HPA is an
  acceptable, honestly-disclosed stopping point — consistent with §7's
  own "strong plus," not "must-have," framing for this whole phase.

### `consumer/`

- Same shape, `replicas: 1` (no horizontal-scaling story needed — a Kafka
  consumer group already handles partition rebalancing if scaled later)
  — a smaller manifest than race-service's, no `Ingress`, and no
  `Service` at all (the consumer is never called into, only reads from
  Kafka and writes to Postgres).

### Ingress

- `nginx-ingress` or Traefik (§7 offers both) fronting `race-service`'s
  `Service`, so the host-machine React app (per §11, deliberately never
  moved into the cluster) can reach the API. Confirm at `start` which
  ingress controller is simpler to get running in kind specifically (kind
  has first-party docs for the nginx-ingress path, which may make it the
  path of least resistance here regardless of either option's general
  merits).

## Verification

- `kubectl scale deployment race-service --replicas=2`, then repeat Phase
  4's `multi-instance-dev-setup.md`'s entire verification script against
  the two pods (via `kubectl port-forward` to each pod individually, or
  through the Ingress with repeated requests landing on different pods)
  — this is the real proof the horizontal-scaling design survives being
  orchestrated instead of hand-run.
- A rolling update (`kubectl rollout restart deployment race-service`)
  performed while a real race is in progress (start one via the frontend
  or a k6 script first) — confirm the outcome matches whatever this
  spec's graceful-shutdown design question above was resolved to (either
  "the race survived the rollout uninterrupted" or "the race was cleanly
  wound down, not silently dropped," depending which was chosen) rather
  than a raw connection drop with no `race_finished`/error surfaced to the
  client.
- Full `go test ./... -race` and `yarn build`/`yarn lint` still clean —
  this spec touches real application code (`internal/app.go`'s shutdown
  logic), not just manifests, so this project's normal verification bar
  applies here too, not just to the Go-only specs earlier in this phase.

## Notes

- This is the spec where the whole horizontal-scaling effort's success or
  failure becomes observable — treat a failure here as a signal to revisit
  Phase 4's `cross-instance-relay.md` design, not just this manifest, per
  that spec's own closing note ("if any step reveals a real design gap...
  fix that spec's design and revise its file before continuing").
- `docs/concurrency.md` is this project's established place for writing up
  exactly this kind of finding in depth (see its existing finish-race
  cascading-disconnect writeup) — if the rolling-update verification above
  surfaces a real concurrency bug, document it there in the same style,
  not just fix it silently.
