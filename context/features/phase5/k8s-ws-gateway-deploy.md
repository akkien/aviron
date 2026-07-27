# Kubernetes — `ws-gateway` Deployment + Ingress

## Overview

The client-facing half of this phase: `ws-gateway` is where the browser
actually connects, and where `Ingress` terminates — the concrete reversal
of `project-overview.md` §7's original "no `api-gateway/` folder"
framing that `phase-5-plan.md` already flagged. Depends on
`k8s-core-infra.md` (Redis/NATS reachable), `graceful-shutdown.md` (real
`SIGTERM` handling, including the new `raceHubRegistry.Shutdown()` that
spec adds), and `k8s-race-service-deploy.md` (the stable per-pod DNS names
this spec's `RACE_SERVICE_INSTANCES` actually points at — verify that
spec's `nslookup` check passed before starting this one).

## Plain `Deployment`, not `StatefulSet`

Unlike `race-service`, `ws-gateway` holds no state that needs a stable
identity of its own: `internal/wsgateway.Config` never sets an
`INSTANCE_ID`-equivalent, and its `raceHubRegistry` is purely
in-memory, scoped to whichever local connections happen to be attached
right now (`racehub.go`, confirmed by reading it directly — reference-counted
per race, torn down the instant the last local connection detaches). A
plain `Deployment`, `replicas: 2` (matching `docker-compose.yml`'s own
two-gateway topology, and the exact topology
`load/multi-instance-check.md` already proved by hand), + a normal
(non-headless) `ClusterIP` `Service` is the right shape.

## `RACE_SERVICE_INSTANCES` — static list of stable StatefulSet DNS names

```yaml
env:
  - name: RACE_SERVICE_INSTANCES
    value: "race-service-0.race-service.aviron.svc.cluster.local:8080,race-service-1.race-service.aviron.svc.cluster.local:8080"
```

Set directly in this Deployment's env (not the shared `ConfigMap` —
`k8s-core-infra.md`'s `ConfigMap` deliberately excludes per-binary values
like this one), hardcoded to `race-service`'s two known stable pod names
from `k8s-race-service-deploy.md`. This is the same "no dynamic service
discovery at this project's scale" stance `internal/wsgateway/config.go`'s
own comment already commits to — a fixed, small replica count makes a
static list of stable names correct, not a stopgap; if `race-service`'s
replica count ever needs to change, this value changes with it, the same
way `docker-compose.yml`'s own `server-a:8080,server-b:8080` would.

## `deployment.yaml` / `service.yaml` / `ingress.yaml`

```text
ws-gateway/
  deployment.yaml
  service.yaml
  ingress.yaml
```

- `replicas: 2`.
- `readinessProbe`: `httpGet /healthz` (`internal/wsgateway/healthz.go`'s
  existing Redis + NATS check).
- `livenessProbe`: `httpGet /livez` — new, per `graceful-shutdown.md`'s
  design, mirroring `race-service`'s split for the identical reason (a
  transient Redis or NATS blip must not cause `kubelet` to restart an
  otherwise-healthy gateway pod).
- `terminationGracePeriodSeconds` aligned with `graceful-shutdown.md`'s
  `ws-gateway`-side `Shutdown` timeout plus its flush window — this
  binary's shutdown path is the most involved of the three (closing every
  locally-held connection via the new `raceHubRegistry.Shutdown()`), so
  give it a little more headroom than `race-service`'s if the two
  timeouts genuinely differ; confirm against `graceful-shutdown.md`'s
  actual chosen numbers rather than assuming parity.
- Env: `REDIS_URL`/`NATS_URL`/`JWT_SECRET`/`CORS_ALLOWED_ORIGIN` from the
  shared `ConfigMap`/`Secret`; `RACE_SERVICE_INSTANCES` and
  `WS_GATEWAY_LISTEN_ADDR` set directly on this Deployment (per-binary,
  not shared).
- `Ingress` (`nginx-ingress` or Traefik — §7 offers both; confirm at
  `start` which is simpler to get running in `kind` specifically, `kind`
  has first-party docs for the `nginx-ingress` path) fronting this
  Deployment's `Service`, so the host-machine React app (deliberately
  never moved into the cluster, per §11) can reach it. `VITE_API_URL`
  (per `README.md`'s frontend setup) points at whatever host/port the
  `Ingress` exposes, replacing today's `http://localhost:9090`.

## No `/metrics` or `/debug/pprof/` exposed here

Confirmed by `grep` across `internal/wsgateway`/`cmd/ws-gateway`: neither
exists on this binary today — both are `cmd/server`-only
(`internal/httpserver/route.go`). Nothing in this spec adds them; if
observability parity across both binaries is wanted later, that's a
`project-overview.md` §9 concern for a future phase, not part of this
phase's scope.

## Verification

- `kubectl get pods -n aviron -l app=ws-gateway` shows both replicas
  `Running`/`Ready`.
- Through the `Ingress`: register a user, create a race, join it, open
  the WebSocket, confirm live state updates — the same manual smoke test
  `README.md`'s own "Install and Run" walks through against
  `docker-compose`, now against the cluster instead.
- The real proof — repeat `load/multi-instance-check.md`'s cross-gateway
  scenario against the cluster: two participants of the same race,
  connected through *different* `ws-gateway` pods (via `kubectl
  port-forward` to each pod individually, or by hitting the `Ingress`
  repeatedly until both pods have served a connection), still see fully
  consistent, correctly-ordered state. See
  `multi-instance-k8s-verification.md` for the full scripted version of
  this check.

## Notes

- This spec's `RACE_SERVICE_INSTANCES` value is the one piece of this
  whole phase most likely to silently drift out of sync with reality if
  `race-service`'s replica count ever changes without this file being
  updated to match — worth a comment in the actual manifest saying so,
  not just here.
- If `nginx-ingress`/Traefik setup in `kind` turns out more finicky than
  expected (a real, common friction point for local Kubernetes), that's
  this spec's own problem to absorb — it doesn't block
  `k8s-consumer-deploy.md`, which needs no `Ingress` at all.
