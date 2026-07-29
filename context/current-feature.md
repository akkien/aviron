# Current Feature: Kubernetes — `ws-gateway` Deployment + Ingress

## Status

In Progress

## Goals

- `kubectl get pods -n aviron -l app=ws-gateway` shows both replicas
  `Running`/`Ready`.
- Through the `Ingress`: register a user, create a race, join it, open
  the WebSocket, confirm live state updates — the same manual smoke test
  `README.md`'s own "Install and Run" walks through against
  `docker-compose`, now against the cluster instead.
- The real proof: repeat `load/multi-instance-check.md`'s cross-gateway
  scenario against the cluster — two participants of the same race,
  connected through *different* `ws-gateway` pods, still see fully
  consistent, correctly-ordered state. (The full scripted version of this
  is `multi-instance-k8s-verification.md`, spec 6/6 — this feature's own
  verification just needs to prove it works by hand first.)

## Explain

- The client-facing half of this phase: `ws-gateway` is where the browser
  actually connects, and where `Ingress` terminates — the concrete
  reversal of `project-overview.md` §7's original "no `api-gateway/`
  folder" framing that `phase-5-plan.md` already flagged.
- Depends on `k8s-core-infra.md` (Redis/NATS reachable),
  `graceful-shutdown.md` (real `SIGTERM` handling, including the new
  `raceHubRegistry.Shutdown()`), and `k8s-race-service-deploy.md` (the
  stable per-pod DNS names this spec's `RACE_SERVICE_INSTANCES` actually
  points at — already verified working: `nslookup` for both
  `race-service-{0,1}` resolved to distinct pod IPs).
- **Plain `Deployment`, not `StatefulSet`** — unlike `race-service`,
  `ws-gateway` holds no state needing a stable identity of its own:
  `internal/wsgateway.Config` never sets an `INSTANCE_ID`-equivalent, and
  its `raceHubRegistry` is purely in-memory, ref-counted per race, torn
  down the instant the last local connection detaches. `replicas: 2`
  (matching `docker-compose.yml`'s own two-gateway topology, and the
  exact topology `load/multi-instance-check.md` already proved by hand) +
  a normal `ClusterIP` `Service`.
- **`RACE_SERVICE_INSTANCES`** — a static list of `race-service`'s two
  stable StatefulSet DNS names
  (`race-service-0.race-service.aviron.svc.cluster.local:8080,
  race-service-1...:8080`), set directly on this Deployment's env, not the
  shared `ConfigMap` (which deliberately excludes per-binary values). Same
  "no dynamic service discovery at this project's scale" stance
  `internal/wsgateway/config.go`'s own comment already commits to — fine
  as long as `race-service`'s replica count stays fixed at 2.
- Probes mirror `race-service`'s split: `readinessProbe` on `GET
  /healthz` (`internal/wsgateway/healthz.go`'s existing Redis+NATS
  check), `livenessProbe` on the new dependency-free `GET /livez`.
- **`terminationGracePeriodSeconds` — confirmed against the real
  constants, not assumed.** `cmd/ws-gateway/run.go` uses the same 25s
  `shutdownTimeout` as `cmd/server`, plus a 500ms `connFlushWindow` before
  `raceHubRegistry.Shutdown()` fires — so this binary's total internal
  wind-down budget (~25.5s) is slightly larger than `race-service`'s
  (25s). `race-service`'s StatefulSet used `terminationGracePeriodSeconds:
  30`; keeping the same value here still leaves a healthy ~4.5s margin,
  so no need to bump it further.
- **No `/metrics` or `/debug/pprof/` on this binary** — confirmed by
  `grep`, both are `cmd/server`-only. Nothing added here; that's a future
  `project-overview.md` §9 concern if observability parity is ever
  wanted, not this phase's scope.
- **`Ingress`** (`nginx-ingress` or Traefik — confirm at `start` which is
  simpler to stand up in `kind` specifically) fronts this Deployment's
  `Service`, so the host-machine React app (never moved into the cluster,
  per §11) can reach it — replacing today's `http://localhost:9090` in
  `VITE_API_URL`.

## Plan

1. `deploy/k8s/ws-gateway/deployment.yaml`:
   - `replicas: 2`.
   - `readinessProbe`: `httpGet /healthz`.
   - `livenessProbe`: `httpGet /livez`.
   - `terminationGracePeriodSeconds: 30`.
   - `resources.requests`/`limits` — proportionate to a laptop `kind`
     cluster, same reasoning as every other manifest in this phase.
   - Env: `REDIS_URL`/`NATS_URL`/`JWT_SECRET`/`CORS_ALLOWED_ORIGIN` from
     the shared `ConfigMap`/`Secret`; `RACE_SERVICE_INSTANCES` and
     `WS_GATEWAY_LISTEN_ADDR` (`:8080`, matching `docker-compose.yml`'s
     own convention) set directly on this Deployment.
   - A code comment in the manifest itself flagging that
     `RACE_SERVICE_INSTANCES` must be updated by hand if
     `race-service`'s replica count ever changes — per the spec's own
     Notes, the piece of this phase most likely to silently drift.
2. `deploy/k8s/ws-gateway/service.yaml`: normal `ClusterIP` (not
   headless).
3. `deploy/k8s/ws-gateway/ingress.yaml`: confirm at implementation time
   whether `kind` already has an ingress controller available, or
   whether `ingress-nginx`'s own `kind`-specific manifest needs applying
   first.
4. No changes to `race-service`/`consumer` manifests, no application code
   — this spec is manifests-only, same as `k8s-race-service-deploy.md`.

## Notes

- Full spec: `context/features/phase5/k8s-ws-gateway-deploy.md`. Phase
  roadmap: `context/features/phase5/phase-5-plan.md`.
- `RACE_SERVICE_INSTANCES`'s value is the one piece of this whole phase
  most likely to silently drift out of sync with reality if
  `race-service`'s replica count ever changes without this file being
  updated to match.
- If `nginx-ingress`/Traefik setup in `kind` turns out more finicky than
  expected (a real, common friction point for local Kubernetes), that's
  this spec's own problem to absorb — it doesn't block
  `k8s-consumer-deploy.md`, which needs no `Ingress` at all.
- The backend image loaded into `kind` is already current (rebuilt/
  reloaded during `k8s-race-service-deploy.md` after the
  `graceful-shutdown` feature landed) — no rebuild needed before starting
  this one, but worth double-checking if any Go code changes land between
  now and implementation.
