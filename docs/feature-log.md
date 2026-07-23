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
- The startup sequence every later feature builds on top of:

  ```mermaid
  flowchart LR
      A["config.Load()"] --> B["db.NewPool(ctx, DATABASE_URL)"]
      B --> C["db.Migrate(DATABASE_URL)"]
      C --> D["httpserver.NewServer()"]
      D --> E["httpserver.RegisterRoutes(...)"]
      E --> F["http.ListenAndServe"]
  ```

## User Registration

Added `POST /auth/register`, introducing the layered `Handler → Service → Repository` architecture every backend domain now follows.

### Goals (User Registration)

- `POST /auth/register` → `201` with `{id, email, display_name}`, password never in the response
- `409 email_taken` on duplicate email; `400` field-keyed errors on invalid input
- Passwords hashed with bcrypt (cost 12), never logged

### Explain (User Registration)

- `Repository` is an interface owned by the service; `internal/postgres` provides the concrete implementation and translates Postgres errors (unique-violation) into domain sentinel errors like `ErrEmailTaken`
- Tests run against a fake in-memory repository — no real Postgres needed for handler/service tests
- The layered architecture this feature introduces, followed by every domain package added afterward (`race`, `leaderboard`, ...):

  ```mermaid
  flowchart LR
      subgraph "internal/auth"
          H["AuthHandler<br/>(decode/encode HTTP)"] --> S["AuthService<br/>(validation, business logic)"]
          S --> R["AuthRepository<br/>(interface)"]
      end
      R -.implemented by.-> P["postgres.AuthRepository<br/>(internal/postgres)"]
      P --> DB[(Postgres)]
      F["fakeRepository<br/>(tests only)"] -.implements.-> R
  ```

- Request/response shape (`internal/auth/dtos.go`):

  ```json
  // POST /auth/register
  // → request
  {"email": "alice@example.com", "password": "hunter2", "display_name": "Alice"}
  // → 201 response (no password field, ever)
  {"id": "3f29a5c9-1b2e-4b7a-9c3d-8e1f2a3b4c5d", "email": "alice@example.com", "display_name": "Alice"}
  ```

## Login & JWT Issuance

Added `POST /auth/login`, exchanging email and password for a signed JWT that later features verify.

### Goals (Login & JWT Issuance)

- `POST /auth/login` → `200` with `{token, expires_at}` for correct credentials
- `401 invalid_credentials` for wrong password or unknown email — identical response either way, so it can't be used to enumerate accounts
- JWT signed HS256 with `sub`/`email`/`exp` claims, 24h expiry

### Explain (Login & JWT Issuance)

- "User not found" and "wrong password" both collapse to one `ErrInvalidCredentials`, following the same repo-boundary error-translation convention as registration (extended `Repository` with `GetUserByEmail`)
- Renamed the domain's types to `AuthHandler`/`AuthService`/`AuthRepository` (matching `postgres.AuthRepository`'s existing naming) and consolidated all request/response DTOs into one `dtos.go` — now a standing convention for every domain
- The actual JWT claims signed (`internal/auth/service.go`), decoded from a real token payload:

  ```go
  claims := jwt.MapClaims{
      "sub":   user.ID,   // subject: the authenticated user's id
      "email": user.Email,
      "exp":   expiresAt.Unix(), // 24h from now
  }
  ```

## JWT Auth Middleware

Added a reusable middleware that verifies the JWT `Login` issues and exposes the caller's user id to downstream handlers.

### Goals (JWT Auth Middleware)

- `Auth(jwtSecret []byte) func(http.Handler) http.Handler` passes valid tokens through; rejects everything else with `401 unauthorized`
- Downstream handlers read the authenticated user id via `UserIDFromContext`

### Explain (JWT Auth Middleware)

- Lives in its own `internal/middleware` package, not inside `internal/auth` — it has zero dependency on auth's domain types, only the raw secret and standard JWT claims, so it's a cross-cutting concern rather than part of the auth domain
- Not wired into any route yet — there's nothing to protect until the races endpoints exist
- What `Auth` actually checks, in order, before letting a request through:

  ```mermaid
  flowchart TD
      A["Authorization header present,<br/>starts with 'Bearer '?"] -->|no| R["401 unauthorized"]
      A -->|yes| B["Parses as a valid HS256 JWT?"]
      B -->|no| R
      B -->|yes| C["exp claim in the future?"]
      C -->|no /expired| R
      C -->|yes| D["sub claim present?"]
      D -->|no| R
      D -->|yes| E["next.ServeHTTP<br/>(UserIDFromContext now readable)"]
  ```

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
- Request/response shape (`internal/race/dtos.go`), showing the field-name holdover from the original fitness-telemetry design mentioned in `context/project-overview.md` §13:

  ```json
  // POST /races  (Authorization: Bearer <jwt>)
  // → request
  {"name": "Friday Night Sprint", "distance_meters": 150}
  // → 201 response
  {
    "id": "9f2kD8mQvxaB",
    "name": "Friday Night Sprint",
    "distance_meters": 150,
    "status": "pending",
    "created_by": "3f29a5c9-1b2e-4b7a-9c3d-8e1f2a3b4c5d",
    "created_at": "2026-07-23T14:00:00Z"
  }
  ```

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
- Every way this endpoint can respond:

  | Status | Body | When |
  | --- | --- | --- |
  | `200` | `{"race_id": "...", "session_token": "..."}` | Joined successfully |
  | `400` | `{"error": "invalid_race_id"}` | `{id}` isn't a valid race id shape |
  | `404` | `{"error": "race_not_found"}` | No race with that id |
  | `409` | `{"error": "already_joined"}` | Caller already has a spot |
  | `409` | `{"error": "race_not_pending"}` | Race already started/finished/cancelled |
  | `409` | `{"error": "race_full"}` | Already at `MaxParticipants` (10) |

- `session_token`'s claims (`internal/race/service.go`) — a *different* JWT from the login token above, scoped to exactly one race:

  ```go
  claims := jwt.MapClaims{
      "race_id": raceID,
      "user_id": userID,
      "exp":     time.Now().Add(6 * time.Hour).Unix(),
  }
  ```

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
- `races.status`'s full state machine as it exists *today* (the `cancelled` state and its two entry points weren't added until much later — Cancelled Race Status, further down this log — but showing the complete picture here once is clearer than redrawing a partial one repeatedly):

  ```mermaid
  stateDiagram-v2
      [*] --> pending: POST /races
      pending --> active: POST /races/{id}/start
      active --> finished: last participant reaches distance_meters
      pending --> cancelled: nobody starts it in time,<br/>or the lobby empties out
      cancelled --> [*]
      finished --> [*]
  ```

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
- The CORS gap this feature found and fixed — a preflight `OPTIONS` request never matches `http.ServeMux`'s method-specific route patterns (`"POST /races"` only matches `POST`), so it has to be answered by middleware wrapping the whole mux instead:

  ```mermaid
  sequenceDiagram
      participant B as Browser (Vite dev server)
      participant M as middleware.Cors
      participant Mux as http.ServeMux

      B->>M: OPTIONS /races (preflight)
      M-->>B: 204, Access-Control-Allow-* headers
      Note over M,Mux: OPTIONS never reaches the mux —<br/>no route is registered for it
      B->>M: POST /races (the real request)
      M->>Mux: forwarded through
      Mux-->>B: 201 Created
  ```

