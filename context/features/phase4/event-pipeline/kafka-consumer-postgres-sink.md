# Kafka Consumer — Postgres Sink

## Overview

**Recreated, not redesigned — unaffected by the WS Gateway revision.**
Same status as `kafka-producer.md`: this spec's file was deleted along
with the rest of the previous Phase 4 set, but `internal/consumer` and
`cmd/consumer` were never touched and are fully implemented and shipped.
`workout.sample`/`race.finished` land on these topics the same way
regardless of which tier terminates the client's WebSocket — see `docs/
knowledge-summary.md`'s "Event Pipeline: Kafka → Postgres" section for
the full reasoning, not repeated here.

## Current state (confirmed by reading the code)

`internal/consumer` (`consumer.go`, `batch.go`, `decode.go`,
`workout_sample_loop.go`, `race_finished_loop.go`) plus `cmd/consumer`
are fully implemented:

- **Two independent reader loops, two distinct consumer groups**
  (`aviron-consumer-workout-sample`, `aviron-consumer-race-finished`) —
  deliberately not one shared `GroupID` across both topics. This was a
  real bug this feature's own live verification caught (documented in
  `context/feature-history.md`): a shared `GroupID` across two different
  topic subscriptions makes Kafka's consumer-group rebalance protocol
  treat both readers as confused members of one group, silently starving
  at least one of them. Fixed, verified live, and worth preserving as a
  concrete reminder in this recreated spec, not just a resolved incident.
- **`workout.sample` batches before writing**: `sampleBatch` (pure state,
  no I/O) accumulates decoded samples and flushes on whichever comes
  first — `flushBatchSize = 200` rows or `flushInterval = 3 *
  time.Second` — into a single `pgx.CopyFrom` bulk insert via
  `WorkoutSampleWriter`.
- **`race.finished` is a narrow, idempotent reconciliation, not a second
  primary writer**: `FinishReconciler.ReconcileParticipantResults`
  targets `WHERE finish_rank IS NULL` — a safety net for the rare case
  `RaceService.FinishRace`'s own synchronous transactional write didn't
  happen, not a duplicate write path. Every-message-processed-twice
  safety comes from that `WHERE` clause, not from any Kafka-level
  exactly-once mechanism.
- **Dead-letter topics**: `workout.sample.dlq`/`race.finished.dlq` —
  an unprocessable message (malformed JSON, `decodeWorkoutSample`/
  `decodeRaceFinished` failure) gets republished via `DLQPublisher.
  PublishRaw` rather than crash-looping the consumer or being silently
  dropped.
- `fetchPollTimeout` bounds each `FetchMessage` call so the two reader
  loops stay responsive to shutdown signals rather than blocking
  indefinitely on an idle topic.

## Requirements

Already met by the existing implementation — restated for completeness,
not as a build task:

- Batch insert via `pgx`'s `CopyFrom`, never row-by-row — matches
  `project-overview.md` §3's explicit steer for `workout_samples`.
- Consumer group semantics (not a plain topic subscription) so multiple
  consumer processes could join the same group and share partitions if
  telemetry volume ever outgrows one process — `project-overview.md` §6's
  "a dedicated Go consumer group... so consumers can scale
  independently."
- Graceful shutdown: `kafka-consumer-postgres-sink.md`'s original
  Concurrency section (this recreation preserves the same requirement)
  anticipated `k8s-race-service-deploy.md`'s later graceful-shutdown work
  — both reader loops need to watch a signal-derived context and stop
  cleanly, flushing any in-flight batch rather than dropping it. Confirm
  at `start` whether this is already wired (`cmd/consumer`'s current
  `main.go`) or still open — `context/features/phase5/` (where this was
  originally going to be finished, alongside `cmd/server`'s equivalent
  gap) is deleted and not yet recreated, so this may still be a real,
  open gap rather than a finished one; re-verify against `cmd/consumer/
  main.go` directly before assuming either way.

## Testing

Already covered by the existing test suite (`batch_test.go`,
`decode_test.go`, `workout_sample_loop_test.go`,
`race_finished_loop_test.go`, using `fakeWriter`/`fakeReconciler`/
`fakeDLQ`/`fakeCommitter` — this project's existing fake-dependency
testing convention) — no new tests needed under this revision.

## Notes

- This spec exists in the recreated set purely so `phase-4-plan.md`'s
  spec list is complete and self-contained again — treat it as
  documentation of already-shipped, unaffected behavior, not a build
  task, with the one exception flagged above (graceful shutdown) worth a
  real look rather than an assumption.
