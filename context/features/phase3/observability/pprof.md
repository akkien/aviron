# pprof

## Overview

`context/project-overview.md` §9: "`pprof` enabled in dev/staging
environments to inspect real goroutine leaks." The smallest spec in this
phase — `net/http/pprof` is stdlib, registering it is a handful of lines —
but it needs one real design decision this project doesn't have an answer
for yet: how to gate it off in a hypothetical production environment,
since `internal/config.Config` has no environment concept at all today
(confirmed by direct read of `internal/config/config.go` — just
`DatabaseURL`/`Port`/`JWTSecret`/`CORSAllowedOrigin`, no `Env` field).

## Requirements

### Registration

- `net/http/pprof`'s `init()` registers its handlers
  (`/debug/pprof/*`) onto `http.DefaultServeMux` the moment it's
  imported — this project's `internal/httpserver.NewServer()` builds its
  own `*http.ServeMux` explicitly rather than using the default one
  (confirmed: `NewServer() *http.ServeMux`), so a blank `import
  _ "net/http/pprof"` alone would silently register onto the wrong mux and
  do nothing. Needs explicit registration of the individual handlers
  (`pprof.Index`, `pprof.Profile`, `pprof.Symbol`, `pprof.Trace`, plus
  `pprof.Handler("goroutine")`/`"heap"`/etc. for the named profiles) onto
  this project's real mux — a well-known gotcha worth calling out
  explicitly so `start` doesn't rediscover it the hard way.

### Gating

- New `Config.PprofEnabled bool` (env var `PPROF_ENABLED`, default
  `true`) — simpler than inventing a full `Env` enum
  (`development`/`staging`/`production`) this codebase has no other use
  for yet; a single bool flag is proportionate to what §9 actually asks
  for ("enabled in dev/staging", i.e. *not* unconditionally-on
  everywhere) without adding an abstraction nothing else needs.
- When enabled, registered under `/debug/pprof/` like the standard
  library's own convention — not a custom path, so `go tool pprof
  http://host:port/debug/pprof/profile` works with zero extra flags.
- **Not** wrapped in `requireAuth` — pprof has no concept of a JWT bearer
  token, and this project has no separate "admin" auth tier to reuse. For
  a side project with no real production deployment yet, unauthenticated
  pprof gated by an env var default is an acceptable trade — flagged here
  explicitly rather than silently decided, since a genuinely
  internet-exposed deployment would want this behind network-level access
  control (a k8s `NetworkPolicy`/ingress rule restricting `/debug/pprof/`,
  which is Phase 4 territory, not this spec's).

## Concurrency

- N/A — `net/http/pprof`'s handlers are part of the standard library and
  already safe for concurrent use; this spec only wires them up, no new
  concurrency of its own.

## Data

```go
// internal/config/config.go
type Config struct {
    // ...existing fields...
    PprofEnabled bool
}
```

```go
// internal/httpserver/route.go or internal/app.go (exact seam TBD at start)
if cfg.PprofEnabled {
    server.HandleFunc("GET /debug/pprof/", pprof.Index)
    server.HandleFunc("GET /debug/pprof/cmdline", pprof.Cmdline)
    server.HandleFunc("GET /debug/pprof/profile", pprof.Profile)
    server.HandleFunc("GET /debug/pprof/symbol", pprof.Symbol)
    server.HandleFunc("GET /debug/pprof/trace", pprof.Trace)
}
```

## Notes

- Independent of `prometheus-metrics.md` and `structured-logging.md` —
  can be built in any order relative to those two; sequenced third in
  `phase-3-plan.md` mainly because it's the smallest, lowest-risk piece.
- This is the tool `load-testing/k6-load-test.md`'s follow-up work
  (diagnosing whatever a load test surfaces) will actually use — a
  goroutine or heap profile is what turns "goroutine count grew during
  the test" (visible via `prometheus-metrics.md`'s gauge) into "grew
  because of *this* code path."