## Cap Race Participants at 10

A small, explicitly-requested cap on race size ahead of Phase 2, so the room actor's per-tick broadcast payload stays bounded.

### Goals (Cap Race Participants at 10)

- A race can never have more than `MaxParticipants` (10) participants; joining a full race returns `409 race_full`

### Explain (Cap Race Participants at 10)

- `internal/race` gains `MaxParticipants = 10` and `ErrRaceFull`, checked via a new `CountParticipants` repository call before `AddParticipant`
- Motivated by Phase 2's room actor, which broadcasts every participant's state on every 250ms tick — an unbounded room means an unbounded per-tick payload
- Accepts the same count-then-insert race-condition gap `start-race.md` already accepted for its own ownership/status check
- Implemented directly on `master`, not through a `/feature` branch — a small change explicitly requested outside the active feature cycle
- The guard added to `JoinRace`, in the same count-then-insert shape the rest of this domain already uses:

  ```go
  const MaxParticipants = 10

  count, err := s.repo.CountParticipants(ctx, raceID)
  if count >= MaxParticipants {
      return "", ErrRaceFull
  }
  ```

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
- `Run()`'s single-writer select loop — the shape every later Phase 2 feature adds a new event or a new `case` to, never a new goroutine touching `participants` directly:

  ```mermaid
  flowchart TD
      Start(["go actor.Run()"]) --> Loop{"select"}
      Loop -->|"ev := &lt;-inbox"| Apply["applyEvent(ev)<br/>(the ONLY code that mutates participants)"]
      Loop -->|"&lt;-ticker.C (every 250ms)"| Broadcast["broadcastSnapshot()"]
      Loop -->|"&lt;-ctx.Done()"| Return(["return — room torn down"])
      Apply --> Loop
      Broadcast --> Loop
  ```

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
- The registry's actual shape — deliberately the simplest thing that works for one process, not pre-built for a future Redis-backed version:

  ```go
  type Registry struct {
      mu    sync.RWMutex
      rooms map[string]*RoomActor
  }

  func (reg *Registry) Spawn(ctx context.Context, raceID string, distanceMeters int, ...) *RoomActor
  func (reg *Registry) Get(raceID string) (*RoomActor, bool)
  func (reg *Registry) Remove(raceID string)
  ```

## WebSocket Protocol

The JSON message schema exchanged over the WebSocket connection, kept as a pure encode/decode concern independent of connection plumbing.

### Goals (WebSocket Protocol)

- Parse `join_race`/`telemetry` client messages; malformed JSON or an unknown `type` is logged and dropped, never connection-ending
- Encode `race_state`/`race_finished` server messages via plain `encoding/json` — no protobuf yet

### Explain (WebSocket Protocol)

- New `internal/ws` package. `internal/room`'s previously-private `race_state` JSON structs are now exported in place (`RaceStateMessage`/`ParticipantStateJSON`) so `internal/ws` reuses them instead of redeclaring an identical shape — keeps the dependency one-directional (`ws` → `room`)
- `decodeClientMessage` only parses/validates the envelope; a separate `toRoomEvent(userID, displayName string)` method does the actual dispatch into `RoomEvent` variants, since the wire format's `join_race` message has no display name to offer on its own — identity comes from the caller instead
- Deliberately did **not** add an `encodeRaceStateMessage`: `RoomActor.broadcastSnapshot` already marshals and hands out pre-encoded `race_state` bytes, so a second encoder for the same shape would be unused dead code
- Every message type on the wire (client→server messages are handled by `decodeClientMessage`/`toRoomEvent`; server→client ones are broadcast by the room actor — later features add `race_started`/`race_expired` to the second list):

  | Direction | `type` | Example |
  | --- | --- | --- |
  | Client → Server | `join_race` | `{"type":"join_race","race_id":"9f2kD8mQvxaB"}` |
  | Client → Server | `telemetry` | `{"type":"telemetry","seq":42,"distance_m":12,"pace_watt":58.3}` |
  | Client → Server | `leave_race` | `{"type":"leave_race"}` |
  | Server → Client | `race_state` | `{"type":"race_state","tick":1234,"participants":[{"user_id":"...","distance_m":12,"rank":1}]}` |

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
- The handshake, and why a slow client's own connection is the only one that suffers (the `hub` in the middle is what makes that true — see `docs/concurrency.md` for the full single-writer rationale):

  ```mermaid
  sequenceDiagram
      participant C as Client
      participant WS as WSHandler
      participant Reg as room.Registry
      participant Hub as hub (per room)

      C->>WS: GET /ws?race_id=...&session_token=...
      WS->>WS: verifySessionToken(session_token)
      alt invalid token or race_id mismatch
          WS-->>C: 401
      end
      WS->>Reg: Get(race_id)
      alt no room actor for that race
          WS-->>C: 404
      end
      WS->>WS: actor.IsEvicted(user_id)?
      alt grace period already expired
          WS-->>C: 401
      end
      WS->>C: 101 Switching Protocols
      WS->>Hub: registerConn(own buffered channel)
      Note over Hub: one connection's full buffer<br/>only drops messages for that connection —<br/>never blocks the room or other players
  ```

## Reconnection & Grace Period

A dropped WebSocket connection gets a 30-second grace period to reconnect before being evicted from the race, instead of losing its spot the instant the socket closes.

### Goals (Reconnection & Grace Period)

- A disconnected participant is marked, not removed — kept in the room for 30s
- Reconnecting within the window resumes exactly where they left off (progress, `LastSeq` preserved)
- Missing the window evicts them permanently (`ParticipantEvicted`), notifying the room
- A reconnect attempt after eviction is rejected with the same `401` as an invalid token

### Explain (Reconnection & Grace Period)

- Reused `ParticipantJoined` for reconnection instead of adding a distinct `ParticipantReconnected` event — told apart from a fresh join purely by checking existing participant state in `applyEvent`
- New `IsEvicted` query — the room actor's first synchronous request/reply-channel query on its own inbox, checked by the WS endpoint before allowing the upgrade
- `graceTimer` self-schedules `ParticipantEvicted` via `time.AfterFunc`, guarded against a race between an already-fired timer and an in-flight reconnect landing at nearly the same moment
- A duplicate `join_race` from an already-connected client (two tabs, a retry) is correctly told apart from "unknown participant" and doesn't reset progress — a real bug caught by direct review before shipping, not by a failing test
- One participant's connection lifecycle:

  ```mermaid
  stateDiagram-v2
      [*] --> Connected: ParticipantJoined
      Connected --> Disconnected: ParticipantDisconnected<br/>(graceTimer starts, 30s)
      Disconnected --> Connected: ParticipantJoined again<br/>(within 30s — timer cancelled,<br/>WordsCorrect/LastSeq preserved)
      Disconnected --> Evicted: graceTimer fires<br/>(30s elapsed, still disconnected)
      Evicted --> [*]: any reconnect attempt now<br/>rejected with 401 (IsEvicted)
  ```

## Race Completion

The room actor detects a race is over and writes final results — rank, finish time, avg WPM — to Postgres in one transaction.

### Goals (Race Completion)

