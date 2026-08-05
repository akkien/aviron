# Current Feature: Dynamic Backend Discovery (`ws-gateway` → `race-service`)

## Status

In Progress

## Goals

- `ws-gateway`'s room-less REST routing (register/login/`POST /races`/
  `GET /races`/`/leaderboard/*`) discovers live `race-service` pods
  dynamically instead of reading a static `RACE_SERVICE_INSTANCES` list
- `race-service` can be scaled up or down (by hand today, by an HPA
  later — `k8s-hpa.md`) without `ws-gateway` routing room-less traffic
  to a dead pod, or leaving a newly-added pod permanently idle
- Local dev (`go run`, `docker-compose`) keeps working completely
  unchanged — this is additive, not a replacement of the static path
- RBAC scoped to exactly what's needed: a namespaced `Role`, not a
  `ClusterRole`

## Explain

- **Room-*scoped*** REST routing (`/races/{id}/...`) already handles a
  changing `race-service` pod set correctly today, with zero changes
  needed — `Gateway.resolveTarget` calls `roomlocator.Owner(ctx, raceID)`,
  which returns whatever `INSTANCE_ID` the actual owning pod registered
  into Redis, a live value, never read from the static list. Confirmed
  by reading `gateway.go` directly, not assumed.
- **Only the room-*less* round-robin path** (`Gateway.backends`/
  `nextBackend()`) reads the static list — that's the entire actual gap
  this feature closes, narrower than `k8s-hpa.md`'s own framing implied.
- New `BackendDiscovery` interface (`internal/wsgateway/discovery.go`),
  mirroring this codebase's existing small-structural-interface +
  `Noop*` pattern (`room.RoomLocator`/`NoopLocator`,
  `room.RoomBus`/`NoopRoomBus`):
  - `StaticBackends` — wraps today's `Config.Backends []string`
    unchanged; what local `go run`/`docker-compose` keeps using.
  - `k8sBackendDiscovery` (new) — backed by a `client-go`
    `SharedIndexInformer` watching `EndpointSlice` objects for the
    `race-service` Service, filtered to `Ready` endpoints only, exposed
    via an `atomic.Pointer[[]string]`.
- A raw `Watch()` loop was considered and rejected — a dropped watch
  silently stops seeing updates unless `resourceVersion` expiry/relist
  is handled by hand. `SharedIndexInformer` does List-then-Watch +
  resync + relist-on-error internally, the same machinery every real
  Kubernetes controller uses.
- Which implementation `cmd/ws-gateway/run.go`'s composition root
  constructs is decided by whether `rest.InClusterConfig()` succeeds —
  **no new env var**; `RACE_SERVICE_INSTANCES` stays required for local
  dev and becomes unused in-cluster.
- `ws-gateway`'s readiness gate must wait for the informer's initial
  `HasSynced()` before reporting ready — otherwise a fresh pod could
  pass its readiness probe with an empty backend pool.
- New RBAC manifests (`deploy/k8s/ws-gateway/rbac.yaml`): a
  `ServiceAccount`, a `Role` (`get`/`list`/`watch` on `endpointslices`,
  namespaced), and a `RoleBinding`. `deployment.yaml` gains
  `serviceAccountName: ws-gateway`.
- `client-go` is this codebase's first `k8s.io/*` dependency —
  `k8sBackendDiscovery` tested against `k8s.io/client-go/kubernetes/fake`
  (no real API server needed), `Gateway`'s own tests get a trivial
  `fakeDiscovery` swap-in.

## Plan

1. Add `client-go`/`k8s.io/apimachinery` as Go module dependencies; run
   a full `go build ./...` afterward to catch any transitive version
   conflict early, before writing any application code against it.
2. `internal/wsgateway/discovery.go` (new): `BackendDiscovery` interface
   and `StaticBackends` (thin wrapper around an existing `[]string`).
