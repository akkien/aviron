# Current Feature: Kubernetes — `race-service` StatefulSet

## Status

In Progress

## Goals

- `kubectl get pods -n aviron -l app=race-service` shows both
  `race-service-0`/`race-service-1` `Running`/`Ready`.
- `kubectl exec` into a throwaway pod, `nslookup
  race-service-0.race-service.aviron.svc.cluster.local` and the `-1`
  equivalent both resolve — confirms the headless `Service`'s per-pod DNS
  actually works, the real prerequisite `k8s-ws-gateway-deploy.md` needs
  before it can even be attempted.
- Creating a race directly against one pod (`kubectl port-forward
  race-service-0 ...`) and checking `redis-cli GET room:<id>` shows that
  pod's own `INSTANCE_ID` (`race-service-0`) as owner — confirms the
  downward-API wiring is correct before layering `ws-gateway` on top.
- `go test ./... -race` and `internal/config`'s existing tests still pass
  unmodified — this spec's only Go-adjacent change is a manifest's
  `fieldRef`, not new application code.

## Explain

- Depends on `k8s-core-infra.md` (Postgres/Redis/Kafka/NATS already up)
  and `graceful-shutdown.md` (real `SIGTERM` handling, plus the real
  `/healthz` + `/livez` split to point probes at) — this spec adds no new
  application logic of its own, only manifests plus one downward-API env
  var.
- **`StatefulSet`, not `Deployment` — closes gap 2 from
  `phase-5-plan.md`.** `internal/wsgateway/config.go`'s
  `RACE_SERVICE_INSTANCES` is a static, comma-separated `host:port` list —
  correct for `docker-compose.yml`'s permanent container names, wrong for
  a `Deployment`'s pods, whose IPs/names are neither fixed nor known ahead
  of time. A `StatefulSet` fronted by a **headless** `Service`
  (`clusterIP: None`) gives each pod a stable, predictable DNS name
  (`race-service-0.race-service.aviron.svc.cluster.local`,
  `race-service-1...`), so `k8s-ws-gateway-deploy.md`'s
  `RACE_SERVICE_INSTANCES` can just be a static list of those two names —
  this is the "DNS-based discovery against a Kubernetes headless Service"
  answer already earmarked twice in this codebase's own history
  (`ws-gateway.md`'s Notes, `context/feature-history.md`'s multi-instance
  writeup).
- **`INSTANCE_ID` via the downward API**
  (`fieldRef: metadata.name`), replacing `internal/config.getEnvInstanceID`'s
  `crypto/rand` fallback for this deployment specifically — a pod's own
  name is already a stable, human-legible identity for its lifetime, so
  Redis's room-ownership records and `kubectl logs` both read
  `race-service-0`/`race-service-1` instead of a random id regenerated on
  every restart. No code change — the random fallback stays correct (if
  less legible) for local `go run` use outside Kubernetes.
- **No `PersistentVolumeClaim` despite being a `StatefulSet`** —
  deliberately, and worth being explicit about since a `StatefulSet` most
  commonly implies `volumeClaimTemplates`. This one is a `StatefulSet`
  purely for the stable network identity the headless `Service` needs,
  not for per-pod storage: `race-service` holds no state that must
  survive a pod restart (a room actor's in-memory state loss on
  ungraceful termination is already an accepted, disclosed risk, and
  `graceful-shutdown.md`'s design already lets in-progress races finish
  before a *graceful* termination removes the pod at all).
- **No `Ingress` here.** It terminates at `ws-gateway`
  (`k8s-ws-gateway-deploy.md`, next spec) — the concrete reversal of
  `project-overview.md` §7's original "no `api-gateway/` folder...
  `Ingress` routes straight to its `Service`" framing.  `race-service`'s
  headless `Service` is reachable only from inside the cluster
  (`ws-gateway`'s REST proxy calls, and nothing else).

## Plan

1. `deploy/k8s/race-service/statefulset.yaml`:
   - `replicas: 2` — the actual point of this spec.
   - `readinessProbe`: `httpGet /healthz` (`graceful-shutdown.md`'s
     existing, dependency-checking endpoint — Postgres reachability).
   - `livenessProbe`: `httpGet /livez` (`graceful-shutdown.md`'s new,
     dependency-free endpoint) — a Postgres blip must not cause `kubelet`
     to restart an otherwise-healthy pod.
   - `terminationGracePeriodSeconds` set to comfortably exceed
     `graceful-shutdown.md`'s `Shutdown(ctx)` timeout (that spec used
     ~25s internally, so this should be ~30s) — the two numbers must
     agree, confirm against the actual constant in
     `cmd/server/run.go` rather than assuming.
   - `resources.requests`/`limits` — proportionate to a laptop `kind`
     cluster, same reasoning as `k8s-core-infra.md`'s dependencies.
   - Env: `DATABASE_URL`/`REDIS_URL`/`NATS_URL`/`KAFKA_BROKERS`/
     `JWT_SECRET`/`CORS_ALLOWED_ORIGIN`/`PPROF_ENABLED` from the
     `ConfigMap`/`Secret` `k8s-core-infra.md` built; `INSTANCE_ID` from
     the downward API (`fieldRef: metadata.name`); `PORT` fixed (e.g.
     `8080`).
   - No `volumeClaimTemplates` block.
2. `deploy/k8s/race-service/service.yaml`: headless (`clusterIP: None`),
   selecting the StatefulSet's pods.
3. No `Ingress`, no changes to `ws-gateway`/`consumer` — those are later
   specs.

## Notes

- Full spec: `context/features/phase5/k8s-race-service-deploy.md`. Phase
  roadmap: `context/features/phase5/phase-5-plan.md`.
- This is where Phase 4's horizontal-scaling design either survives being
  orchestrated by Kubernetes or doesn't — full proof (two real gateways
  in front of these two pods, not just two pods existing in isolation)
  comes from `k8s-ws-gateway-deploy.md` and
  `multi-instance-k8s-verification.md`, not this spec alone.
- Real, if unlikely, risk flagged by the spec itself: if `nslookup`-ing a
  StatefulSet pod's stable DNS name doesn't behave as expected in this
  `kind` setup, that's a signal to debug at this layer specifically,
  before `k8s-ws-gateway-deploy.md` tries to consume addresses that were
  never confirmed to resolve.
