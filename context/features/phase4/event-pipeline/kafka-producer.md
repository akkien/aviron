# Kafka Event Producer

## Overview

**Recreated, not redesigned — unaffected by the WS Gateway revision.**
This spec's file was deleted along with the rest of the previous Phase 4
set, but `internal/kafka` was never touched and is fully implemented and
shipped. Nothing about whether `race-service` terminates a WebSocket
itself or receives events over `internal/roomrelay`
(`room-message-bus.md`) changes what happens once a `RoomActor` decides
to publish a telemetry sample or a finished race's results — that
decision is made inside `applyEvent`/`finishRace`, which
`room-service-adapter.md` leaves completely untouched. See `docs/
knowledge-summary.md`'s "Event Pipeline: Kafka → Postgres" section for
the full "why Kafka instead of a direct Postgres write" reasoning — not
repeated here, only the concrete shape of what's built.

## Current state (confirmed by reading the code)

`internal/kafka/producer.go` is fully implemented:

```go
const (
    TopicWorkoutSample = "workout.sample"
    TopicRaceFinished  = "race.finished"
)

type Producer struct { /* wraps a *kafka.Writer, Async: true */ }

func NewProducer(brokers []string, logger *slog.Logger) *Producer
func (p *Producer) PublishWorkoutSample(ctx context.Context, raceID, userID string, ts time.Time, wordsCorrect int, paceWatt float64) error
func (p *Producer) PublishRaceFinished(ctx context.Context, raceID string, results []room.ParticipantResult) error
func (p *Producer) PublishRaw(ctx context.Context, topic string, key, value []byte) error
func (p *Producer) Close() error
```

- `kafka.Writer` constructed with `Async: true` — fire-and-forget,
  `WriteMessages` hands off to an in-memory client-side buffer and
  returns immediately, never blocking the caller on a broker round trip.
- Message key is `race_id` for both topics — ordering is preserved
  within a race (`project-overview.md` §6), matching the "message
  ordering" theme the JD emphasizes.
- `PublishRaw` exists specifically so `internal/consumer`
  (`kafka-consumer-postgres-sink.md`) can republish an unprocessable
  message to its own dead-letter topic without needing a second
  `Producer`-shaped type.

## Call sites (unchanged by this revision)

- `RoomActor.applyEvent`'s `TelemetryReceived` case calls
  `PublishWorkoutSample` — fires once per client telemetry frame, same
  as before. `room-service-adapter.md`'s `InboundKindMessage` handling
  feeds `TelemetryReceived` into `applyEvent` exactly the way a local
  `readLoop` used to; this call site inside `applyEvent` itself never
  needed to know or care where the event originated.
- `RaceService.FinishRace` calls `PublishRaceFinished` **after** its own
  synchronous, transactional Postgres write (`races`/`race_participants`/
  `leaderboard_alltime`) already succeeded — never before, never as a
  substitute for it. This ordering is what makes the consumer's own
  handling of `race.finished` a safe reconciliation path rather than a
  second primary writer — see `kafka-consumer-postgres-sink.md`.

## Requirements

Already met by the existing implementation — restated for completeness:

- Both topics keyed by `race_id`, partitioned/ordered accordingly.
- Publish calls never block the room actor's hot path — `Async: true`
  plus this project's existing non-blocking-send discipline
  (`broadcastSnapshot`'s own pattern) means a slow or unavailable broker
  degrades to dropped/delayed publishes, never a stalled `RoomActor.Run()`
  `select` loop.
- `NewProducer`'s `kafka.Writer` is constructed with a `slogErrorLogger`
  adapter so `kafka-go`'s own internal error logging integrates with this
  project's structured logging instead of going to a separate stream.

## Testing

Already covered by the existing test suite — no new tests needed under
this revision, since no call site or payload shape changes.

## Notes

- This spec exists in the recreated set purely so `phase-4-plan.md`'s
  spec list is complete and self-contained again — treat it as
  documentation of already-shipped, unaffected behavior, not a build
  task.
