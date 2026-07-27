# Kubernetes — `consumer` Deployment

## Overview

The smallest spec in this phase. Depends on `k8s-core-infra.md`
(Postgres/Kafka reachable) and `graceful-shutdown.md` (the one-line
`cmd/consumer/run.go` fix — wiring a signal-derived context into
`c.Run(ctx)` instead of `context.Background()`; `internal/consumer`'s own
fetch-loop logic already drains cleanly on cancellation, confirmed by
reading `workout_sample_loop.go`/`race_finished_loop.go` directly, so
there's no consumer-side application code left for this spec to add).
Independent of `k8s-race-service-deploy.md`/`k8s-ws-gateway-deploy.md` —
no dependency on either's StatefulSet/Deployment conversion — sequenced
last in `phase-5-plan.md`'s build order only for scope reasons.

## `deployment.yaml`

```text
consumer/
  deployment.yaml
```

- `replicas: 1` — no horizontal-scaling story needed here. A Kafka
  consumer group already handles partition rebalancing if this were ever
  scaled up later (`internal/consumer`'s two `GroupID`s,
  `aviron-consumer-workout-sample`/`aviron-consumer-race-finished`,
  confirmed by reading `consumer.go` directly); nothing about this phase
  requires exercising that.
- No `Service` at all — the consumer is never called into over HTTP, only
  reads from Kafka and writes to Postgres. No `readinessProbe`/
  `livenessProbe` in the usual HTTP sense either: this binary exposes no
  HTTP surface to poll (confirmed by reading `cmd/consumer/run.go` — no
  `http.ListenAndServe` call exists). If Kubernetes still wants some
  liveness signal, an `exec` probe (e.g. checking the process is still
  running via `pgrep`, or a trivial file-touch heartbeat) is the honest
  option rather than inventing an HTTP endpoint this binary has no other
  reason to expose — confirm at `start` whether that's worth adding at
  all, or whether `restartPolicy: Always`'s own crash-detection is a
  sufficient substitute for a side project's local cluster.
- `terminationGracePeriodSeconds` aligned with whatever budget
  `graceful-shutdown.md` implicitly gives this binary — its fetch loops
  already return promptly on context cancellation (no explicit `Shutdown`
  timeout needed the way an `http.Server` needs one), so this can be
  tighter than `race-service`'s/`ws-gateway`'s.
- Env: `DATABASE_URL`/`KAFKA_BROKERS` from the shared `ConfigMap`/`Secret`
  `k8s-core-infra.md` built. No `INSTANCE_ID`, no `REDIS_URL`, no
  `NATS_URL` — confirmed by reading `cmd/consumer/run.go`: this binary
  never touches Redis or NATS.
- `resources.requests`/`limits` — small; this is a batching consumer, not
  a per-request service.

## Dead-letter topics

No new manifest needed — `workout.sample.dlq`/`race.finished.dlq`
(`internal/consumer/consumer.go`'s existing `workoutSampleDLQTopic`/
`raceFinishedDLQTopic` constants) are created the same way the primary
topics are, via `kafka-go`'s producer-side auto-creation, whichever Kafka
chart `k8s-core-infra.md` settles on. Nothing in this spec pre-creates
them explicitly.

## Verification

- `kubectl get pods -n aviron -l app=consumer` shows the one replica
  `Running`.
- Run a race to completion through `ws-gateway`/`race-service` (once
  those specs are also deployed), then confirm `workout_samples` and
  `race_participants` rows actually land in Postgres — `kubectl exec`
  into the Postgres pod (`k8s-core-infra.md`) and query directly, the same
  end-to-end check `kafka-consumer-postgres-sink.md`'s own Verification
  section already established against `docker-compose`.
- `kubectl delete pod` on the consumer pod mid-batch, confirm the
  replacement pod (via `restartPolicy: Always`) picks up from the last
  committed Kafka offset rather than reprocessing or dropping messages —
  the real proof this binary's existing at-least-once, idempotent-write
  design (`kafka-consumer-postgres-sink.md`) survives Kubernetes-driven
  pod churn, not just a graceful `SIGTERM`.
- `go test ./internal/consumer/... -race` unmodified — this spec's only
  change to `cmd/consumer` is the one-line context swap
  `graceful-shutdown.md` already covers; nothing here touches
  `internal/consumer` itself.

## Notes

- This is the one spec in this phase that could plausibly be built any
  time after `k8s-core-infra.md`, in parallel with
  `k8s-race-service-deploy.md`/`k8s-ws-gateway-deploy.md` — noted in
  `phase-5-plan.md`'s dependency graph, repeated here since it's easy to
  assume everything in this phase is strictly linear when most of it is.