- A race finishes once every live participant has reached the target word count (or the room empties out)
- `races`/`race_participants`/`leaderboard_alltime` updated atomically — all or nothing
- `race_finished` broadcast to every connection with final ranks

### Explain (Race Completion)

- `RaceFinisher`/`ParticipantResult` live in `internal/room` (not `internal/race`) to avoid an import cycle — satisfied structurally by `RaceService.FinishRace`, the same repository-interface pattern this project already uses elsewhere
- A room with zero live participants tears down via a registry watcher goroutine, rather than requiring every caller to remember to call `Remove`
- Known, disclosed gaps at the time: no retry if the Postgres transaction fails (logged, room stays running); `AvgPaceWatt` always wrote `0.0` (fixed much later, in User Stats)
- Everything one `FinishRace` transaction writes, all-or-nothing:

  | Table | What changes |
  | --- | --- |
  | `races` | `status = 'finished'`, `ended_at = now()` |
  | `race_participants` | `finish_rank`, `finish_time_ms`, `avg_pace_watt`, `disconnected_count` per row |
  | `leaderboard_alltime` | `total_races`/`total_distance_m` incremented (upsert) |

- The wire message every connection receives the moment this commits:

  ```json
  {"type":"race_finished","results":[{"user_id":"...","finish_rank":1,"finish_time_ms":48213,"avg_pace_watt":62.4}]}
  ```

## Leave Race

`POST /races/{id}/leave` for backing out of a still-pending race, and an immediate WebSocket `leave_race` message for quitting mid-race.

### Goals (Leave Race)

- Leaving before start removes the participant outright, no trace left
- Quitting mid-race is immediate — no 30s grace period, since it's a deliberate choice, not a dropped connection
- Every participant who ever raced gets a result row, even quitters — sharing one last-place rank rather than vanishing from the leaderboard silently

### Explain (Leave Race)

- New `ParticipantLeft` event, distinct from the grace-period's `ParticipantEvicted` — a quit is always honored immediately, an eviction only after the grace window and only if still marked disconnected
- A real rank-collision bug caught during review: computing "the next finisher's rank" by counting live participants missed anyone who'd already finished and then disconnected, handing two finishers the same rank — fixed with a monotonic counter immune to who's since departed, with a regression test reproducing the exact collision
- `ParticipantLeft` vs. the grace-period's `ParticipantEvicted` — same removal machinery, different guard:

  | | `ParticipantLeft` (quit) | `ParticipantEvicted` (grace period lapsed) |
  | --- | --- | --- |
  | Triggered by | Client sends `leave_race` | 30s grace timer fires |
  | Guard in `applyEvent` | None — always honored | Only if still marked disconnected (a stale timer firing after a reconnect is ignored) |
  | Delay | Immediate | Up to 30s |
  | Result rank | Shared last place (`totalParticipants`) | Shared last place (`totalParticipants`) |

## WebSocket Client (Frontend)

The React app opens the WebSocket connection, sends one `telemetry` message per correctly-typed word, and renders every participant's car moving live from the server's `race_state` broadcasts.

### Goals (WebSocket Client)

