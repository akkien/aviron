# Multi-Instance Local Dev Setup & Verification

The actual acceptance test for `redis-room-registry.md` and
`race-router.md` — see
`context/features/phase4/horizontal-scaling/multi-instance-dev-setup.md`
for the full spec and design rationale. Two real `cmd/server` processes
plus a real `cmd/race-router` in front of them, proving a client that only
ever talks to the router reaches the correct owning instance regardless of
which backend process actually owns a given room.

This is infrastructure verification, not application load — see
`load/README.md` for the separate k6 load test.

## Prerequisites

- Docker running (Postgres + Redis via `docker compose`).
- Ports `8080`, `8081`, `9090`, `5432`, `6379` free on your machine.
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
`cmd/server` processes plus `cmd/race-router` as background OS processes,
runs the full verification, kills one of those processes on purpose
(step 10 below), and tears everything down at the end. Don't run it
against anything other than local dev Docker.

## What it does

1. Builds `cmd/server`/`cmd/race-router` into real binaries (not `go run`
   — see "Corrections" below) and starts instance A (`:8080`), instance B
   (`:8081`), and the router (`:9090`) against the shared Postgres/Redis.
2. Registers two users, creates a race, and joins the second user — **all
   through the router**, never talking to `:8080`/`:8081` directly.
3. Confirms which instance actually owns the race two independent ways: a
   direct `redis-cli GET room:<id>`, and grepping that instance's own
   structured logs for the `race_id` — not just "it seemed to work."
4. Opens both players' WebSocket connections through the router, starts
   the race while they're already connected (confirming they receive
   `race_started` live), streams telemetry, and confirms both receive
   matching `race_finished` results.
5. Repeats steps 2-4 several times (`REPEAT_RUNS`, default 6) until both
   "instance A owns it" and "instance B owns it" have occurred at least
   once — ruling out a routing bug that only happens to work for one
   specific instance.
6. Runs one more race, then **kills the owning instance's process
   mid-race** on purpose, and confirms a fresh reconnect attempt through
   the router eventually fails cleanly (not hangs) once the registry's
   ownership TTL and the router's cache TTL both lapse. This confirms a
   **documented, accepted limitation** behaves as predicted
   (`race-router.md`'s Notes: an owning instance dying mid-race is
   unrecoverable) — a pass here means "the failure mode is exactly what's
   already written down," not "this is now fixed."

Logs for all three processes and every k6 run are kept in a temp directory
printed at the end of the run.

## Corrections to the spec's own example commands

`multi-instance-dev-setup.md`'s own example commands have two real bugs
against code this project already shipped — recorded here so nobody
copies them again:

```sh
# Wrong — INSTANCE_ID must be this instance's own reachable host:port
# (race-router.md's Director uses Owner()'s raw return value directly as
# req.URL.Host), and race-router reads RACE_ROUTER_LISTEN_ADDR, not PORT.
INSTANCE_ID=a PORT=8080 go run ./cmd/server
INSTANCE_ID=b PORT=8081 go run ./cmd/server
RACE_SERVICE_INSTANCES=localhost:8080,localhost:8081 \
  PORT=9090 go run ./cmd/race-router
```

Correct, for manual/ad-hoc testing outside this script:

```sh
INSTANCE_ID=localhost:8080 PORT=8080 go run ./cmd/server
INSTANCE_ID=localhost:8081 PORT=8081 go run ./cmd/server
RACE_SERVICE_INSTANCES=localhost:8080,localhost:8081 \
  RACE_ROUTER_LISTEN_ADDR=:9090 go run ./cmd/race-router
```

A third correction, specific to this script (not the spec's example
commands, which are fine for manual testing where you kill things by hand
with `Ctrl+C`): `multi-instance-check.sh` builds real binaries and runs
those directly rather than `go run`, because `go run` execs the compiled
program as a *child* process, and killing `go run`'s own PID doesn't
reliably kill that child — step 10's entire point is actually killing the
owning instance, so it needs a directly-killable real process.

## Real bugs this script's own first live runs caught

