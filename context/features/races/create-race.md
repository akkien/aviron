# Create Race

## Overview

An authenticated user creates a new typing race, naming it and setting a target word count. This is the entry point for the "simple race flow" Phase 1 promises. The race starts in `pending` status — nobody can start typing until the creator calls Start Race (`races/start-race.md`).

## Requirements

### Endpoint

- `POST /races` (requires JWT auth)
- Request body: `{ "name": string, "distance_meters": int }` — `distance_meters` is the target word count for this project's typing race (e.g. `1000`); the field name is kept from the original fitness-telemetry schema rather than renamed (context/project-overview.md §13)
- `201` response: `{ "id": uuid, "name": string, "distance_meters": int, "status": "pending", "created_by": uuid, "created_at": string }`
- The creator is recorded in `races.created_by` but is **not** automatically added as a participant — they still call Join Race like anyone else

## Validation

- `name` — required, 1–100 characters, trimmed
- `distance_meters` — required, integer, greater than 0 (a positive word-count target)

## Handler

- `internal/race`, `CreateRaceHandler(pool *pgxpool.Pool) http.HandlerFunc`
- Registered as `POST /races`, wrapped with `auth.RequireAuth`

## Data

- `CreateRace(ctx, pool, name, distanceMeters, createdBy) (Race, error)`

## Notes

- No limit yet on how many races a user can create — fine for a side project
- `status` stays `pending` until the creator calls `POST /races/{id}/start` (`races/start-race.md`) — that's what generates the shared prompt text and flips the race live, entirely within Phase 1 (no WebSocket needed for this transition)
- See context/project-overview.md §13 for the full typing-race mechanic and why field names like `distance_meters` are reused rather than renamed
