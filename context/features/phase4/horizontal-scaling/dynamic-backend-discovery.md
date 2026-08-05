# Dynamic Backend Discovery (`ws-gateway` → `race-service`)

## Overview

Closes the open question `ws-gateway.md`'s own Notes left twice
("`ws-gateway` doesn't need `race-router`'s eventual... resolved any
differently... the same headless-Service question resurfaces once
Kubernetes work resumes") and that `k8s-hpa.md` (`phase5/`) ran into
directly: `race-service` was fixed at `replicas: 2` specifically because
`ws-gateway`'s `RACE_SERVICE_INSTANCES` is a static, comma-separated list
read once at process startup, making a `HorizontalPodAutoscaler` against
`race-service` unsafe. This spec replaces that static list with real
Kubernetes-native service discovery, decided directly with the user in
favor of watching `EndpointSlice` objects via `client-go` — the same
mechanism `kube-proxy` and every ingress controller already use
internally, not a hand-rolled polling loop and not a second registry
(etcd, Consul, ZooKeeper) standing up a competing source of truth next to
the one Kubernetes already runs.

**A correction to how big this problem actually is**, found while
scoping this spec — `k8s-hpa.md`'s own framing overstated the blast
radius: room-*scoped* REST routing (`/races/{id}/...`) already handles a
changing `race-service` pod set correctly today, with zero changes
needed. `Gateway.resolveTarget` calls `roomlocator.Owner(ctx, raceID)`,
which returns whatever `INSTANCE_ID` the actual owning pod registered
into Redis at `Claim` time — a live, dynamically-composed DNS name
(`k8s-race-service-deploy.md`'s `$(POD_NAME).race-service.....`), not
anything read from the static list. That path was never broken by
scaling. **The real gap is narrower**: only the room-*less* round-robin
path (`register`, `login`, `POST /races`, `GET /races`, `/leaderboard/*`
— anything with no `race_id` in it) reads `gw.backends`, and only that
path needs to become dynamic. This spec is scoped to fixing exactly
that, not re-deriving routing logic that already works.

## Current state (confirmed by reading the code)

`internal/wsgateway/config.go`'s `Config.Backends []string` is populated
once, at startup, from `RACE_SERVICE_INSTANCES`:

```go
backends := splitAndTrim(os.Getenv("RACE_SERVICE_INSTANCES"))
if len(backends) == 0 {
    return Config{}, fmt.Errorf("wsgateway: RACE_SERVICE_INSTANCES is required...")
}
```

`internal/wsgateway/gateway.go`'s `Gateway.backends` is set once at
construction (`NewGateway`) and never mutated afterward — `nextBackend()`
reads it with no locking, safe only because nothing ever writes to it
after `NewGateway` returns:

```go
func (gw *Gateway) nextBackend() string {
    n := gw.next.Add(1)
    return gw.backends[(n-1)%uint64(len(gw.backends))]
}
```

`deploy/k8s/ws-gateway/deployment.yaml`'s own comment already flags this
as the phase's known drift risk: "the one value in this whole phase most
likely to silently drift out of sync with reality" — because scaling
`race-service` (by hand or by an HPA) never updates this env var, and
nothing currently would notice if it did drift.

## Requirements

### A `BackendDiscovery` interface, not a `Gateway` rewrite

Mirrors this codebase's existing small-structural-interface-plus-Noop
pattern (`room.RoomLocator`/`NoopLocator`, `room.RoomBus`/`NoopRoomBus`)
rather than baking Kubernetes-specific code into `Gateway` itself:

```go
// internal/wsgateway/discovery.go
type BackendDiscovery interface {
    // Backends returns the current live backend pool. Called by
    // nextBackend() on every room-less request — cheap, non-blocking,
    // reads whatever the discovery source last observed.
    Backends() []string
}
```

- `StaticBackends` — wraps the existing `Config.Backends []string`
  unchanged, satisfies the interface with a fixed slice. This is what
  local `go run`/`docker-compose` keeps using; **no regression for
  either dev workflow** (`load/multi-instance-check.sh`'s two-`go run`-process topology and `docker-compose.yml`'s `server-a`/`server-b`
  both keep working exactly as today).
- `k8sBackendDiscovery` (new) — backed by a `client-go`
  `SharedIndexInformer` watching `EndpointSlice` objects for the
  `race-service` Service, described below.

`Gateway.backends []string` becomes `Gateway.discovery BackendDiscovery`;
`nextBackend()` calls `gw.discovery.Backends()` instead of reading a
field directly — the round-robin index (`gw.next atomic.Uint64`) is
unchanged, it just now indexes into whatever `Backends()` returns at the
moment of the call rather than a fixed slice. **`nextBackend()` must
handle an empty slice** (return an error the caller turns into `503`,
matching the existing "lookup failure" — not "genuine miss" — status
code already used for the room-scoped path's own failure mode) — new
requirement that didn't exist when the list was guaranteed non-empty by
`LoadConfig`'s own validation.

### `k8sBackendDiscovery`: `EndpointSlice`, via a `SharedIndexInformer`

A raw `Watch()` loop was considered and rejected: a dropped watch
connection silently stops seeing updates unless the caller handles
`resourceVersion` expiry and relists by hand — exactly the class of bug
this spec exists to avoid introducing. `client-go`'s
`SharedIndexInformer` (`k8s.io/client-go/informers`) handles
List-then-Watch, resync, and relist-on-error internally — the same
machinery every real Kubernetes controller uses, not a bespoke
simplification of it:

```go
factory := informers.NewSharedInformerFactoryWithOptions(
    clientset, 30*time.Second,
    informers.WithNamespace(namespace),
    informers.WithTweakListOptions(func(opts *metav1.ListOptions) {
        opts.LabelSelector = "kubernetes.io/service-name=race-service"
    }),
)
informer := factory.Discovery().V1().EndpointSlices().Informer()
informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
    AddFunc:    func(obj any) { d.recompute() },
    UpdateFunc: func(_, obj any) { d.recompute() },
    DeleteFunc: func(obj any) { d.recompute() },
})
```

- `recompute()` lists every `EndpointSlice` currently in the informer's
  local cache/indexer for `race-service`, filters to endpoints where
  `Conditions.Ready != nil && *Conditions.Ready` (excludes pods still
  starting, mid-`terminationGracePeriodSeconds` graceful shutdown, or
  otherwise not passing `/healthz`) — respecting the exact readiness
  signal `graceful-shutdown.md`'s `ReadinessGate` already exists to
  produce, not a second liveness check invented here — and writes
  `host:port` strings (`Addresses[0]`, `Ports[0].Port`) into an
  `atomic.Pointer[[]string]` that `Backends()` reads.
- In-cluster config via `rest.InClusterConfig()` — returns
  `rest.ErrNotInCluster` when not actually running inside a pod, which
  is exactly the signal `cmd/ws-gateway/run.go`'s composition root uses
  to decide `StaticBackends` vs. `k8sBackendDiscovery`: **no new env var
  to toggle this** — `RACE_SERVICE_INSTANCES` stays required for local
  dev (where `InClusterConfig` always fails) and becomes unused/ignorable
  in-cluster (where it succeeds), mirroring how `client-go` itself
  already decides the same thing internally.
- Namespace read from the same mounted service-account file `client-go`
  already knows how to find (`/var/run/secrets/kubernetes.io/
  serviceaccount/namespace`) — no new `POD_NAMESPACE` downward-API env
  var needed, unlike `race-service`'s own `POD_NAME`/`INSTANCE_ID`
  pattern, which needed the downward API because nothing else already
  exposed that value to the container.

### Readiness: don't accept traffic before the first list lands

`k8sBackendDiscovery` must not report `ws-gateway` ready
(`graceful-shutdown.md`'s `ReadinessGate`) until the informer's initial
`HasSynced()` returns `true` — otherwise a freshly-started pod could pass
its readiness probe and start receiving room-less traffic with an empty
backend pool, hitting the new empty-slice `503` above for every request
until the first `List` completes. Mirrors this codebase's existing
"synchronous subscribe before returning" precedent (`Registry.Spawn`'s
`SubscribeIn`, `raceHubRegistry.attach`'s first-subscribe) applied to
readiness instead of correctness.

## Data

No new persistent data — `EndpointSlice` objects are Kubernetes' own,
already existing the moment the `race-service` headless `Service`
(`k8s-core-infra.md`) has any pod behind it. Nothing new to provision,
migrate, or clean up.

## RBAC

`ws-gateway`'s pods need read access to one resource type, scoped to one
namespace — the minimum a `Role` can express, not a `ClusterRole`:

```yaml
# deploy/k8s/ws-gateway/rbac.yaml
apiVersion: v1
kind: ServiceAccount
metadata:
  name: ws-gateway
  namespace: aviron
---
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: ws-gateway-endpointslice-reader
  namespace: aviron
rules:
  - apiGroups: ["discovery.k8s.io"]
    resources: ["endpointslices"]
    verbs: ["get", "list", "watch"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: ws-gateway-endpointslice-reader
  namespace: aviron
subjects:
  - kind: ServiceAccount
    name: ws-gateway
    namespace: aviron
roleRef:
  kind: Role
  name: ws-gateway-endpointslice-reader
  apiGroup: rbac.authorization.k8s.io
```

`deploy/k8s/ws-gateway/deployment.yaml` gains
`spec.template.spec.serviceAccountName: ws-gateway` — the one manifest
change to the existing `Deployment` this spec needs.

## Concurrency

- `Gateway.discovery` replaces a field that was safe precisely because
  it was immutable; `k8sBackendDiscovery`'s `atomic.Pointer[[]string]`
  keeps that same "no lock needed on the read path" property — `Backends()`
  is a single atomic load, called on every room-less request, same cost
  profile `nextBackend()` already had.
- The informer's event handlers (`AddFunc`/`UpdateFunc`/`DeleteFunc`) run
  on `client-go`'s own goroutine, never the request-handling goroutine —
  `recompute()` only ever writes the atomic pointer, never blocks a
  request on Kubernetes API latency.
- Informer lifecycle: started via `factory.Start(ctx.Done())` against
  `cmd/ws-gateway/run.go`'s own root context, so it shuts down cleanly
  alongside every other background goroutine `graceful-shutdown.md`
  already coordinates — no new shutdown path to design, it slots into
  the existing one.

## Testing

- `k8sBackendDiscovery` itself is tested against
  `k8s.io/client-go/kubernetes/fake` — a fake clientset that can have
  `EndpointSlice` objects created/updated/deleted against it directly in
  a test, no real API server needed, the same "fake the dependency, not
  the network" precedent `roomrelay.FakeBus`/`miniredis` already
  established for NATS/Redis in this codebase.
- `Gateway`'s own tests inject a trivial `fakeDiscovery` (a struct
  literal returning a fixed `[]string`, swappable mid-test) instead of
  either real implementation — `nextBackend()`'s round-robin and
  empty-slice-`503` behavior are tested against that, independent of
  whether the real source is `RACE_SERVICE_INSTANCES` or Kubernetes.
- No live-cluster verification new to *this* spec — that's
  `multi-instance-k8s-verification.md`'s job, extended (see Notes) once
  `k8s-hpa.md` actually adds a `race-service` `HorizontalPodAutoscaler`.

## Notes

- **This spec is what makes `k8s-hpa.md`'s deferred `race-service`
  autoscaling question answerable**, not something `k8s-hpa.md` itself
  needs to change today. Once this ships, `k8s-hpa.md` should be
  revisited to actually add a `race-service` `HorizontalPodAutoscaler`
  alongside `ws-gateway`'s — flagged here, not done as a drive-by edit to
  that file in this spec.
- **Correction owed to `k8s-hpa.md`'s own "real constraint" section**:
  it framed the entire routing layer as unsafe under `race-service`
  scaling. As this spec's Overview above found, only the room-less
  round-robin path was ever actually at risk — worth fixing that file's
  own text once this ships, so it doesn't keep overstating the blast
  radius for whoever reads it next.
- **Why not etcd/Consul/ZooKeeper directly**: considered and rejected in
  the discussion that produced this spec. Kubernetes already runs its
  own etcd as the backing store for exactly this kind of data
  (`EndpointSlice` objects); a second, independent registry would be a
  second source of truth that could itself drift from what's actually
  running, solving a problem Kubernetes' own API already solves for
  free. `client-go` against the existing cluster is strictly less new
  infrastructure than any of those three.
- `client-go` is a new Go dependency for this binary — `go.mod` currently
  has no `k8s.io/*` import anywhere in this codebase; this is the first.
  Worth a `go build ./...`/`go test ./... -race` sanity pass across the
  whole module once added, not just the touched package, in case of any
  unexpected transitive version conflict.
