# Race Status

## Overview

Anyone authenticated can check a race's current status and who's in it. This powers the frontend's create/join page, showing what a race looks like before Phase 2 makes it live.

## Requirements

### Endpoint

- `GET /races/{id}` (requires JWT auth — any authenticated user, not just participants)
- `200` response:

  ```json
  {
    "id": "uuid",
    "name": "string",
    "distance_meters": 2000,
    "status": "pending",
    "created_by": "uuid",
    "participants": [
      { "user_id": "uuid", "display_name": "string", "joined_at": "2026-07-18T00:00:00Z" }
    ]
  }
  ```

- `404` if the race doesn't exist

## Validation

- `{id}` path param must be a valid UUID — `400` if not parseable

## Handler

- `internal/race`, `GetRaceHandler(pool *pgxpool.Pool) http.HandlerFunc`
- Registered as `GET /races/{id}`, wrapped with `auth.RequireAuth`

## Data

- `GetRaceWithParticipants(ctx, pool, raceID) (RaceDetail, error)` — joins `races`, `race_participants`, and `users` (for `display_name`); split into two queries instead if a single join reads awkwardly with `pgx`'s row scanning

## Notes

- `finish_rank` / `finish_time_ms` / `avg_pace_watt` are always `null` in Phase 1 responses — nothing populates them until Phase 2's race-finish logic exists
- `distance_meters` here is the typing race's target word count, not a literal distance — see context/project-overview.md §13
- Deliberately doesn't include `prompt_text` — that's a separate, potentially large payload fetched via `GET /races/{id}/text` (`races/start-race.md`), kept out of this otherwise-small, frequently-polled response
