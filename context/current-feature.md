# Current Feature: Kafka Consumer — Postgres Sink

## Status

In Progress

## Goals

- A new `cmd/consumer` binary (separate deployable process from
  `cmd/server`, sharing `internal/config`/`internal/db`) reads
  `workout.sample` and `race.finished` as a real consumer group and
  makes `workout_samples` — a table that has existed since the first
  migration and has never once been written to — real for the first time.
- `workout.sample`: accumulate in memory, flush via `pgx`'s `CopyFrom`
  bulk-insert on either a time window (~3s) or a max batch size (~200
  rows), whichever comes first. Manual offset commits — only after a
  batch durably lands in Postgres — so a crash mid-batch redelivers
  rather than silently losing samples.
- `race.finished`: an idempotent reconciliation `UPDATE ... WHERE
  finish_rank IS NULL` per participant, a safety net for the rare case
  the room actor's own synchronous `FinishRace` write didn't happen —
  not a second primary writer of `race_participants` (that stays
  `RaceService.FinishRace`'s job).
- Malformed-decode or permanently-failing-write messages go to
  `workout.sample.dlq`/`race.finished.dlq`, offset still committed (a
  DLQ'd message would otherwise crash-loop the consumer on the same
  offset forever) — transient write failures (e.g. connection loss)
  don't commit and just redeliver instead.
- `cmd/consumer` joins the existing `backend/Dockerfile`/
  `docker-compose.yml` multi-binary pattern (`command:` override on the
  shared `aviron-backend:local` image), same as `race-router` already
  does.

## Explain

- Spec file: `context/features/phase4/event-pipeline/kafka-consumer-postgres-sink.md`
  — second and final `event-pipeline/` spec; `kafka-producer.md` (shipped
  2026-07-26) is what this consumer reads back out, closing the loop that
  spec's own Notes left open ("ordering/partitioning... confirmed via
  kafka-console-consumer/kcat, not an end-to-end read").
- Re-grounded several of the spec's own claims against the real, current
  schema/dependencies before writing this plan, not assumed from the spec
  text alone:
  - **`workout_samples.race_id` is `TEXT` (base58), not the `UUID` migration
    000001 originally created it as** — migration `000003_shorten_race_id`
    changed it alongside `races.id`/`race_participants.race_id`. The
    spec's own `WorkoutSample` struct sketch (`RaceID, UserID string`)
    already anticipates this correctly; nothing to fix, just confirmed
    rather than assumed.
  - `pgx v5.10.0` (`go.mod`, confirmed): `*pgxpool.Pool` has its own
    `CopyFrom(ctx, pgx.Identifier, columnNames, pgx.CopyFromSource)`
    method directly — `pgx.CopyFromRows([][]any)` is the simplest way to
    build the `CopyFromSource` — no need to check out a raw `*pgx.Conn`
    first. This is the first `CopyFrom` use anywhere in this codebase.
  - `kafka-go`'s `Reader` has both `ReadMessage` (auto-commits when
    `GroupID` is set) and `FetchMessage`+`CommitMessages` (manual) — the
    spec's "manual offset commits, not auto-commit" design maps directly
    onto `FetchMessage`+`CommitMessages`, confirmed via `go doc` rather
    than assumed from memory.
  - `internal/postgres/race_repository.go`'s existing `FinishRace` already
    runs `UPDATE race_participants SET finish_rank = $1, finish_time_ms =
    $2, avg_pace_watt = $3, disconnected_count = $4 WHERE race_id = $5 AND
    user_id = $6` with no `IS NULL` guard (it doesn't need one — it's the
    one-shot primary writer) — confirms the reconciliation UPDATE's shape
    is the same statement plus one extra `AND finish_rank IS NULL` clause,
    not a different query pattern.
  - `internal/postgres` file-naming convention confirmed
    (`auth_repository.go`, `race_repository.go`, `leaderboard_repository.go`)
    → the new file is `workout_sample_repository.go`.
  - `cmd/server`/`cmd/race-router`'s thin-`main`-plus-real-logic-in-`internal`
    split confirmed by reading both — `cmd/consumer/main.go` mirrors this
    exactly.
- **A design decision resolved now, not left as the spec's own open
  question**: `FinishReconciler` (the spec's Data sketch floats either
  `*race.RaceService` or "a smaller reconciler interface") is implemented
  by a **new method on the existing `*postgres.RaceRepository`**, not
  routed through `internal/race.RaceService` — the reconciliation UPDATE
  is pure SQL against a table `RaceRepository` already owns (`FinishRace`
  writes the same table), and `internal/consumer` isn't REST-layered, so
  pulling in the whole `RaceService`/domain-validation stack for one raw
  SQL statement would be a heavier dependency than the operation needs,
  for no benefit (no business logic, just "if not already set, set it").
- **DLQ publishing reuses the existing `internal/kafka.Producer`** rather
  than a second Kafka client: it gains one more generic method (e.g.
  `PublishRaw(ctx, topic string, key, value []byte) error`) alongside its
  existing typed `PublishWorkoutSample`/`PublishRaceFinished`, since a DLQ
  message is just the original malformed/failing payload republished
  verbatim to a different topic.
