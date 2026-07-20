# Feature Log

## Project Scaffolding & Local Postgres

Stood up the initial Go backend skeleton — module, package layout, local Postgres via Docker Compose, the initial schema migration, and a `/healthz` endpoint — so every later feature has a working build/test/run loop to land on.

### Goals

- `go build ./...` succeeds from `backend/` (module `github.com/akkien/aviron`)
- `docker compose up -d postgres` starts Postgres 18-alpine on `localhost:5432`; `go run ./cmd/server` applies pending migrations on startup
- `GET /healthz` returns `200 {"status":"ok"}` when `pool.Ping(ctx)` succeeds, `503 {"status":"db_unreachable"}` otherwise
- `go test ./... -race` passes
- `make run` starts the server, `make test` runs the suite

### Explain

- Startup flow: `main.go` loads `config.Load()` from env → opens a `pgxpool.Pool` via `db.NewPool` → runs `db.Migrate` (golang-migrate, using the `pgx/v5` driver over `database/sql`) → builds the mux via `httpserver.NewServer(pool)` → `http.ListenAndServe`
- `/healthz` is the only endpoint in this feature; it exists purely to prove the DB connection works end-to-end before any real feature (auth, races) is built on top
- Migrations are versioned `.sql` files under `migrations/`, applied via `golang-migrate`, not a single init script — later features add new numbered migrations instead of editing this one
- Routing uses Go 1.22's method-aware `http.ServeMux` (`"GET /healthz"`) — no router dependency needed yet
- Tests hit a real Postgres rather than a mock: `TestHealthz_OK` skips (not fails) if no DB is reachable, so `go test ./...` still works without Docker running; `TestHealthz_DBUnreachable` points at `localhost:1` (nothing listens there) to exercise the failure path without needing to fake a broken pool
- Trade-off: used `postgres:18-alpine` instead of the "PostgreSQL 16" originally suggested in context/project-overview.md §11 — newer, smaller image, no schema-relevant difference. This surfaced one gotcha worth remembering: the 18+ image expects its volume mounted at `/var/lib/postgresql`, not `/var/lib/postgresql/data` — the old mount point makes the container refuse to start, thinking it's an unmigrated pre-18 data directory
- Module path (`github.com/akkien/aviron`) was chosen to match the `origin` git remote once it turned out this repo already had git initialized (an earlier assumption that it didn't was wrong)

## User Registration

Added `POST /auth/register`, introducing the layered `Handler → Service → Repository` architecture every backend domain now follows.

### Goals (User Registration)

- `POST /auth/register` → `201` with `{id, email, display_name}`, password never in the response
- `409 email_taken` on duplicate email; `400` field-keyed errors on invalid input
- Passwords hashed with bcrypt (cost 12), never logged

### Explain (User Registration)

- `Repository` is an interface owned by the service; `internal/postgres` provides the concrete implementation and translates Postgres errors (unique-violation) into domain sentinel errors like `ErrEmailTaken`
- Tests run against a fake in-memory repository — no real Postgres needed for handler/service tests

## Login & JWT Issuance

Added `POST /auth/login`, exchanging email and password for a signed JWT that later features verify.

### Goals (Login & JWT Issuance)

- `POST /auth/login` → `200` with `{token, expires_at}` for correct credentials
- `401 invalid_credentials` for wrong password or unknown email — identical response either way, so it can't be used to enumerate accounts
- JWT signed HS256 with `sub`/`email`/`exp` claims, 24h expiry

### Explain (Login & JWT Issuance)

- "User not found" and "wrong password" both collapse to one `ErrInvalidCredentials`, following the same repo-boundary error-translation convention as registration (extended `Repository` with `GetUserByEmail`)
- Renamed the domain's types to `AuthHandler`/`AuthService`/`AuthRepository` (matching `postgres.AuthRepository`'s existing naming) and consolidated all request/response DTOs into one `dtos.go` — now a standing convention for every domain

## JWT Auth Middleware

Added a reusable middleware that verifies the JWT `Login` issues and exposes the caller's user id to downstream handlers.

### Goals (JWT Auth Middleware)

- `Auth(jwtSecret []byte) func(http.Handler) http.Handler` passes valid tokens through; rejects everything else with `401 unauthorized`
- Downstream handlers read the authenticated user id via `UserIDFromContext`