3. `internal/wsgateway/k8s_discovery.go` (new): `k8sBackendDiscovery` —
   constructs the informer factory (namespaced, label-selector-scoped to
   `kubernetes.io/service-name=race-service`), registers
   Add/Update/Delete handlers calling `recompute()`, exposes
   `Backends() []string` off the `atomic.Pointer[[]string]`, and a
   `WaitForSync(ctx) bool` the readiness wiring in step 6 depends on.
4. `internal/wsgateway/gateway.go`: `Gateway.backends []string` →
   `Gateway.discovery BackendDiscovery`; `NewGateway`'s signature updates
   to take a `BackendDiscovery` instead of `[]string`; `nextBackend()`
   reads `gw.discovery.Backends()` and returns an error on an empty
   slice; `ServeHTTP`'s room-less branch turns that error into the same
   `503` status the room-scoped "lookup failure" path already uses.
5. `cmd/ws-gateway/run.go`: composition-root branch — try
   `rest.InClusterConfig()`; on success, build a `kubernetes.Clientset`,
   construct `k8sBackendDiscovery`, start its informer against the root
   context, and gate readiness on `WaitForSync`; on
   `rest.ErrNotInCluster`, construct `StaticBackends` from
   `cfg.Backends` exactly as today.
6. Extend the existing `ReadinessGate` wiring so `k8sBackendDiscovery`'s
   sync state is one more thing `/healthz` checks, alongside whatever it
   already checks (Redis/NATS) — not a second, parallel readiness
   mechanism.
7. `deploy/k8s/ws-gateway/rbac.yaml` (new):
   `ServiceAccount`/`Role`/`RoleBinding` exactly as specced.
   `deploy/k8s/ws-gateway/deployment.yaml`: add
   `spec.template.spec.serviceAccountName: ws-gateway`.
8. Tests: `discovery_test.go` (or similar) against
   `k8s.io/client-go/kubernetes/fake`, covering add/update/delete →
   `Backends()` reflecting the change, and `Ready`-filtering. Update
   `gateway_test.go`'s existing backend-related tests to use a
   `fakeDiscovery` instead of a raw slice; add the new empty-slice-`503`
   case.
9. `go build ./...` and `go test ./... -race` across the whole module
   (not just `internal/wsgateway`) before considering this done, per the
   spec's own note about a first-time dependency addition.

**Divergence from `project-overview.md`, called out explicitly**: §5's
original horizontal-scaling design only ever describes Redis as the
service-registry mechanism ("every instance publishes/subscribes to a
Redis pub/sub channel... a simple registry"). This feature introduces a
second, different discovery mechanism — the Kubernetes API itself, via
`client-go` — for one specific piece (the room-less round-robin backend
pool), while room ownership itself stays exactly as `project-overview.md`
§5 describes, unchanged. This isn't a reversal of §5, it's an addition
§5 never anticipated because it predates `ws-gateway`/Kubernetes
existing in this project at all — flagged here per this workflow's own
"call out anything that diverges" instruction, not silently introduced.

## Notes

- **Why not etcd/Consul/ZooKeeper**: considered and rejected during the
  discussion that produced this spec. Kubernetes already runs its own
  etcd as the backing store for `EndpointSlice` data; a second registry
  would just be a second source of truth to keep in sync — `client-go`
  against the existing cluster is strictly less new infrastructure.
- **This feature is what makes `k8s-hpa.md`'s deferred `race-service`
  autoscaling question answerable** — once this ships, `k8s-hpa.md`
  should be revisited to (a) correct its "real constraint" section,
  which overstated the blast radius (only the round-robin path was ever
  at risk, not all routing), and (b) actually add a `race-service`
  `HorizontalPodAutoscaler`. Neither is done as part of this feature —
  flagged for later, per the spec's own Notes.
- No live-cluster verification is new to this feature —
  `multi-instance-k8s-verification.md` covers that, to be extended later
  once `race-service` actually gets an HPA.
- Full spec: `context/features/phase4/horizontal-scaling/dynamic-backend-discovery.md`.
