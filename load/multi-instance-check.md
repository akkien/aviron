# Multi-Instance Local Dev Setup & Verification

The real acceptance test for `redis-room-registry.md`,
`room-message-bus.md`, `room-service-adapter.md`, and `ws-gateway.md`
together — see
`context/features/phase4/horizontal-scaling/multi-instance-dev-setup.md`
for the full spec and design rationale. Two real `cmd/server` processes,
**two** real `cmd/ws-gateway` processes in front of them, and a real NATS
instance, proving that two participants of the *same* race, connected
through *two different* gateways, to a room owned by either backend, still
see fully consistent, correctly-ordered real-time state — because the bus
carried it correctly, not because their sockets happened to share a
process. This is a genuinely harder property than the previous
(`race-router`) version of this script proved: under `race-router`, a
client's WS connection *was* proxied straight through to the owning
instance, so correctness inside a room was trivially local. Under this
revision, every WS connection terminates locally at whichever gateway
accepted it, with zero relationship to which `race-service` instance owns
the room — the bus is the only thing keeping two participants on different
gateways in sync.

This is infrastructure verification, not application load — see
`load/README.md` for the separate k6 load test.

## Topology

```text
server-A (:8080)   server-B (:8081)      -- race-service, unchanged
gateway-1 (:9090)  gateway-2 (:9091)     -- two ws-gateway instances, each
                                             RACE_SERVICE_INSTANCES=
                                             localhost:8080,localhost:8081
nats (:4222)                              -- message bus (room-message-bus.md);
                                             server-A/B and both gateways all
                                             point NATS_URL at it
```

The two participants of every race this script creates are deliberately
registered, and connect their WebSocket, through **different** gateways —
the creator through gateway-1, the joiner through gateway-2 — the exact
scenario that actually exercises the bus rather than a single process's
local memory.

## Prerequisites

- Docker running (Postgres + Redis + NATS via `docker compose`).
- Ports `8080`, `8081`, `9090`, `9091`, `5432`, `6379`, `4222` free on your
  machine.
- `jq` installed (`brew install jq` on macOS).
- The [k6](https://k6.io/) binary installed (`brew install k6`) — used for
  the WebSocket legs of the check.

## Running it

From the repo root:

```sh
bash load/multi-instance-check.sh
```

This is a **live** script: it resets your local Docker state
(`docker compose down -v && up -d`), builds and starts two real
`cmd/server` processes plus two real `cmd/ws-gateway` processes as
background OS processes, runs the full verification, kills one of the
`cmd/server` processes on purpose (the kill test below), and tears
everything down at the end. Don't run it against anything other than local
dev Docker.

## What it does

1. Builds `cmd/server`/`cmd/ws-gateway` into real binaries (not `go run` —
   see "Corrections" below) and starts instance A (`:8080`), instance B
   (`:8081`), gateway-1 (`:9090`), and gateway-2 (`:9091`) against the
   shared Postgres/Redis/NATS.
2. Registers two users, creates a race, and joins the second user —
   **through two different gateways** (creator via gateway-1, joiner via
   gateway-2), never talking to `:8080`/`:8081` directly.
3. Confirms which instance actually owns the race two independent ways: a
   direct `redis-cli GET room:<id>`, and grepping that instance's own
   structured logs for the `race_id` — not just "it seemed to work."
4. **New assertion this revision needed that the previous version didn't**:
   confirms the bus itself actually carried this race's traffic — grepping
   the owning instance's log for a `roombus: published` line and each
   gateway's log for a `wsgateway: received` line, all tagged with the
   race's ID. Without this, a bug that silently drops every relay message
   could still pass step 4/5 below on a small enough test race, papered
   over by the client's own retry/reconnect behavior.
5. Opens both players' WebSocket connections through their own respective
   gateway, starts the race while they're already connected (confirming
   they receive `race_started` live), streams telemetry, and confirms both
   receive matching `race_finished` results.
6. Repeats steps 2-5 several times (`REPEAT_RUNS`, default 6) until both
   "instance A owns it" and "instance B owns it" have occurred at least
   once — ruling out a routing bug that only happens to work for one
   specific instance.
7. Runs one more race, then **kills the owning `race-service` instance's
   process mid-race** on purpose, and records — rather than asserts a
   predetermined pass/fail for — what actually happens to the live k6 run
   and to a fresh reconnect attempt afterward. See "What the kill test
   actually proves now" below: this revision's failure mode when the owner
   dies is expected to differ materially from the previous version's.

Logs for all four processes and every k6 run are kept in a temp directory
printed at the end of the run.

