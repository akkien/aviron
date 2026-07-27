# Kubernetes — `race-service` StatefulSet

## Overview

The payoff spec for the `race-service` side of this phase: `replicas: 2`
here is what makes Phase 4's horizontal-scaling design matter in practice
rather than in theory — the exact scenario
`load/multi-instance-check.md`'s two-`cmd/server`-processes topology
already proved by hand, orchestrated instead of hand-run. Depends on
`k8s-core-infra.md` (Postgres/Redis/Kafka/NATS already up) and
`graceful-shutdown.md` (real `SIGTERM` handling and a real `/healthz` +
`/livez` split to point probes at) — this spec adds no new application
logic of its own beyond what those two already built.

## `StatefulSet`, not `Deployment` — closing gap 2 from `phase-5-plan.md`

`internal/wsgateway/config.go`'s `RACE_SERVICE_INSTANCES` is a static,
comma-separated `host:port` list — correct for `docker-compose.yml`'s
permanent `server-a`/`server-b` container names, wrong for a plain
`Deployment`'s pods, whose IPs and names are neither fixed nor known
ahead of time. This gap is already earmarked twice in this codebase's own
history (`ws-gateway.md`'s Notes, `context/feature-history.md`'s
multi-instance writeup: "DNS-based discovery against a Kubernetes
headless Service is the already-earmarked Phase 5 answer") — this is
where that gets built.

A `StatefulSet` named `race-service` fronted by a **headless** `Service`
(`clusterIP: None`) gives each pod a stable, predictable DNS name:
`race-service-0.race-service.aviron.svc.cluster.local`,
`race-service-1.race-service.aviron.svc.cluster.local`. With
`replicas: 2` fixed (matching `docker-compose.yml`'s own two-instance
topology), `k8s-ws-gateway-deploy.md`'s `RACE_SERVICE_INSTANCES` can be a
static list of exactly those two names — consistent with `Config`'s own
existing comment ("no dynamic service discovery at this project's
scale"), not a reason to abandon that stance for real watch-based
discovery against the Kubernetes API.

## `INSTANCE_ID` via the downward API

`internal/config.getEnvInstanceID` falls back to a `crypto/rand`-generated
id when `INSTANCE_ID` isn't set — a reasonable default for local `go run`,
but a pod already has a better, human-legible, stable-for-its-lifetime
identity: its own name. Set:

```yaml
env:
  - name: INSTANCE_ID
    valueFrom:
      fieldRef:
        fieldPath: metadata.name
```

so Redis's room-ownership records (`redis-room-registry.md`) and this
pod's own logs both read `race-service-0`/`race-service-1` — legible in
`kubectl logs` and in `redis-cli GET room:<id>` output alike, unlike a
random id regenerated on every restart. The random fallback stays correct
(if less legible) for local `go run` use outside Kubernetes — no change
needed to `getEnvInstanceID` itself.

## `deployment.yaml` → `statefulset.yaml`

```text
race-service/
  statefulset.yaml   # replicas: 2, probes, resources, terminationGracePeriodSeconds
  service.yaml        # headless (clusterIP: None) — stable per-pod DNS
```

- `replicas: 2` — the actual point of this spec.
- `readinessProbe`: `httpGet /healthz` (`graceful-shutdown.md`'s existing,
  dependency-checking endpoint — Postgres reachability).
- `livenessProbe`: `httpGet /livez` (`graceful-shutdown.md`'s new,
  dependency-free endpoint) — a Postgres blip must not cause `kubelet` to
  restart an otherwise-healthy pod.
- `terminationGracePeriodSeconds` set to comfortably exceed
  `graceful-shutdown.md`'s `Shutdown(ctx)` timeout (e.g. 30s against a 25s
  internal timeout) — the two numbers must agree; confirm the exact
  values chosen there before setting this one.
- `resources.requests`/`limits` — proportionate to a laptop `kind`
  cluster, same reasoning as `k8s-core-infra.md`'s dependencies.
- Env: `DATABASE_URL`/`REDIS_URL`/`NATS_URL`/`KAFKA_BROKERS`/`JWT_SECRET`/
  `CORS_ALLOWED_ORIGIN`/`PPROF_ENABLED` from the `ConfigMap`/`Secret`
  `k8s-core-infra.md` built; `INSTANCE_ID` from the downward API above;
  `PORT` fixed (e.g. `8080`).
- No `PersistentVolumeClaim` on this StatefulSet itself — `race-service`
  holds no state that needs to survive a pod restart; a room actor's
  in-memory state is already an accepted, disclosed loss on ungraceful
  termination (`context/feature-history.md`), and `graceful-shutdown.md`'s
  design means a *graceful* termination lets in-progress races finish
  before the pod goes away at all. Using a `StatefulSet` here is purely
  for the stable network identity headless `Service`s provide, not for
  per-pod storage — worth being explicit about, since a `StatefulSet`
  most commonly implies a `volumeClaimTemplates` block that this one
  deliberately omits.

## No `Ingress` here

`Ingress` terminates at `ws-gateway` (`k8s-ws-gateway-deploy.md`), not
`race-service` directly — the concrete reversal of
`project-overview.md` §7's original "no `api-gateway/` folder... `Ingress`
routes straight to its `Service`" framing, flagged in
`phase-5-plan.md`. `race-service`'s headless `Service` is reachable only
from inside the cluster (`ws-gateway`'s REST proxy calls, and nothing
else).

## Verification

- `kubectl get pods -n aviron -l app=race-service` shows both
  `race-service-0`/`race-service-1` `Running`/`Ready`.
- `kubectl exec` into a throwaway pod, `nslookup
  race-service-0.race-service.aviron.svc.cluster.local` and the `-1`
  equivalent — confirms the headless `Service`'s per-pod DNS actually
  resolves, the real prerequisite `k8s-ws-gateway-deploy.md` needs before
  it can be tested at all.
- Repeat `load/multi-instance-check.md`'s core assertion by hand against
  this StatefulSet alone (before `ws-gateway` exists in-cluster):
  `redis-cli GET room:<id>` after creating a race directly against one
  pod (`kubectl port-forward race-service-0 ...`) shows that pod's own
  `INSTANCE_ID` (`race-service-0`) as owner — confirms the downward-API
  wiring is correct before layering `ws-gateway` on top.
- `go test ./... -race` and `internal/config`'s existing tests still
  pass unmodified — this spec's only Go-adjacent change is the manifest's
  `fieldRef`, not new application code.

## Notes

- This is where Phase 4's horizontal-scaling design either survives being
  orchestrated by Kubernetes or doesn't — `k8s-ws-gateway-deploy.md` and
  `multi-instance-k8s-verification.md` are where that actually gets
  proven with two real gateways in front of these two pods, not just two
  pods existing in isolation.
- If `nslookup`-ing a StatefulSet pod's stable DNS name doesn't behave as
  expected in the chosen `kind` setup (a real, if unlikely, risk with
  local cluster DNS configuration), that's a signal to debug at this
  layer specifically, before `k8s-ws-gateway-deploy.md` tries to consume
  addresses that were never confirmed to resolve.
