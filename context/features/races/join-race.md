# Join Race

## Overview

An authenticated user joins an existing race, becoming a participant. This returns a per-race session token that Phase 2's WebSocket handshake will use to identify reconnecting clients — Phase 1 only issues it, since there's no WebSocket yet to consume it.

## Requirements

### Endpoint

- `POST /races/{id}/join` (requires JWT auth)
- `200` response: `{ "race_id": uuid, "session_token": string }`
- `404` if the race doesn't exist
- `409` if the user already joined this race
- `409` if the race isn't `pending` — always true in Phase 1 since nothing advances race status yet, but the guard matters once Phase 2 lands

### Session Token

- A signed JWT-style token (HS256, same `JWT_SECRET` as Login) carrying `race_id` and `user_id` claims — no DB column needed. Phase 2's WebSocket handler will verify it the same way `jwt-middleware` verifies the main JWT
- Generous fixed TTL (e.g. 6h) — no need to make it shorter than a race could plausibly run

## Validation

- `{id}` path param must be a valid UUID — `400` if not parseable

## Handler

- `internal/race`, `JoinRaceHandler(pool *pgxpool.Pool, jwtSecret []byte) http.HandlerFunc`
- Registered as `POST /races/{id}/join`, wrapped with `auth.RequireAuth`

## Data

- `GetRace(ctx, pool, raceID) (Race, error)` — powers the 404 and status checks
- `AddParticipant(ctx, pool, raceID, userID) error` — inserts into `race_participants`; the `(race_id, user_id)` primary key produces the 409-on-duplicate-join case (catch the unique-violation error)

## Notes

- Deliberately no `session_token` column on `race_participants` — stateless, matching how the main JWT already works
