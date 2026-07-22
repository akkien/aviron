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

## Leave Race

`POST /races/{id}/leave` for backing out of a still-pending race, and an immediate WebSocket `leave_race` message for quitting mid-race.

### Goals (Leave Race)

- Leaving before start removes the participant outright, no trace left
- Quitting mid-race is immediate — no 30s grace period, since it's a deliberate choice, not a dropped connection
- Every participant who ever raced gets a result row, even quitters — sharing one last-place rank rather than vanishing from the leaderboard silently

### Explain (Leave Race)

- New `ParticipantLeft` event, distinct from the grace-period's `ParticipantEvicted` — a quit is always honored immediately, an eviction only after the grace window and only if still marked disconnected
- A real rank-collision bug caught during review: computing "the next finisher's rank" by counting live participants missed anyone who'd already finished and then disconnected, handing two finishers the same rank — fixed with a monotonic counter immune to who's since departed, with a regression test reproducing the exact collision

## WebSocket Client (Frontend)

The React app opens the WebSocket connection, sends one `telemetry` message per correctly-typed word, and renders every participant's car moving live from the server's `race_state` broadcasts.

### Goals (WebSocket Client)

- Every participant's position updates live, not just the local player's own (closing Phase 1's biggest limitation)
- A "Quit Race" button sends `leave_race` and shows results/DNF immediately

### Explain (WebSocket Client)

- Extracted into a `useRaceSocket` hook rather than inlining the connection in the typing view, deliberately, so a later reconnect feature could extend it instead of requiring a rewrite
- Caught and fixed a spec-compliance slip before shipping: an early draft special-cased the local player's own lane for snappier feedback, but the spec explicitly said no more special-casing — corrected to drive every lane, including the local one, purely from the server's broadcast

## Reconnect UI

A dropped connection is retried automatically (3 attempts, 2s apart) instead of leaving the player stuck.

### Goals (Reconnect UI)

- A dropped connection retries automatically; the UI shows "Reconnecting..." rather than an error
- Exhausting every retry (or a rejected reattach) shows a clear "evicted" state

### Explain (Reconnect UI)

- Two real React 18 StrictMode bugs caught and fixed during design review, before shipping: a shared ref tracking "did this effect already stop" got reset by StrictMode's dev-only double-invoke before a stale connection's `onclose` actually fired, and `onclose` unconditionally nulled out the active connection reference even when it was a late-firing stale one — both fixed by scoping state correctly per effect execution
- No exponential backoff — a fixed, short retry window is enough for a side project, not a production reconnect strategy

## Race ID Display & Shortening

The race status view gained a copyable Race ID, and the ID format itself shrank from a raw UUID to a 12-character, hand-typeable string.

### Goals (Race ID Display & Shortening)

- A race's ID is visible and copyable from its status view
- IDs are short enough to read aloud or type by hand to invite another player

### Explain (Race ID Display & Shortening)

- `crypto/rand`-backed generation using the Bitcoin base58 alphabet (excludes `0`/`O`/`I`/`l` for readability); `races.id` and its two FK columns switched from `UUID` to `TEXT` via migration
- Postgres no longer guarantees uniqueness on its own, so `CreateRace` retries up to 5 times on a collision — at roughly 70 bits of entropy, vanishingly unlikely but no longer structurally impossible
- Verified against a live database, not just tests — confirmed the migration applied cleanly to existing rows and a full register→create→join→leave flow round-tripped a real generated id

## UI Revamp — Theme

Fonts, a warm color palette, and rounded card chrome applied globally from a supplied design mockup.

### Goals (UI Revamp — Theme)

- The app's visual tokens (fonts, colors, corner radius) match the supplied mockup

### Explain (UI Revamp — Theme)

- Applied entirely through global CSS theme tokens, not per-component overrides — confirmed the login page needed zero direct edits to pick up the new look, proving the token-based approach actually works instead of requiring per-page touch-ups later

## UI Revamp — Dashboard

A real Dashboard (header, stat cards, open-races list, create/join forms) replaces the old plain forms page.

### Goals (UI Revamp — Dashboard)

- The Dashboard shows account info, placeholder stat cards, and create/join forms in the new visual style

### Explain (UI Revamp — Dashboard)

- Stat cards shipped with hardcoded placeholder values from the start, explicitly flagged as pending real backend support — closed later by User Stats
- `CreateRace` was changed to auto-join the creator as a participant, closing a gap flagged back in Phase 1, as a small bundled fix alongside this rebuild

## UI Revamp — Race Screen

A single full-height race screen (30/70 sidebar/track split) replaces the old stacked status-view-plus-typing-view cards.

### Goals (UI Revamp — Race Screen)

- One unified screen handles both the pending lobby and the active race, instead of two separate stacked components
- The typing box behaves like a real typing-test tool: a wrong keystroke is rejected outright, never inserted-then-flagged

### Explain (UI Revamp — Race Screen)

- The typing box went through many iterative rounds of direct user feedback before converging on its final strict-validation behavior
- Several real rendering bugs (word-wrap, scroll-position drift, a keyboard-sound clip playing the wrong sample) were caught from user screenshots and fixed iteratively, since no browser is available in this environment to view the running app directly

## Race Detail Route & Race-Finish Disconnect Fix

Each race got its own URL (`/races/:raceId`), and a real concurrency bug where every player got disconnected the instant the last one finished was root-caused and fixed.

### Goals (Race Detail Route & Race-Finish Disconnect Fix)

- Visiting `/races/:raceId` shows that race directly — reloadable, shareable
- Finishing a race no longer disconnects every other still-connected player

### Explain (Race Detail Route & Race-Finish Disconnect Fix)

- The disconnect bug was two stacked races, not one: broadcasting `race_finished` and cancelling the room happened close enough together that Go's `select` could pick the shutdown case over the still-unread final message — fixed by making each connection's context independent of the room's, so only that connection's own errors can cancel it, and draining broadcasts deterministically before signaling done
- Confirmed with a real regression test proven to fail 20/20 times against the pre-fix code and pass 50/50 after, via an actual `git stash` comparison rather than just reasoning about it
- Full root-cause writeup lives in `docs/concurrency.md`

## Early Room Spawn

The room actor now spawns when a race is created, not when it starts — the prerequisite for every player holding a live connection before the race begins.

### Goals (Early Room Spawn)

- A room actor exists from the moment a race is created, not just once started
- `POST /races/{id}/start` activates the already-existing actor instead of spawning a new one

### Explain (Early Room Spawn)

- Root cause traced from a user report that other players had to manually refresh after the creator started a race — every player needing a live connection *before* start requires a room to connect to before start
- A room can now legitimately be `pending` and empty at the same time, so the no-show-timeout logic had to stop assuming "empty room" always means "abandoned mid-race"

## Pending Connections

`GET /ws` can now attach to a still-pending room, with an explicit active/pending status gating telemetry until the race actually starts.

### Goals (Pending Connections)

- A pending room accepts WebSocket connections, not just active ones
- Telemetry sent before the race is active is dropped, not accumulated
- Leaving a pending lobby goes through the same WebSocket path as quitting mid-race, not a separate REST endpoint

### Explain (Pending Connections)

- A real, previously-live exploitable gap found during design review: a client connected to a pending race could already accumulate progress before the race legitimately started — fixed by gating `TelemetryReceived` on the room's active flag
- `POST /races/{id}/leave` was removed entirely in favor of a WebSocket `leave_race` message for both pending and active races — one mechanism instead of two

## race_started Broadcast

The actual fairness fix the surrounding work exists for — every pending player learns the race started at the same moment, over the WebSocket connection they're already holding.

### Goals (race_started Broadcast)

- The instant a race starts, every connected pending player receives `race_started` with the prompt text already included
- No more manual refresh or polling needed to discover the race began

### Explain (race_started Broadcast)

- Reuses the exact fan-out mechanism `race_state` already broadcasts through — no new delivery path, just a new message type
- Carries `prompt_text` directly so a client can start typing immediately, with no follow-up REST round-trip adding its own delay variance between players

## Pending Expiry & race_expired Broadcast

A pending race now has a bounded lifetime instead of sitting open forever if the creator never starts it.

### Goals (Pending Expiry & race_expired Broadcast)

- A pending race nobody starts eventually expires and tears down cleanly
- Every connected player sees a `race_expired` message and a visible countdown beforehand, not a connection that silently dies

### Explain (Pending Expiry & race_expired Broadcast)

- Found a real, previously-nonexistent gap while building this: a full or partial lobby sitting pending was never torn down by anything that existed before this feature — the only existing teardown path only fired once every participant had already finished, which a pending race's participants never do
- Shares its teardown path with the empty-room case rather than duplicating it

## Cancelled Race Status

A race that expires or empties out before starting is now persisted as `cancelled` in Postgres, instead of silently staying `pending` forever.

### Goals (Cancelled Race Status)

- An expired/abandoned pending race's status becomes `cancelled`, not stuck on `pending`
- Joining or starting a cancelled race is correctly rejected
- A visitor arriving at a dead race sees a clear message, not a permanent loading spinner

### Explain (Cancelled Race Status)

- Found by asking what a real visitor actually sees after a race expires: the previous teardown wrote zero Postgres changes, so `races.status` stayed `'pending'` forever, meaning `POST /races/{id}/join` kept succeeding into a room whose actor was already gone
- `RaceCanceller` mirrors the same structural-interface pattern already used for finishing/leaving a race

## Live Lobby (Frontend)

The frontend now holds a live WebSocket connection the moment a player lands on a pending race, consuming every message the backend work above added.

### Goals (Live Lobby)

- Every pending player sees new joins/leaves and the race starting live, no manual refresh
- A visible countdown shows how long until an unstarted race expires
- The manual "Refresh" button is gone entirely — nothing it worked around still needs it

### Explain (Live Lobby)

- The connection now opens the instant a session token exists, not gated on the race already being active
- An already-connected non-creator learns the race went active via the same REST re-fetch mechanism the creator's own start action already used — one uniform path instead of a second "am I active" field to reconcile

## Race Detail — Cold Visit & Spectator View

Visiting a race's URL cold — after it finished, or without ever having joined — now renders correctly instead of showing a broken "disconnected" state or a permanent loading spinner.

### Goals (Race Detail — Cold Visit & Spectator View)

- Reloading right after finishing a race shows results, not "you were disconnected"
- Visiting a race you never joined shows a read-only spectator view, not a broken state

### Explain (Race Detail — Cold Visit & Spectator View)

- Root cause was the page only ever rendering correctly for a client holding a live, successfully-connected WebSocket — a REST-only visitor had no path
- The WebSocket connection gate checks for *terminal* status (finished/cancelled) rather than *known-good* status, so a fresh join/create's connection isn't delayed by a REST round-trip first — deliberately chosen to avoid reintroducing the exact connection-delay unfairness the Live Lobby work above was built to eliminate
- `GetRaceWithParticipants`'s query was missing `finish_rank`/`finish_time_ms`/`avg_pace_watt` entirely — a real backend gap, not just a frontend one

## User Stats (Backend for Dashboard Stat Cards)

The dashboard's stat cards (races joined, races won, avg WPM) now show real per-user data from Postgres instead of hardcoded placeholder numbers.

### Goals (User Stats)

- `GET /leaderboard/me` returns the caller's own races-joined/races-won/avg-WPM
- A brand-new account gets all-zero stats, not a 404

### Explain (User Stats)

- Closed a real, previously-disclosed gap: `AvgPaceWatt` had been written as `0.0` unconditionally since Race Completion shipped, because the WebSocket layer decoded `pace_watt` off the wire but never forwarded it into the room actor's telemetry event — now wired through end to end
- New `internal/leaderboard` domain package, following the same Handler/Service/Repository layering as every other REST domain
- `AvgWPM` rounds to 2 decimal places server-side, added after live testing surfaced a real, explainable oddity: a brand-new real race's WPM looked implausibly low because it was averaged against dozens of historical test races that had `0` pace recorded before this fix existed

## Open Races (Real List + Polling)

The dashboard's "Open Races" list now shows real, joinable pending races from Postgres, polling every 5 seconds, with a working "Join" button instead of a decorative fake one.

### Goals (Open Races)

- `GET /races` lists pending, joinable races the caller hasn't already created or joined
- The list updates on its own every 5 seconds — no manual refresh
- Clicking "Join" actually joins the race and lands the player on it

### Explain (Open Races)

- Excludes races the caller already created or joined, not just full ones — otherwise a creator would see their own just-created race in their own browsable list and get a conflict clicking it
- Two real bugs found and fixed while testing this feature live: a pending lobby's player list never updated on a join/leave without a manual refresh (a frontend rendering gap, not a missing broadcast), and the last participant leaving a pending race never actually cancelled it (a missing call to the existing finish-check logic)

## Redirect to Login on 401

Any API call that comes back `401 Unauthorized` now clears the stored session and redirects to the login page automatically, instead of leaving the user stuck on a broken page.

### Goals (Redirect to Login on 401)

- An expired or invalid JWT on any authenticated request redirects to `/login`, app-wide
- A wrong password on the login form itself still shows inline, not a redirect

### Explain (Redirect to Login on 401)

- Centralized in `apiFetch`, the one function every authenticated request already goes through — cheaper and more reliable than adding an `isAuthenticated()` check to every page individually
- `/auth/login`/`/auth/register` are explicitly excluded, since a `401` there means "wrong credentials," a normal validation outcome, not "your session expired"

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