## What the kill test actually proves now

Under the previous (`race-router`) version, the WS connection itself was
proxied raw through the router, so killing the owning instance broke the
actual proxied TCP socket immediately — fast, unambiguous, and that's
exactly what that version's kill test checked for ("a fresh reconnect
attempt... must eventually fail cleanly — not hang").

Under this revision, `GET /ws` terminates locally at whichever gateway
accepted it and never touches the owning instance's socket at all — a
gateway's `raceHub` just sits subscribed to `room.{race_id}.out`, and the
only observable effect of the owner dying is that subject simply stops
receiving publishes. Nothing in this phase's design gives a `raceHub` any
way to notice "the room's actual owner died"; it has no fallback timeout
of its own. This is a documented, accepted gap in this phase's design, not
a bug in this script.

**Confirmed by this script's own first live run against the real
two-gateway topology, not just predicted:** the live k6 run's two WS
sessions stayed open the entire time (`ws_sessions: 2`, both still
connected) and never received `race_finished` — it ran to its own full
2-minute `maxDuration` (`iteration_duration: avg=2m0s`) rather than
failing fast, exactly the silent-hang behavior predicted above. A fresh
reconnect attempt made *after* the registry's ~60s claim TTL had lapsed
then failed cleanly in about 4 seconds (`reconnect eventually fails after
the owning instance dies (documented gap)` — passed), the same fast-clean
failure the previous version's kill test already established, once the
registry itself no longer reports a (stale) owner.

## Corrections to the spec's own example commands

`multi-instance-dev-setup.md`'s own example commands are fine for
manual/ad-hoc testing outside this script, where you kill things by hand
with `Ctrl+C`:

```sh
INSTANCE_ID=localhost:8080 PORT=8080 go run ./cmd/server
INSTANCE_ID=localhost:8081 PORT=8081 go run ./cmd/server
RACE_SERVICE_INSTANCES=localhost:8080,localhost:8081 \
  WS_GATEWAY_LISTEN_ADDR=:9090 go run ./cmd/ws-gateway
RACE_SERVICE_INSTANCES=localhost:8080,localhost:8081 \
  WS_GATEWAY_LISTEN_ADDR=:9091 go run ./cmd/ws-gateway
```

A correction specific to this script (not the commands above): this script
builds real binaries and runs those directly rather than `go run`, because
`go run` execs the compiled program as a *child* process, and killing
`go run`'s own PID doesn't reliably kill that child — the kill test's
entire point is actually killing the owning instance, so it needs a
directly-killable real process.

## Real bugs this script's own first live runs caught

The previous (`race-router`) version of this script already caught three
real bugs during its own first live runs, recorded here since they're
topology-independent and still apply unchanged to this version:

1. **A silent orphaned-process bug, not just a slow one.** Backgrounding
   each instance as `(cd backend && ... "$BIN") &` and capturing `$!` as
   its PID is unreliable — depending on how bash handles the final command
   in a subshell, `$!` isn't guaranteed to be the *actual* server binary's
   PID. Fixed by adding `exec` inside each subshell, which forces the
   subshell to replace itself with the server binary in place — no fork,
   no ambiguity.
2. **The ownership log-grep assertion was checking for something that
   never gets logged under normal operation.** It originally grepped for a
   `"race_id":"<id>"` JSON key, which only appears on a room actor's
   *error*-path log lines — never on a healthy run. Fixed by grepping for
   the race ID as it actually appears in a healthy run: inside the `path`
   field of the `RequestLog` middleware's `http_request` access-log line.
3. **That log-grep bug's failure never actually stopped the script**,
   because macOS's default `/bin/bash` (3.2.57) has no `inherit_errexit` —
   a failing bare statement inside a function whose *own* invocation is
   captured via `$(...)` does not abort that function the way `set -e`
   would suggest. Fixed by checking that assertion's result with an
   explicit `if`, not a bare statement.

This revision's own first live runs against the real two-gateway topology
caught two more real bugs, neither anticipated by the spec that introduced
this rewrite:

1. **`docker-compose.yml`'s `nats` service used the plain `nats:latest`
   image, which is a distroless-style build containing only the
   `nats-server` binary** — no shell, no `wget`, nothing `docker compose
   exec` or a `CMD`-exec healthcheck can run against it. This meant the
   compose file's own pre-existing `nats` healthcheck could never actually
   pass (`docker compose ps` showed it permanently `unhealthy`), which
   would also have silently hung any service depending on
   `condition: service_healthy` for it (`ws-gateway`, under a real
   `docker compose up`) — and it broke this script's own NATS readiness
   wait outright, which tried the same `docker compose exec ... wget`
   approach the existing Redis/Postgres waits already used. Fixed by
   switching to `nats:2-alpine` (confirmed to have a real `sh`/`wget`),
   same `HEALTHCHECK` definition otherwise unchanged.