- Every participant's position updates live, not just the local player's own (closing Phase 1's biggest limitation)
- A "Quit Race" button sends `leave_race` and shows results/DNF immediately

### Explain (WebSocket Client)

- Extracted into a `useRaceSocket` hook rather than inlining the connection in the typing view, deliberately, so a later reconnect feature could extend it instead of requiring a rewrite
- Caught and fixed a spec-compliance slip before shipping: an early draft special-cased the local player's own lane for snappier feedback, but the spec explicitly said no more special-casing — corrected to drive every lane, including the local one, purely from the server's broadcast
- `useRaceSocket`'s `onmessage` handler (`frontend/src/hooks/useRaceSocket.ts`) — every lane, including the local player's own, is driven from `race_state`, never from local input directly:

  ```ts
  ws.onmessage = (event) => {
    const msg = JSON.parse(event.data) as { type: string }
    if (msg.type === "race_state") {
      setRaceState(msg as RaceStateMessage)      // every car's position, every tick
    } else if (msg.type === "race_finished") {
      setFinished(msg as RaceFinishedMessage)
      ws.close()
    }
  }
  ```

## Reconnect UI

A dropped connection is retried automatically (3 attempts, 2s apart) instead of leaving the player stuck.

### Goals (Reconnect UI)

- A dropped connection retries automatically; the UI shows "Reconnecting..." rather than an error
- Exhausting every retry (or a rejected reattach) shows a clear "evicted" state

### Explain (Reconnect UI)

- Two real React 18 StrictMode bugs caught and fixed during design review, before shipping: a shared ref tracking "did this effect already stop" got reset by StrictMode's dev-only double-invoke before a stale connection's `onclose` actually fired, and `onclose` unconditionally nulled out the active connection reference even when it was a late-firing stale one — both fixed by scoping state correctly per effect execution
- No exponential backoff — a fixed, short retry window is enough for a side project, not a production reconnect strategy
- The frontend's own reconnect loop, layered on top of the backend grace-period state machine documented under Reconnection & Grace Period above:

  ```mermaid
  stateDiagram-v2
      [*] --> Connected
      Connected --> Reconnecting: onclose (not intentional)
      Reconnecting --> Connected: retry succeeds<br/>(within 3 attempts, 2s apart)
      Reconnecting --> Evicted: 3 attempts exhausted,<br/>or server rejects the reattach (401)
      Connected --> [*]: intentional close<br/>(quit / finished / expired)
      Evicted --> [*]
  ```

  The browser can't tell "the retry was rejected because the grace period already expired server-side" apart from "the retry just failed to connect" — both surface through the exact same `onclose` event, so one state machine correctly covers both causes without special-casing either.

## Race ID Display & Shortening

The race status view gained a copyable Race ID, and the ID format itself shrank from a raw UUID to a 12-character, hand-typeable string.

### Goals (Race ID Display & Shortening)

- A race's ID is visible and copyable from its status view
- IDs are short enough to read aloud or type by hand to invite another player

### Explain (Race ID Display & Shortening)

- `crypto/rand`-backed generation using the Bitcoin base58 alphabet (excludes `0`/`O`/`I`/`l` for readability); `races.id` and its two FK columns switched from `UUID` to `TEXT` via migration
- Postgres no longer guarantees uniqueness on its own, so `CreateRace` retries up to 5 times on a collision — at roughly 70 bits of entropy, vanishingly unlikely but no longer structurally impossible
- Verified against a live database, not just tests — confirmed the migration applied cleanly to existing rows and a full register→create→join→leave flow round-tripped a real generated id
- Before/after:

  | | Before | After |
  | --- | --- | --- |
  | Example id | `a1b2c3d4-e5f6-47a8-b9c0-d1e2f3a4b5c6` | `TykeGcespDKY` |
  | Length | 36 characters | 12 characters |
  | Generated by | Postgres (`gen_random_uuid()`) | Go (`internal/race.GenerateRaceID`) |
  | Column type | `UUID` | `TEXT` |

- The generator itself (`internal/race/id.go`) — `crypto/rand`, never `math/rand`, for anything identifier-shaped:

  ```go
  const base58Alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz" // no 0/O/I/l
  const raceIDLength = 12

  func GenerateRaceID() (string, error) {
      id := make([]byte, raceIDLength)
      for i := range id {
          n, _ := rand.Int(rand.Reader, big.NewInt(int64(len(base58Alphabet))))
          id[i] = base58Alphabet[n.Int64()]
      }
      return string(id), nil
  }
  ```

## UI Revamp — Theme

Fonts, a warm color palette, and rounded card chrome applied globally from a supplied design mockup.

### Goals (UI Revamp — Theme)

- The app's visual tokens (fonts, colors, corner radius) match the supplied mockup

### Explain (UI Revamp — Theme)

- Applied entirely through global CSS theme tokens, not per-component overrides — confirmed the login page needed zero direct edits to pick up the new look, proving the token-based approach actually works instead of requiring per-page touch-ups later
- The actual token values that changed (`frontend/src/index.css`) — every `components/ui/*` primitive (`Card`, `Button`, ...) picks these up automatically since they're already built on these variable names, not hardcoded colors:

  | Token | Before | After |
  | --- | --- | --- |
  | `--background` | plain white/near-white | `oklch(0.97 0.015 75)` (warm cream) |
  | `--card` | plain white | `oklch(0.995 0.006 80)` |
  | `--radius` | `0.625rem` | `1rem` |
  | `--font-heading` | *(none — sans everywhere)* | `"Baloo 2", sans-serif` |
  | `--primary` | `#3a59d1` | unchanged (already-approved blue) |

## UI Revamp — Dashboard

A real Dashboard (header, stat cards, open-races list, create/join forms) replaces the old plain forms page.

### Goals (UI Revamp — Dashboard)

- The Dashboard shows account info, placeholder stat cards, and create/join forms in the new visual style

### Explain (UI Revamp — Dashboard)

- Stat cards shipped with hardcoded placeholder values from the start, explicitly flagged as pending real backend support — closed later by User Stats
- `CreateRace` was changed to auto-join the creator as a participant, closing a gap flagged back in Phase 1, as a small bundled fix alongside this rebuild
- The Dashboard's layout (`RacesPage.tsx`):

  ```mermaid
  flowchart TD
      Page["RacesPage"] --> Header["AppHeader<br/>(avatar, email, logout)"]
      Page --> Stats["StatCards<br/>(Races Joined / Won / Avg WPM)"]
      Page --> Row["two-column row"]
      Row --> Forms["CreateRaceForm + JoinRaceForm"]
      Row --> Open["OpenRacesList"]
  ```

## UI Revamp — Race Screen

A single full-height race screen (30/70 sidebar/track split) replaces the old stacked status-view-plus-typing-view cards.

### Goals (UI Revamp — Race Screen)

- One unified screen handles both the pending lobby and the active race, instead of two separate stacked components
- The typing box behaves like a real typing-test tool: a wrong keystroke is rejected outright, never inserted-then-flagged

### Explain (UI Revamp — Race Screen)

- The typing box went through many iterative rounds of direct user feedback before converging on its final strict-validation behavior
- Several real rendering bugs (word-wrap, scroll-position drift, a keyboard-sound clip playing the wrong sample) were caught from user screenshots and fixed iteratively, since no browser is available in this environment to view the running app directly
- The typing box's strict validator, per keystroke — converged on after an intermediate "cap and flag" attempt was explicitly rejected as worse:

  ```mermaid
  flowchart LR
      K["Keystroke"] --> M{"Matches the<br/>current expected character?"}
      M -->|yes| Advance["Cursor advances,<br/>play the real sample click"]
      M -->|no| Reject["Rejected outright — never inserted,<br/>current character flashes red,<br/>play the synthesized error click"]
  ```

## Race Detail Route & Race-Finish Disconnect Fix

Each race got its own URL (`/races/:raceId`), and a real concurrency bug where every player got disconnected the instant the last one finished was root-caused and fixed.

### Goals (Race Detail Route & Race-Finish Disconnect Fix)

- Visiting `/races/:raceId` shows that race directly — reloadable, shareable
- Finishing a race no longer disconnects every other still-connected player

### Explain (Race Detail Route & Race-Finish Disconnect Fix)

- The disconnect bug was two stacked races, not one: broadcasting `race_finished` and cancelling the room happened close enough together that Go's `select` could pick the shutdown case over the still-unread final message — fixed by making each connection's context independent of the room's, so only that connection's own errors can cancel it, and draining broadcasts deterministically before signaling done
- Confirmed with a real regression test proven to fail 20/20 times against the pre-fix code and pass 50/50 after, via an actual `git stash` comparison rather than just reasoning about it
- Full root-cause writeup lives in `docs/concurrency.md`. The bug, boiled down to why `select` made it possible: `r.broadcast` is buffered, so `finishRace` never blocks sending the final message before immediately calling `r.cancel()` — meaning the channel carrying the message and the "room is done" signal both become ready at *almost* the same instant, and Go's `select` picks a ready case pseudo-randomly, not in send order:

  ```mermaid
  sequenceDiagram
      participant Room as RoomActor.finishRace
      participant Hub as hub.run
      participant Write as writeLoop (per connection)

      Room->>Hub: broadcast <- race_finished (buffered, doesn't block)
      Room->>Room: r.cancel() — right after, no gap
      Note over Hub,Write: broadcast (has a value) and done/ctx.Done()<br/>(just closed) are BOTH ready now
      alt select picks done first (the bug)
          Hub->>Write: returns without forwarding race_finished
          Write->>Write: returns without ever writing it
          Note over Write: connection closes — client sees a dropped<br/>connection, never the results screen
      else select picks broadcast first (lucky timing)
          Hub->>Write: forwards race_finished
          Write->>Write: writes it, then exits
      end
  ```

  The fix makes the lucky path the *only* path: `hub.run` drains `broadcast` exhaustively before actually returning on `done`, and each connection's own context is no longer derived from the room's context at all — so `writeLoop` only ever learns "the room is gone" via `hub.closed`, which is only closed *after* draining completes. That turns a coin flip into a guarantee (Go's channel-close semantics make it a genuine happens-before relationship).

## Early Room Spawn

The room actor now spawns when a race is created, not when it starts — the prerequisite for every player holding a live connection before the race begins.

### Goals (Early Room Spawn)

- A room actor exists from the moment a race is created, not just once started
- `POST /races/{id}/start` activates the already-existing actor instead of spawning a new one

### Explain (Early Room Spawn)

- Root cause traced from a user report that other players had to manually refresh after the creator started a race — every player needing a live connection *before* start requires a room to connect to before start
- A room can now legitimately be `pending` and empty at the same time, so the no-show-timeout logic had to stop assuming "empty room" always means "abandoned mid-race"
- Before vs. after, for when a room actor actually exists relative to when players can connect:

  ```mermaid
  sequenceDiagram
      participant A as Player A (creator)
      participant B as Player B
      participant Srv as Backend

      rect rgb(235, 235, 235)
      Note over A,Srv: Before — B has no room to connect to until start
      A->>Srv: POST /races (create)
      A->>Srv: POST /races/{id}/start
      Srv->>Srv: registry.Spawn (room actor created here)
      B->>Srv: GET /ws (only now possible)
      end
      rect rgb(220, 235, 255)
      Note over A,Srv: After — B can connect the moment the race exists
      A->>Srv: POST /races (create)
      Srv->>Srv: registry.Spawn (room actor created here, pending)
      B->>Srv: GET /ws (already possible)
      A->>Srv: POST /races/{id}/start
      Srv->>Srv: actor.MarkActive() (same actor, not a new one)
      end
  ```

## Pending Connections

`GET /ws` can now attach to a still-pending room, with an explicit active/pending status gating telemetry until the race actually starts.

### Goals (Pending Connections)

- A pending room accepts WebSocket connections, not just active ones
- Telemetry sent before the race is active is dropped, not accumulated
- Leaving a pending lobby goes through the same WebSocket path as quitting mid-race, not a separate REST endpoint

### Explain (Pending Connections)

- A real, previously-live exploitable gap found during design review: a client connected to a pending race could already accumulate progress before the race legitimately started — fixed by gating `TelemetryReceived` on the room's active flag
- `POST /races/{id}/leave` was removed entirely in favor of a WebSocket `leave_race` message for both pending and active races — one mechanism instead of two
- The exploit this closed — one line, first thing `TelemetryReceived` checks now (`internal/room/room.go`):

  ```go
  case TelemetryReceived:
      if !r.active {
          return // race hasn't started yet — nothing to accumulate progress against
      }
      // ... only reachable once the race is genuinely active
  ```

## race_started Broadcast

The actual fairness fix the surrounding work exists for — every pending player learns the race started at the same moment, over the WebSocket connection they're already holding.

### Goals (race_started Broadcast)

- The instant a race starts, every connected pending player receives `race_started` with the prompt text already included
- No more manual refresh or polling needed to discover the race began

### Explain (race_started Broadcast)

- Reuses the exact fan-out mechanism `race_state` already broadcasts through — no new delivery path, just a new message type
- Carries `prompt_text` directly so a client can start typing immediately, with no follow-up REST round-trip adding its own delay variance between players
- The message every already-connected pending player receives at essentially the same instant (`internal/room/finish.go`'s `RaceStartedMessage`):

  ```json
  {"type": "race_started", "prompt_text": "quick brown fox jumps over the lazy dog ..."}
  ```

## Pending Expiry & race_expired Broadcast

A pending race now has a bounded lifetime instead of sitting open forever if the creator never starts it.

### Goals (Pending Expiry & race_expired Broadcast)

- A pending race nobody starts eventually expires and tears down cleanly
- Every connected player sees a `race_expired` message and a visible countdown beforehand, not a connection that silently dies

### Explain (Pending Expiry & race_expired Broadcast)

- Found a real, previously-nonexistent gap while building this: a full or partial lobby sitting pending was never torn down by anything that existed before this feature — the only existing teardown path only fired once every participant had already finished, which a pending race's participants never do
- Shares its teardown path with the empty-room case rather than duplicating it
- The two independent timers a pending room now races against, both converging on the same `expirePendingRoom()`:

  ```mermaid
  flowchart TD
      Spawn(["Room spawned (pending)"]) --> T1["noShowTimeout timer<br/>(nobody ever joined)"]
      Spawn --> T2["PendingTimeoutDuration timer<br/>(a full countdown, shown to players)"]
      T1 -->|fires, still empty| Expire["expirePendingRoom()"]
      T2 -->|fires, still pending| Expire
      Start(["POST /races/{id}/start"]) -->|actor.MarkActive()| Active(["active — neither timer<br/>can expire it anymore"])
      Expire --> Broadcast["broadcast race_expired"] --> Cancel["races.status = 'cancelled'"]
  ```

## Cancelled Race Status

A race that expires or empties out before starting is now persisted as `cancelled` in Postgres, instead of silently staying `pending` forever.

### Goals (Cancelled Race Status)

- An expired/abandoned pending race's status becomes `cancelled`, not stuck on `pending`
- Joining or starting a cancelled race is correctly rejected
- A visitor arriving at a dead race sees a clear message, not a permanent loading spinner

### Explain (Cancelled Race Status)

- Found by asking what a real visitor actually sees after a race expires: the previous teardown wrote zero Postgres changes, so `races.status` stayed `'pending'` forever, meaning `POST /races/{id}/join` kept succeeding into a room whose actor was already gone
- `RaceCanceller` mirrors the same structural-interface pattern already used for finishing/leaving a race
- The single-statement fix, with the ownership guard the room actor's own `!r.active` check already made redundant but kept anyway as defense-in-depth:

  ```sql
  UPDATE races SET status = 'cancelled', ended_at = now()
  WHERE id = $1 AND status = 'pending'
  ```

## Live Lobby (Frontend)

The frontend now holds a live WebSocket connection the moment a player lands on a pending race, consuming every message the backend work above added.

### Goals (Live Lobby)

- Every pending player sees new joins/leaves and the race starting live, no manual refresh
- A visible countdown shows how long until an unstarted race expires
- The manual "Refresh" button is gone entirely — nothing it worked around still needs it

### Explain (Live Lobby)

- The connection now opens the instant a session token exists, not gated on the race already being active
- An already-connected non-creator learns the race went active via the same REST re-fetch mechanism the creator's own start action already used — one uniform path instead of a second "am I active" field to reconcile
- One callback, reused for both the creator's own action and every other player's broadcast:

  ```ts
  // useRaceSocket.ts
  } else if (msg.type === "race_started") {
    const started = msg as RaceStartedMessage
    onRaceStartedRef.current?.(started.prompt_text)
    onRefreshRef.current?.()   // same callback RaceDetailPage's own handleStart already calls
  }
  ```

## Race Detail — Cold Visit & Spectator View

Visiting a race's URL cold — after it finished, or without ever having joined — now renders correctly instead of showing a broken "disconnected" state or a permanent loading spinner.

### Goals (Race Detail — Cold Visit & Spectator View)

- Reloading right after finishing a race shows results, not "you were disconnected"
- Visiting a race you never joined shows a read-only spectator view, not a broken state

### Explain (Race Detail — Cold Visit & Spectator View)

- Root cause was the page only ever rendering correctly for a client holding a live, successfully-connected WebSocket — a REST-only visitor had no path
- The WebSocket connection gate checks for *terminal* status (finished/cancelled) rather than *known-good* status, so a fresh join/create's connection isn't delayed by a REST round-trip first — deliberately chosen to avoid reintroducing the exact connection-delay unfairness the Live Lobby work above was built to eliminate
- `GetRaceWithParticipants`'s query was missing `finish_rank`/`finish_time_ms`/`avg_pace_watt` entirely — a real backend gap, not just a frontend one
- `RaceScreenSidebar`'s real dispatch order (`frontend/src/components/race-screen/RaceScreenSidebar.tsx`) — the first matching row wins, checked top to bottom:

  | Condition | What renders |
  | --- | --- |
  | `raceDetail === null` | Loading |
  | `raceDetail.status === "pending"` | Lobby (player list, Start/Leave) |
  | `leaving` | "You left the race." |
  | `evicted` | "You were disconnected too long..." |
  | `expired` | "This race wasn't started in time..." |
  | `raceDetail.status === "cancelled"` | "This race was cancelled..." |
  | `finished` or `status === "finished"` | Results list (from the live message, or `raceDetail.participants` on a cold visit — same rendering path either way) |
  | `status === "active" && !isParticipant` | Read-only **Spectating** view — leaderboard only, no `TypingBox` |
  | *(none of the above)* | The interactive typing view |

## User Stats (Backend for Dashboard Stat Cards)

The dashboard's stat cards (races joined, races won, avg WPM) now show real per-user data from Postgres instead of hardcoded placeholder numbers.

### Goals (User Stats)

- `GET /leaderboard/me` returns the caller's own races-joined/races-won/avg-WPM
- A brand-new account gets all-zero stats, not a 404

### Explain (User Stats)

- Closed a real, previously-disclosed gap: `AvgPaceWatt` had been written as `0.0` unconditionally since Race Completion shipped, because the WebSocket layer decoded `pace_watt` off the wire but never forwarded it into the room actor's telemetry event — now wired through end to end
- New `internal/leaderboard` domain package, following the same Handler/Service/Repository layering as every other REST domain
- `AvgWPM` rounds to 2 decimal places server-side, added after live testing surfaced a real, explainable oddity: a brand-new real race's WPM looked implausibly low because it was averaged against dozens of historical test races that had `0` pace recorded before this fix existed
- `GET /leaderboard/me`'s response, computed from `leaderboard_alltime`'s running counters (`total_wins`/`total_pace_watt_sum` added by this feature's migration):

  ```json
  {"races_joined": 41, "races_won": 6, "avg_wpm": 47.82}
  ```

## Open Races (Real List + Polling)

The dashboard's "Open Races" list now shows real, joinable pending races from Postgres, polling every 5 seconds, with a working "Join" button instead of a decorative fake one.

### Goals (Open Races)

- `GET /races` lists pending, joinable races the caller hasn't already created or joined
- The list updates on its own every 5 seconds — no manual refresh
- Clicking "Join" actually joins the race and lands the player on it

### Explain (Open Races)

- Excludes races the caller already created or joined, not just full ones — otherwise a creator would see their own just-created race in their own browsable list and get a conflict clicking it
- Two real bugs found and fixed while testing this feature live: a pending lobby's player list never updated on a join/leave without a manual refresh (a frontend rendering gap, not a missing broadcast), and the last participant leaving a pending race never actually cancelled it (a missing call to the existing finish-check logic)
- The actual query (`internal/postgres/race_repository.go`) — one round-trip for every race's player count (`LEFT JOIN` + `GROUP BY`) instead of one `COUNT` query per race, plus the two exclusion rules in the same `WHERE`:

  ```sql
  SELECT r.id, r.name, r.distance_meters, u.display_name, count(rp.user_id), r.created_at
  FROM races r
  JOIN users u ON u.id = r.created_by
  LEFT JOIN race_participants rp ON rp.race_id = r.id
  WHERE r.status = 'pending'
    AND NOT EXISTS ( -- excludes races the caller already created or joined
      SELECT 1 FROM race_participants mine
      WHERE mine.race_id = r.id AND mine.user_id = $1
    )
  GROUP BY r.id, u.display_name
  HAVING count(rp.user_id) < $2 -- excludes already-full races
  ORDER BY r.created_at DESC
  LIMIT $3
  ```

## Redirect to Login on 401

Any API call that comes back `401 Unauthorized` now clears the stored session and redirects to the login page automatically, instead of leaving the user stuck on a broken page.

### Goals (Redirect to Login on 401)

- An expired or invalid JWT on any authenticated request redirects to `/login`, app-wide
- A wrong password on the login form itself still shows inline, not a redirect

### Explain (Redirect to Login on 401)

- Centralized in `apiFetch`, the one function every authenticated request already goes through — cheaper and more reliable than adding an `isAuthenticated()` check to every page individually
- `/auth/login`/`/auth/register` are explicitly excluded, since a `401` there means "wrong credentials," a normal validation outcome, not "your session expired"
- The actual guard (`frontend/src/lib/api.ts`) — every authenticated request passes through `apiFetch`, so this one check covers the whole app:

  ```ts
  const PUBLIC_PATHS = ["/auth/login", "/auth/register"]

  if (res.status === 401 && !PUBLIC_PATHS.includes(path)) {
    clearToken()
    window.location.href = "/login"
  }
  ```

## Idempotent Join / Session Token Recovery

Joining a race you're already part of no longer errors — it hands back a working session token instead, closing a real gap where reloading the page mid-race could permanently strand a player as a read-only spectator.

### Goals (Idempotent Join / Session Token Recovery)

- Re-joining a race you're already in returns a fresh session token instead of a `409`
- This now also works for a race already in progress, not just one still pending
- Reloading the race page recovers automatically instead of getting stuck

### Explain (Idempotent Join / Session Token Recovery)

- Traced end to end before writing any code: the session token only ever lived in router navigation state, never persisted, so any reload lost it — and the only way to get a new one, re-joining, previously failed two different ways depending on race status
- Fixed by checking participation before checking status — an already-joined caller gets a fresh token regardless of whether the race is pending, active, finished, or cancelled
- Verified live against a real running server and Postgres, not just unit tests, reproducing every scenario directly with curl
- What `POST /races/{id}/join` returns for the exact same caller, before and after this feature:

  | Scenario | Before | After |
  | --- | --- | --- |
  | Re-joining a race you're already in, still `pending` | `409 already_joined` | `200` with a fresh token |
  | Recovering a lost token on a race that's already `active` | `409 race_not_pending` (no way to recover at all) | `200` with a fresh token |
  | A genuinely new user joining an `active` race | `409 race_not_pending` | `409 race_not_pending` (unchanged — correctly still rejected) |

## Structured Logging

The backend's logging switched from plain, unstructured text lines to `slog`-based structured logs tagged with `race_id`/`user_id`/`request_id`, and every request now gets a random id that ties its whole story together across handlers, goroutines, and log lines — the foundation Phase 3's later observability work (metrics, load testing) builds on.

### Goals (Structured Logging)

- Replace every existing `log.Printf` call site with structured `slog` logging, tagged with `race_id`/`user_id`/`request_id` wherever that information is available
- Introduce `request_id` as a new concept in this codebase: generated per request, attached to the request, and logged
- Add one summary log line per HTTP request (method, path, status, duration, `request_id`, and `user_id` when known) so a downstream `room`/`ws` log line can be tied back to the request that caused it
- Thread a single process-wide `*slog.Logger` explicitly through constructors — no package-level global logger

### Explain (Structured Logging)

- **What "structured" actually means here**: the old logs were free-text sentences built with `fmt`-style verbs, e.g. `room %s: leave race for user %s: %v` — readable by a human scrolling a terminal, but not filterable by a machine except with fragile string matching. `slog` instead emits one JSON object per line, with named fields. That shape is what makes a real log aggregator (or even just `grep`/`jq` on a raw log file) able to answer "show me every error for race X" instead of "show me every line that happens to mention race X somewhere in its sentence."
- **Example log output** — three real lines this backend now actually emits (each one is a single line in the real log file; shown here as pretty-printed-looking JSON only for readability), generated by running this project's exact logger setup:

  ```json
  {"time":"2026-07-23T14:02:11.482103+07:00","level":"INFO","msg":"http_request","method":"POST","path":"/races/9f2kD8mQvxaB/join","status":200,"duration":1372000,"request_id":"a13f9c02e4b7419dbb6f0a3c8e21d9f4","user_id":"7c1e2b3a-9f4d-4e2a-8b3c-1d2e3f4a5b6c"}
  {"time":"2026-07-23T14:02:45.109553+07:00","level":"ERROR","msg":"finish race failed","race_id":"9f2kD8mQvxaB","error":"pool: acquiring connection: EOF"}
  {"time":"2026-07-23T14:03:01.774820+07:00","level":"WARN","msg":"dropping malformed message","race_id":"9f2kD8mQvxaB","user_id":"7c1e2b3a-9f4d-4e2a-8b3c-1d2e3f4a5b6c","error":"unexpected end of JSON input"}
  ```

  The first line is the per-request summary (`RequestLog`): note it has both `request_id` *and* `user_id`, because this particular request happened to be authenticated (`Auth` ran and filled in the recorder — see the context-propagation bullet below). The second and third lines come from a room's own pre-tagged child logger, so they carry `race_id` (and `user_id`, for the WebSocket one) automatically without a `request_id` at all — they weren't triggered by an HTTP request, they happened later, inside a long-lived room actor's own goroutine. One easy-to-miss detail worth flagging for anyone reading raw output like this for the first time: `"duration":1372000` is *not* milliseconds, it's the raw nanosecond count from Go's `time.Duration` (`slog.Duration` doesn't format it as a string) — `1372000` ns = `1.372ms`. A log viewer or query would typically divide by `1e6` (or `1e9`) to turn it back into a human unit.
- **Why these three specific tags**: `race_id` scopes a line to one race, `user_id` scopes it to one player, and `request_id` scopes it to one HTTP request — this is the standard "correlation ID" pattern used to reconstruct a single request's full journey through a system that has many things happening concurrently. This app already has a lot of concurrency to untangle: one long-lived goroutine per race room, two goroutines per WebSocket connection (reader + writer), and one goroutine per in-flight HTTP request — without a shared id threaded through all of them, a log line from deep inside a room actor has no way to say which request (or which user's click) actually caused it.
- **`request_id` is genuinely new**: nothing in this codebase generated or tracked one before. It's created with `crypto/rand` (the same cryptographically-secure random source `internal/race.GenerateRaceID` already used for race ids — never Go's `math/rand`, which is predictable and wrong for anything identifier-shaped), stored on the request's `context.Context` (Go's built-in per-request "bag of values" that every handler in the chain can read from), and echoed back to the client as an `X-Request-ID` response header — so if a user reports "the app broke," that header value can be pasted straight into a log search instead of guessing which request it was.
- **A real Go subtlety this feature had to work around**: the new per-request summary line is logged by an *outer* middleware (`RequestLog`, wrapping the whole app), but whether a request is authenticated is only known by an *inner* middleware (`Auth`, which only wraps specific routes, deep inside route registration). Go's `context.Context` only flows one direction — a value an inner handler attaches is invisible to the outer caller that invoked it, because `context.WithValue` returns a brand-new context rather than mutating the one the outer caller is holding onto. The fix: the outer `RequestLog` middleware creates a small mutable "recorder" object and puts a *pointer* to it in the context before calling onward; `Auth`, if it runs, writes the authenticated user id into that same object through the pointer. Since both sides are looking at the same object in memory (not a copied value), `RequestLog` can read the user id back out after the request finishes — even though `Auth` ran "underneath" it. This is a small but genuinely useful pattern to recognize any time one middleware needs to learn something that only becomes known further down the chain.

  ```mermaid
  sequenceDiagram
      participant Req as Request
      participant RL as RequestLog (outer)
      participant Auth as Auth (inner, per-route)
      participant H as Handler

      Req->>RL: next.ServeHTTP(w, r)
      RL->>RL: attrs := &requestLogAttrs{}<br/>ctx := context.WithValue(r.Context(), key, attrs)
      RL->>Auth: next.ServeHTTP(w, r.WithContext(ctx))
      Note over Auth: r.WithContext returns a NEW *http.Request —<br/>RL's own r variable is never mutated
      Auth->>Auth: setUserIDForLog(ctx, userID)<br/>writes into *attrs — same object RL still holds
      Auth->>H: next.ServeHTTP(w, r.WithContext(ctx2))
      H-->>Auth: response written
      Auth-->>RL: returns
      Note over RL: attrs.userID is now populated,<br/>even though RL's own r/ctx never changed
      RL->>RL: logger.Info("http_request", ..., "user_id", attrs.userID)
  ```

