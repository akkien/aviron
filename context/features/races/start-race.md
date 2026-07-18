# Start Race & Prompt Text

## Overview

The race creator starts the typing race once enough players have joined: this generates the shared prompt text every participant races against, and flips the race from `pending` to `active`. A second endpoint lets any participant fetch that same text — needed for players who load the page after start, or reconnect mid-race once Phase 2 lands.

## Requirements

### Start Endpoint

- `POST /races/{id}/start` (requires JWT auth; only the race's `created_by` user may call this)
- `200` response: `{ "id": uuid, "status": "active", "started_at": string, "prompt_text": string }`
- `403` if the caller isn't the race's creator
- `404` if the race doesn't exist
- `409` if the race isn't `pending` (already started, finished, or cancelled)

### Text Endpoint

- `GET /races/{id}/text` (requires JWT auth — any authenticated user, not just participants, matching `race-status.md`'s access rule)
- `200` response: `{ "prompt_text": string }`
- `404` if the race doesn't exist
- `409` if the race hasn't started yet (`prompt_text` is `NULL` while `pending`) — the client shouldn't be asking for text before Start Race ran

### Prompt Text Generation

- A random word string sized to the race's `distance_meters` word-count target (e.g. 1000 words), generated server-side from a fixed common-word list (similar in spirit to 10fastfingers), words joined with single spaces
- Generated once per race, at start time, and stored on the race row (`races.prompt_text`) — every participant races against the exact same text
- No uniqueness/no-repeat guarantee across races — random selection with replacement is fine for a side project

## Validation

- `{id}` path param must be a valid UUID — `400` if not parseable

## Handler

- `internal/race`, `StartRaceHandler(pool *pgxpool.Pool) http.HandlerFunc` and `GetRaceTextHandler(pool *pgxpool.Pool) http.HandlerFunc`
- Registered as `POST /races/{id}/start` and `GET /races/{id}/text`, both wrapped with `auth.RequireAuth`

## Data

- `StartRace(ctx, pool, raceID, callerID) (Race, error)` — loads the race, checks `callerID == created_by` and `status == 'pending'`, generates the prompt text, updates `status='active'`, `started_at=now()`, `prompt_text=<generated>` in one statement
- `GetRaceText(ctx, pool, raceID) (string, error)` — `SELECT prompt_text FROM races WHERE id = $1`; a `NULL` value maps to the `409` "hasn't started" case
- Word list: a small hardcoded Go slice of common English words (no DB table, no external API) — pick `distance_meters` random entries (with replacement) and `strings.Join(..., " ")`

## Notes

- This is new REST-only scope that didn't exist in the original Phase 1 plan (which assumed race status only ever changes via Phase 2's room actor) — `pending` → `active` now happens here, over plain REST. Finishing a race (typing all words, ranking, `finished` status) still depends on Phase 2's live progress tracking, so that part is unchanged.
- A separate `prompt_text` fetch endpoint (instead of returning it from Race Status) keeps the potentially large word blob out of a response that's otherwise small and gets polled/refreshed frequently
- See context/project-overview.md §13 for the full typing-race mechanic and why field names like `distance_meters`/`prompt_text` are shaped the way they are
