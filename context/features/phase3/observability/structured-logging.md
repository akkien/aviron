# Structured Logging

## Overview

`context/project-overview.md` §9 calls for structured logging (`slog`),
always tagged with `race_id`, `user_id`, `request_id`. Today the whole
backend logs through the plain `log` package — confirmed by direct grep,
there are exactly 7 call sites, all `log.Printf`, none structured, none
carrying any of those three identifiers:

```text
internal/app.go:35        log.Printf("listening on :%s", cfg.Port)
internal/room/room.go:342 log.Printf("room %s: leave race for user %s: %v", r.id, e.UserID, err)
internal/room/room.go:415 log.Printf("room %s: cancel race: %v", r.id, err)
internal/room/room.go:555 log.Printf("room %s: finish race: %v", r.id, err)
internal/race/handler.go:189 log.Printf("race %s: room actor missing at start", raceID)
internal/ws/endpoint.go:157  log.Printf("ws: dropping malformed message from user %s: %v", userID, err)
internal/ws/endpoint.go:163  log.Printf("ws: dropping message from user %s: %v", userID, err)
```

This is the foundation spec for the rest of Phase 3 — deliberately first,
since load testing (`load-testing/k6-load-test.md`) is far more useful once
failures/backpressure show up as structured, filterable log lines instead
of unlabeled `Printf` text.

## Requirements

### Logger setup

- One process-wide `*slog.Logger`, constructed in `internal/app.go`'s
  `Run(cfg)` using `slog.NewJSONHandler(os.Stdout, ...)` — JSON output,
  matching "production-style operations" framing in §9 (a human-readable
  text handler is easy to swap to locally later if wanted, but JSON is the
  format every real log aggregator expects).
- Threaded explicitly, not via a package-level global — matches this
  project's existing no-hidden-globals convention (`pool`, `registry`, etc.
  are all constructor/handler parameters, never package vars). Every
  existing constructor that currently logs (`RoomActor`, `WSHandler`,
  `RaceHandler`) gains a `*slog.Logger` field/param.

### The 3 required tags

- `race_id` — already available at every one of the 7 existing call sites
  above (`r.id`/`raceID` in scope already); just needs to move from a
  `Printf` verb into a `slog.String("race_id", ...)` attribute.
- `user_id` — same: already available at 4 of the 7 sites
  (`room.go:342`, `endpoint.go:157`, `endpoint.go:163`, and implicitly
  anywhere a handler already has it via `middleware.UserIDFromContext`).
- `request_id` — **does not exist anywhere in this codebase today.** Needs
  a new `internal/middleware/requestid.go`, following the exact
  `func(http.Handler) http.Handler` + context-key + `XFromContext(ctx)`
  accessor shape `middleware.Auth`/`middleware.Cors` already establish
  (`internal/middleware/auth.go`'s `contextKey`/`userIDContextKey`/
  `UserIDFromContext` is the literal template). Generates a random id
  (`crypto/rand`-backed, not `math/rand` — matches `internal/race`'s
  existing `GenerateRaceID` precedent of using `crypto/rand` for anything
  identifier-shaped) per request, attaches it to the request context, and
  — open question for `load`/`start`: should it also echo back as a
  response header (e.g. `X-Request-ID`)? Useful for correlating a client-
  reported bug with server logs, cheap to add, no existing precedent
  either way in this codebase.
- Registered as the **outermost** middleware in `internal/app.go` (wrapping
  even `middleware.Cors`), so every request — including ones that fail
  auth or CORS — gets a request id before anything else runs.

### What gets logged

- The 7 existing call sites, converted to `slog` with the tags above
  attached wherever available.
- One new log line per request at the HTTP layer: method, path, status
  code, duration, `request_id` (and `user_id` if `middleware.Auth` already
  ran) — needs a small logging middleware, same shape as `requestid.go`,
  registered right after it. This is the piece that actually makes
  `request_id` useful — without a per-request summary line, there's
  nothing to correlate a downstream `room`/`ws` log line against.
- `internal/room`'s `RoomActor` and `internal/ws`'s `WSHandler`/`hub` need
  a `*slog.Logger` field (or a `.With(slog.String("race_id", r.id))` child
  logger stored once at construction, rather than passing `race_id` at
  every call site) — **open question for `load`/`start`**: store a
  pre-tagged child logger on `RoomActor` at spawn time (`logger.With(...)`
  once), or pass `race_id` explicitly at every log call? A child logger
  is less repetitive and matches `slog`'s own idiomatic pattern, but
  changes `NewRoomActor`'s constructor signature (already grown several
  params across `finisher`/`leaver`/`canceller` — see
  `context/current-feature.md`'s History for `room-registry.md`'s existing
  concern about constructor signature growth forcing test-fixture churn).

### What's explicitly NOT in scope here

- `log.Fatalf` call sites (`internal/app.go`'s DB connection/migration
  failures) stay as-is — these happen before the logger or any server is
  even running, converting them buys nothing.
- No log *level* filtering/config (e.g. `LOG_LEVEL` env var) — `slog`
  defaults to `Info` and up; adding a configurable minimum level is a
  legitimate future nice-to-have but not required by §9's wording.

## Concurrency

- `*slog.Logger` (and handlers built via `slog.NewJSONHandler`) are safe
  for concurrent use by design — this is explicitly documented in the
  standard library, unlike the plain `log` package's `Logger` which is
  *also* concurrency-safe but produces unstructured text. No new
  synchronization needed anywhere this gets threaded into
  (`RoomActor.Run()`'s single goroutine, `internal/ws`'s reader/writer
  goroutines, HTTP handler goroutines).

## Data

```go
// internal/middleware/requestid.go
func RequestID() func(http.Handler) http.Handler
func RequestIDFromContext(ctx context.Context) (string, bool)
```

```go
// internal/app.go (sketch)
logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
...
handler := middleware.RequestID()(middleware.Cors(cfg.CORSAllowedOrigin)(server))
```

## Notes

- Depends on nothing else in Phase 3 — this is the foundation the other
  specs build observability on top of.
- `prometheus-metrics.md` and this spec touch some of the same files
  (`internal/room/room.go`, `internal/ws`) but are otherwise independent —
  order between them doesn't matter functionally, logging is sequenced
  first here mainly because it's lower-risk/higher-value to land before
  load testing generates a lot of noisy output to sift through.
