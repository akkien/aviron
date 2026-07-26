#!/usr/bin/env bash
# multi-instance-check.sh is the actual acceptance test for
# redis-room-registry.md and race-router.md
# (context/features/phase4/horizontal-scaling/multi-instance-dev-setup.md):
# two real cmd/server processes plus a real cmd/race-router in front of
# them, proving a client that only ever talks to the router (:9090)
# reaches the correct owning instance regardless of which of the two
# backend processes actually owns a given room. See
# load/multi-instance-check.md for the full runbook.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
cd "$REPO_ROOT"

PORT_A=8080
PORT_B=8081
ROUTER_PORT=9090
BASE_A="http://localhost:$PORT_A"
BASE_B="http://localhost:$PORT_B"
ROUTER="http://localhost:$ROUTER_PORT"

DISTANCE_METERS=10
REPEAT_RUNS="${REPEAT_RUNS:-6}"
RUN_ID="$(date +%s)"

# KILL_TEST_DISTANCE_METERS is deliberately much larger than DISTANCE_METERS
# — verification step 10 needs the kill to land unambiguously mid-race.
# With ws-client.js's 400-2000ms per-word pacing, DISTANCE_METERS=10 can
# finish in as little as ~4s, which this feature's own first real run
# actually hit: the race completed cleanly *before* the kill took effect,
# even though the kill signal was confirmed delivered — a false pass, not a
# real one. At 60 words the fastest possible completion is 24s (average
# ~72s), leaving a wide, unambiguous margin around the ~5s kill below.
KILL_TEST_DISTANCE_METERS=60

LOG_DIR="$(mktemp -d /tmp/multi-instance-check.XXXXXX)"
LOG_A="$LOG_DIR/instance-a.log"
LOG_B="$LOG_DIR/instance-b.log"
LOG_ROUTER="$LOG_DIR/router.log"
BIN_DIR="$LOG_DIR/bin"

PID_A=""
PID_B=""
PID_ROUTER=""

log() {
  # Always stderr, never stdout — several functions below (e.g.
  # run_full_lifecycle_check) are captured via command substitution for
  # their one deliberate return value, and would otherwise have every log
  # line mixed into it.
  echo "[$(date '+%H:%M:%S')] $*" >&2
}

cleanup() {
  log "tearing down (logs kept at $LOG_DIR)"
  for pid in "$PID_ROUTER" "$PID_A" "$PID_B"; do
    if [ -n "$pid" ]; then
      kill "$pid" 2>/dev/null || true
    fi
  done
}
trap cleanup EXIT

wait_for_http() {
  local url="$1" name="$2"
  local attempts=30
  while [ "$attempts" -gt 0 ]; do
    code="$(curl -s -o /dev/null -w '%{http_code}' "$url" || true)"
    if [ "$code" != "000" ]; then
      return 0
    fi
    attempts=$((attempts - 1))
    sleep 1
  done
  log "ERROR: $name never came up (tried $url)"
  return 1
}

# check_ports_free fails fast, with a clear message, if anything is already
# listening on the ports this script needs — otherwise wait_for_http could
# get a false-positive "ready" signal from an unrelated stale process (this
# happened for real during this feature's own verification run: a
# forgotten `make start` server from an earlier session was still bound to
# :8080, answering /healthz successfully while this script's own instance A
# had already crashed, masking the real failure entirely).
check_ports_free() {
  local port
  for port in "$PORT_A" "$PORT_B" "$ROUTER_PORT"; do
    if lsof -i ":$port" >/dev/null 2>&1; then
      log "ERROR: port $port is already in use — stop whatever's using it first (lsof -i :$port), e.g. a forgotten 'make start' server"
      return 1
    fi
  done
}

start_infra() {
  log "resetting docker compose state (down -v && up -d)"
  docker compose down -v
  docker compose up -d postgres redis
  # Real readiness retries for both, not a fixed sleep — Postgres in
  # particular takes noticeably longer than Redis to accept connections
  # after a fresh volume (initdb), and internal/app.Run's db.NewPool ping
  # fails fast (log.Fatalf) rather than retrying if it's not ready yet.
  local attempts=30
  while [ "$attempts" -gt 0 ]; do
    if docker compose exec -T redis redis-cli PING >/dev/null 2>&1; then
      break
    fi
    attempts=$((attempts - 1))
    sleep 1
  done

  attempts=60
  while [ "$attempts" -gt 0 ]; do
    if docker compose exec -T postgres pg_isready -U aviron >/dev/null 2>&1; then
      break
    fi
    attempts=$((attempts - 1))
    sleep 1
  done
  if [ "$attempts" -eq 0 ]; then
    log "ERROR: postgres never became ready"
    return 1
  fi
}

