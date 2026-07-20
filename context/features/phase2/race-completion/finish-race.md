# Race Completion

## Overview

Phase 1 only ever moves a race from `pending` to `active` (`internal/race`'s `StartRace`). Nothing yet takes it to `finished` — context/features/phase1/phase-1-plan.md's own out-of-scope note flags this as "Phase 2's race-completion logic." This spec covers the room actor deciding a race is over and persisting the result to Postgres in one transaction (context/project-overview.md §3, §2 step 4). Depends on the room actor already tracking real per-participant progress via `websocket/ws-endpoint.md`'s telemetry ingestion — this isn't reachable from REST alone.

## Requirements

### Finish Condition

- **Design decision** (not fully specified in context/project-overview.md, decided here): a participant individually "finishes" the moment their `WordsCorrect` reaches `distanceMeters` — matching how a real rowing-machine race works, where everyone completes the set distance at their own pace rather than the race ending the instant one person crosses the line. `applyEvent` (`room-actor-core.md`) records `FinishedAt` and assigns `FinishRank` in finishing order (first to reach the target = rank 1) the moment this happens, as part of normal telemetry processing — no separate polling needed.
- The **race** (not an individual) transitions `active → finished` once every participant is in a terminal state: either `Finished`, or `Left` for good (grace period expired per `reconnection/grace-period.md`, or they disconnected before ever really starting and their grace period lapsed). A participant currently connected-and-racing, or disconnected-but-still-within-grace-period, blocks the race from finishing — they might still cross the line.
- Edge case: a race where every participant leaves without anyone finishing still needs to end (nobody left to finish it) — treat "zero participants remaining active" as the same finish trigger, just with everyone's `FinishRank`/`FinishTimeMs` left `NULL` for anyone who never finished.

### Persistence

- One Postgres transaction, triggered by the room actor once the finish condition is met:
  1. `UPDATE races SET status = 'finished', ended_at = now() WHERE id = $1`
  2. For every participant: `UPDATE race_participants SET finish_rank = $1, finish_time_ms = $2, avg_pace_watt = $3, disconnected_count = $4 WHERE race_id = $5 AND user_id = $6` — these rows already exist from Phase 1's `AddParticipant` (join time), so this is an update, not an insert
  3. Upsert `leaderboard_alltime` per participant: increment `total_races`, add to `total_distance_m` (this race's `distance_meters`, i.e. word count), update `best_2000m_ms` only if this race's `finish_time_ms` beats the existing value (or the row doesn't exist yet) — despite the column name (a holdover from the original fitness-telemetry schema, §13), this project has no fixed 2000m/word-count race type yet, so treat it simply as "best finish time recorded so far" rather than literally filtering by distance
- All three steps commit together or not at all — a race that's `finished` with incomplete `race_participants`/`leaderboard_alltime` rows would be a real data-integrity bug, exactly what a transaction here prevents

### Notifying Clients

- Once the transaction commits, the room actor broadcasts one `race_finished` message (`websocket/protocol.md`) with every participant's final `{user_id, finish_rank, finish_time_ms}`, then triggers its own removal from `room-actor/room-registry.md` and cancels its context — no more ticks needed after this

## Concurrency

- The finish check happens inside `applyEvent`, in the same single-writer goroutine as everything else — there's no separate "checker" goroutine polling for completion, which would just be another way to accidentally violate `room-actor-core.md`'s single-writer principle
- The actual Postgres transaction is I/O and must not block the room actor's `select` loop from processing other events — run it from a helper called by `Run()` once the finish condition is detected, but be deliberate about whether the actor blocks on it or hands it off; blocking is simplest and acceptable here since a finished race no longer needs to process new telemetry anyway (there's nothing left for it to do concurrently)

## Data

```go
func (s *RaceService) FinishRace(ctx context.Context, raceID string, results []ParticipantResult) error {
    // wraps all of races/race_participants/leaderboard_alltime writes in one pgx transaction
}

type ParticipantResult struct {
    UserID            string
    FinishRank        *int    // nil if they never finished
    FinishTimeMs      *int64  // nil if they never finished
    AvgPaceWatt       float64
    DisconnectedCount int
}
```

- Lives in `internal/race` (extends Phase 1's existing `RaceService`/`RaceRepository`, following this project's "extend only when needed" pattern) rather than a new domain package — it's still fundamentally about a `race` row's lifecycle, just triggered by the room actor instead of an HTTP handler.

## Notes

- The Kafka emission described alongside this step in context/project-overview.md §2 ("...and emits an event to Kafka/NATS") is explicitly **out of scope** here — that's Phase 4 (§6). This spec only covers the Postgres transaction.
- `GET /races/{id}` (Phase 1's Race Status endpoint) already returns whatever `status` the race is in — once this feature lands, `status: "finished"` becomes a real, reachable value instead of theoretical.
- `GET /leaderboard` is still out of scope (Phase 3/4) — this spec only writes to `leaderboard_alltime`, it doesn't add anything that reads from it.