2. **A real concurrency bug in `internal/room/registry.go`'s
   `drainBroadcast`**, exposed by the very first successful two-gateway
   lifecycle run: both k6 VUs received `race_started` but neither ever
   received `race_finished`. `finishRace` sends the final broadcast onto a
   buffered channel and calls `r.cancel()` immediately after, without
   blocking — so `drainBroadcast`'s `select` could see both the buffered
   message and `ctx.Done()` ready at the same instant, and `select` does
   not pick in send order. A previous fix already protected messages
   consumed via the `ctx.Done()` branch (uses `context.Background()`
   there), but the main branch — `case msg := <-broadcast:
   PublishOut(ctx, ...)` — still used the original, possibly-already-
   cancelled `ctx`, so the real `roomrelay.Bus.PublishOut`'s own
   `ctx.Err()` check silently dropped the final `race_finished` broadcast
   on this run. Fixed by using `context.Background()` in that branch too.
   See `current-feature.md`'s Explain for the full trace, including how the
   existing regression test for the *previous* `drainBroadcast` bug
   (`TestRegistry_Spawn_DrainsBroadcastBeforePublishingRoomClosed`)
   couldn't have caught this one — its fake bus ignored the `ctx` argument
   entirely — and how it was strengthened to actually assert on it.

With both fixed, a full run (`REPEAT_RUNS=6` plus the kill test) passed
cleanly end to end: both instances seen as owner, both gateways confirmed
relaying via the new bus-traffic assertion on every run, and the kill
test's outcome matched the prediction in "What the kill test actually
proves now" above exactly — the live run hit its full 2-minute
`maxDuration` rather than failing fast, and the post-TTL reconnect attempt
failed cleanly in about 4 seconds.

## Scale knobs

| Variable | Default | Meaning |
| --- | --- | --- |
| `REPEAT_RUNS` | `6` | How many times to repeat the full lifecycle check before moving on to the kill test |

## Troubleshooting

- **A step fails immediately**: check the logs in the printed temp
  directory (`instance-a.log`, `instance-b.log`, `gateway-1.log`,
  `gateway-2.log`, `k6-run-*.log`) to see which of the four processes is
  responsible.
- **The script exits immediately with "port N is already in use"**: the
  script checks `8080`/`8081`/`9090`/`9091` up front and refuses to start
  if anything already has them bound. Stop whatever's using the port first
  (`lsof -i :8080`, or `make stop` from `backend/` for a forgotten
  `make start` server) and re-run.
- **The script hangs (or fails) on `wait_for_http` after that check
  already passed**: something didn't start — check the relevant log file
  directly. `gateway-1`/`gateway-2`'s `/healthz` returns `503` (not just a
  connection failure) if it can't reach Redis or NATS — check
  `docker compose ps` if that's what's failing.
- **The bus-traffic assertion fails** (`roombus: published`/
  `wsgateway: received` not found for a race ID): check that the four Info
  log lines this revision added
  (`internal/wsgateway/endpoint.go`/`racehub.go`,
  `internal/roombus/adapter.go`) are still present — a future refactor of
  the bus's message path could silently remove them without breaking any
  unit test, since they're pure logging with no behavioral effect.
- **The live k6 run hangs for close to 2 minutes after the kill step**:
  this is the *expected*, documented behavior under this revision — see
  "What the kill test actually proves now" above — not a bug in this
  script.
- **A fresh reconnect attempt after the kill unexpectedly succeeds and
  stays healthy** (not just "opens and then hangs"): this would mean
  something has changed about `room-message-bus.md`/`ws-gateway.md`'s
  design — per this project's own convention, fix that spec's file, don't
  just patch around it here.

## For manual bus-traffic inspection

Beyond this script's own log-grep assertion, a raw NATS subject tap is
useful for manual first-run debugging (not scripted here, since it needs a
local tool this script doesn't otherwise depend on):

```sh
brew tap nats-io/nats-tools
brew install nats
nats sub "room.>" --server nats://localhost:4222
```

Run it alongside the script (or a manual `go run ./cmd/server`/
`go run ./cmd/ws-gateway` session) to watch every race's `in`/`out` traffic
in one readable stream.