### Explain (JWT Auth Middleware)

- Lives in its own `internal/middleware` package, not inside `internal/auth` — it has zero dependency on auth's domain types, only the raw secret and standard JWT claims, so it's a cross-cutting concern rather than part of the auth domain
- Not wired into any route yet — there's nothing to protect until the races endpoints exist

## Create Race

Added `POST /races`, the first domain package beyond `auth` and the one that establishes the layered `Handler → Service → Repository` convention every later domain follows.

### Goals (Create Race)

- `POST /races` (JWT-authenticated) creates a race in `pending` status
- `201` with `{id, name, distance_meters, status, created_by, created_at}`
- `name`: 1–100 characters, trimmed; `distance_meters`: positive integer (the typing race's target word count, field name kept from the original fitness-telemetry schema)
- The creator is recorded in `created_by` but **not** auto-added as a participant — they join like anyone else

### Explain (Create Race)

- `internal/race`: `RaceHandler`/`RaceService`/`RaceRepository`, `NewRaceHandler`/`NewRaceService` — the first domain package to follow the layered convention exactly, deliberately diverging from this feature's own spec (which predates that convention and describes a flat-function handler)
- First route actually wrapped with `middleware.Auth`, closing the loop the JWT Auth Middleware feature left open
- No new migration needed — `races` already had every column this feature uses from the initial scaffolding schema

## Join Race

Added `POST /races/{id}/join`, letting an authenticated user become a participant and receive a per-race session token that Phase 2's WebSocket handshake later consumes.

### Goals (Join Race)

- `POST /races/{id}/join` (JWT-authenticated) → `200` with `{race_id, session_token}`
- `404` if the race doesn't exist; `409` if already joined, the race isn't `pending`, or it's already at `MaxParticipants` (10)
- `session_token`: HS256 JWT with `race_id`/`user_id` claims, 6h TTL

### Explain (Join Race)

- Extended `internal/race` (not a new package) with `GetRace`/`AddParticipant`/`CountParticipants` and three new sentinel errors (`ErrRaceNotFound`, `ErrAlreadyJoined`, `ErrRaceNotPending`)
- `{id}` path param validated as a UUID via a small regex, not a UUID library — consistent with this project's plain-`string`-id convention
- The count-then-insert check before `AddParticipant` has a small accepted race-condition gap (two near-simultaneous joins could both pass the count check) — not worth a transaction/row lock for a side project

## Start Race, Prompt Text & Race Status

Added `POST /races/{id}/start`, `GET /races/{id}/text`, and `GET /races/{id}` — the creator generates a shared typing prompt and flips the race live, and anyone can check its status.

### Goals (Start Race, Prompt Text & Race Status)

- `POST /races/{id}/start` (creator-only): generates the shared prompt text, flips `pending` → `active`; `403` if not the creator, `409` if not `pending`
- `GET /races/{id}/text`: fetch the generated prompt; `409` if the race hasn't started yet
- `GET /races/{id}`: race details plus the participant list

### Explain (Start Race, Prompt Text & Race Status)

- Prompt text generated via `gofakeit`'s `Word()` called `distance_meters` times, for exact word-count control — diverged from the spec's original "hardcoded word list" suggestion after confirming `gofakeit` v7's `Paragraph`/`Sentence` helpers are deprecated no-ops
- New migration `000002_add_prompt_text`: `races.prompt_text` had only ever existed in the schema doc, not a real migration
- Two new sentinel errors, `ErrNotCreator` and `ErrPromptNotReady`, deliberately not collapsed into the existing `ErrRaceNotPending` despite all three being `409`s — `ErrPromptNotReady` means the opposite condition
- Open design questions raised during review, not yet actioned: should `CreateRace` auto-add the creator as a participant, and should `start` require a minimum participant count before allowing the race to begin

## Frontend Client — Login Page & Create/Join Race Page

The first React screens: `/login` and `/races`, enough to manually exercise the full Phase 1 REST flow (register → login → create → join → start → text → status) end to end.

### Goals (Frontend Client)

- `/login`: email/password form, stores the JWT (`localStorage`), redirects to `/races`
- `/races`: create/join/start forms, a status view, and a typing view with per-word progress tracked client-side — no WebSocket yet, so only the local player's own car moves; a manual "Refresh" button is the only way to see updated state
- Open multiple browser tabs as different users to manually verify join/participant-list behavior

### Explain (Frontend Client)

- Scaffolded with Vite + React + TypeScript, styled with Tailwind CSS v4 (CSS-based `@theme`, no `tailwind.config.ts`) and hand-authored shadcn/ui primitives (`Button`/`Input`/`Label`/`Card`) rather than running the shadcn CLI
- Found and fixed a gap neither spec covered: the backend had zero CORS support, so every authenticated frontend request would have failed preflight — added `internal/middleware/cors.go` (wraps the whole mux, since `OPTIONS` preflights never match `http.ServeMux`'s method-specific patterns) plus a `CORSAllowedOrigin` config field
- Added `make start`/`stop`/`restart` to `backend/Makefile`, and a custom race-lane color palette plus a blue primary theme color, both per explicit request after the initial pass
- Full REST chain manually verified via curl with two users, plus a CORS preflight check — not visually verified in an actual browser, since no browser automation tool was available; a known, disclosed gap rather than a claimed pass

## Cap Race Participants at 10

A small, explicitly-requested cap on race size ahead of Phase 2, so the room actor's per-tick broadcast payload stays bounded.

### Goals (Cap Race Participants at 10)

- A race can never have more than `MaxParticipants` (10) participants; joining a full race returns `409 race_full`

### Explain (Cap Race Participants at 10)

- `internal/race` gains `MaxParticipants = 10` and `ErrRaceFull`, checked via a new `CountParticipants` repository call before `AddParticipant`
- Motivated by Phase 2's room actor, which broadcasts every participant's state on every 250ms tick — an unbounded room means an unbounded per-tick payload
- Accepts the same count-then-insert race-condition gap `start-race.md` already accepted for its own ownership/status check
- Implemented directly on `master`, not through a `/feature` branch — a small change explicitly requested outside the active feature cycle

## Room Actor Core

The first Phase 2 feature: `internal/room`'s `RoomActor`, the per-race goroutine that owns all of a room's state and ticks a broadcast every 250ms.

### Goals (Room Actor Core)

- `RoomActor` with an `inbox` channel accepting `ParticipantJoined`/`TelemetryReceived`/`ParticipantDisconnected` events
- `Run()` ticks every 250ms, broadcasting a ranked `race_state` snapshot as `[]byte`
- `applyEvent` is the single writer of room state, rejecting stale/duplicate/out-of-order telemetry (`Seq <= LastSeq`)
- `go test -race` clean, with no goroutine leaks on context cancellation

### Explain (Room Actor Core)

- `RoomEvent` is a Go interface + marker method rather than one flat tagged struct — anticipated later reconnection-related variants (`ParticipantReconnected`, `ParticipantLeft`) that would otherwise force optional fields onto a shared struct
- The broadcast send in `broadcastSnapshot` is non-blocking (`select`/`default`), so one full or slow consumer can never stall the actor
- Deliberately skips this project's Handler/Service/Repository layering — that convention is for REST domains with a DB round trip, and this is an in-memory actor with none of that shape
- `tick` is an incrementing counter rather than a wall-clock timestamp, both for more conventional "tick" semantics and to make the broadcast test deterministic
- Three concurrency-focused tests (broadcast-on-tick, clean shutdown with no leak, concurrent senders from 5 goroutines × 50 events each), re-run 5× to rule out timing flakiness

## Room Registry

`internal/room` gains `Registry`, mapping a `race_id` to its running `RoomActor` — spawned when a race starts, looked up for the (then not-yet-built) WebSocket endpoint, removed when the race ends.

### Goals (Room Registry)

- `Registry` with `Spawn`/`Get`/`Remove`, spawning an actor the moment a race starts
- Lookup and spawn/remove safe under concurrent access (`go test -race`)
- Single-instance scope only — no Redis, that's Phase 4

### Explain (Room Registry)

- A `sync.RWMutex`-guarded `map[string]*RoomActor` — deliberately the simplest thing that works for one instance, not pre-built for Phase 4's future Redis-backed cross-instance version
- Resolved a gap in the original spec: `Spawn`'s signature had no way to supply `RoomActor`'s required broadcast channel, so `RoomActor.broadcast` became a bidirectional channel created internally by `Spawn`, exposed via a new `Broadcast()` accessor
- `Spawn` is called from `RaceHandler.Start` (not `RaceService`) using the process's **root** context, not the per-request context — a per-request context would have cancelled the room actor the instant the HTTP handler returned
- `Remove` exists but isn't wired into anything yet — the features that would call it (race finishing, grace-period expiry) don't exist yet
- Tests cover the two required concurrency scenarios: concurrent `Get` during `Spawn`, and `Remove` racing an in-flight `Get`, both across 50 races

## WebSocket Protocol

The JSON message schema exchanged over the WebSocket connection, kept as a pure encode/decode concern independent of connection plumbing.

### Goals (WebSocket Protocol)

- Parse `join_race`/`telemetry` client messages; malformed JSON or an unknown `type` is logged and dropped, never connection-ending
- Encode `race_state`/`race_finished` server messages via plain `encoding/json` — no protobuf yet

### Explain (WebSocket Protocol)

- New `internal/ws` package. `internal/room`'s previously-private `race_state` JSON structs are now exported in place (`RaceStateMessage`/`ParticipantStateJSON`) so `internal/ws` reuses them instead of redeclaring an identical shape — keeps the dependency one-directional (`ws` → `room`)
- `decodeClientMessage` only parses/validates the envelope; a separate `toRoomEvent(userID, displayName string)` method does the actual dispatch into `RoomEvent` variants, since the wire format's `join_race` message has no display name to offer on its own — identity comes from the caller instead
- Deliberately did **not** add an `encodeRaceStateMessage`: `RoomActor.broadcastSnapshot` already marshals and hands out pre-encoded `race_state` bytes, so a second encoder for the same shape would be unused dead code

## WebSocket Endpoint

`GET /ws?race_id=...&session_token=...` — the actual connection clients open to join a live race, bridging it to a room actor via per-connection reader/writer goroutines.

### Goals (WebSocket Endpoint)

- Verify the per-race session token the same way `middleware.Auth` verifies the main JWT, but against `race_id`/`user_id` claims
- Reject the upgrade outright (plain HTTP error) for an invalid/expired token, a `race_id` mismatch, or no running room actor for that race
- No leaked goroutines on abrupt disconnect, proven by tests, not assumed
- A slow client's full buffer drops broadcasts for that connection only — it must never stall the room's shared tick

### Explain (WebSocket Endpoint)

- Added `github.com/coder/websocket` (the renamed continuation of `nhooyr.io/websocket`) — its context-native `Read`/`Write` fit this project's context-driven goroutine style better than `gorilla/websocket`'s deadline-based cancellation
- The central design problem: `RoomActor.Broadcast()` is one channel per *room*, but each *connection* needs its own buffered channel so one slow client can't affect others. Solved with a per-room `hub` — refined mid-implementation from a sketched mutex-guarded map to a channel-driven single-writer design instead, the same pattern `RoomActor` already uses for its own state (see `docs/concurrency.md` for the full rationale and both versions compared side by side)
- `RoomActor` gained two more accessors: `Context()` (so a connection's context can be a child of the room's) and `Send(ev RoomEvent)` (the only way outside code may enqueue an event, guarded against blocking forever if the room has already closed)
- `displayName` falls back to `userID` — the session token only carries `race_id`/`user_id`, and a DB lookup would have been this endpoint's only Postgres round-trip, contradicting the "no new Postgres access" requirement
- `applyEvent`'s `ParticipantJoined` case now broadcasts immediately instead of waiting for the next 250ms tick, and the reader goroutine's read-error exit path pushes `ParticipantDisconnected` — both were event types/behaviors earlier features had already built the plumbing for but nothing had triggered yet
- Registered outside `middleware.Auth`, since this endpoint authenticates via the query-string `session_token` rather than the `Authorization` header; reuses the existing `CORSAllowedOrigin` config for `coder/websocket`'s origin check, since without it the frontend's cross-origin handshake would be rejected by default
- A `wsConn` interface lets most tests run against a fake connection instead of real sockets; one real end-to-end suite still dials an actual `coder/websocket` client against `httptest.Server` to prove the wire-level integration works