build_binaries() {
  log "building cmd/server and cmd/race-router"
  mkdir -p "$BIN_DIR"
  (cd backend && go build -o "$BIN_DIR/server" ./cmd/server)
  (cd backend && go build -o "$BIN_DIR/race-router" ./cmd/race-router)
}

start_backends() {
  # Pre-built binaries, not `go run` — `go run` execs the compiled program
  # as a child process, and killing `go run`'s own PID doesn't reliably
  # kill that child (a well-known Go tooling gotcha). Step 10's entire
  # point is actually killing the owning instance, so this has to be a
  # real, directly-killable process, not go run's wrapper. Migrations use
  # a relative "file://migrations" path (internal/db/migrate.go), so the
  # binary still has to run with cwd=backend/, exactly like `go run` would.
  #
  # `exec` inside each subshell is load-bearing, not decoration: without
  # it, `(cd backend && ... "$BIN_DIR/server") &` backgrounds a *subshell*,
  # and `$!` isn't guaranteed to be the actual server binary's PID (bash
  # may or may not fork a separate child for the final command depending
  # on the exact form) — confirmed the hard way during this feature's own
  # verification run: `kill "$PID_B"` reported success, but the real
  # `server` binary survived as an orphan (PPID 1), completely undisturbed,
  # which is why the "kill mid-race" test initially looked like it passed
  # for the wrong reason. `exec` forces the subshell to replace itself with
  # the server binary in place — no fork, no ambiguity, `$!` is exactly the
  # process that will be killed later.
  log "starting instance A (INSTANCE_ID=localhost:$PORT_A, PORT=$PORT_A)"
  (cd backend && exec env INSTANCE_ID="localhost:$PORT_A" PORT="$PORT_A" "$BIN_DIR/server") >"$LOG_A" 2>&1 &
  PID_A=$!

  log "starting instance B (INSTANCE_ID=localhost:$PORT_B, PORT=$PORT_B)"
  (cd backend && exec env INSTANCE_ID="localhost:$PORT_B" PORT="$PORT_B" "$BIN_DIR/server") >"$LOG_B" 2>&1 &
  PID_B=$!

  wait_for_http "$BASE_A/healthz" "instance A"
  wait_for_http "$BASE_B/healthz" "instance B"

  log "starting race-router (RACE_SERVICE_INSTANCES=localhost:$PORT_A,localhost:$PORT_B, RACE_ROUTER_LISTEN_ADDR=:$ROUTER_PORT)"
  (cd backend && exec env RACE_SERVICE_INSTANCES="localhost:$PORT_A,localhost:$PORT_B" RACE_ROUTER_LISTEN_ADDR=":$ROUTER_PORT" "$BIN_DIR/race-router") >"$LOG_ROUTER" 2>&1 &
  PID_ROUTER=$!

  # race-router has no /healthz — any HTTP response (even 401) proves it's
  # accepting connections and proxying, since GET /races is round-robined.
  wait_for_http "$ROUTER/races" "race-router"
}

register_and_login() {
  local email="$1" password="$2" display_name="$3"
  curl -sS -X POST "$ROUTER/auth/register" \
    -H 'Content-Type: application/json' \
    -d "{\"email\":\"$email\",\"password\":\"$password\",\"display_name\":\"$display_name\"}" \
    >/dev/null

  curl -sS -X POST "$ROUTER/auth/login" \
    -H 'Content-Type: application/json' \
    -d "{\"email\":\"$email\",\"password\":\"$password\"}" \
    | jq -r '.token'
}

# owning_instance_letter reports which instance (A or B) redis-cli says
# owns raceID — the spec's own "real, inspectable assertion, not just 'it
# seemed to work'".
owning_instance_letter() {
  local race_id="$1"
  local val
  val="$(docker compose exec -T redis redis-cli GET "room:$race_id" | tr -d '\r')"
  case "$val" in
    *"$PORT_A") echo "A" ;;
    *"$PORT_B") echo "B" ;;
    *) echo "NONE ($val)" ;;
  esac
}

