# Graceful Shutdown

## Overview

Pure Go application code — no Kubernetes manifests. Sequenced before
`k8s-race-service-deploy.md`/`k8s-ws-gateway-deploy.md`/
`k8s-consumer-deploy.md` so their `terminationGracePeriodSeconds` and
`readinessProbe`/`livenessProbe` fields configure real behavior instead of
a number chosen in a vacuum.

Confirmed by reading all three `cmd/*/run.go` files directly: none of
`cmd/server`, `cmd/ws-gateway`, `cmd/consumer` handle `SIGTERM` today.
`cmd/ws-gateway/run.go` even says so in its own comment ("No SIGTERM/
graceful-shutdown handling, consistent with every other binary in this
codebase"). Kubernetes sends `SIGTERM` on every pod termination — rolling
update, scale-down, eviction — followed by `SIGKILL` after
`terminationGracePeriodSeconds` (30s default). Today, that first signal
does nothing: the process dies exactly as hard as `SIGKILL` would, just a
few seconds sooner if anything happens to be watching. `project-overview.md`
§7 names the concrete cost directly: "a pod receiving `SIGTERM`... must
close its active room actors properly, without cutting off the WebSocket
of someone mid-race."

The three binaries need three genuinely different amounts of new code —
not the same patch copy-pasted three times.

## `cmd/consumer` — already does the hard part, just needs wiring

The lightest lift. Reading `internal/consumer/workout_sample_loop.go` and
`race_finished_loop.go` directly: both fetch loops already check
`ctx.Err()` on every iteration and return cleanly when it's non-nil —
`runWorkoutSampleLoop` even flushes its in-flight batch
(`c.flushWorkoutSampleBatch(context.Background(), reader, batch)`) before
returning, so no accumulated-but-unwritten samples are lost. This loop-level
logic was already built defensively; it has just never been exercised,
because `cmd/consumer/run.go` calls `c.Run(ctx)` with `ctx :=
context.Background()`, a context that's never cancelled.

**The only change needed**: replace `context.Background()` in
`cmd/consumer/run.go` with a `signal.NotifyContext(context.Background(),
syscall.SIGTERM, syscall.SIGINT)`-derived context, and defer its `stop()`
call. No other line in `internal/consumer` needs to change.

## `cmd/server` — the real design decision

### Mechanical part (`cmd/server`)

Replace the blocking `log.Fatal(http.ListenAndServe(...))` call with an
explicit `*http.Server`, started via `go server.ListenAndServe()`, with
the main goroutine blocking on a `signal.NotifyContext(context.Background(),
syscall.SIGTERM, syscall.SIGINT)`-derived context instead. On signal:
call `server.Shutdown(shutdownCtx)` with a bounded timeout — **2 minutes**,
corrected from an original 25s guess (see the correction below) — with
`terminationGracePeriodSeconds` set to comfortably exceed it; the two
numbers must agree, confirmed against whatever
`k8s-race-service-deploy.md` actually sets, not chosen independently.

### The real question: what happens to in-progress room actors

`stdlib`'s `http.Server.Shutdown` does **not** forcibly close
already-hijacked connections — it stops accepting new ones and waits for
in-flight handlers to return. A `WSHandler`'s connection-serving call
blocks for that connection's entire lifetime, so `Shutdown` already waits
for it naturally — the open question is what, if anything, should
actively encourage those connections (and the room actors behind them) to
wind down sooner, rather than just sitting in `Shutdown`'s wait until the
grace period runs out.

**Decision: let in-progress races finish naturally; only stop admitting
new ones.** The moment `SIGTERM` arrives, the readiness endpoint (see
below) flips to unready — a rolling update's replacement pod is already
starting, and Kubernetes stops routing new traffic to this one — but the
root `context.Context` threaded through `internal/httpserver.RegisterRoutes`
into `room.Registry` is **not** cancelled early. Existing room actors keep
ticking, finish or get cancelled by their own normal lifecycle rules
(finish, grace-period expiry, explicit cancel), exactly as
`project-overview.md` §7's own wording implies wanting ("without cutting
off the WebSocket of someone mid-race"), not forcibly ended the instant a
rolling update starts.

This is a bounded, disclosed tradeoff, not an unbounded wait: `Shutdown`'s
own timeout (above) still applies, and if a room genuinely outlives it,
Kubernetes' `SIGKILL` after `terminationGracePeriodSeconds` ends the
process regardless — the same category of accepted, bounded-impact
limitation `context/feature-history.md` already discloses for an owning
instance crashing outright (a room's live state lives only in that one
pod's RAM; no snapshotting or reassignment exists, or is planned, per that
same writeup). A graceful rolling update is expected to look nothing like
that crash scenario in practice — `multi-instance-k8s-verification.md`
below is where that expectation actually gets tested against a real
`kubectl rollout restart`.

**Correction (found by `multi-instance-k8s-verification.md`'s own first
live run): the original 25s `shutdownTimeout` was too short for entirely
ordinary races, not just extreme ones.** A real rolling update against a
race running `ROLLOUT_TEST_DISTANCE_METERS=40` hung until the `k6`
scenario's own 2-minute `maxDuration` — neither client ever received
`race_finished`. Root cause, traced directly rather than guessed: this
project's own default k6 load scenario
(`load/scenarios/race-lifecycle.js`) already uses a 30-word race, which
alone averages ~36s to finish at realistic telemetry pacing
(`project-overview.md` §4.2's 0.4-2s per word) — comfortably longer than
the 25s budget `waitForRoomsToDrain` had to work with, so the old pod
gave up waiting (or Kubernetes' own `SIGKILL` arrived first) before the
race could finish naturally, losing that room's state exactly like an
ungraceful crash would. Fixed by raising `shutdownTimeout` to **2
minutes** (`cmd/server/run.go`) and `terminationGracePeriodSeconds` to
**150s** (`k8s-race-service-deploy.md`'s `statefulset.yaml`) — confirmed
against a real re-run afterward: the same rollout now delivers
`race_finished` to both clients. A genuinely long race (e.g.
`project-overview.md` §3's own `distance_meters: 1000` example) still
would not fully survive a graceful rollout within this larger budget —
an accepted, disclosed limitation, the same category as this project's
other bounded scope decisions (single Redis/NATS instance), not silently
pretended away.

### Readiness vs. liveness — resolving gap 3 from `phase-5-plan.md`

`internal/httpserver.NewHealthzHandler`'s existing `Ping`-the-pool check
is the right shape for **readiness** (a pod that can't reach Postgres
genuinely shouldn't receive traffic) but wrong for **liveness**: wiring
the same dependency check into `livenessProbe` means a transient Postgres
blip gets `kubelet` to kill and restart an otherwise-healthy process,
turning a brief dependency hiccup into a pod-restart storm — exactly the
anti-pattern `phase-5-plan.md` flags.

Resolution: keep `GET /healthz` exactly as it is today, wired only to
`readinessProbe`. Add a new `GET /livez` — no dependency checks, just
`w.WriteHeader(http.StatusOK)` — wired to `livenessProbe`. This is new
code (a few lines in `internal/httpserver`), not a rename: `/healthz`'s
existing behavior and meaning are preserved for anything that already
depends on it (nothing does today, confirmed by `grep`, but the endpoint
itself stays stable regardless).

Additionally: on `SIGTERM`, `/healthz` should start returning `503`
immediately — before `Shutdown` even begins draining — so a rolling
update's readiness-based traffic removal (`kubectl` marks the pod
`NotReady`, removed from the `Service`'s Endpoints) starts as early as
possible. A small atomic bool flipped by the signal handler, checked at
the top of the existing handler, is enough; no new package needed.

## `cmd/ws-gateway` — the hardest of the three

### Mechanical part (`cmd/ws-gateway`)

Same `signal.NotifyContext` + explicit `*http.Server` + `Shutdown`
pattern as `cmd/server`.

### The real gap: local connections have no shutdown signal today

Reading `internal/wsgateway/racehub.go`/`endpoint.go` directly:
`raceHub.run` only exits on its bus subscription closing, a
`room_closed` envelope arriving, or `signalStop()` (triggered when this
gateway's last local connection for that race detaches) — there is no
existing path for "the whole gateway process is shutting down, close
every local connection now." Without one, `Shutdown` would simply wait
out the connections' entire remaining lifetime, exactly the
`terminationGracePeriodSeconds`-exhausting hang the mechanical part above
is meant to avoid.

**New method needed on `raceHubRegistry`**: a `Shutdown()` that iterates
every currently-tracked `raceHubEntry` and closes each of that hub's
registered connections directly (not `signalStop`, which only tears down
the hub's own bus subscription — the connections themselves need their
underlying `wsConn.Close(...)` called so `serveConn`'s blocking read/write
loops actually return). Confirm at `start` whether the cleanest shape is
`raceHub` tracking its `conns` map's keys as closable connections
directly (today `conns map[chan []byte]struct{}` only holds the outbound
channel, not the connection itself — `endpoint.go`'s `serveConn` would
need to be read in full to confirm exactly what's reachable from where),
or whether a simpler signal (closing a new shared `done` channel that
`serveConn`'s own read/write loops already select on, if such a select
exists) is less invasive than plumbing `wsConn.Close` through the hub
layer. This is genuinely new design, not just wiring — treat it with the
same care `ws-gateway.md` itself gave the original hub design, and update
that spec's file once resolved rather than letting this decision live
only here.

- On `SIGTERM`: flip `/healthz` to `503` immediately (same pattern as
  `cmd/server`, add the same `/livez` split), stop accepting new `GET /ws`
  upgrades and new REST proxy requests (readiness removal handles routing
  new traffic away; the handler itself doesn't need an explicit "reject"
  branch beyond what `Shutdown`'s stopped-accepting behavior already
  gives it), then — after a short flush window (a few hundred
  milliseconds to let any already-queued final broadcasts drain through
  each `raceHub`'s fan-out) — call the new `raceHubRegistry.Shutdown()`
  to close every local connection. `Shutdown(ctx)`'s own wait then
  returns promptly instead of blocking for the full grace period.

## Verification

- `go test ./... -race` — this spec touches real concurrency-adjacent code
  (signal handling, a new `raceHubRegistry.Shutdown()` closing connections
  concurrently with `run`'s own loop), so this project's normal
  concurrency-testing bar applies here, not just to manifest-only specs.
- Manual: start each binary directly (`go run ./cmd/server`, etc.),
  `kill -TERM <pid>`, confirm the process exits within the configured
  budget and logs show the expected sequence (readiness flips first,
  in-flight work finishes or is drained, process exits cleanly — not a
  bare unexplained exit).
- For `cmd/server` specifically: start a race, send `SIGTERM` mid-race,
  confirm the race is allowed to finish (or reach its own natural
  grace-period-expiry end) rather than being cut off immediately —
  the concrete behavior the "let in-progress races finish naturally"
  decision above commits to.
- For `cmd/ws-gateway` specifically: open a WebSocket connection, send
  `SIGTERM`, confirm the connection receives a clean close frame (not a
  silent TCP drop) within the flush window, not the full grace period.

## Notes

- This spec is a hard prerequisite for `k8s-race-service-deploy.md`'s and
  `k8s-ws-gateway-deploy.md`'s `terminationGracePeriodSeconds` and
  `readinessProbe`/`livenessProbe` fields to mean anything real — those
  specs reference the exact endpoints and timing decided here rather than
  re-deriving them.
- The room-actor product decision above ("let in-progress races finish
  naturally") is a real, disclosed product choice, not a default — if it
  turns out wrong in practice once `multi-instance-k8s-verification.md`
  runs a real rolling update against a live race, revisit it there and
  update this file, per this project's own convention for design
  decisions that don't survive contact with a real run.
