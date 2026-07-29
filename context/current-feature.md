# Current Feature: Kubernetes — `consumer` Deployment + Phase 5 Multi-Instance Verification

## Status

In Progress

## Goals

**From `k8s-consumer-deploy.md` (spec 5/6):**

- `kubectl get pods -n aviron -l app=consumer` shows the one replica
  `Running`.
- Running a race to completion (through the already-deployed
  `ws-gateway`/`race-service`) results in `workout_samples` and
  `race_participants` rows actually landing in Postgres — confirmed via
  `kubectl exec` into the Postgres pod and querying directly.
- `kubectl delete pod` on the consumer pod mid-batch — the replacement
  pod (via `restartPolicy: Always`) resumes from the last committed Kafka
  offset, not reprocessing or dropping messages.
- `go test ./internal/consumer/... -race` unmodified — no application
  code changes, `graceful-shutdown.md` already did the one-line context
  fix.

**From `multi-instance-k8s-verification.md` (spec 6/6, the real "done" bar
for the whole phase):**

- A full run (`REPEAT_RUNS=6` lifecycle checks, via two distinct
  `kubectl port-forward`ed `ws-gateway` pods) with both `race-service`
  pods seen as owner at least once, and the bus-traffic assertions
  (`roombus: published` / `wsgateway: received`, tagged with the race ID)
  passing on every run.
- Two rolling-update passes performed mid-race — `kubectl rollout
  restart statefulset/race-service`, then `deployment/ws-gateway` — each
  resulting in a proper `race_finished` delivered to both clients, *not*
  the silent-hang-until-Redis-TTL symptom already documented for an
  *ungraceful* crash.
- `docker compose`-based `load/multi-instance-check.sh` still passes
  unmodified — this is a new, Kubernetes-hosted verification alongside
  it, not a replacement.

## Explain

- **`k8s-consumer-deploy.md`** — the smallest spec in this phase.
  Depends only on `k8s-core-infra.md` (Postgres/Kafka reachable); no new
  application code (`graceful-shutdown.md` already wired the
  signal-derived context into `c.Run(ctx)`, and `internal/consumer`'s
  fetch loops already drain cleanly on cancellation). Independent of the
  `race-service`/`ws-gateway` StatefulSet/Deployment chain — it's only
  being built last for phase-ordering reasons, not a real dependency.
  `replicas: 1` (a Kafka consumer group already handles rebalancing if
  ever scaled), no `Service` at all (never called into over HTTP), no
  real `readinessProbe`/`livenessProbe` in the usual sense since this
  binary exposes no HTTP surface — confirm at `start` whether an `exec`
  probe is worth adding or whether `restartPolicy: Always`'s own
  crash-detection is a sufficient substitute for a local cluster. Env is
  just `DATABASE_URL`/`KAFKA_BROKERS` — no `INSTANCE_ID`, no `REDIS_URL`,
  no `NATS_URL`, confirmed by reading `cmd/consumer/run.go` directly.
- **`multi-instance-k8s-verification.md`** — the real acceptance test for
  this entire phase, not any single spec's own verification section.
  Proves two things nothing earlier covers in combination: (1)
  cross-gateway room consistency survives being orchestrated by
  Kubernetes itself, not just two hand-run `go run` processes (already
  proven once, informally, during `k8s-ws-gateway-deploy.md`'s own
  cross-gateway test — this spec's job is the *repeatable, scripted*
  version, `REPEAT_RUNS` times); (2) a rolling update actually behaves
  like `graceful-shutdown.md`'s "let in-progress races finish naturally"
  decision, not like the documented ungraceful-crash silent-hang-until-TTL
  gap.
- Since a plain `Deployment`'s pods don't have per-pod stable names the
  way a `StatefulSet`'s do, pinning one participant to each `ws-gateway`
  pod means grabbing the two real pod names via `kubectl get pods -l
  app=ws-gateway` first, then `kubectl port-forward pod/<name>` to each
  individually — not `kubectl port-forward svc/ws-gateway-0`, which
  doesn't exist for a `Deployment`.
- This spec's own script is new, not a straight port of
  `load/multi-instance-check.sh` — reuse that script's `lib/` helpers
  (`ws-client.js`, `reconnect-client.js`, `auth.js`) where the underlying
  HTTP/WS protocol interactions are identical, but drop its
  process-management layer entirely (starting/killing real OS processes)
  since `kubectl`/`kind` already own that lifecycle here.
- **If the rolling-update step fails**, the spec itself calls out two
  distinct possible causes to distinguish between: a manifest/timing bug
  (`terminationGracePeriodSeconds` too tight, or the readiness flip not
  happening before `Shutdown` starts draining — fixable without touching
  `graceful-shutdown.md`'s own logic) versus a real design gap (e.g. no
  continuity between an old pod still finishing a race and a replacement
  pod with a different `INSTANCE_ID`) — the latter would mean revising
  `graceful-shutdown.md` itself, per this project's established
  convention for findings that don't survive a real run.

## Plan

1. `deploy/k8s/consumer/deployment.yaml`: `replicas: 1`, no `Service`,
   env `DATABASE_URL`/`KAFKA_BROKERS` from the shared `ConfigMap`/
   `Secret`, small `resources.requests`/`limits`, `terminationGracePeriodSeconds`
   tighter than `race-service`'s/`ws-gateway`'s (no `http.Server.Shutdown`
   involved). Decide at implementation time whether to add an `exec`
   liveness probe or rely on `restartPolicy: Always`.
2. Apply, confirm `Running`, run a race to completion, verify
   `workout_samples`/`race_participants` rows land in Postgres via
   `kubectl exec`.
3. `kubectl delete pod` on the consumer mid-batch, confirm clean
   resumption from the last committed offset.
4. Write the new Kubernetes-hosted multi-instance verification (script
   location and exact shape TBD at `start` — likely alongside `load/`,
   reusing its k6 `lib/` helpers for the WS legs, `kubectl`/shell for
   orchestration).
5. Run it: `REPEAT_RUNS=6` full lifecycle checks via two distinct
   port-forwarded `ws-gateway` pods, confirming ownership + bus-traffic
   assertions every run.
6. Two rolling-update passes mid-race (`race-service`, then
   `ws-gateway`), confirming a proper `race_finished` both times — not
   the ungraceful-crash symptom.
7. Confirm `load/multi-instance-check.sh` (`docker compose`-based) still
   passes unmodified.

## Notes

- Full specs: `context/features/phase5/k8s-consumer-deploy.md`,
  `context/features/phase5/multi-instance-k8s-verification.md`. Phase
  roadmap: `context/features/phase5/phase-5-plan.md`. These are the last
  two specs in Phase 5 (5/6 and 6/6) — loaded together since the
  verification spec's own topology already assumes `consumer` is
  deployed.
- `k8s-consumer-deploy.md` could have been built any time after
  `k8s-core-infra.md`, independent of the `race-service`/`ws-gateway`
  chain — noted in `phase-5-plan.md`'s own dependency graph, easy to
  forget since most of this phase really is linear.
- Per this project's own established pattern for every prior
  verification script (`k6-load-test.md`, `multi-instance-dev-setup.md`,
  `load/multi-instance-check.md`'s own two rounds of real bugs): expect
  this spec's first live run to find at least one real bug neither this
  plan nor the specs before it anticipated, and document it here or in
  the relevant upstream spec — not silently.