- **Avoiding repetition with "child loggers"**: rather than writing `slog.String("race_id", raceID)` at every single log call inside a room actor's entire lifetime, each room is handed a logger that's already been "pre-stamped" with its own `race_id` once, at the moment the room is created (via `logger.With(slog.String("race_id", raceID))`). Every log call made through that pre-stamped logger automatically carries the tag from then on — like writing a return address on an envelope once instead of on every letter you ever send from that address. The same idea tags each WebSocket connection's logger with both `race_id` and `user_id` the moment the connection is accepted.
- **Explicitly left out of scope**: no configurable log *level* filtering (e.g. an env var to only show warnings and above in production) — `slog` already defaults to showing `Info` level and up, and a minimum-level knob was judged a genuine "nice to have later," not something the original requirement actually asked for. The handful of `log.Fatalf` calls that can happen before the server even starts (failing to connect to Postgres, a broken migration) were deliberately left as plain, unstructured fatal errors, since nothing is listening to structured logs yet at that point in startup anyway.
- Verified with dedicated tests proving the tricky parts actually work, not just assumed to: one confirms two different requests really do get two different `request_id`s, and one specifically drives a request through `RequestLog` wrapping `Auth` end to end and checks the resulting log line really does contain the authenticated user's id — proving the pointer-recorder trick above works, not just reasoning that it should.

