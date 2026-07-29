# Current Feature: Graceful Shutdown

## Status

In Progress

## Goals

- `go test ./... -race` passes, including the new concurrency-adjacent
  code this spec adds (signal handling in all three `cmd/*/run.go`, the
  new `raceHubRegistry.Shutdown()` closing connections concurrently with
  `run`'s own loop).
- Manual: `kill -TERM <pid>` against each binary started directly
  (`go run ./cmd/server`, `./cmd/ws-gateway`, `./cmd/consumer`) — process
  exits within its configured budget, logs show the expected sequence
  (readiness flips first, in-flight work finishes or drains, clean exit —
  not a bare unexplained one).
- `cmd/server`: start a race, send `SIGTERM` mid-race, confirm the race is
  allowed to finish (or reach its own natural grace-period-expiry end)
  rather than being cut off immediately.
- `cmd/ws-gateway`: open a WebSocket connection, send `SIGTERM`, confirm
  the connection receives a clean close frame (not a silent TCP drop)
  within the flush window, not the full grace period.

## Explain

- Pure Go application code — no Kubernetes manifests. Sequenced before
  `k8s-race-service-deploy.md`/`k8s-ws-gateway-deploy.md`/
  `k8s-consumer-deploy.md` so their `terminationGracePeriodSeconds` and
  `readinessProbe`/`livenessProbe` fields configure real behavior instead
  of a number chosen in a vacuum.
- Confirmed gap: none of `cmd/server`, `cmd/ws-gateway`, `cmd/consumer`
  handle `SIGTERM` today — `cmd/ws-gateway/run.go` says so in its own
  comment. Kubernetes sends `SIGTERM` on every pod termination (rolling
  update, scale-down, eviction), followed by `SIGKILL` after
  `terminationGracePeriodSeconds` (30s default) — today that first signal
  does nothing, the process dies exactly as hard as `SIGKILL` would.
- The three binaries need three genuinely different amounts of new code:
  - `cmd/consumer` — lightest lift. `internal/consumer`'s fetch loops
    already check `ctx.Err()` and flush in-flight batches before
    returning; the only change is wiring a `signal.NotifyContext`-derived
    context into `c.Run(ctx)` instead of `context.Background()`.
  - `cmd/server` — the real design decision: **let in-progress races
    finish naturally, only stop admitting new connections.** Readiness
    flips to unready the instant `SIGTERM` arrives, but the root
    `context.Context` behind `room.Registry` is *not* cancelled early —
    existing room actors keep running until their own normal lifecycle
    ends them. Bounded, not unbounded: `Shutdown`'s own timeout plus
    Kubernetes' eventual `SIGKILL` are the backstop.
  - `cmd/ws-gateway` — hardest of the three. Real gap: nothing today
    tells a locally-held WebSocket connection "the whole process is
    shutting down" — `raceHub.run` only exits on its bus subscription
    closing, `room_closed`, or its own last-local-connection detach. Needs
    a genuinely new `raceHubRegistry.Shutdown()` method to close every
    locally-registered connection directly.
- Readiness vs. liveness split (resolves gap 3 from `phase-5-plan.md`):
  keep `GET /healthz`'s existing dependency check for `readinessProbe`
  only; add a new, dependency-free `GET /livez` for `livenessProbe` — so a
  transient Postgres/Redis/NATS blip doesn't make `kubelet` restart an
  otherwise-healthy pod.

## Plan

1. `cmd/consumer/run.go`: replace `context.Background()` with a
   `signal.NotifyContext(context.Background(), syscall.SIGTERM,
   syscall.SIGINT)`-derived context, `defer stop()`. No other line in
   `internal/consumer` changes.
2. `internal/httpserver`: add `GET /livez` (trivial `200`, no dependency
   checks) alongside the existing `GET /healthz`. Add a small atomic bool,
   flipped by each binary's signal handler, checked at the top of
   `/healthz` so it starts returning `503` the instant `SIGTERM` arrives —
   before `Shutdown` even begins draining — so Kubernetes' readiness-based
   traffic removal starts as early as possible.
3. `cmd/server/run.go`: build an explicit `*http.Server`, `go
   server.ListenAndServe()`, block the main goroutine on a
   `signal.NotifyContext`-derived context. On signal: flip the
   `/healthz`-unready bool, then `server.Shutdown(shutdownCtx)` with a
   bounded timeout (~25s, comfortably inside Kubernetes' default 30s
   `terminationGracePeriodSeconds` — the two numbers must agree once
   `k8s-race-service-deploy.md` sets the manifest side). The root context
   threaded into `RegisterRoutes`/`room.Registry` stays independent of
   this shutdown context — that's what lets in-progress room actors keep
   running per the design decision above.
4. `cmd/ws-gateway`: same mechanical shape (own `/livez`, own
   `/healthz`-flip). Design and add `raceHubRegistry.Shutdown()` — confirm
   at implementation time whether the cleanest shape is `raceHub` tracking
   closable connection objects directly (today's `conns map[chan
   []byte]struct{}` only holds the outbound channel) or a simpler shared
   `done` channel `serveConn`'s own loops already select on. On `SIGTERM`:
   flip readiness, wait a short flush window (a few hundred ms, letting
   any already-queued final broadcasts drain through each `raceHub`'s
   fan-out), then call `raceHubRegistry.Shutdown()` so `Shutdown(ctx)`'s
   own wait returns promptly instead of blocking the full grace period.
5. Update `context/features/phase4/horizontal-scaling/ws-gateway.md` once
   the `raceHubRegistry.Shutdown()` design is actually resolved — this is
   genuinely new design, not just wiring, per this spec's own note not to
   let that decision live only in this file.

**Disclosed product decision, not a default:** "let in-progress races
finish naturally" (step 3) is a real tradeoff, not the only option — the
alternative (cancelling the root context immediately on `SIGTERM` to
force-drain every room) would forcibly end every in-progress race the
instant a rolling update starts, which reads as a worse outcome given
`project-overview.md` §7's own wording ("without cutting off the
WebSocket of someone mid-race"). If `multi-instance-k8s-verification.md`
later shows this decision doesn't hold up under a real `kubectl rollout
restart`, revisit and update this spec's file rather than patching around
it silently — same convention this project already uses for design
decisions that don't survive contact with a real run.

## Notes

- Full spec: `context/features/phase5/graceful-shutdown.md`. Phase
  roadmap: `context/features/phase5/phase-5-plan.md`.
- Hard prerequisite for `k8s-race-service-deploy.md`'s and
  `k8s-ws-gateway-deploy.md`'s `terminationGracePeriodSeconds` and
  `readinessProbe`/`livenessProbe` fields to mean anything real — those
  specs reference the exact endpoints and timing decided here rather than
  re-deriving them.
- No manifests in this feature at all — `deploy/k8s/` isn't touched;
  everything here is `backend/` Go code.
- `raceHubRegistry.Shutdown()`'s exact design needs `internal/wsgateway/
  endpoint.go`'s `serveConn` read in full before deciding the plumbing —
  flagged in the spec as a real open question, not pre-solved.
