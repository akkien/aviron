# Kafka Consumer — Postgres Sink (ClickHouse Dropped)

## Overview

`context/project-overview.md` §6 has the consumer group write into
ClickHouse, "a wide table optimized for queries like 'top N this week/
month', 'personal PR'." **This project drops ClickHouse entirely** (explicit
instruction) — this spec is what the consumer does instead, and why that's
not just "the same thing, pointed at Postgres."

### Why Postgres, and why this isn't a dual-write hazard

`workout_samples` (`backend/migrations/000001_init_schema.up.sql`) has
existed since this project's very first migration and **has never once
been written to** — confirmed by grep across the entire backend before
writing this spec, not assumed from the schema alone. This consumer is
what makes that table real for the first time: it's a genuinely new
capability, not a duplicate of something Postgres already does
synchronously.

`race_participants`/`races`/`leaderboard_alltime`, by contrast, are
already written synchronously and transactionally by
`RaceService.FinishRace` the instant a race actually finishes
(`race-completion/finish-race.md`, still the system of record — this phase
does not change that). Having this consumer *also* write final results
from `race.finished` would be a real dual-write correctness risk (two
independent paths racing to write the same row) for zero benefit, since
nothing downstream needs it to arrive that way. Instead, this consumer's
`race.finished` handler does an **idempotent reconciliation UPSERT**
(`ON CONFLICT (race_id, user_id) DO NOTHING`) — a genuine safety net for
the case the synchronous write somehow didn't happen (e.g., the process
crashed between the room actor's Postgres commit and returning, or between
committing and publishing — see Notes), not a second primary write path.
This gives the consumer a real, honest reason to exist for both topics
instead of `race.finished` being a no-op consumer added just to say the
topic is "handled."

## Design

### A separate binary: `cmd/consumer`

Per §6, "a dedicated Go consumer group... so consumers can scale
independently" — this only actually means something if the consumer is a
separate deployable process from `cmd/server`, not a goroutine embedded in
the same binary. New `cmd/consumer/main.go`, sharing `internal/config` and
`internal/db` with `cmd/server` but running its own `main`/lifecycle.

**Deployment, per the Dockerize feature's (2026-07-26) now-established
precedent**: `cmd/consumer` joins `backend/Dockerfile`'s existing
multi-stage build alongside `cmd/server`/`cmd/race-router` (one shared
`aviron-backend:local` image, not a new Dockerfile), and gets its own
`docker-compose.yml` service with `command: ["/app/consumer"]` — exactly
the pattern `race-router`'s Compose service already uses to pick its own
binary out of the same image. Confirm at `start` what a meaningful
`healthcheck:` looks like for a Kafka consumer group (unlike `server-a`/
`server-b`'s `GET /healthz`, this process has no HTTP surface to poll —
a liveness signal would have to come from somewhere else, e.g. a small
internal `/healthz` the consumer serves anyway, or accept it has none and
relies on `depends_on: condition: service_started` against `kafka`
instead, matching `race-router`'s own precedent of having no meaningful
healthcheck of its own).

### `internal/consumer` package

Not REST-layered (same exemption `internal/room` already has, for the same
reason: no HTTP request/response shape here to justify
Handler/Service/Repository). Holds:

- A `kafka.Reader` per topic, `GroupID` set (consumer-group semantics —
  `kafka-go` handles partition assignment/rebalancing), reading
  `workout.sample` and `race.finished`.
- **Manual offset commits, not auto-commit** — `Reader.CommitMessages`
  called only after a batch is durably written to Postgres, so a crash
  mid-batch redelivers rather than silently losing samples. This is
  at-least-once, not exactly-once: a redelivered `workout.sample` batch
  after a crash-before-commit could double-insert a few rows into
  `workout_samples`. Accepted, documented gap — `workout_samples` is a
  telemetry log, not a balance or a count anything else derives from
  today, so a handful of duplicate rows after a rare crash is a
  proportionate risk to accept rather than building exactly-once delivery
  (`kafka-go` doesn't make that easy without transactional producers,
  which is real added complexity for a side project). `race.finished`'s
  `DO NOTHING` UPSERT is naturally idempotent regardless, so redelivery
  there is a non-issue by construction.

### `workout.sample` handling — time-windowed batch insert

- Accumulate consumed samples in memory; flush on **either** a time window
  (e.g. 3s, matching §3's "every ~2-5s" guidance) **or** a max batch size
  (e.g. 200 rows) — whichever comes first, so a burst of activity doesn't
  wait the full window and a quiet period doesn't wait forever holding a
  half-empty batch (bounded by the time window either way).
- Flush via `pgx`'s `CopyFrom` (bulk-load path, not N individual `INSERT`s
  — this is exactly the "batch insert (`COPY` or multi-row insert...)"
  §3 calls for), through a new `internal/postgres.WorkoutSampleRepository`
  satisfying a small interface defined in `internal/consumer`:

  ```go
  // internal/consumer/consumer.go
  type WorkoutSampleWriter interface {
      InsertBatch(ctx context.Context, samples []WorkoutSample) error
  }
  type WorkoutSample struct {
      RaceID, UserID string
      Ts             time.Time
      DistanceM      float64 // words correct so far, per project-overview.md §13
      PaceWatt       float64
  }
  ```