## Prometheus Metrics

Added a `GET /metrics` endpoint that exposes five numbers about the server's live internal state — active race count, live WebSocket connections, how full each internal queue is, how long each race "tick" takes, and goroutine count — in the text format the Prometheus monitoring tool expects, giving direct visibility into the exact kind of concurrency problems (leaks, backlogs, slowdowns) this project is built to practice diagnosing.

### Goals (Prometheus Metrics)

- `GET /metrics` (Prometheus text format) exposing exactly the 5 metrics the project's observability goals call for: active room count, connection count, broadcast tick latency, goroutine count, channel buffer usage — plus the standard Go process/runtime metrics that come for free
- No new locks or goroutines: every metric reads state that's already safe to read concurrently (a mutex that already existed, a Go builtin documented as concurrency-safe, or a new plain atomic counter)
- Keep the core `room`/`ws` packages free of any Prometheus-specific import — all the metrics-library wiring lives in one new, separate `internal/metrics` package

### Explain (Prometheus Metrics)

- **What Prometheus is and what "scraping" means, for context**: Prometheus is a widely-used monitoring system that works by periodically making an HTTP GET request ("scraping") to a `/metrics` endpoint an application exposes, reading back a snapshot of numbers in a simple text format, and storing that snapshot with a timestamp — repeated every few seconds, this builds up a time series you can graph ("how did active connection count change over the last hour?") or alert on ("page someone if tick latency stays above 200ms for 5 minutes"). This feature is entirely about building that `/metrics` endpoint correctly; it doesn't include setting up an actual Prometheus server to scrape it.
- **The four kinds of numbers Prometheus deals in**: every metric declares a "type" that tells Prometheus (and anyone reading the raw output) how to interpret and aggregate it correctly. This project only *creates* Gauges and one Histogram directly, but the auto-registered Go runtime collectors bring real examples of all four kinds along for free, so the last column below points at genuine lines from this project's own `GET /metrics` output rather than a hypothetical:

  | Type | What it means | When you'd reach for it | A real example from this project's `/metrics` output |
  | --- | --- | --- | --- |
  | **Gauge** | A value that can go up *or* down, read fresh at scrape time — "what's true right now" | Counting something that fluctuates: current queue depth, how many things are open/active/connected at this instant | `aviron_rooms_active`, `aviron_connections_active`, `aviron_channel_buffer_used` (this project's own), plus `go_goroutines` (from the Go runtime collector) |
  | **Counter** | A cumulative total that only ever goes *up* (it only resets to 0 if the whole process restarts) | Counting total occurrences over the process's lifetime: total requests served, total bytes sent, total errors | `process_cpu_seconds_total`, `go_memstats_mallocs_total` (both from the runtime/process collectors — this project has no custom Counter yet) |
  | **Histogram** | Buckets many individual timed (or sized) observations into ranges, plus a running sum and count — lets Prometheus compute percentiles later, aggregating correctly even across multiple server instances | A *distribution* of many measurements where you care about the shape, not just the latest value — "is p99 duration creeping up?" | `aviron_tick_latency_seconds` (this project's one Histogram, observing every room's broadcast-tick duration) |
  | **Summary** | Similar idea to a Histogram, but the *client* (this Go process) computes the percentiles itself before they're ever scraped, instead of handing Prometheus the raw buckets to compute them from | Rarely the right first choice today — a Summary's pre-computed percentiles can't be correctly combined across multiple server instances the way a Histogram's buckets can | `go_gc_duration_seconds` (from the Go runtime collector, reporting how long recent garbage-collection pauses took — this project registers no custom Summary) |

  For this project's own 5 required metrics specifically: active room count, connection count, and channel buffer usage are all Gauges (a fresh "what's true right now" read on every scrape); broadcast tick latency is the one Histogram (a distribution built up from many individual tick measurements over time); and goroutine count deliberately isn't a custom metric at all — the Go runtime collector already reports it as the Gauge `go_goroutines`, so adding a second, custom-named gauge measuring the exact same underlying number would just be a duplicate, not a second useful data point.
- **Why "no new locks" mattered as a hard constraint**: this whole project follows a "single-writer" concurrency style — each race room and each WebSocket fan-out hub is owned by exactly one goroutine, and every other goroutine talks to it only through channels, specifically to avoid the class of bugs that comes from multiple goroutines fighting over a shared mutex. Metrics collection could easily have broken that discipline (a scrape happening on its own goroutine, reaching into a room's live state) — so each metric was deliberately chosen to read something already safe to read from any goroutine: `len(aChannel)` is documented in Go as always safe to call concurrently, a `sync.RWMutex`-guarded registry already existed for room lookups, and a brand-new `atomic.Int64` counter was used anywhere nothing safe already existed.
- **A genuine circular-construction problem, and how it was resolved**: the room registry needs to be told about the metrics system when it's built (so a spawning room can report its own tick timing), but the metrics system needs the room registry (and the WebSocket handler) to already exist before it can wire up the gauges that read from them — each side seems to need the other to exist first. Solved by splitting metrics construction into stages: build the metrics object first with zero dependencies (it can already record tick timings even with nothing else built yet), then hand it to the room registry as it's built, then — once the room registry and WebSocket handler both exist — go back and register the gauges that read from them. This "construct now, wire up fully later" pattern is a common way to break exactly this kind of circular dependency.

  ```mermaid
  flowchart TD
      A["metrics.NewMetrics()<br/>(zero dependencies)"] --> B["room.NewRegistry(logger, m)<br/>m used as the TickObserver"]
      B --> C["ws.NewWSHandler(registry, ...)"]
      C --> D["m.RegisterRoomGauges(registry)"]
      D --> E["m.RegisterWSGauges(wsHandler)"]
      E --> F["server.Handle('GET /metrics', m.Handler())"]
  ```

  All five steps run once, in this exact order, inside `internal/app.go`/`internal/httpserver/route.go` — nothing here is circular anymore, it just has to happen in this specific sequence.
- **A subtle, easy-to-get-wrong decision about where a connection counter lives**: the natural first instinct for "count live WebSocket connections" is a single global counter variable, incremented/decremented wherever a connection map is updated. Two things were wrong with the obvious version of that: (1) this project had *just* moved away from global mutable state in the logging feature above, so a bare global counter would have quietly reintroduced exactly the pattern that was just removed — fixed by making it a field on the one object that already represents "the whole WebSocket handler," not a floating package variable; (2) incrementing/decrementing tied to the internal connection map's own add/remove points turned out to have a real leak: once a race room finishes, its connection-fan-out object shuts itself down, and the code path that would normally decrement the counter turns into a silent no-op at exactly that moment — meaning every connection present when a race ended would never actually get un-counted, and the number would only ever climb. Fixed by counting at the *outer* boundary instead — one increment when a connection starts being handled, one decrement (guaranteed to run, via Go's `defer`) when that handling finishes — which can't leak regardless of how the connection's internals shut down.
- **Reading a private, goroutine-owned map from the outside**: one of the three channel-buffer-usage numbers (how full each WebSocket connection's own outbound message queue is) lives inside data that only one specific goroutine is allowed to touch directly, by design — there was no existing field anywhere else to just read it from. The fix mirrors a pattern this codebase already used elsewhere for the same problem: send a small "please tell me the answer" request into that goroutine's own inbox channel, along with a private reply channel, and let it reply once it gets around to processing that request in its normal turn — the same shape as asking someone a question by leaving a note on their desk with a return envelope, rather than walking into their office and looking at their notes yourself.
- **Naming convention**: every custom metric is prefixed `aviron_` (e.g. `aviron_rooms_active`, `aviron_connections_active`, `aviron_tick_latency_seconds`) to keep this app's own numbers clearly separate from the automatically-provided Go runtime ones (`go_goroutines`, `go_memstats_*`, etc.) in the scraped output.
- **Deliberately avoided per-race labels**: Prometheus lets a metric carry extra dimensions ("labels"), e.g. a separate connection count *per race*. That was deliberately not done here — a label value is created for every distinct value ever seen, so a `race_id` label would mean the metrics system accumulates one new permanent label value for every race ever created for as long as the process runs, which is a well-known Prometheus foot-gun ("cardinality explosion") worth avoiding on purpose rather than discovering by accident later.
- Verified with a real HTTP round-trip test that drives the actual route-registration code used in production (not just the metrics package in isolation) and checks the scraped response body contains every expected metric name — plus dedicated tests for each of the trickier pieces above (the connection counter surviving a disconnect, the goroutine-owned-map query, the sum-across-multiple-rooms arithmetic), all re-run multiple times under Go's race detector to rule out flakiness.