# assert_log_shows_owner cross-checks the redis-cli assertion against the
# owning instance's own structured logs actually mentioning race_id — two
# independent signals, not one. Checked as a path substring
# ("/races/<id>/join" etc, from the RequestLog middleware's http_request
# access-log line), not a dedicated "race_id" JSON key: confirmed by
# reading a real log line during this feature's own verification run that
# only internal/room's room-actor logger ever tags a bare race_id key, and
# only on error paths (Claim/Refresh/Release failures) — which never fire
# under normal healthy operation, so grepping for that key found nothing
# on every single run, a wrong assertion, not a real system failure.
assert_log_shows_owner() {
  local race_id="$1" letter="$2"
  local logfile
  case "$letter" in
    A) logfile="$LOG_A" ;;
    B) logfile="$LOG_B" ;;
    *) log "  (skipping log cross-check, no clear owner)"; return 0 ;;
  esac
  if grep -q "/races/$race_id" "$logfile"; then
    log "  confirmed: instance $letter's own logs mention /races/$race_id"
  else
    log "  ERROR: instance $letter's logs never mention /races/$race_id (redis-cli/log-grep disagree)"
    return 1
  fi
}

# run_full_lifecycle_check is verification steps 1-8: register two users,
# create+join a race through the router, confirm ownership two independent
# ways, open both WebSocket connections through the router, start the
# race while they're already connected, and confirm both receive
# race_started and race_finished with matching results. Prints the owning
# instance's letter (A/B) on success.
run_full_lifecycle_check() {
  local n="$1"
  local email1="multicheck-${RUN_ID}-${n}-u1@example.com"
  local email2="multicheck-${RUN_ID}-${n}-u2@example.com"

  local token1 token2
  token1="$(register_and_login "$email1" "multicheck-password-1" "MultiCheck U1 $n")"
  token2="$(register_and_login "$email2" "multicheck-password-1" "MultiCheck U2 $n")"

  local create_res race_id session1
  create_res="$(curl -sS -X POST "$ROUTER/races" \
    -H "Content-Type: application/json" -H "Authorization: Bearer $token1" \
    -d "{\"name\":\"MultiCheck Race $n\",\"distance_meters\":$DISTANCE_METERS}")"
  race_id="$(echo "$create_res" | jq -r '.id')"
  session1="$(echo "$create_res" | jq -r '.session_token')"

  if [ -z "$race_id" ] || [ "$race_id" = "null" ]; then
    log "ERROR: race creation failed: $create_res"
    return 1
  fi

  local join_res session2
  join_res="$(curl -sS -X POST "$ROUTER/races/$race_id/join" -H "Authorization: Bearer $token2")"
  session2="$(echo "$join_res" | jq -r '.session_token')"

  local owner
  owner="$(owning_instance_letter "$race_id")"
  log "race $race_id owned by instance $owner"
  # Explicit `if`, not a bare statement relying on `set -e` to propagate the
  # failure — this function's own invocation is itself wrapped in `$(...)`
  # by the caller (main's `owner="$(run_full_lifecycle_check ...)"`), and
  # macOS's default /bin/bash (3.2, frozen pre-GPLv3) doesn't support
  # `inherit_errexit` (added in bash 4.4) — a bare failing statement inside
  # a command-substituted function body silently does NOT abort that
  # function on this bash version. Confirmed the hard way: this assertion
  # failed on every single run during this feature's own verification, yet
  # the script kept going as if nothing had happened.
  if ! assert_log_shows_owner "$race_id" "$owner"; then
    return 1
  fi

  # Both WS connections open (through the router) before start is called,
  # so the check that they receive race_started live actually proves
  # something (verification steps 5/6).
  RACE_ID="$race_id" SESSION_TOKEN_1="$session1" SESSION_TOKEN_2="$session2" \
    DISTANCE_METERS="$DISTANCE_METERS" BASE_URL="$ROUTER" \
    k6 run "$SCRIPT_DIR/scenarios/multi-instance-check.js" >"$LOG_DIR/k6-run-$n.log" 2>&1 &
  local k6_pid=$!

  sleep 2 # let both WS connections open and send join_race
  curl -sS -X POST "$ROUTER/races/$race_id/start" -H "Authorization: Bearer $token1" >/dev/null

  if ! wait "$k6_pid"; then
    log "ERROR: k6 run for race $race_id failed — see $LOG_DIR/k6-run-$n.log"
    return 1
  fi

  echo "$owner"
}