- On a flush failure (not a parse failure — a real Postgres error, e.g.
  connection loss), **do not commit offsets** — the same batch redelivers
  on the next poll after `kafka-go`'s own retry/backoff, no bespoke retry
  loop needed in this package.

### `race.finished` handling — idempotent reconciliation

- One handler per consumed message: decode → for each `ParticipantResult`
  in the payload, `UPDATE race_participants SET finish_rank = ...,
  finish_time_ms = ..., avg_pace_watt = ... WHERE race_id = $1 AND user_id
  = $2 AND finish_rank IS NULL` (an `UPDATE ... WHERE ... IS NULL` reads
  more naturally here than an `INSERT ... ON CONFLICT DO NOTHING`, since
  the row from `JoinRace`'s `AddParticipant` already exists by the time a
  race finishes — there's no insert to conflict with, only a
  maybe-already-filled-in row to leave alone. Confirm the exact SQL shape
  at `start`, but the "no-op if already reconciled" property is the actual
  requirement, whichever statement shape expresses it most simply).
- Commit offset immediately after (no batching needed here — `race.finished`
  volume is orders of magnitude lower than `workout.sample`, one message
  per race, batching would add complexity for no real benefit).

### Malformed / failing messages — dead-letter topics

Per §6: "Consider adding a Dead Letter Topic (`*.dlq`) for messages that
fail to parse/write." Two new topics, `workout.sample.dlq` and
`race.finished.dlq`:

- A message that fails to **decode** (malformed JSON — shouldn't happen
  given this project's own producer is the only writer to these topics,
  but a consumer must not assume its producer is the only thing that will
  ever exist on this topic) is published to the matching `.dlq` topic
  verbatim, logged at `Warn`, and its offset is still committed — a
  malformed message will never become parseable by retrying it, so leaving
  it stuck in the main topic would crash-loop the consumer forever on the
  same offset instead of making progress.
- A message that decodes fine but fails to **write** for a reason that
  won't resolve on its own (e.g. a genuine constraint violation, not a
  transient connection error) — same DLQ treatment. Distinguishing
  "transient, worth blocking-and-retrying" (don't commit, let redelivery
  retry) from "permanent, worth dead-lettering" (commit, DLQ, move on) is
  a real judgment call per error type — confirm at `start` which Postgres
  errors this project can realistically hit here (a nonexistent `race_id`
  foreign-key violation is the most likely one, and is permanent, not
  transient) and route accordingly, rather than treating every write
  failure as transient by default.

## Concurrency

- Two independent reader loops (one per topic), each its own goroutine,
  each with its own consumer-group membership — no shared mutable state
  between them, so no new synchronization primitive is needed beyond what
  each loop's own local batch buffer requires (owned entirely by that
  loop's goroutine, never touched from outside it — same single-writer-
  per-goroutine shape this codebase already uses everywhere else, just
  applied to a Kafka consumer loop instead of a room actor).
- Graceful shutdown: `cmd/consumer`'s `main` needs the same `SIGTERM`
  handling `context/features/phase5/k8s-race-service-deploy.md` will
  require of `cmd/server` — a context cancelled on signal, each reader
  loop flushing its current in-progress batch (if any) before exiting
  rather than dropping it. Worth building this consumer's shutdown
  handling with that spec's requirements already in mind, even though
  this spec ships well before it (Phase 5 depends on the whole of Phase 4
  being done first).

## Data

```go
// internal/consumer/consumer.go
type Consumer struct { /* two *kafka.Reader, WorkoutSampleWriter, *race.RaceService (or a smaller reconciler interface) */ }
func NewConsumer(brokers []string, writer WorkoutSampleWriter, reconciler FinishReconciler) *Consumer
func (c *Consumer) Run(ctx context.Context) error // runs both loops, returns when ctx is done

// internal/postgres/workout_sample_repository.go
type WorkoutSampleRepository struct { /* *pgxpool.Pool */ }
func (r *WorkoutSampleRepository) InsertBatch(ctx context.Context, samples []consumer.WorkoutSample) error // pgx.CopyFrom
```

## Testing

- `internal/consumer`'s batching/windowing logic (flush-on-size,
  flush-on-timer, DLQ routing decisions) should be unit-testable against
  fake `WorkoutSampleWriter`/`FinishReconciler` implementations, same
  testability rationale `coding-standards.md` already states for the
  REST-domain `<Domain>Repository` interfaces — this package isn't
  REST-layered, but the "test against a fake, not real infra" principle
  still applies.
- An integration-style test (real `miniredis`-equivalent for Kafka doesn't
  really exist the same way — confirm at `start` whether a real local
  Kafka broker is required for at least one end-to-end
  produce-then-consume test, accepting that one test in this package may
  need real infra where the rest of this codebase's tests don't, given
  Kafka has no lightweight in-memory fake as mature as `miniredis`).

## Notes

- New dependency already added by `kafka-producer.md`
  (`github.com/segmentio/kafka-go`) — this spec doesn't add a new one.
- `workout_samples.stroke_rate` stays unwritten (always `NULL`) — per
  `project-overview.md` §13, it's explicitly unused for this project's
  typing-race mechanic.
- Explicitly out of scope: any query *reading* `workout_samples` (a "PR
  history" or "pace-over-time" chart) — this spec only makes the table
  real, it doesn't add anything to `GET /leaderboard/me` or the frontend
  that reads from it. A candidate for a future, separate feature once this
  data actually exists to query.
