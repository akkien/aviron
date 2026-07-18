# Create Race

## Overview

An authenticated user creates a new race, naming it and setting a target distance. This is the entry point for the "simple race flow" Phase 1 promises. The race starts in `pending` status since there's no real-time start yet — that's Phase 2.

## Requirements

### Endpoint

- `POST /races` (requires JWT auth)
- Request body: `{ "name": string, "distance_meters": int }`
- `201` response: `{ "id": uuid, "name": string, "distance_meters": int, "status": "pending", "created_by": uuid, "created_at": string }`
- The creator is recorded in `races.created_by` but is **not** automatically added as a participant — they still call Join Race like anyone else

## Validation

- `name` — required, 1–100 characters, trimmed
- `distance_meters` — required, integer, greater than 0

## Handler

- `internal/race`, `CreateRaceHandler(pool *pgxpool.Pool) http.HandlerFunc`
- Registered as `POST /races`, wrapped with `auth.RequireAuth`

## Data

- `CreateRace(ctx, pool, name, distanceMeters, createdBy) (Race, error)`

## Notes

- No limit yet on how many races a user can create — fine for a side project
- `status` will only ever be `pending` after this endpoint in Phase 1 — transitions to `active`/`finished` are driven by Phase 2's room actor, not built here
