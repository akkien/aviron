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

## Swagger API Docs

A standalone infra addition, not part of the Phase 1 feature plan: generated interactive API documentation from Go code comments, browsable in a web UI instead of hand-written and separately maintained.

### Goals (Swagger API Docs)

- `GET /healthz` and `POST /auth/register` documented via `github.com/swaggo/http-swagger` annotations
- A browsable UI at `GET /swagger/`
- A `make docs` target to regenerate after future annotation changes

### Explain (Swagger API Docs)

- Swagger (also called OpenAPI) is a standard way of describing a REST API's endpoints, request/response shapes, and status codes as structured data, which tools can then turn into an interactive "try it out in your browser" page — instead of a developer hand-writing and manually keeping a separate API doc in sync, the description is generated straight from specially-formatted comments already sitting above each handler function
- `swag init` (the `swaggo` project's generator) scans the codebase for those comments and writes the generated output into `backend/docs/` (`docs.go`, `swagger.json`, `swagger.yaml`) — checked into the repo like any other generated-but-committed artifact, regenerated by hand via `make docs` rather than on every build
- One annotation block per documented handler, directly above the function it describes:

  ```go
  // @Summary Register a new user
  // @Tags auth
  // @Router /auth/register [post]
  func (h *AuthHandler) Register(w http.ResponseWriter, r *http.Request) { ... }
  ```

- `internal/httpserver/route.go` serves the generated UI itself via `httpSwagger.WrapHandler` mounted at `GET /swagger/`
- Only two endpoints were annotated at this point (`/healthz`, `/auth/register`) — every domain added afterward (races, leaderboard, ...) picked up its own annotations as it shipped, the same incremental way the rest of this API grew

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

## pprof

Enabled Go's built-in `net/http/pprof` profiler under `/debug/pprof/`, gated by an env var — the third and smallest of Phase 3's observability tools, giving a way to answer "*why* did goroutine count climb" once Prometheus's gauge (above) shows *that* it did.

### Goals (pprof)

- `GET /debug/pprof/*` registered on this project's real mux (not the process-wide default one)
- Gated behind `Config.PprofEnabled` (env `PPROF_ENABLED`, default `true`) — a single bool, not a full environment enum this codebase has no other use for
- Unauthenticated, matching `GET /metrics`'s precedent — an operator/tool endpoint, not browser or API traffic

### Explain (pprof)

- **The gotcha this feature exists to avoid**: `net/http/pprof`'s own `init()` function registers its handlers onto Go's *global* `http.DefaultServeMux` the instant the package is imported — a common trap, since most real servers (this one included) build their own separate `*http.ServeMux` and never actually serve `http.DefaultServeMux` at all. A bare `import _ "net/http/pprof"` here would compile clean, add nothing to any request log, and simply never be reachable — silent, not a crash, which is exactly what makes it worth calling out explicitly rather than discovering it by confused troubleshooting later:

  ```mermaid
  flowchart LR
      subgraph wrong["A blank import (wrong for this project)"]
          I1["import _ \"net/http/pprof\""] -->|registers onto| D["http.DefaultServeMux<br/>(never actually served)"]
      end
      subgraph right["What this feature does instead"]
          I2["explicit pprof.Index/Cmdline/<br/>Profile/Symbol/Trace"] -->|registered onto| M["this project's own<br/>*http.ServeMux"]
      end
  ```

- **Exactly 5 handlers cover every profile**, not one registration per profile type — `pprof.Index`, registered at the trailing-slash pattern `/debug/pprof/`, dispatches `/debug/pprof/goroutine`, `/debug/pprof/heap`, `/debug/pprof/allocs`, etc. itself, the same subtree-matching behavior this project's `GET /swagger/` route already relies on:

  | Registered pattern | Handler | What it serves |
  | --- | --- | --- |
  | `/debug/pprof/` | `pprof.Index` | The index page, *and* every named profile (`goroutine`, `heap`, `allocs`, `block`, `mutex`, `threadcreate`) via its own internal dispatch |
  | `/debug/pprof/cmdline` | `pprof.Cmdline` | The running process's command-line invocation |
  | `/debug/pprof/profile` | `pprof.Profile` | A CPU profile, sampled over `?seconds=N` |
  | `/debug/pprof/symbol` | `pprof.Symbol` | Program counter → function name lookups |
  | `/debug/pprof/trace` | `pprof.Trace` | An execution trace, over `?seconds=N` |

- **A real correctness bug caught before shipping, not after**: every other route in this file is registered with a `"GET /path"` method-restricted pattern (Go 1.22's method-aware `http.ServeMux`). Doing the same for these 5 would have silently broken `go tool pprof`'s use of `/debug/pprof/symbol`, since `pprof.Symbol`'s own implementation branches on the request method — reading the query string for `GET`, but reading the request body for `POST` (used when resolving a large batch of addresses at once). All 5 pprof handlers are registered unrestricted by method instead, deliberately breaking from this file's own convention, with a comment explaining why.
- The gate itself (`internal/config/config.go`):

  ```go
  PprofEnabled: getEnvBool("PPROF_ENABLED", true),
  ```

- Verified both states directly, not just the happy path: `PprofEnabled: true` → `200` on `/debug/pprof/`, `/goroutine`, `/cmdline`; `PprofEnabled: false` → `404` (nothing registered at all, not just an auth wall in front of it).

## k6 Load Test

Simulates hundreds of real players — register, log in, create/join/start a race, open the real WebSocket handshake, type at a realistic pace — to generate genuine concurrent load against a running instance, closing out Phase 3. Along the way, running it for real against a real server surfaced and fixed a serious bug that had been silently breaking every real WebSocket connection since Structured Logging shipped.

### Goals (k6 Load Test)

- A `load/` directory (repo root) with k6 scripts simulating the full real client flow, ending in genuinely concurrent WebSocket + telemetry traffic
- Scale knobs (race count, players per race, target word count) tunable per run via `-e` flags, not hardcoded
- `make loadtest` to run it
- Builds the load-generation tool and the means to watch a run happen — explicitly **not** fixing whatever it finds, and **not** deploying a Prometheus server/Grafana

### Explain (k6 Load Test)

- **The coordination problem this design had to solve**: the naive reading of "one VU creates a race, others join it" runs straight into a real constraint — k6 virtual users are independent JS runtimes with no shared memory and no message-passing between them at runtime. Resolved by moving every REST call (register, login, create, join, start) into k6's `setup()`, which runs once, single-threaded, before any VU executes — turning "coordinate N concurrent actors" into "run N sequential steps," which needs no coordination mechanism at all. The part that actually matters for a *load* test — hundreds of concurrent WebSocket connections and telemetry streams stressing the room actor's single-writer goroutines — is completely unaffected, since it all still happens in the genuinely-parallel per-VU phase:

  ```mermaid
  sequenceDiagram
      participant Setup as setup() — single-threaded
      participant Backend
      participant VUs as All VUs — genuinely parallel

      Note over Setup,Backend: Sequential: no coordination needed
      Setup->>Backend: register + login (N users)
      Setup->>Backend: POST /races (creator, auto-joined)
      Setup->>Backend: POST /races/{id}/join (every other player)
      Setup->>Backend: POST /races/{id}/start
      Setup-->>VUs: returns [{raceID, sessionToken}, ...] per VU

      Note over VUs,Backend: Parallel: the actual load
      par VU 1
          VUs->>Backend: GET /ws, join_race, telemetry×N
      and VU 2
          VUs->>Backend: GET /ws, join_race, telemetry×N
      and VU N
          VUs->>Backend: GET /ws, join_race, telemetry×N
      end
  ```

- **The `pace_watt` telemetry sent is not approximated — it's the same formula the real frontend uses** (`frontend/src/components/race-screen/TypingBox.tsx`), so a load-test run produces numbers the room actor treats identically to a real player's:

  ```js
  // load/lib/ws-client.js, mirroring TypingBox.tsx exactly
  const elapsedMinutes = (Date.now() - startedAtMs) / 60000
  const paceWatt = elapsedMinutes > 0 ? Math.round(wordsCompleted / elapsedMinutes) : 0
  ```

- **A real, severe bug found by actually running this against a real server — not a load-test finding in the "backpressure/goroutine leak" sense this spec deliberately deferred, but a full regression**: the very first real run got `501 Not Implemented` on every single WebSocket handshake. Root cause: `RequestLog` (Structured Logging, above) wraps every request's `http.ResponseWriter` in a small `statusWriter` struct to capture the status code for the log line — but that wrapper only embeds the 3-method `http.ResponseWriter` *interface*, not the concrete writer's full method set, so `http.Hijacker` (which `coder/websocket.Accept` requires to take over the raw TCP connection for a WebSocket upgrade) silently stopped being reachable through it. Since `RequestLog` wraps the *entire* mux, this broke every real WebSocket connection — including real users' — the moment that feature shipped, and nothing in the test suite caught it because `internal/ws`'s own tests exercise a fake connection, never a real `net/http` hijack:

  ```mermaid
  sequenceDiagram
      participant Client
      participant RL as RequestLog
      participant WS as GET /ws handler
      participant Lib as coder/websocket.Accept

      Client->>RL: GET /ws (Upgrade: websocket)
      RL->>RL: wraps w in statusWriter{ResponseWriter: w}
      RL->>WS: next.ServeHTTP(statusWriter, r)
      WS->>Lib: Accept(statusWriter, r, ...)
      Lib->>Lib: w.(http.Hijacker) — FAILS, statusWriter has no Hijack()
      Lib-->>Client: 501 Not Implemented
  ```

  Fixed by giving `statusWriter` its own `Hijack()` method that forwards to the underlying writer (erroring cleanly if that writer genuinely doesn't support it, rather than panicking) — a one-line-conceptually fix once found, but the finding itself only came from an end-to-end run through the real middleware chain, exactly the gap unit tests with fakes can't close. Two regression tests now cover it directly: one proving the forward actually happens, one proving the error path behaves when it genuinely can't.
- **A real successful run, after the fix** — 5 races × 8 players, 30 words each, against a real Postgres-backed instance:

  | Metric | Result |
  | --- | --- |
  | Iterations completed | 40 / 40 |
  | Checks passed | 6,382 / 6,382 (100%) |
  | HTTP failures | 0 |
  | `ws_msgs_sent` | 1,240 (= 40 VUs × 31 messages: 1 `join_race` + 30 `telemetry`, exactly) |
  | Broadcast tick latency | all 987 ticks under 5ms |
  | Goroutines, before → after | 7 → 8 (no climb — no leak signature) |
  | `aviron_rooms_active`/`aviron_connections_active` after | 0 / 0 |

  A clean result at this scale is itself a meaningful finding, not a non-result: it establishes a healthy baseline before the next run ramps `NUM_RACES`/`VUS_PER_RACE` up further to actually find where (if anywhere) this instance starts to strain.
- Explicitly not built in this first pass: deliberate disconnect/reconnect chaos (a natural follow-up once steady-state numbers are understood), and testing actual horizontal scaling (Redis, ≥2 instances — Phase 4). This closes out every spec in Phase 3.

## Ranked Leaderboard

A new `GET /leaderboard` endpoint ranks every player against each other, not just against their own history — the existing `GET /leaderboard/me` only ever answered "how am I doing," never "who's actually winning."

### Goals (Ranked Leaderboard)

- `GET /leaderboard?window=alltime|weekly` returns a ranked list of players, sorted by wins first and average WPM as the tiebreaker
- `alltime` reads straight from the existing `leaderboard_alltime` table; `weekly` is computed live from `race_participants`/`races` for races that finished in the last 7 days — no second aggregate table to keep in sync
- A caller-supplied `limit` (clamped to a sane range) instead of always returning every player

### Explain (Ranked Leaderboard)

- The response reuses `LeaderboardEntryResponse`'s vocabulary (`races`/`wins`/`avg_wpm`) from the existing `GET /leaderboard/me` shape rather than inventing new field names for the same metrics:

  ```json
  {
    "window": "weekly",
    "entries": [
      { "rank": 1, "user_id": "...", "display_name": "kien", "races": 12, "wins": 9, "avg_wpm": 71.4 },
      { "rank": 2, "user_id": "...", "display_name": "trung", "races": 8, "wins": 5, "avg_wpm": 68.2 }
    ]
  }
  ```

- `weekly` can't just read a pre-aggregated table the way `alltime` does — there's no "this week's" materialized view — so it's a live query joining `race_participants` to `races`, filtered to `status = 'finished' AND ended_at >= now() - interval '7 days'`, and only sorted/limited at query time
- Deliberately stays on Postgres rather than reaching for a columnar store: `context/project-overview.md` §6 already scoped ClickHouse out of this project, and a single `ORDER BY wins DESC, avg_wpm DESC LIMIT $1` is well within what a regular index can serve at this project's scale
- 8 new tests cover both windows, the sort/tiebreak order, and the limit clamping

## Ranked Leaderboard — Frontend, then a full `/races` Dashboard Redesign

The `GET /leaderboard` endpoint from the previous section had no UI yet — this feature gave it one, then used the same pass to redesign the `/races` dashboard around it so the page fit on one screen without scrolling.

### Goals (Ranked Leaderboard Frontend)

- A leaderboard view in the React app, switchable between `alltime` and `weekly`
- Replace the original caller-supplied `limit` with real page-based pagination, since a growing player base makes "return up to N players" an increasingly bad fit for a UI that can only show a handful at a time
- Fold the leaderboard into the existing `/races` dashboard layout rather than shipping it as a disconnected extra page

### Explain (Ranked Leaderboard Frontend)

- The backend response shape changed to support pagination — `limit`/a flat `entries` array became `page`/`total_pages`, with the page size fixed server-side rather than caller-chosen:

  ```go
  // backend/internal/leaderboard/service.go
  const pageSize = 5 // fixed, not caller-configurable — the client only ever asks for a page number

  type LeaderboardTopResponse struct {
      Window     string                     `json:"window"`
      Page       int                        `json:"page"`
      TotalPages int                        `json:"total_pages"`
      Entries    []LeaderboardEntryResponse `json:"entries"`
  }
  ```

- Fixing the page size server-side (rather than trusting a caller-supplied count) keeps the UI's pagination controls and the API's own response in lockstep — there's no page size for the frontend to get out of sync with
- The `/races` dashboard redesign was a pure layout change driven by the new leaderboard panel needing screen space: existing race-list/create-race UI was reflowed to fit the leaderboard alongside it on one page instead of pushing it below the fold

## Redis Room Registry

The first piece of Phase 4's horizontal-scaling work (project-overview.md §5): a way for any instance to answer "which instance is actually running race X," so a second `race-service` process can exist without every room becoming a coin flip about which pod a client happens to land on.

### Goals (Redis Room Registry)

- Whichever instance spawns a room's `RoomActor` durably records itself as that room's owner in Redis
- The claim survives for as long as the room is actually running, via a periodic heartbeat, and disappears automatically if the owning instance crashes without cleaning up
- Every instance can be notified the moment a room appears or disappears, not just poll for it

### Explain (Redis Room Registry)

- `internal/roomlocator.Locator` wraps a plain `*redis.Client` with four operations: `Claim` (`SET NX EX`, so only the first instance to try ever succeeds), `Refresh` (extends the TTL), `Release` (deletes the key), and `Owner` (reads it back) — `Owner` is what a second instance calls to find out where to route a request for a room it doesn't hold itself:

  ```go
  // backend/internal/roomlocator/locator.go
  const claimTTL = 60 * time.Second

  func (l *Locator) Claim(ctx context.Context, raceID string) (bool, error) {
      claimed, err := l.client.SetNX(ctx, roomKey(raceID), "instance:"+l.instanceID, claimTTL).Result()
      ...
  }
  ```

- `internal/room.Registry.Spawn` calls `Claim` synchronously, before the room actor's goroutines start, and then spawns a dedicated `heartbeat` goroutine that calls `Refresh` every 20s (`heartbeatInterval`) — comfortably inside the 60s TTL, so an ordinary GC pause or scheduling delay never lets a live room's claim silently expire out from under it
- `Claim`/`Release` also publish a `created`/`removed` event onto a shared `room:events` Redis pub/sub channel, so another instance's routing layer can react the instant ownership changes instead of only finding out on its next `Owner` lookup
- Ownership is decided once, by construction — whichever instance's `POST /races` request spawned the room — never contested afterward; `Claim` reporting `claimed=false` for an already-owned room is a defensive signal, not an expected code path
- A `NoopLocator` (every method a no-op returning success) is what single-instance dev and most tests run against, so `internal/room` never has to special-case "is Redis configured" — the interface is identical either way
- Re-confirmed, unchanged, after Phase 4's WS Gateway pivot rewrote the surrounding spec set: this package's own responsibility — durable room ownership — never moved once `internal/roomrelay`/`internal/wsgateway` took over connection routing; only the caller composing it changed

## Race Router

**Superseded** — replaced by the WS Gateway (see below) once Phase 4's design shifted from "route REST requests to the owning instance" to a dedicated proxy service in front of `race-service`. Documented here for completeness, since it shipped and worked before being replaced.

### Goals (Race Router)

- A client could `POST`/connect to any `race-service` instance and still reach the correct room, even if that instance didn't own it
- Requests for a room owned by another instance were transparently forwarded, not rejected with a "wrong instance" error

### Explain (Race Router)

- Every `race-service` instance checked `roomlocator.Locator.Owner` for the target `race_id`; a local hit was served directly, a remote hit was reverse-proxied to the owning instance's own address
- This meant `race-service` itself did double duty as both the application and its own routing layer — workable with 2 instances, but the reason it was later replaced: routing logic and race logic lived in the same process, and every new instance had to know how to proxy for every other one
- The WS Gateway entry below replaced this with a dedicated proxy in front of `race-service`, so `race-service` itself never needs to know about any instance but its own

## Multi-Instance Local Dev Setup & Verification (Race Router era)

**Superseded** — replaced by the WS Gateway–era Multi-Instance Dev Setup & Verification (see below) once Race Router itself was replaced. Documented here for completeness.

### Goals (Multi-Instance Setup, Race Router era)

- Run two `race-service` instances locally against the same Redis/Postgres, and prove a client connected to instance A could still see live state for a room owned by instance B
- A repeatable script/`docker-compose` profile for spinning both instances up together, instead of a manual two-terminal dance every time

### Explain (Multi-Instance Setup, Race Router era)

- Two `race-service` containers (`server-a`, `server-b`) in `docker-compose.yml`, each with a distinct `INSTANCE_ID`, sharing one Postgres and one Redis
- Verification joined a race via one instance and drove telemetry through the other, confirming Race Router's forwarding path actually worked end to end, not just in unit tests
- Once Race Router was replaced by the WS Gateway, this setup's assumptions (clients connect directly to a `race-service` instance) no longer matched the new architecture (clients connect to `ws-gateway`, which never runs room state itself) — replaced outright rather than patched, since the whole point of the check was proving the routing path that no longer exists

## Dockerize race-service & race-router, plus a Register Page

Two unrelated-but-shipped-together pieces: containerizing the backend so `docker-compose.yml` could run two `race-service` instances side by side (needed for the Race Router work above), and a real registration page so a demo actually had a way to create a second account without hitting the API by hand.

### Goals (Dockerize & Register Page)

- A single Dockerfile builds all of this project's binaries (`server`, `ws-gateway`, `consumer`) into one shared image, reused by every `docker-compose.yml` service via a different `command` override
- `docker-compose.yml` runs Postgres, Redis, Kafka, NATS, and two backend instances (`server-a`, `server-b`) wired to the same shared infra, with healthchecks gating startup order
- A `/register` page in the React app, mirroring the existing login flow, so a second browser tab can create its own account instead of reusing one login everywhere

### Explain (Dockerize & Register Page)

- The Dockerfile is a two-stage build — compile all three binaries in a `golang:1.26-alpine` builder stage, then copy just the binaries and `migrations/` into a bare `alpine` runtime stage, keeping the shipped image small:

  ```dockerfile
  # backend/Dockerfile
  RUN go build -o /out/server ./cmd/server
  RUN go build -o /out/ws-gateway ./cmd/ws-gateway
  RUN go build -o /out/consumer ./cmd/consumer
  ...
  CMD ["/app/server"]
  ```

- `docker-compose.yml`'s `server-a`/`server-b` both build from the same image (`aviron-backend:local`) and differ only in `INSTANCE_ID`/port mapping — the same one-image-many-roles pattern the `ws-gateway` service reuses later, via `command: ["/app/ws-gateway"]` overriding the Dockerfile's default `CMD`
- Every infra dependency (`postgres`, `redis`, `kafka`, `nats`) has its own `healthcheck`, and `server-a`/`server-b` `depends_on` all four with `condition: service_healthy` — a backend instance never starts trying to serve traffic before what it depends on can actually answer
- `RegisterPage.tsx` mirrors `LoginPage`'s own shape exactly (`apiFetch("/auth/register", ...)`, the same shadcn `Card`/`Input`/`Button` primitives, `setToken` + `navigate` on success) rather than introducing a different form pattern for what is, from the frontend's point of view, the same kind of request-then-redirect flow

## Kafka Event Producer

The room actor's side of project-overview.md §6's event pipeline: publishing every workout sample and every race's final result onto Kafka, so a separate consumer (next section) can persist them without slowing down the room actor's own tick loop.

### Goals (Kafka Event Producer)

- `workout.sample` and `race.finished` topics, each keyed by `race_id` so every event for the same race lands on the same partition and stays in order
- Publishing never blocks the room actor's single-writer loop — a slow or unavailable broker must not stall a live race
- A dead-letter path for messages a downstream consumer can't process, rather than a silent drop

### Explain (Kafka Event Producer)

- One shared `*kafka-go.Writer` handles both topics, with `Balancer: &kafkago.Hash{}` explicitly set — the project's entire ordering guarantee (same `race_id` → same partition) depends on a key-aware balancer; kafka-go's own default is round-robin, which would silently break it:

  ```go
  // backend/internal/kafka/producer.go
  func NewProducer(brokers []string, logger *slog.Logger) *Producer {
      return &Producer{
          writer: &kafkago.Writer{
              Addr:     kafkago.TCP(brokers...),
              Balancer: &kafkago.Hash{},
              Async:    true, // room actor never blocks on a publish
              ...
          },
      }
  }
  ```

- `Async: true` is what actually protects the room actor: `WriteMessages` returns immediately and any broker-side failure only surfaces through the Writer's `ErrorLogger`, not as a blocking call the room's tick loop would have to wait on
- `PublishRaw` republishes an already-encoded message verbatim to a different topic — this is what the consumer's dead-letter path (next section) calls when a message fails to decode or write for a permanent reason, reusing the same Writer rather than standing up a second one

## Kafka Consumer — Postgres Sink

The other side of the event pipeline: a standalone `cmd/consumer` process that reads both Kafka topics as a real consumer group and batch-writes them into Postgres, making `workout_samples` (a table that existed in the schema since Phase 1 but was never written to) real for the first time.

### Goals (Kafka Consumer Postgres Sink)

- Batch-insert `workout.sample` events into `workout_samples` — buffered by count and time, not one row per message
- Idempotently reconcile `race.finished` events into `race_participants`, as a backstop for the room actor's own synchronous write (project-overview.md §6), not a second primary writer of that table
- Malformed or permanently-failing messages go to a `*.dlq` topic instead of crash-looping the consumer

### Explain (Kafka Consumer Postgres Sink)

- Two independent reader loops, one per topic, each its own goroutine and — critically — its own consumer group ID:

  ```go
  // backend/internal/consumer/consumer.go
  workoutSampleGroupID = "aviron-consumer-workout-sample"
  raceFinishedGroupID  = "aviron-consumer-race-finished"
  ```

  A real bug caught during this feature's own live verification: two readers subscribed to *different* topics but sharing one group ID join as two members of the same confused consumer group, and Kafka's rebalance protocol silently starves at least one of them of any partitions at all — nothing crashes, messages just never arrive. Separate group IDs per topic fixed it.
- `ErrPermanentWrite` distinguishes a write that will never succeed (e.g. a foreign-key violation against a `race_id` that doesn't exist) from a transient one (a dropped connection): only the former gets dead-lettered and its offset committed anyway — a transient failure is left uncommitted so kafka-go redelivers it instead of losing it
- Batching is both size- and time-bounded (`flushBatchSize = 200`, `flushInterval = 3 * time.Second`), so a quiet topic still flushes promptly instead of waiting for 200 samples that may never arrive
- `race.finished`'s reconciliation write is deliberately idempotent, not a duplicate primary writer — `race_participants` is already written transactionally by the room actor the instant a race finishes; this consumer only fills in a row if that synchronous write somehow missed it

## Room Message Bus

The piece that made the WS Gateway pivot possible: `internal/roomrelay`, a NATS Core–backed message bus carrying every real-time, room-scoped message between whichever instance holds a client's WebSocket connection and whichever instance actually runs that room's `RoomActor` — the two are no longer guaranteed to be the same process once a dedicated gateway sits in front of `race-service`.

### Goals (Room Message Bus)

- A room's inbound traffic (client frames, detected disconnects) reaches the owning `race-service` instance regardless of which gateway instance the client is physically connected to
- A room's outbound traffic (broadcasts, room-closed) reaches every gateway instance holding a connection for that room
- No message replay needed — NATS Core, not JetStream, since a missed broadcast tick is superseded by the next one 250ms later anyway

### Explain (Room Message Bus)

- Two subjects per race, `room.{race_id}.in` and `room.{race_id}.out`, each carrying a small typed envelope rather than the raw client/server wire format directly:

  ```go
  // backend/internal/roomrelay/types.go
  type InboundEnvelope struct {
      Kind        InboundKind     `json:"kind"` // "message" | "disconnected"
      RaceID      string          `json:"race_id"`
      UserID      string          `json:"user_id"`
      DisplayName string          `json:"display_name,omitempty"`
      Message     json.RawMessage `json:"message,omitempty"`
  }
  ```

- `UserID`/`DisplayName` are attached by the gateway itself — already known from session-token verification at connect time — and never trusted from the client's own message body, the same trust boundary the pre-gateway code already enforced by deriving them itself
- `InboundKindDisconnected` has no client frame behind it at all: the gateway's reader loop synthesizes this envelope directly the instant a WebSocket read fails, so a dropped connection reaches the owning instance the same way a real client message would, through the same bus
- Every subscription is unsubscribed and its goroutine exits the instant its context is done, the NATS subscription itself ends, or `unsubscribe()` is called — whichever comes first — mirroring the same lifecycle contract `internal/roomlocator.SubscribeRoomEvents` already established

## Room Service Adapter

The `race-service` side of the bus: a small adapter (`internal/roombus`) translating raw `roomrelay` envelopes into the `room.RoomEvent` types the room actor already understands, so the room actor itself never needs to know NATS exists.

### Goals (Room Service Adapter)

- The room actor keeps consuming `room.RoomEvent` exactly as it did before the bus existed — zero changes to `RoomActor.applyEvent`
- Malformed frames arriving over the bus are dropped with a log line, not forwarded as garbage into the room actor's inbox
- No import cycle: `internal/room` must never import `internal/roomrelay` or `internal/ws` directly

### Explain (Room Service Adapter)

- `natsRoomBus` lives in its own package, one level above `internal/room`, specifically to avoid a cycle: `internal/ws` already imports `internal/room` for the `RoomEvent` type itself, so `internal/room` importing `internal/ws` back (to reuse its decode logic) would cycle — the adapter is the one place that needs both, so it's the one place that imports both:

  ```go
  // backend/internal/roombus/adapter.go
  func (b *natsRoomBus) toRoomEvent(raceID string, env roomrelay.InboundEnvelope) (room.RoomEvent, bool) {
      switch env.Kind {
      case roomrelay.InboundKindMessage:
          msg, err := ws.DecodeClientMessage(env.Message)
          ...
          return msg.ToRoomEvent(env.UserID, env.DisplayName)
      case roomrelay.InboundKindDisconnected:
          return room.ParticipantDisconnected{UserID: env.UserID}, true
      }
  }
  ```

- The adapter depends on a small `relayBus` interface (`SubscribeIn`/`PublishOut`), not the concrete `*roomrelay.Bus` — satisfied by a `FakeBus` in tests, so the decode/translate logic is unit-testable without a real or embedded NATS server
- This is the same "interface lives next to its consumer, concrete type lives where it's implemented" shape `coding-standards.md` already establishes for REST-domain repositories, applied here to a non-REST internal package for the same reason: testability without standing up real infrastructure

## WS Gateway

Phase 4's biggest architectural pivot: a new, dedicated `cmd/ws-gateway` binary that terminates every client's WebSocket connection itself, instead of `race-service` doing that directly. The client no longer needs to land on the specific instance that owns its room — the gateway routes REST requests to the right backend and relays WebSocket traffic over the message bus (previous two sections) to whichever instance actually runs the room.

### Goals (WS Gateway)

- One process type (`ws-gateway`) that clients always connect to, regardless of which `race-service` instance owns their race
- REST requests scoped to a specific race (`/races/{id}/...`) are proxied to that race's owning instance; everything else round-robins across all configured backends
- The WebSocket connection itself is terminated at the gateway, not proxied raw — decoded client frames are relayed over NATS, not the TCP connection itself

### Explain (WS Gateway)

- Room-scoped REST requests are resolved via a small in-memory cache backed by `roomlocator.Owner`, kept warm by subscribing to the same `room:events` pub/sub channel the registry already publishes on — a cache miss falls back to a direct Redis lookup rather than failing:

  ```go
  // backend/internal/wsgateway/gateway.go
  func (gw *Gateway) resolveTarget(ctx context.Context, raceID string) (string, bool, error) {
      gw.mu.RLock()
      entry, ok := gw.cache[raceID]
      gw.mu.RUnlock()
      if ok && time.Now().Before(entry.expiresAt) {
          return entry.instance, true, nil
      }
      instanceID, found, err := gw.locator.Owner(ctx, raceID)
      ...
  }
  ```

- Requests with no race in their path (`/auth/*`, `POST /races`, `/leaderboard`, ...) are "room-less" and just round-robin across every configured backend via an atomic counter — there's no ownership to resolve for them
- `GET /ws` is deliberately never routed through the REST proxy above — it's registered as its own mux entry (`WSHandler`) that terminates the connection itself, decodes each client frame, and republishes it onto the room's `roomrelay` inbound subject; this is the actual pivot from the deleted Race Router, which proxied the raw connection through to the owning instance instead of terminating it locally
- This split (gateway holds connections, `race-service` holds room state) is exactly what makes a rolling update of either binary independently safe: restarting a `race-service` pod doesn't have to drop any client's WebSocket, since the gateway holding that connection is a different, untouched process

## Multi-Instance Dev Setup & Verification (WS Gateway era)

The real acceptance test for the four Phase 4 pieces above (Redis Room Registry, Room Message Bus, Room Service Adapter, WS Gateway) working together — a genuinely harder property than the old Race Router version proved, since a client's WebSocket now terminates locally at whichever gateway accepted it, with zero relationship to which `race-service` instance actually owns the room.

### Goals (Multi-Instance Dev Setup, WS Gateway era)

- Two real `cmd/server` processes, two real `cmd/ws-gateway` processes, and a real NATS instance, proving two participants of the *same* race — connected through *two different* gateways — still see fully consistent, correctly-ordered real-time state
- Confirm the bus itself actually carried the traffic, not just that the end result looked right (a silently-dropped relay message could still pass on a small enough test race, papered over by client retry behavior)
- Deliberately kill the owning `race-service` instance mid-race and record — not just assert pass/fail on — what actually happens, since this design's failure mode here differs from the old Race Router's

### Explain (Multi-Instance Dev Setup, WS Gateway era)

- The topology deliberately crosses the two participants' connections: the creator connects through gateway-1, the joiner through gateway-2 — the one arrangement that actually exercises the bus instead of a single process's local memory:

  ```text
  server-A (:8080)   server-B (:8081)      -- race-service, unchanged
  gateway-1 (:9090)  gateway-2 (:9091)     -- two ws-gateway instances
  nats (:4222)                              -- message bus
  ```

- A dedicated assertion this revision needed that the old version didn't: grepping the owning instance's log for `roombus: published` and each gateway's log for `wsgateway: received`, both tagged with the race's ID — proof the bus carried this specific race's traffic, not just that the race finished successfully
- The kill test's expected outcome is now fundamentally different from before: under the old Race Router, the WebSocket itself was proxied raw through the router, so killing the owner broke the socket immediately. Under this design, `GET /ws` never touches the owner's socket at all — a gateway's `raceHub` just sits subscribed to a NATS subject that simply stops receiving publishes, with no fallback timeout of its own. Confirmed live: the k6 run's WS sessions stayed open for the full 2-minute `maxDuration` rather than failing fast, and a fresh reconnect only failed cleanly once the registry's own ~60s claim TTL had lapsed
- Two real bugs were caught by this revision's own first live runs: `docker-compose.yml`'s `nats:latest` image had no shell for its healthcheck to run against (fixed by switching to `nats:2-alpine`), and a real concurrency bug in `internal/room/registry.go`'s `drainBroadcast` silently dropped the final `race_finished` broadcast because a `select` case used the room's already-cancelled `ctx` instead of `context.Background()` — the same class of bug this file's "Fix Spurious InboundKindDisconnected" section (below) later found a sibling of

## Fix Spurious InboundKindDisconnected on Race Finish

A bug found after the WS Gateway shipped: finishing a race — even a solo one, nobody actually disconnecting — always published a spurious `InboundKindDisconnected` event for every participant, indistinguishable on the bus from a real dropped connection.

### Goals (Fix Spurious Disconnected on Race Finish)

- `readLoop` must not publish `InboundKindDisconnected` when a race ends normally, only for a genuine client disconnect or network drop

### Explain (Fix Spurious Disconnected on Race Finish)

- The root cause: finishing a race makes `writeLoop` drain the final broadcast, then cancel the connection's shared context — and a cancelled context makes `readLoop`'s blocking `Read` fail exactly the same way a real disconnect would, with no way to tell the two apart from the error alone
- The fix threads the race hub's own `closed` channel into `readLoop`, checked the instant `Read` fails, before publishing anything:

  ```go
  // backend/internal/wsgateway/endpoint.go
  if err != nil {
      select {
      case <-hubClosed:
          return // room already announced room_closed — not a real disconnect
      default:
      }
      // ... only now publish InboundKindDisconnected
  }
  ```

- `hubClosed` already closed means the room already told every participant it finished via `room_closed` — publishing a disconnect event on top of that would just be spurious bus traffic for a race that's already torn down, not a signal anyone still needs
- The regression test (`TestServeConn_RoomClosedClosesConnectionWithoutClientDisconnect`) previously only checked that the connection got closed — it was strengthened to actually assert no disconnect event was published, and confirmed to fail deterministically against the pre-fix code before the fix landed

## Kubernetes Core Infrastructure

Phase 5's first spec: standing up Postgres, Redis, NATS, and Kafka on a local `kind` cluster under new `deploy/k8s/` manifests — genuinely running this project's dependencies on Kubernetes for the first time, with none of this project's own binaries deployed yet (that's the next three sections).

### Goals (Kubernetes Core Infrastructure)

- A fresh `kind` cluster reaches every dependency pod `Running`/`Ready`, applied via plain `kubectl apply`
- Each dependency is confirmed actually reachable, not just "pod exists" — a healthy-looking pod can still be misconfigured in a way that only shows up once something tries to use it
- `aviron-backend:local`'s existing image (already built by `docker-compose.yml` today) loads cleanly into the cluster

### Explain (Kubernetes Core Infrastructure)

- Postgres runs as a `StatefulSet` with an inline `volumeClaimTemplates` block — a correction made during implementation from the spec's own original plan (a separate hand-written `pvc.yaml`), since a `StatefulSet`'s idiomatic storage mechanism already provisions and binds its own PVC per replica, and `kind`'s default StorageClass supports it directly
- Redis and NATS are plain `Deployment`s — no PVC, since losing Redis's room-registry data or NATS's in-flight messages on a restart is an accepted, self-healing, bounded-impact event, not real data loss. NATS deliberately uses `nats:2-alpine`, not `nats:latest`, reusing a lesson already learned once in `docker-compose.yml`: the latter is a distroless-style image with no shell, which breaks any `exec`-based healthcheck
- Kafka runs via the Bitnami Helm chart in KRaft mode, and needed two real fixes only discovered against a live cluster: the chart's default image is unpullable for free since Broadcom's 2025 licensing change (fixed via a `bitnamilegacy/kafka` mirror override), and the chart defaults every listener to `SASL_PLAINTEXT`, which this project's plain `segmentio/kafka-go` clients have never spoken (forced back to `PLAINTEXT`)
- Every dependency was confirmed actually working, not just running, via a direct protocol check:

  | Dependency | Check | Result |
  | --- | --- | --- |
  | Postgres | `pg_isready -U aviron` | accepting connections |
  | Redis | `redis-cli ping` | `PONG` |
  | NATS | `GET :8222/healthz` | `{"status":"ok"}` |
  | Kafka | `kafka-broker-api-versions.sh` | broker `id: 0` listed |

- `docs/k8s-deployment.md` was added as the standalone operational runbook (create/deploy/verify/maintain/troubleshoot/shut down) — deliberately separate from the `context/features/phase5/` spec docs, which explain *why* a decision was made; the runbook explains *how* to actually run the cluster day to day

## Graceful Shutdown

Phase 5's second spec, and a hard prerequisite for deploying `race-service`/`ws-gateway` to Kubernetes at all: before this, none of the three binaries handled `SIGTERM`, so a pod being terminated (a rolling update, a scale-down) died exactly as hard as `SIGKILL` — cutting off in-progress races and dropping WebSocket connections silently instead of closing them cleanly.

### Goals (Graceful Shutdown)

- `cmd/server` stops accepting new traffic immediately on `SIGTERM` but lets in-progress races finish naturally rather than force-ending them
- `cmd/ws-gateway` cleanly disconnects every locally-held WebSocket connection instead of just dropping the TCP socket
- Readiness and liveness become two genuinely different checks, so a transient dependency blip never makes Kubernetes restart an otherwise-healthy pod

### Explain (Graceful Shutdown)

- `cmd/server`'s shutdown sequence: mark unready immediately, stop the HTTP server from accepting new requests, then wait — bounded by a 2-minute `shutdownTimeout` — for every currently-running room actor to finish on its own:

  ```go
  // backend/cmd/server/run.go
  <-signalCtx.Done()
  gate.MarkShuttingDown()                 // readiness flips first
  httpSrv.Shutdown(shutdownCtx)           // stop taking new requests
  waitForRoomsToDrain(shutdownCtx, registry, logger) // let live races finish
  ```

  `waitForRoomsToDrain` turned out to be a real necessity, not a nice-to-have: `main.go` exits the instant `Run` returns, regardless of any background goroutine still running, so without an explicit wait the "let races finish naturally" decision would have been silently undone by the process exiting anyway
- `cmd/ws-gateway` was the harder case: nothing previously told a locally-held WebSocket connection the whole process was going away. A new `raceHubRegistry.Shutdown()` cancels each connection's own `context.CancelFunc` (tracked via a new `connRegistration{ch, cancel}` pairing) — deliberately cancelling the *connection's* context, not just `hub.closed`, so the disconnect routes through the exact same path a real network drop already takes, publishing `InboundKindDisconnected` the normal way instead of inventing a second shutdown-specific code path
- `GET /healthz` (dependency-checking: Postgres/Redis/NATS reachability) and a new `GET /livez` (dependency-free) are now genuinely separate endpoints in both `internal/httpserver` and `internal/wsgateway`, each behind its own small `ReadinessGate` type — reusing one check for both readiness and liveness would let a transient Redis blip get an otherwise-healthy pod killed by `kubelet`
- Verified against real running binaries, not just unit tests: a mid-race `SIGTERM` to `cmd/server` let the room keep broadcasting for ~2.5 more seconds until it finished naturally before logging `shutdown complete`; a live WebSocket through `cmd/ws-gateway`, `SIGTERM`'d ~0.8s after connecting, received two more broadcasts during its flush window then closed cleanly in 502ms total

## Kubernetes race-service StatefulSet

Phase 5's third spec: deploying this project's own `race-service` binary (`cmd/server`) to the cluster, as a `StatefulSet` with `replicas: 2` — the actual point being that two replicas force every Phase 4 horizontal-scaling bug to surface for real, since a single pod can never expose a cross-instance sync bug.

### Goals (Kubernetes race-service StatefulSet)

- `race-service` runs with 2 replicas, each with a stable, dialable network identity that `ws-gateway`'s REST routing can reach directly
- Readiness (dependency-checking) and liveness (dependency-free) probes wired to the two endpoints Graceful Shutdown just split apart
- The pod survives a real rolling update without dropping an in-progress race

### Explain (Kubernetes race-service StatefulSet)

- `INSTANCE_ID` has to be a genuinely dialable address, not just an identity label — `ws-gateway`'s REST proxy dials `Owner()`'s stored value directly. The downward API alone only exposes the bare pod name (`race-service-0`), which isn't resolvable on its own, so it's composed with the headless Service's own DNS suffix:

  ```yaml
  # deploy/k8s/race-service/statefulset.yaml
  - name: POD_NAME
    valueFrom:
      fieldRef: { fieldPath: metadata.name }
  - name: INSTANCE_ID
    value: "$(POD_NAME).race-service.aviron.svc.cluster.local:8080"
  ```

  Confirmed the hard way: a bare pod name here produces "no such host" errors in `ws-gateway`'s REST proxy the instant a room-scoped request needs to route to its actual owner
- `terminationGracePeriodSeconds: 150` — a number arrived at empirically, not guessed up front. The original 25s/30s pair (matching Graceful Shutdown's first-pass `shutdownTimeout`) turned out too short for entirely ordinary races: this project's own default k6 scenario (a 30-word race) already averages ~36s to finish, so a real rolling-update test hung for the full budget before both values were raised (`shutdownTimeout` to 2 minutes, `terminationGracePeriodSeconds` to 150s) — found and fixed as part of the final Phase 5 verification pass, not this spec's own first attempt
- Readiness uses `/healthz` (a pod that can't reach Postgres genuinely shouldn't receive traffic); liveness uses `/livez` (a transient Postgres/Redis/NATS blip must not make `kubelet` restart an otherwise-healthy pod) — the direct payoff of Graceful Shutdown splitting the two apart
- No `volumeClaimTemplates`: this `StatefulSet` exists purely for the stable per-pod network identity the headless Service needs, not for storage — `race-service` holds no state that has to survive a pod restart

## Kubernetes ws-gateway Deployment + Ingress

Phase 5's fourth spec: deploying `cmd/ws-gateway` to the cluster as a plain `Deployment` (2 replicas, matching `docker-compose.yml`'s own two-gateway topology), plus an `Ingress` so the React app running on the host machine can actually reach it from outside the cluster.

### Goals (Kubernetes ws-gateway Deployment + Ingress)

- Two `ws-gateway` replicas, each able to route a room-scoped request to either `race-service` pod
- An `Ingress` exposing `ws-gateway` at a plain `http://localhost/` URL, with WebSocket upgrades surviving nginx's default proxy timeouts
- A clean rolling update: an in-progress connection disconnects cleanly rather than hanging, matching Graceful Shutdown's `disconnectAll()` design

### Explain (Kubernetes ws-gateway Deployment + Ingress)

- `RACE_SERVICE_INSTANCES` is a static, hand-maintained list of `race-service`'s two StatefulSet DNS names — deliberately per-binary env, not the shared ConfigMap, and the one value in this whole phase most likely to silently drift if `race-service`'s replica count ever changes:

  ```yaml
  # deploy/k8s/ws-gateway/deployment.yaml
  - name: RACE_SERVICE_INSTANCES
    value: "race-service-0.race-service.aviron.svc.cluster.local:8080,race-service-1.race-service.aviron.svc.cluster.local:8080"
  ```

- The `Ingress` sets `nginx.ingress.kubernetes.io/proxy-read-timeout`/`proxy-send-timeout` to `3600` — `ws-gateway`'s own `WSHandler` does the WebSocket upgrade itself, nginx just needs to not time out an otherwise-idle long-lived connection while a race is still in progress
- Verifying WebSocket routing through `Ingress` surfaced a real, unrelated gap: `nginx-ingress` reserves `/healthz` on its own default server, shadowing the app's identical path — harmless in practice (`kubelet` probes the pod directly, bypassing `Ingress` entirely) but confirmed by testing `/livez` externally instead, which routed correctly
- Rolling `ws-gateway` correctly force-disconnects local connections rather than keeping them alive — it holds no race state of its own, so there's nothing to preserve across the restart, unlike `race-service`

## Kubernetes consumer Deployment & Multi-Instance Verification

Phase 5's final two specs, loaded and built together since the verification script's own topology assumes the consumer already exists — closing out the entire Kubernetes phase.

### Goals (Kubernetes consumer & Multi-Instance Verification)

- `cmd/consumer` runs as a single-replica `Deployment` with no `Service` and no probes (it exposes no HTTP surface at all)
- The Kubernetes-hosted equivalent of the earlier local Docker multi-instance check, reusing the same k6 scenarios unchanged
- Two new rolling-update passes: prove a `race-service` update doesn't drop an in-progress race, and a `ws-gateway` update disconnects cleanly rather than hanging

### Explain (Kubernetes consumer & Multi-Instance Verification)

- The consumer's `Deployment` genuinely has no readiness/liveness probes — confirmed by reading `cmd/consumer/run.go` directly, it opens no `http.ListenAndServe` at all, and an exec probe (e.g. `pgrep`) was considered and rejected as inventing a signal this binary has no other reason to expose; `restartPolicy: Always`'s own crash detection is a sufficient substitute on a local cluster
- `load/multi-instance-k6-check.sh` reuses `load/scenarios/multi-instance-check.js` and `lib/ws-client.js` completely unchanged — they never cared what process or pod sat behind a plain HTTP/WS URL — while dropping the old script's entire process-management layer, since `kubectl`/`kind` already own that lifecycle
- Three real bugs found by actually running the full verification against the live cluster, none of them anticipated by the spec:

  1. Kafka's `__consumer_offsets` topic never got created (`offsets.topic.replication.factor` defaults to 3, impossible with one broker) — every consumer group fetch failed forever, not just at startup. A first fix attempt (a raw string under the chart's top-level `config:` key) made it *worse*, crash-looping the broker, since that key replaces the entire generated config rather than layering on top. Correctly fixed via `controller.overrideConfiguration`.
  2. The `shutdownTimeout`/`terminationGracePeriodSeconds` pair (see the race-service section above) was too short for a real rolling update against an ordinary race — found here, fixed in both `graceful-shutdown.md` and `k8s-race-service-deploy.md`.
  3. The verification script's own rolling-update assertion was wrong for the `ws-gateway` pass specifically — it expected `race_finished` on the same connection, but `ws-gateway` correctly force-disconnects local connections instead of keeping them alive. Fixed by checking session duration instead of reusing `race-service`'s own success criterion.

- Verified against the real cluster: `workout_samples`/`race_participants` both land correctly after a completed race; killing the consumer pod mid-batch (`kubectl delete pod`) produced exactly the right row set afterward — no duplicates, no gaps; a full 6-repeat run plus both rolling-update passes all pass. **This closes out Phase 5** — every dependency, and every one of this project's own binaries, now runs and has been verified on a real `kind` cluster, not just designed on paper

## Dynamic Backend Discovery

`ws-gateway` learns which `race-service` pods currently exist by watching Kubernetes directly, instead of trusting a fixed instance list that goes stale the moment `race-service` scales.

### Goals (Dynamic Backend Discovery)

- Room-less REST requests (e.g. `POST /races`) round-robin across whatever `race-service` pods currently exist, discovered live
- No behavior change for local `go run`/`docker-compose` dev, which never scales `race-service` beyond a fixed set anyway

### Explain (Dynamic Backend Discovery)

- A `BackendDiscovery` interface has two implementations: `StaticBackends` (a plain `[]string`, what local dev keeps using via `RACE_SERVICE_INSTANCES`) and `K8sBackendDiscovery` (a `client-go` informer watching `EndpointSlice` objects for the `race-service` headless `Service`) — `ws-gateway`'s room-less routing code depends only on the interface, not on which implementation is live
- `K8sBackendDiscovery` never blocks a request to compute the pool: `recompute()` runs only from the informer's own event handlers (`AddFunc`/`UpdateFunc`/`DeleteFunc`) and stores the result behind `atomic.Pointer[[]string]`, so `Backends()` is always a single atomic load

  ```go
  type BackendDiscovery interface {
      // Backends returns the current backend pool. Called on every
      // room-less request — must be cheap and non-blocking.
      Backends() []string
  }

  func (d *K8sBackendDiscovery) Backends() []string {
      return *d.backends.Load()
  }
  ```

- `recompute()` keeps only `Ready` endpoints — a pod still starting, or mid-`terminationGracePeriodSeconds` graceful shutdown, must not receive fresh room-less traffic. This reuses the exact readiness signal `graceful-shutdown.md` already produces (kubelet only marks an `EndpointSlice` entry `Ready` once `/healthz` passes) rather than inventing a second liveness check
- Room-scoped requests never consult this at all — they resolve via `RoomLocator.Owner` instead (Redis), which already reflects a changing `race-service` pod set on its own

## Kubernetes `HorizontalPodAutoscaler`

`race-service` and `ws-gateway` both scale 2-5 replicas on CPU utilization — the real prerequisite everything from `dynamic-backend-discovery.md` onward exists to make safe.

### Goals (Kubernetes HorizontalPodAutoscaler)

- Both services scale automatically between 2 and 5 replicas, triggered at 70% average CPU utilization
- `metrics-server` installed as a one-time `kind`-specific prerequisite (not part of a plain `kind` cluster by default)

### Explain (Kubernetes HorizontalPodAutoscaler)

- Same shape for both, just a different `scaleTargetRef` kind — `race-service` targets its `StatefulSet` (`autoscaling/v2` scales anything exposing a `/scale` subresource), `ws-gateway` targets its `Deployment`:

  | | `race-service` | `ws-gateway` |
  | --- | --- | --- |
  | `scaleTargetRef.kind` | `StatefulSet` | `Deployment` |
  | `minReplicas` / `maxReplicas` | 2 / 5 | 2 / 5 |
  | Metric | CPU, 70% average utilization | CPU, 70% average utilization |
  | What actually drives it under real load | Room actor ticking + broadcast fan-out (CPU-heavy) | Thin REST proxy — needs its own dedicated burst to cross 70%, stays cheap under a normal race workload |

- Only safe because `ws-gateway` already discovers `race-service` pods dynamically (`dynamic-backend-discovery.md`) — a static instance list would leave a newly-scaled `race-service` pod an unreachable, silent routing dead end
- `kind` needs a one-time patch for `metrics-server` to work at all: kubelet's serving certs on a `kind` node aren't signed by a CA `metrics-server` trusts by default, so without `--kubelet-insecure-tls` it stays permanently unable to scrape and every HPA shows `<unknown>` instead of a real percentage
- Verified live: `NUM_RACES=5 VUS_PER_RACE=8` via k6 crosses 70% CPU on `race-service`; a raw `ab`/REST-proxy burst crosses it on `ws-gateway`; both climb from 2 to 5 replicas and settle back down after the load stops (subject to `autoscaling/v2`'s own 5-minute default `scaleDown.stabilizationWindowSeconds`, by design — avoids flapping on a brief lull)

## Metrics Parity — `ws-gateway` + `consumer`

The first Phase 6 spec: closes the gap where `race-service` had `internal/metrics` and Prometheus wiring but `ws-gateway`/`consumer` had neither `/metrics` nor `/debug/pprof/*` at all.

### Goals (Metrics Parity)

- `ws-gateway` and `consumer` both expose `GET /metrics` (Prometheus text format) and `/debug/pprof/*`
- Metrics tied to what each binary actually depends on cross-process — not just generic process/goroutine stats

### Explain (Metrics Parity)

- Each binary gets its own `prometheus.Registry` (`GatewayMetrics`, `ConsumerMetrics`) rather than sharing `race-service`'s `Metrics` type — the three processes have nothing in common to register beyond the standard Go/process collectors

  | Binary | New metric | Type | What it measures |
  | --- | --- | --- | --- |
  | `ws-gateway` | `aviron_ws_connections_active` | `GaugeFunc` | Local WebSocket connections this instance holds, summed across every `raceHub` |
  | `ws-gateway`/`race-service` (shared pkgs) | `aviron_roomrelay_publish_total`/`_errors_total`/`_duration_seconds` | Counter/Histogram | NATS publish rate/errors/latency, both directions |
  | `ws-gateway`/`race-service` (shared pkgs) | `aviron_roomlocator_lookup_duration_seconds` | Histogram | Redis room-ownership lookup latency — on the critical path of every room-scoped request |
  | `consumer` | `aviron_consumer_batch_insert_duration_seconds{topic}` | HistogramVec | Postgres batch-write duration per Kafka topic |
  | `consumer` | `aviron_consumer_dlq_total{topic}` | CounterVec | Messages republished to a dead-letter topic |
  | `consumer` | `aviron_kafka_consumer_lag{topic}` | `GaugeFunc` | Consumer-group lag, read from `kafka-go`'s own `*Reader.Stats()` at scrape time — no separate polling goroutine needed, `Stats()` is already safe for concurrent use |

- `consumer`'s batch-insert/DLQ metrics are observed through a `MetricsRecorder` interface `ConsumerMetrics` satisfies structurally — `internal/consumer` itself never imports `prometheus`, the same seam `internal/room`'s `TickObserver` already established for `race-service`
- No `Service`/probes needed for these — they ride on each binary's existing HTTP surface (`ws-gateway`'s admin port, `consumer`'s only HTTP surface at all)

## Halve `kind` Cluster Resource Requests/Limits

A small, explicitly-requested tuning pass, not tied to any spec — `kubectl top` showed every service reserving far more CPU/memory than it actually used on a laptop-sized cluster.

### Goals (Halve kind Cluster Resource Requests/Limits)

- Cut CPU/memory requests and limits roughly in half across `race-service`, `ws-gateway`, `redis`, `nats`, `postgres`, `consumer` — without risking an `OOMKilled` crash loop on any of them

### Explain (Halve kind Cluster Resource Requests/Limits)

- A strict 50% cut, not "cut down to measured usage" — the first attempt did the latter and got reverted by the user for being too aggressive; the 50% floor was set directly by the user afterward

  | Service | CPU request (before → after) | Memory request (before → after) |
  | --- | --- | --- |
  | `ws-gateway` | 100m → 50m | 64Mi → 32Mi |
  | (others) | halved the same way | halved the same way |
  | Kafka | **untouched** | **untouched** |

- Kafka was deliberately excluded: its ~465Mi actual usage already sits close to what a 50%-cut *limit* would allow (768Mi → 384Mi), and cutting it risked a real `OOMKilled` crash loop rather than just running leaner — flagged to the user via a direct question instead of applied silently, since this was a functional risk, not a style choice. The user's answer generalized to a standing rule: "scale down only when possible, otherwise leave untouched," applied consistently to every later resource-limit decision this project made (e.g. Tempo's memory bump in `phase-6-verification.md`, the opposite direction, for the same reason)
- Applied live, not just edited on disk: all six pods rolled cleanly with zero `OOMKilled` restarts; node-level allocated requests dropped from 57% to 49% of the `kind` node's capacity, limits from 100% to 60%

## OpenTelemetry Collector + Tempo Deployment

Pure infra, no application code — stands up the single OTLP fan-out point every binary later pushes spans to, and the trace backend behind it.

### Goals (OTel Collector + Tempo Deployment)

- One OTel Collector `Deployment` (core distribution, not `-contrib`) receiving OTLP/gRPC on `:4317`
- Tempo running in monolithic mode, local filesystem backend — no distributed microservices mode, this is a laptop cluster
- A manual test span round-trips through the Collector and is queryable via Tempo's own search API before any real binary sends one

### Explain (OTel Collector + Tempo Deployment)

- The Collector's pipeline is deliberately minimal: `memory_limiter` first (so a misbehaving instrumented binary sheds load before `batch` even runs, protecting the Collector itself from OOM), then `batch` (fewer, larger export calls under this system's per-`telemetry`-message span volume), then the single `otlp` exporter to Tempo
- Tempo receives OTLP directly on `:4317` too, in principle — but the Collector stays the architecture's single fan-out point on purpose, so every binary only needs to know one address rather than each hand-rolling its own per-backend exporter wiring
- No `PersistentVolumeClaim` on either — `emptyDir` for Tempo's WAL/blocks, the same "laptop `kind` cluster, not a real retention target" stance this whole phase takes; a pod restart loses trace history, which is an accepted tradeoff, not an oversight

  ```mermaid
  flowchart LR
      APP["race-service / ws-gateway / consumer"]
      OTEL["OTel Collector<br/>memory_limiter -> batch -> otlp exporter"]
      TEMPO["Tempo<br/>monolithic mode, local filesystem"]

      APP -->|"OTLP gRPC :4317"| OTEL
      OTEL -->|"OTLP gRPC :4317"| TEMPO
  ```

- Verified before any application code existed to generate real spans: a tiny Go program using `otlptracegrpc.New(..., otlptracegrpc.WithEndpoint("localhost:4317"))` against a port-forwarded Collector, then confirmed queryable via `curl 'http://localhost:3200/api/search?tags=service.name%3D<test-name>'`

## Prometheus Deployment

Scrapes all three binaries' `/metrics`, discovered dynamically rather than from a static target list.

### Goals (Prometheus Deployment)

- Prometheus scrapes `race-service`/`ws-gateway`/`consumer` automatically, without a fixed target list that would go stale the moment any of them scales
- `/targets` shows every currently-running pod discovered, not a config-time snapshot

### Explain (Prometheus Deployment)

- `kubernetes_sd_configs` with `role: pod`, filtered to pods annotated `prometheus.io/scrape: "true"` — the exact same "annotation opt-in over static list" reasoning `dynamic-backend-discovery.md` already established for `ws-gateway`'s own routing, applied here to scraping instead

  ```yaml
  scrape_configs:
    - job_name: aviron-pods
      honor_labels: true
      kubernetes_sd_configs:
        - role: pod
  ```

- `honor_labels: true` turned out to matter for a real reason, found only once `kube-state-metrics` (`grafana-deploy.md`) started emitting its own native `pod`/`namespace` labels identifying the *target* object a metric describes (e.g. which pod actually restarted) — without it, this scrape job's own `relabel_configs` (meant to identify the *scraped* pod) silently clobbered those into `exported_pod`, making `PodRestartLooping`'s `{{ $labels.pod }}` always read `kube-state-metrics-xxxxx` instead of the real pod. Safe for every other target in this job since `race-service`/`ws-gateway`/`consumer` never emit their own `pod`/`app` labels natively — no collision to honor for them
- Alertmanager wiring (`alerting.alertmanagers`) and the rule file mount (`rule_files`) both live in this same `prometheus.yml`, added later by `alert-rules.md` without touching the scrape config itself — the two concerns stay independently editable

## Distributed Tracing — Instrumentation

Full depth: REST/WebSocket entry points (including a span per `telemetry` message), NATS, Redis, Kafka, and `pgx` all get real spans — not just the infra to receive them.

### Goals (Distributed Tracing — Instrumentation)

- One shared `tracing.Init` bootstrap all three binaries call, tagged with their own `service.name`
- A single `telemetry` message's trace crosses the `ws-gateway` → NATS → `race-service` process boundary as one connected trace
- Every cross-process hop (NATS, Redis `roomlocator`, Kafka, `pgx`) carries its own span

### Explain (Distributed Tracing — Instrumentation)

- `internal/tracing.Init` wires an OTLP/gRPC exporter, a batching `TracerProvider`, and the W3C `TraceContext` propagator every cross-process hop needs to actually carry `traceparent` across a process boundary (NATS headers, Kafka headers, `otelhttp`) — one function all three `cmd/*/run.go` call the same way
- A single correctly-typed word produces one connected, real trace — confirmed live against Tempo, not just designed: `ws-gateway`'s `ws.frame` span (the WS entry point) parents `roomrelay.publish`, which in turn parents `race-service`'s `roomrelay.receive`

  ```mermaid
  sequenceDiagram
      participant FE as Browser
      participant WG as ws-gateway
      participant NATS
      participant RS as race-service

      FE->>WG: WS frame: telemetry, seq=N
      WG->>WG: span ws.frame (root)
      WG->>NATS: publish room.<id>.in (traceparent header)
      NATS->>RS: deliver
      RS->>RS: span roomrelay.receive (child of ws.frame)
  ```

- `pgx` queries get spans for free via `otelpgx.NewTracer()` wired into the connection pool's own `Tracer` field — no per-query code change anywhere `internal/db` is already used
- **A real, deliberate limit found later by `phase-6-verification.md`, not a bug in this spec**: the outbound broadcast leg (room state → WS fan-out) never joins that same trace, and structurally can't — `RoomActor`'s tick fires every 250ms regardless of how many `telemetry` messages arrived since the last one, so there's no single inbound message a batched broadcast could unambiguously parent to. This spec's own scope only ever promised per-hop spans, not a connected round-trip

## Log/Trace Correlation

No new logging pipeline — every binary already logged structured JSON tagged with `race_id`/`user_id`/`request_id`. This spec adds exactly one more pair of fields.

### Goals (Log/Trace Correlation)

- Every `slog` line emitted from inside an active span carries that span's `trace_id`/`span_id`
- Clicking a span in Grafana's Tempo view can jump straight to the matching log lines by `trace_id`

### Explain (Log/Trace Correlation)

- One small helper reads the active span off the request's `context.Context` and returns it as `slog` attributes — called wherever a log line already has a `ctx` in scope:

  ```go
  // LogAttrs returns trace_id/span_id slog attributes for ctx's active span
  func LogAttrs(ctx context.Context) []slog.Attr {
      sc := trace.SpanContextFromContext(ctx)
      if !sc.IsValid() {
          return nil
      }
      return []slog.Attr{
          slog.String("trace_id", sc.TraceID().String()),
          slog.String("span_id", sc.SpanID().String()),
      }
  }
  ```

- If `ctx` carries no active span (e.g. a log line outside any request), `LogAttrs` returns nil rather than emitting empty/zero-value IDs — a log line with no trace context stays exactly as it was before this spec, not polluted with a fake `trace_id`
- Confirmed live, both directions: a real log line's `trace_id`/`span_id` matched exactly a real span's IDs pulled from Tempo, found both by direct Elasticsearch query and by clicking "Trace to logs" in Grafana's UI (`phase-6-verification.md`)

## EFK Deployment

Elasticsearch, Fluent Bit, and Kibana — the log storage/shipping/search backend behind the `trace_id`-tagged JSON every binary already emits.

### Goals (EFK Deployment)

- Fluent Bit tails every `aviron` pod's stdout and ships parsed JSON fields (not one opaque string) into Elasticsearch
- Kibana gives full-text search over the result

### Explain (EFK Deployment)

- Fluent Bit runs as a `DaemonSet`, one per node, tailing container log files directly via the node's `kubelet` — the standard Kubernetes-native path, no application-side change needed
- Two config details that look interchangeable but aren't, both found by real startup failures rather than assumed from the docs:

  ```text
  [INPUT]
      Name              tail
      Path              /var/log/containers/*_aviron_*.log
      Parser            cri-log      # kind's runtime is containerd (CRI format), not Docker's own per-line JSON

  [FILTER]
      Name              kubernetes
      Match             kube.*
      Merge_Log         On           # only merges a field literally named "log" —
                                      # the image's built-in "cri" parser names it "message" instead,
                                      # which silently no-ops Merge_Log and ships one opaque string per line
  ```

- Path filtered to `*_aviron_*.log` (the kubelet-generated filename embeds `<pod>_<namespace>_<container>`) — the same "opt in by filtering" reasoning `prometheus-deploy.md`'s own annotation-based scrape opt-in already established, avoiding noise from infra pods never meant to ship
- `Suppress_Type_Name On` on the `es` output — Elasticsearch 8 dropped mapping types entirely, and without this the `es` output plugin still tries to send a `_type` field that ES8 rejects outright

## Grafana Deployment

The single pane of glass tying Prometheus, Tempo, and Elasticsearch together — plus the pod-aware RED/USE dashboards this whole phase's dashboards exist to prove.

### Goals (Grafana Deployment)

- All three data sources (Prometheus, Tempo, Elasticsearch) provisioned and reachable, with bidirectional trace↔log correlation wired
- RED dashboards per binary, aggregated by `pod` — not collapsed into one averaged line across 2-5 replicas
- An HPA panel showing replica count overlaid with the CPU metric that triggered a scale event

### Explain (Grafana Deployment)

- All three data sources get an explicit `uid` (not Grafana's auto-generated one) — `tracesToLogsV2.datasourceUid`/`derivedFields.datasourceUid` both need a UID they can resolve deterministically for the correlation features to work at all
- Every panel query aggregates `by (pod, ...)`, on purpose — this is what actually proves the dashboards are fleet-aware rather than just decorative:

  ```json
  {
    "title": "Rate — roomrelay publish/sec",
    "expr": "sum by (pod, subject_kind) (rate(aviron_roomrelay_publish_total{app=\"race-service\"}[5m]))",
    "legendFormat": "{{pod}} ({{subject_kind}})"
  }
  ```

- `kube-state-metrics` is a real, additional prerequisite for the HPA panel specifically — `kube_horizontalpodautoscaler_*` metrics come from it, not from any of this project's own `/metrics` endpoints, and it's scraped automatically by Prometheus's existing pod-discovery mechanism, no separate scrape job needed
- Verified live, not just configured: ran a real k6 race and confirmed the RED dashboards populate with non-zero, correctly `pod`-labeled series for every replica that actually handled traffic; clicked a span in Tempo's Explore view and confirmed "Trace to logs" opens the exact matching Elasticsearch lines

## Prometheus Alert Rules + Alertmanager Deployment

Real SLO-driven rules tied to this system's actual failure modes, not a generic textbook list — the first Phase 6 spec to touch application Go code again since the tracing track.

### Goals (Prometheus Alert Rules + Alertmanager Deployment)

- 8 alert rules, each tied to a failure mode this exact system can actually hit
- Alertmanager deployed, routing every firing alert toward a webhook (the not-yet-built `telegram-relay`)

### Explain (Prometheus Alert Rules + Alertmanager Deployment)

| Rule | Condition | Severity |
| --- | --- | --- |
| `ElevatedErrorRate` | Error rate > 5% over 5m | warning |
| `TickLatencySLOBurn` | Room broadcast tick p99 > 200ms for 10m | warning |
| `GoroutineCountTrendingUp` | Linear projection crosses 100k goroutines within 1h | warning |
| `PodRestartLooping` | > 3 container restarts in 15m | critical |
| `HPAStuckAtMaxReplicas` | Current replicas == max for 15m | warning |
| `KafkaConsumerLagHigh` | Consumer lag > 2000 messages for 10m | warning |
| `NATSReconnectStorm` | > 3 reconnects in 15m | warning |
| `PostgresPoolSaturation` | Pool > 80% acquired for 5m | critical |

- Two new metrics landed specifically to make `NATSReconnectStorm`/`PostgresPoolSaturation` possible: `aviron_nats_reconnects_total` (wired via `nats.ReconnectHandler`/`nats.DisconnectErrHandler` on both binaries' existing `nats.Connect` calls) and `aviron_pg_pool_acquired_conns`/`_max_conns` (`race-service`-only, reading `pgxpool.Pool.Stat()` at scrape time)
- Alertmanager's route groups by `alertname`/`app` explicitly (`group_by: ["alertname", "app"]`) — this turned out to matter later: `log-alert-rules.md`'s own Grafana-side notification policy initially omitted it, and without a `group_by`, the webhook payload's `groupLabels` comes through empty, breaking the Telegram message's title
- **A real, non-obvious bug found only through live verification**: forcing `PodRestartLooping` for real (`kubectl exec ... kill -9 1`) turned out to be a no-op — a container's PID 1 is immune to any signal, including `SIGKILL`, originating from inside its own PID namespace (`pid_namespaces(7)`'s own documented behavior). The working fix was `crictl stop` run on the `kind` node itself, outside the pod's PID namespace entirely (`docker exec aviron-control-plane crictl stop <containerID>`)

## Telegram Relay

The fourth binary — a small, purpose-built adapter translating Alertmanager's (and, later, Grafana's) webhook format into a real Telegram message.

### Goals (Telegram Relay)

- `POST /alert` accepts an Alertmanager-shaped webhook and forwards one formatted message per call to the Telegram Bot API
- Never retry-storms a bad bot token — a failed Telegram send is logged and counted, not surfaced as a webhook failure the caller would retry

### Explain (Telegram Relay)

- The handler always responds `200`, regardless of whether the Telegram call itself succeeded — retrying a webhook won't fix a bad bot token, so a `Notify` failure is logged and counted (`aviron_telegram_relay_errors_total`) instead of surfaced as a non-2xx response the caller would retry forever:

  ```json
  // POST /alert (from Alertmanager or Grafana's Unified Alerting)
  {
    "status": "firing",
    "groupLabels": {"alertname": "PodRestartLooping"},
    "alerts": [{"status": "firing", "labels": {...}, "annotations": {"summary": "race-service-0 restarted more than 3 times in 15m"}}]
  }
  ```

  ```text
  -> HTTP/1.1 200 OK   (always, even if the Telegram send below failed)
  ```

- One Telegram message per webhook call, not per alert — every alert in one call already shares the same `alertname`/`app` (the caller's own `group_by`), so the formatted message's header appears once and each alert's own `summary` annotation becomes its own line underneath
- A dedicated `telegram-secret` rather than two extra keys on the shared `aviron-secret` — a deliberate blast-radius choice confirmed directly with the user: rotating or viewing the bot token should never touch `JWT_SECRET`/`POSTGRES_PASSWORD`
- Since silence is this handler's success signal (it only logs on failure), verification meant watching for the *absence* of an error line plus a flat `aviron_telegram_relay_errors_total`, confirmed alongside a real message actually landing in the configured Telegram chat — not inferring success from a lack of visible feedback alone

## Log-Based Alert Rule — Grafana Alerting on Elasticsearch

A second, parallel alerting path for the one thing Alertmanager structurally can't reach: log *content*.

### Goals (Log-Based Alert Rule)

- One rule, `LogErrorRateHigh`: count of `level:ERROR` documents in the `aviron-logs` index over 5m, threshold `>10`
- Routes through the same `telegram-relay` webhook, no new adapter

### Explain (Log-Based Alert Rule)

- Alertmanager only ever speaks PromQL against Prometheus — it has no way to query Elasticsearch at all, so this rule lives entirely inside Grafana's own built-in Unified Alerting instead, provisioned as config on the already-running Grafana `Deployment` (no new pod, no new `Service`)
- **A real bug in the spec's own sketch, caught only by a live evaluation**: a `threshold` expression can't operate directly on a `date_histogram`-bucketed query's time-series output — Grafana rejects it outright (`"looks like time series data, only reduced data can be alerted on"`). Fixed by inserting a `reduce` step in between:

  ```yaml
  data:
    - refId: A
      datasourceUid: elasticsearch
      model:
        query: "level:ERROR"
        bucketAggs: [{ type: date_histogram, id: "2", field: "@timestamp" }]
    - refId: B   # <- the missing piece: date_histogram's time series has no
      datasourceUid: __expr__ #    single scalar value a threshold can compare
      model:
        type: reduce
        expression: A
        reducer: sum
    - refId: C
      datasourceUid: __expr__
      model:
        type: threshold
        expression: B   # not A
        conditions: [{ evaluator: { type: gt, params: [10] } }]
  ```

- A second real bug, found only once the actual Telegram message text was inspected rather than just confirming delivery: the notification policy's missing `group_by` meant `groupLabels` came through empty on the webhook payload, so `telegram-relay`'s title read a bare "FIRING:" with nothing after it. Fixed by adding `group_by: ["alertname"]` to match `alert-rules.md`'s own Alertmanager route
- Triggered for real: scaling `race-service` to 0 and bursting a room-less REST route crosses the threshold via `ws-gateway`'s own `"no backends available"` ERROR logs almost immediately — confirmed firing in Grafana's UI and a real message landing in Telegram

## Trace-Based Alert Rule — Tempo Metrics-Generator

The trace-data counterpart to the log-based rule above — but deliberately *not* a third alerting engine.

### Goals (Trace-Based Alert Rule)

- One rule, `SpanErrorRateHigh`: a service's span error ratio (`STATUS_CODE_ERROR` / total calls) above 10% over 5m
- Appended to the same Alertmanager pipeline `alert-rules.md` already owns — no new receiver, no new `ConfigMap`

### Explain (Trace-Based Alert Rule)

- Tempo has no native "count over a threshold" query interface the way Elasticsearch does, so the standard mechanism is different: Tempo's own metrics-generator derives Prometheus-shaped RED metrics (`traces_spanmetrics_calls_total`) from every span it already receives and remote-writes them into Prometheus — turning "alert on traces" into one more ordinary Prometheus rule instead of a second log-alerting-style engine
- **A real bug in the spec's own sketch, confirmed after it crashed the pod**: `remote_write` is a `storage`-level field in Tempo's config schema, not a sibling of `storage`/`registry` as the spec assumed — the literal sketch failed to parse (`field remote_write not found in type generator.Config`):

  ```yaml
  metrics_generator:
    storage:
      path: /var/tempo/generator-wal
      remote_write:              # nested under storage, not top-level
        - url: http://prometheus.aviron.svc.cluster.local:9090/api/v1/write
    registry:
      external_labels:
        cluster: aviron-kind
  ```

- Prometheus needed one new flag to accept the push, `--web.enable-remote-write-receiver` — the one deliberate exception to this project's otherwise pull-only metrics stance, scoped narrowly to this single path
- **The spec's own trigger method doesn't actually work against this codebase**: scaling `postgres` to 0 makes `race-service`'s `/healthz` readiness probe fail (it checks Postgres), which pulls the pod out of `ws-gateway`'s routing entirely before any request — and thus any span — is ever produced. Triggered instead by port-forwarding directly to the `race-service` pod, bypassing the readiness gate, to get a real `500` inside a live span
- Confirmed live: `traces_spanmetrics_calls_total` flowing into Prometheus with exactly the labels the rule expects (`service`, `status_code`); the rule fired at a real ratio of `0.606`; reached Alertmanager and Telegram

## Phase 6 Verification

The last spec: not new features, but the one pass that generates a real race and follows it through every pillar plus all three alert types as a single connected story — proving the pieces work *together*, not just in isolation.

### Goals (Phase 6 Verification)

- 12 concrete pass/fail checks, from "every pod is Running" through a real `go test ./... -race` run
- Whatever small fixes the steps surface get made — not pre-built, scoped to what a real run actually shows

### Explain (Phase 6 Verification)

| Step | What it proved | Real finding |
| --- | --- | --- |
| 1-3 | All pods healthy; real k6 race generates traffic; RED dashboards show non-collapsed per-`pod` series | HPA scaled `race-service` to 5 replicas live during the run |
| 4 | A `telemetry` trace crosses `ws-gateway` → NATS → `race-service` | The outbound broadcast leg can never join that trace — documented as architectural, not fixed (`tracing/instrumentation.md`'s entry above) |
| 5 | Collector/Tempo keep up under load | 18,216 spans, zero drops — ~137/sec, well past the original 5-25/sec estimate |
| 6 | "Trace to logs" lands on the matching document | Same Elasticsearch doc ID confirmed both by direct query and by the UI click-through |
| 7 | A real metrics-based alert reaches Telegram | Closed `alert-rules.md`'s own deferred gap — `PodRestartLooping` had never actually fired anywhere before this |
| 8-9 | `LogErrorRateHigh`/`SpanErrorRateHigh` still reach Telegram as part of one coherent pass | Confirmed alongside everything else, not re-litigated in isolation |
| 10 | HPA panel shows replicas overlaid with CPU | Visually confirmed 2→5 climbing in lockstep with the metric that triggered it |
| 11 | Mid-load rolling restart doesn't break the pipeline | Two real capacity bugs found (below), one open gap left documented |
| 12 | `go build`/`go test ./... -race` | Clean, forced non-cached run |

- Two real capacity bugs, found only by combining heavy load (a real k6 race plus a 60k-request `ab` burst together) with a mid-load rolling restart — neither ever surfaced by any earlier, lighter-load step:

  ```yaml
  # otel-collector: the un-capped default (send_batch_size: 8192) let a single
  # 5s batch exceed gRPC's 4MiB default message size — Tempo rejected it as
  # non-retryable, permanently dropping thousands of spans per event
  batch:
    timeout: 5s
    send_batch_size: 2000
    send_batch_max_size: 3000
  ```

  Tempo itself got `OOMKilled` under the same load — its `256Mi` memory limit predated `trace-alert-rules.md`'s metrics-generator and was too small for the added footprint; bumped to `256Mi request / 512Mi limit`. Both re-verified fixed under the identical load that broke them
- One finding left genuinely open, confirmed with the user rather than guessed at: a handful of Fluent Bit chunks got stuck in growing retry backoffs against Elasticsearch despite its write thread pool showing zero rejections — root-causing it needs debug-level Fluent Bit logging this pass didn't apply
- The rolling restart itself, otherwise, was clean: all 40 WebSocket sessions/iterations completed with zero interrupted iterations — in-progress races survived real pod churn uncorrupted. **This closes out Phase 6 entirely.**