- Kafka broker/topics already exist (`kafka-producer.md`): the Docker
  Compose `kafka` service's internal listener is `kafka:29092` — this
  consumer's `docker-compose.yml` service reuses the same
  `KAFKA_BROKERS=kafka:29092` value `server-a`/`server-b` already use.

## Plan

1. `cmd/consumer/main.go` — thin entrypoint mirroring `cmd/server`:
   `cfg := config.Load()`, then `consumer.Run(cfg)` (or equivalent) in a
   new `internal/consumer` package. No new `internal/config` fields
   needed — `DatabaseURL`/`KafkaBrokers` already exist.
2. `internal/postgres/workout_sample_repository.go` —
   `WorkoutSampleRepository.InsertBatch(ctx, samples []consumer.WorkoutSample) error`
   via `pool.CopyFrom(ctx, pgx.Identifier{"workout_samples"}, []string{"race_id",
   "user_id", "ts", "distance_m", "pace_watt"}, pgx.CopyFromRows(...))`
   (`stroke_rate` stays unwritten/`NULL`, per project-overview.md §13).
3. `internal/postgres/race_repository.go` — new
   `ReconcileParticipantResults(ctx, raceID string, results []room.RaceResultJSON) error`,
   one `UPDATE race_participants SET finish_rank = $1, finish_time_ms =
   $2, avg_pace_watt = $3 WHERE race_id = $4 AND user_id = $5 AND
   finish_rank IS NULL` per result — confirm at `start` whether to loop
   per-result in a transaction (matching `FinishRace`'s own shape) or a
   single multi-row statement; a transaction loop is simpler and this
   path is low-volume (one message per race), so likely no real reason to
   optimize further.
4. `internal/kafka/producer.go` — add `PublishRaw(ctx, topic string, key,
   value []byte) error` for DLQ republishing, reusing the existing
   `*Producer`'s Writer.
5. New `internal/consumer` package (not REST-layered):
   - `WorkoutSampleWriter`/`FinishReconciler` interfaces + `WorkoutSample`
     struct (per the spec's own Data sketch).
   - `Consumer`/`NewConsumer(brokers []string, writer WorkoutSampleWriter,
     reconciler FinishReconciler, dlqPublisher, logger) *Consumer`,
     `Run(ctx) error` starting two independent goroutines (one per topic,
     own `kafka.Reader` with `GroupID` set), each `FetchMessage`-loops
     with its own local batch buffer.
   - `workout.sample` loop: accumulate, flush on time-window-or-size,
     `CommitMessages` only after a successful `InsertBatch`; a decode
     failure → DLQ + commit; a write failure → no commit, let redelivery
     retry (confirm at `start` which Postgres errors are permanent
     vs. transient — a nonexistent-`race_id` FK violation is the likely
     permanent case).
   - `race.finished` loop: decode → `ReconcileParticipantResults`, commit
     immediately (no batching — one message per race).
   - Graceful shutdown: `ctx` cancellation flushes each loop's
     in-progress batch before exiting, per the spec's Concurrency section
     (same context-cancellation pattern this codebase already uses
     everywhere else, applied to a consumer loop instead of a room
     actor).
6. `backend/Dockerfile` — add `go build -o /out/consumer ./cmd/consumer`
   alongside the existing `server`/`race-router` builds, `COPY` into the
   final image.
7. `docker-compose.yml` — new `consumer` service: same shared
   `aviron-backend:local` image, `command: ["/app/consumer"]`,
   `KAFKA_BROKERS=kafka:29092`/`DATABASE_URL` env, `depends_on: postgres:
   condition: service_healthy` + `kafka: condition: service_healthy` (no
   healthcheck of its own — no HTTP surface to poll, matching
   `race-router`'s already-accepted precedent).
8. Verify: `go build ./...`/`go test ./...` clean; `internal/consumer`
   unit-tested against fake `WorkoutSampleWriter`/`FinishReconciler`
   (batching/windowing/DLQ-routing logic, no real Kafka needed for those);
   confirm at `start` whether at least one real produce-then-consume test
   needs a live local broker (no `miniredis`-equivalent fake exists for
   Kafka). Live end-to-end check, deliberately deferred from
   `kafka-producer.md`: `docker compose up` the full stack, drive a real
   race to completion, confirm `workout_samples` actually gets rows and
   `race_participants` reconciliation is a no-op against data the
   synchronous path already wrote correctly.

## Notes

- No new Go dependency — `segmentio/kafka-go` already added by
  `kafka-producer.md`.
- `workout_samples.stroke_rate` stays unwritten (always `NULL`) — unused
  for this project's typing-race mechanic (project-overview.md §13).
- Explicitly out of scope, per the spec: any query *reading*
  `workout_samples` (a "PR history"/"pace-over-time" chart) — this spec
  only makes the table real, nothing reads from it yet. A candidate for a
  future, separate feature.
- This closes out Phase 4's entire `event-pipeline/` sub-area — per
  `phase-4-plan.md`, `horizontal-scaling/` was already fully shipped, so
  once this lands, Phase 4 is complete in full.
