# Kafka Event Producer

## Overview

`context/project-overview.md` §6: the Race Service publishes to two
topics, `workout.sample` (telemetry) and `race.finished` (final results),
"split... so consumers can scale independently and schemas don't get mixed
up," keyed by `race_id` or `user_id` "to ensure messages for the same
entity land in the same partition → preserving ordering within that
entity." This spec is the producing side only —
`kafka-consumer-postgres-sink.md` is what reads these topics back out.

Independent of `horizontal-scaling/` — a room actor publishing to Kafka
doesn't care which instance owns the room, it only publishes its own
events regardless. Sequenced after `horizontal-scaling/` in
`phase-4-plan.md` purely by this project's own convention of finishing one
sub-area before starting the next, not a real dependency.

## Design

### One shared `*kafka.Writer`, not a goroutine per room

`segmentio/kafka-go`'s `Writer` already does its own internal batching and
supports `Async: true` (fire-and-forget, non-blocking `WriteMessages`
calls) — this directly satisfies both of §3's requirements at once
("batch insert... instead of every tick" and the room actor's own
non-blocking-send discipline, `broadcastSnapshot`'s existing pattern) with
no hand-rolled ticker or buffer inside `RoomActor`. One process-wide
`*kafka.Writer` per topic (or one multi-topic `Writer`, selecting `Topic`
per-message — confirm which `kafka-go` supports more cleanly at `start`),
constructed once in `internal/app.go`, not per-room.

This means `RoomActor.applyEvent`'s `TelemetryReceived` case
(`internal/room/room.go`, ~line 302) publishes one `workout.sample` message
**per telemetry event as it happens** — not batched client-side into a
slow ticker — while the *Postgres write* the consumer eventually does is
where the actual "every 2-5s, not every event" batching from §3 happens.
Producer = real-time publish, consumer = batched write. This is a
deliberate reinterpretation of §3's "batch insert... every ~2-5s" wording,
worth confirming at `load`/`start`: the alternative (a slow ticker inside
`RoomActor` batching several samples into one Kafka message) would add a
new per-room goroutine and buffer for a problem `kafka-go`'s own
`Async`+batch-size/batch-timeout config already solves at the producer
level, so the simpler reading is preferred.

### The `EventPublisher` interface

Same structural-interface-in-`internal/room` pattern this codebase already
uses three times (`RaceFinisher`, `RaceLeaver`, `RaceCanceller`,
`internal/room/finish.go`) — for the same reason: the concrete
implementation needs to live in a new `internal/kafka` package, and
`internal/room` must not import it directly (`internal/room` importing
`internal/kafka` would be fine on its own, but defining the interface at
the consumer keeps every one of these seams consistent and reviewable the
same way).

```go
// internal/room/events.go
type EventPublisher interface {
    PublishWorkoutSample(ctx context.Context, raceID, userID string, ts time.Time, wordsCorrect int, paceWatt float64) error
    PublishRaceFinished(ctx context.Context, raceID string, results []ParticipantResult) error
}
```

- `PublishRaceFinished` reuses the existing `ParticipantResult` type
  (`internal/room/finish.go`) rather than a parallel Kafka-specific struct
  — one shape for "a race's final results," used by both `RaceFinisher`
  (Postgres) and `EventPublisher` (Kafka) call sites inside `finishRace`.
- Like `RoomLocator`/`TickObserver`, this is a **`Registry`-level**
  dependency, not a per-`Spawn` argument: `NewRegistry(logger,
  tickObserver, locator, publisher)` — every room gets the same
  process-wide publisher, consistent with how `TickObserver`/`RoomLocator`
  were threaded in the two specs before this one, and avoids growing
  `Registry.Spawn`'s already-long parameter list further.
- A `NoopPublisher` (mirrors `NoopLocator` from `redis-room-registry.md`)
  for single-instance/no-Kafka local dev and every existing test fixture
  that constructs a `Registry` — same mechanical-churn tradeoff already
  paid twice this phase.

### Call sites

- `applyEvent`'s `TelemetryReceived` case: after updating
  `ParticipantState` (unchanged), call
  `r.publisher.PublishWorkoutSample(ctx, r.id, ev.UserID, time.Now(),
  p.WordsCorrect, p.PaceWatt)` — non-blocking given `Async: true`, so this
  doesn't change `applyEvent`'s existing "never blocks the single-writer
  loop" property.
- `finishRace` (`internal/room/room.go`, ~line 573): after the existing
  `finisher.FinishRace(...)` Postgres call succeeds (same ordering
  `race-completion/finish-race.md` already established — persist before
  notify), call `r.publisher.PublishRaceFinished(ctx, r.id, results)`. If
  the Postgres write itself fails, do **not** publish — a `race.finished`
  event for a race that didn't actually finish in the source of truth
  would be a real correctness gap, not just a missed nice-to-have.
- Both calls: log-and-continue on error, no retry — same no-retry
  precedent `finishRace`'s own Postgres write and `leave_race`'s
  fire-and-forget persist already established in this codebase. A dropped
  Kafka publish here is strictly less severe than either of those, since
  nothing downstream of Kafka is this project's system of record (Postgres
  still is — see `kafka-consumer-postgres-sink.md`'s Overview for why).

### Message keys and topic naming

- Both topics keyed by **`race_id`**, not `user_id` — confirm at `start`,
  this is a real judgment call §6 leaves open ("depending on the query
  pattern you want to optimize for"). Reasoning: `workout_samples`'s
  existing index is `(race_id, user_id, ts)` — race-scoped queries are the
  primary access pattern already established by the schema — and keying
  by `race_id` also means every sample for a given race lands in one
  partition, which is a natural, ready-made batching unit for the consumer
  (see next spec) with zero extra bookkeeping. `user_id`-keying would
  scale better for a user with many races across many partitions, but this
  project has no per-user-across-races query that would benefit, and the
  schema's own index ordering already signals which axis matters more
  here.
- Topic names exactly as §6 specifies: `workout.sample`, `race.finished`.
  New `Config` fields: `KafkaBrokers string` (comma-separated, env
  `KAFKA_BROKERS`, default `localhost:9092`).

## Concurrency

- `kafka.Writer` is documented safe for concurrent `WriteMessages` calls
  from multiple goroutines — every room actor's own single goroutine calls
  into the same shared `Writer`, which is exactly this safe-for-concurrent-
  use case, not a violation of any single-writer principle (the *room
  state* still has exactly one writer; the *Kafka client* is a shared,
  externally-synchronized resource, the same category `*pgxpool.Pool`
  already is for this codebase's Postgres writes).
- `Async: true` means `WriteMessages` returns before the broker round trip
  completes — errors surface via the `Writer`'s configured `Completion`
  callback or `ErrorLogger`, not the call's own return value. Confirm at
  `start` how those get wired into `structured-logging.md`'s existing
  `slog.Logger` (a `Writer.Logger`/`Writer.ErrorLogger` adapter, likely a
  small `slog`-backed shim implementing `kafka.Logger`'s `Printf`-shaped
  interface).

## Data

```go
// internal/room/events.go
type EventPublisher interface {
    PublishWorkoutSample(ctx context.Context, raceID, userID string, ts time.Time, wordsCorrect int, paceWatt float64) error
    PublishRaceFinished(ctx context.Context, raceID string, results []ParticipantResult) error
}
type NoopPublisher struct{}

// internal/kafka/producer.go
type Producer struct { /* *kafka.Writer(s) */ }
func NewProducer(brokers []string) *Producer
func (p *Producer) PublishWorkoutSample(...) error
func (p *Producer) PublishRaceFinished(...) error
func (p *Producer) Close() error
```

## Notes

- New dependency: `github.com/segmentio/kafka-go`, per
  `project-overview.md` §11's suggestion.
- No DLQ handling in this spec — §6's dead-letter-topic idea is about a
  **consumer** failing to parse/write a message, which only exists once a
  consumer exists (`kafka-consumer-postgres-sink.md`). A producer with a
  well-typed `EventPublisher` interface has nothing to dead-letter on its
  own side.
- No local Kafka broker exists in `docker-compose.yml` yet — this spec
  needs one added (a single-broker KRaft-mode image, e.g.
  `bitnami/kafka` or `apache/kafka`, no Zookeeper — confirm the exact
  image at `start`; project-overview.md §11 already steers away from
  hand-managing Zookeeper/brokers).
- This spec's own verification can't fully confirm ordering/partitioning
  end to end without a consumer reading the topic back — a reasonable
  interim check is inspecting messages directly via `kafka-console-consumer`
  (or `kcat`) against the local broker, confirming the right topic/key/
  payload shape, before `kafka-consumer-postgres-sink.md` builds the real
  consumer.
