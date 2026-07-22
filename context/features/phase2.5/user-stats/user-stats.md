# User Stats (Backend for Dashboard Stat Cards)

## Overview

`dashboard.md` ships `StatCards.tsx` as a 3-card row (Races Joined, Races Won, Avg WPM) with hardcoded values, explicitly flagged there as a placeholder pending real backend support. The mockup's 4th card (Avg Accuracy) was never built at all — the server never validates typed text (`context/project-overview.md` §13's trust model), so there's no server-side notion of "correct vs incorrect" to average without adding a new self-reported client metric, which is out of scope here. This spec builds the real backend support for all 3 cards `StatCards.tsx` already has.

Unlike the rest of Phase 2.5, this is primarily a **backend** spec (new migration, a fixed pre-existing gap, a new domain package) with one small frontend piece (swap `StatCards.tsx`'s hardcoded source for a real fetch).

## Requirements

### Races Joined — already tracked, no change needed

`leaderboard_alltime.total_races` is already incremented inside `RaceRepository.FinishRace`'s existing transaction (`race-completion/finish-race.md`), once per participant per finished race. Read directly, no new column.

**Known nuance, not fixed here**: this counts races that reached a finish transaction while the room actor knew about the participant (i.e., they sent `join_race` over WS at least once) — someone who registers via `POST /races/{id}/join` but never opens the WebSocket is invisible to the room actor and never gets a finish row at all (already-documented behavior, `race-completion/finish-race.md`). "Races Joined" is therefore closer to "races actually played" than "races registered for." Acceptable, not a new gap introduced by this spec.

### Races Won — new column, incrementally maintained

- Migration: `ALTER TABLE leaderboard_alltime ADD COLUMN total_wins INT NOT NULL DEFAULT 0`.
- `RaceRepository.FinishRace`'s existing per-participant upsert (`internal/postgres/race_repository.go`) gains one more increment, matching the exact pattern `total_races`/`total_distance_m` already use: `total_wins = total_wins + CASE WHEN EXCLUDED.<rank-is-1> THEN 1 ELSE 0 END` (or pass a `won bool`/`1`/`0` param computed from `res.FinishRank != nil && *res.FinishRank == 1` before the query, simpler than an in-SQL case on a value not otherwise passed) — same transaction, same row, no new query.

### Avg WPM — fixes a known, already-disclosed gap; no new column

`race_participants.avg_pace_watt` and `leaderboard_alltime` (via a new running-sum column) are both real, but `AvgPaceWatt` has written `0.0` unconditionally since `race-completion/finish-race.md` shipped — the room actor never forwarded `pace_watt` from telemetry into anything (`internal/room/room.go`'s comment at the `AvgPaceWatt` line names this exact gap). Fixing it end-to-end:

1. `internal/room/events.go`: `TelemetryReceived` gains a `PaceWatt float64` field.
2. `internal/ws/protocol.go`: `toRoomEvent`'s `telemetry` case forwards `m.PaceWatt` into `TelemetryReceived{... PaceWatt: m.PaceWatt}` — `ClientMessage.PaceWatt` is already decoded off the wire today, it's just dropped at this one line.
3. `internal/room/room.go`: `ParticipantState` gains a `PaceWatt float64` field, updated in `applyEvent`'s `TelemetryReceived` case alongside `WordsCorrect`/`LastSeq`.
4. `buildParticipantResult`: `AvgPaceWatt: p.PaceWatt` instead of the zero value — **use the participant's latest reported `pace_watt` at finish time, not a new server-side average.** `frontend-realtime/websocket-client.md`'s `TypingView` already computes `pace_watt` client-side as a *cumulative* average WPM from race start to the current word (`wordsCompleted / elapsedMinutes`) and sends it with every `telemetry` message — the latest value received already *is* "this participant's average WPM for the race so far." No new averaging logic needed server-side, just wiring the already-computed value through instead of discarding it.
5. Migration: `ALTER TABLE leaderboard_alltime ADD COLUMN total_pace_watt_sum NUMERIC NOT NULL DEFAULT 0`, incremented by `res.AvgPaceWatt` in the same `FinishRace` upsert (matching `total_distance_m`'s existing pattern) — read-time "Avg WPM" is `total_pace_watt_sum / total_races` (cheap division, no scan), consistent with how every other `leaderboard_alltime` stat is a maintained running counter rather than a live aggregate query.

### New endpoint: `GET /leaderboard/me`

- New domain package `internal/leaderboard` (not bolted onto `internal/race` — `leaderboard_alltime` is conceptually its own domain). Follows the standard Handler/Service/Repository layering (`coding-standards.md`) like every other REST domain in this codebase — `LeaderboardHandler`/`LeaderboardService`/`LeaderboardRepository`, `NewLeaderboardHandler`/`NewLeaderboardService`, a `postgres.LeaderboardRepository` implementation.
- `GET /leaderboard/me` (authenticated, wrapped in `middleware.Auth` like every other real endpoint): returns the caller's own `{races_joined, races_won, avg_wpm}` — a single-row lookup (`SELECT total_races, total_wins, total_pace_watt_sum FROM leaderboard_alltime WHERE user_id = $1`, dividing `total_pace_watt_sum / total_races` in Go, guarding the zero-races divide). A user with no `leaderboard_alltime` row yet (never finished a race) gets all-zero stats, not a 404 — this is a normal, expected state for a new account, not an error.
- This is the only leaderboard endpoint this project has — a public ranked/windowed list across all users was considered and explicitly dropped, not deferred; per-user stats for one's own dashboard is the whole scope.

### Frontend wiring

- `StatCards.tsx` (from `dashboard.md`, already a 3-card row): fetch its 3 values from `GET /leaderboard/me` instead of the hardcoded constant. Same loading-state conventions the rest of the app already uses (`RaceStatusView`'s `raceDetail === null ? "Loading..." : ...}` pattern).
- New type in `frontend/src/types/`: `LeaderboardMeResponse { races_joined: number; races_won: number; avg_wpm: number }`, mirroring the Go DTO.

## Validation

- No new client-side validation.
- Server-side: `GET /leaderboard/me` requires auth (401 if missing/invalid), same as every other authenticated endpoint. No path/body params to validate.

## Data

```go
// internal/leaderboard/dtos.go
type LeaderboardMeResponse struct {
    RacesJoined int     `json:"races_joined"`
    RacesWon    int     `json:"races_won"`
    AvgWPM      float64 `json:"avg_wpm"`
}
```

```sql
-- new migration
ALTER TABLE leaderboard_alltime ADD COLUMN total_wins INT NOT NULL DEFAULT 0;
ALTER TABLE leaderboard_alltime ADD COLUMN total_pace_watt_sum NUMERIC NOT NULL DEFAULT 0;
```

## Notes

- Depends on `dashboard.md`'s `StatCards.tsx` existing (this spec replaces its hardcoded data source, doesn't build the component from scratch).
- Depends on `race-completion/finish-race.md` (done) — extends its existing `FinishRace` transaction rather than adding a new write path.
- Closes a real, previously-disclosed gap (`AvgPaceWatt` always `0.0`) as a side effect, not just new work — worth calling out at `complete` since it fixes something flagged as a known limitation in an earlier feature's History entry.
- No changes to `dashboard.md`'s `OpenRacesList.tsx` — that stays hardcoded/decorative; this spec is scoped to "statistic" per the explicit request, not the open-races list.