Beyond the three corrections above, getting this script to actually pass
caught three more real bugs — recorded here since they're exactly the kind
of thing this spec's own Notes warn about ("do not skip ahead to
Kubernetes until every step here passes cleanly" — this is what "cleanly"
actually took):

1. **A silent orphaned-process bug, not just a slow one.** The first
   attempt backgrounded each instance as `(cd backend && ... "$BIN") &`
   and captured `$!` as its PID. This is unreliable: depending on how bash
   handles the final command in a subshell, `$!` isn't guaranteed to be
   the *actual* server binary's PID — confirmed the hard way when
   `kill "$PID_B"` reported success but the real `server` process survived
   as an orphan (`PPID 1`), completely undisturbed. This made step 10's
   "kill mid-race" test look like it passed for the wrong reason: the k6
   run finished cleanly not because a reconnect was correctly handled, but
   because the "killed" instance was never actually killed. Fixed by
   adding `exec` inside each subshell (`exec env VAR=val "$BIN"`), which
   forces the subshell to replace itself with the server binary in place —
   no fork, no ambiguity.
2. **The ownership log-grep assertion was checking for something that
   never gets logged under normal operation.** It originally grepped for a
   `"race_id":"<id>"` JSON key, which only appears on a room actor's
   *error*-path log lines (a failed `Claim`/`Refresh`/`Release`) — never
   on a healthy run. Every single run failed this assertion, 100% of the
   time, without the overall check ever being wrong. Fixed by grepping for
   the race ID as it actually appears in a healthy run: inside the
   `path` field of the `RequestLog` middleware's `http_request` access-log
   line (e.g. `"path":"/races/<id>/join"`).
3. **That log-grep bug's failure never actually stopped the script**,
   which is a second, independent bug: macOS's default `/bin/bash` is
   3.2.57 (frozen pre-GPLv3, no `inherit_errexit` — that option didn't
   exist until bash 4.4), so a failing bare statement inside a function
   whose *own* invocation is captured via `$(...)` — like
   `run_full_lifecycle_check`, called as `owner="$(run_full_lifecycle_check
   ...)"` — does not abort that function the way `set -e` would suggest.
   Fixed by checking that assertion's result with an explicit `if`, not a
   bare statement, so it doesn't depend on `errexit` propagation semantics
   that vary by bash version.

With all three fixed, two full back-to-back runs (fresh
`docker compose down -v && up -d` between them, per this spec's own "done"
bar) both passed cleanly end to end, including a kill-test race long
enough (60 words, `KILL_TEST_DISTANCE_METERS`) that the kill unambiguously
lands mid-race rather than possibly racing a fast-finishing short one.

## Scale knobs

| Variable | Default | Meaning |
| --- | --- | --- |
| `REPEAT_RUNS` | `6` | How many times to repeat the full lifecycle check (step 9) before moving on to the kill test |

## Troubleshooting

- **A step fails immediately**: check the logs in the printed temp
  directory (`instance-a.log`, `instance-b.log`, `router.log`,
  `k6-run-*.log`) to see which of the three processes is responsible.
- **The script exits immediately with "port N is already in use"**: the
  script checks `8080`/`8081`/`9090` up front and refuses to start if
  anything already has them bound — this actually happened during this
  tool's own first real run: a forgotten `make start` server from an
  earlier session was still listening on `:8080`, silently answering
  `/healthz` for the *old* process while this script's own instance A had
  already crashed, masking the real failure entirely. Stop whatever's
  using the port first (`lsof -i :8080`, or `make stop` from `backend/`
  for a forgotten `make start` server) and re-run.
- **The script hangs (or fails) on `wait_for_http` after that check
  already passed**: something didn't start — check the relevant log file
  directly.
- **Step 10 doesn't reproduce a failure** (the reconnect attempt
  unexpectedly succeeds): this means either the kill didn't actually reach
  the owning instance's real process (check `instance-a.log`/
  `instance-b.log` timestamps against the kill time), or something has
  changed about `race-router.md`/`redis-room-registry.md`'s design — per
  this spec's own Notes, fix that spec's file, don't just patch around it
  here.