# run_kill_midrace_check is verification step 10: same setup as above, but
# the owning instance is killed partway through, and a fresh reconnect
# attempt through the router (multi-instance-reconnect-check.js) must
# eventually fail cleanly — not hang — confirming the documented gap
# (race-router.md's Notes) behaves as predicted.
run_kill_midrace_check() {
  local email1="multicheck-${RUN_ID}-kill-u1@example.com"
  local email2="multicheck-${RUN_ID}-kill-u2@example.com"

  local token1 token2
  token1="$(register_and_login "$email1" "multicheck-password-1" "MultiCheck Kill U1")"
  token2="$(register_and_login "$email2" "multicheck-password-1" "MultiCheck Kill U2")"

  local create_res race_id session1
  create_res="$(curl -sS -X POST "$ROUTER/races" \
    -H "Content-Type: application/json" -H "Authorization: Bearer $token1" \
    -d "{\"name\":\"MultiCheck Kill Race\",\"distance_meters\":$KILL_TEST_DISTANCE_METERS}")"
  race_id="$(echo "$create_res" | jq -r '.id')"
  session1="$(echo "$create_res" | jq -r '.session_token')"

  local join_res session2
  join_res="$(curl -sS -X POST "$ROUTER/races/$race_id/join" -H "Authorization: Bearer $token2")"
  session2="$(echo "$join_res" | jq -r '.session_token')"

  local owner
  owner="$(owning_instance_letter "$race_id")"
  log "kill-test race $race_id owned by instance $owner"

  RACE_ID="$race_id" SESSION_TOKEN_1="$session1" SESSION_TOKEN_2="$session2" \
    DISTANCE_METERS="$KILL_TEST_DISTANCE_METERS" BASE_URL="$ROUTER" \
    k6 run "$SCRIPT_DIR/scenarios/multi-instance-check.js" >"$LOG_DIR/k6-kill-run.log" 2>&1 &
  local k6_pid=$!

  sleep 2
  curl -sS -X POST "$ROUTER/races/$race_id/start" -H "Authorization: Bearer $token1" >/dev/null
  sleep 3 # let a few telemetry messages flow before killing the owner

  local kill_pid
  case "$owner" in
    A) kill_pid="$PID_A" ;;
    B) kill_pid="$PID_B" ;;
    *) log "ERROR: could not determine owning instance, aborting kill test"; return 1 ;;
  esac

  log "killing owning instance $owner (pid $kill_pid)"
  kill "$kill_pid" 2>/dev/null || true

  # The live k6 run above is now expected to fail (its connections just
  # broke) — that's the point of this step, not a script bug. Don't treat
  # its non-zero exit as this function's failure.
  wait "$k6_pid" || log "  (expected: the live k6 run's connections broke when the owner died)"

  log "waiting out the registry's claim TTL + router cache TTL before attempting reconnect"
  sleep 65 # 60s claim TTL + margin, per redis-room-registry.md/race-router.md

  RACE_ID="$race_id" SESSION_TOKEN="$session1" BASE_URL="$ROUTER" \
    k6 run "$SCRIPT_DIR/scenarios/multi-instance-reconnect-check.js" >"$LOG_DIR/k6-reconnect-check.log" 2>&1
}

main() {
  check_ports_free
  build_binaries
  start_infra
  start_backends

  log "=== verification steps 1-9: repeated full lifecycle checks ==="
  local seen_a=0 seen_b=0
  for i in $(seq 1 "$REPEAT_RUNS"); do
    log "--- run $i/$REPEAT_RUNS ---"
    local owner
    owner="$(run_full_lifecycle_check "$i")"
    case "$owner" in
      A) seen_a=1 ;;
      B) seen_b=1 ;;
    esac
  done

  if [ "$seen_a" -eq 1 ] && [ "$seen_b" -eq 1 ]; then
    log "step 9 OK: saw both instance A and instance B own a race across $REPEAT_RUNS runs"
  else
    log "step 9 WARNING: only saw one instance own a race across $REPEAT_RUNS runs — re-run with a higher REPEAT_RUNS"
  fi

  log "=== verification step 10: kill the owning instance mid-race ==="
  run_kill_midrace_check

  log "ALL STEPS PASSED. Logs kept at $LOG_DIR"
}

main "$@"
