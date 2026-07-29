#!/usr/bin/env bash
# multi-instance-k8s-check.sh is the real acceptance test for Phase 5
# (context/features/phase5/multi-instance-k8s-verification.md): everything
# in k8s-core-infra.md, graceful-shutdown.md, k8s-race-service-deploy.md,
# k8s-ws-gateway-deploy.md, and k8s-consumer-deploy.md, together, under
# kubectl — not five specs each individually "looking done."
#
# Two things this script proves that no earlier spec's own verification
# covers in combination: (1) cross-gateway room consistency survives being
# orchestrated by Kubernetes itself, not just two hand-run go run processes
# (load/multi-instance-check.sh's own job); (2) a rolling update behaves
# like graceful-shutdown.md's "let in-progress races finish naturally"
# decision, not like the ungraceful-crash silent-hang-until-TTL gap
# load/multi-instance-check.sh's own kill test already documented.
#
# Assumes the whole stack is already deployed (deploy/k8s/ + the Bitnami
# Kafka release) and every pod is Running/Ready — this script does not
# apply manifests itself, unlike load/multi-instance-check.sh which owns
# its own process lifecycle. See
# context/features/phase5/multi-instance-k8s-verification.md for the full
# design, and docs/k8s-deployment.md for how to actually stand the cluster
# up first.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

NAMESPACE="aviron"
DISTANCE_METERS=10
# ROLLOUT_TEST_DISTANCE_METERS is deliberately larger than DISTANCE_METERS
# — a rolling update needs the race still in progress when SIGTERM
# actually reaches the pod (StatefulSet/Deployment rollouts don't happen
# instantly), the same reasoning
# load/multi-instance-check.sh's own KILL_TEST_DISTANCE_METERS uses.
ROLLOUT_TEST_DISTANCE_METERS=40
REPEAT_RUNS="${REPEAT_RUNS:-6}"
RUN_ID="$(date +%s)"

GATEWAY_1_LOCAL_PORT=19191
GATEWAY_2_LOCAL_PORT=19192
GATEWAY_1="http://localhost:$GATEWAY_1_LOCAL_PORT"
GATEWAY_2="http://localhost:$GATEWAY_2_LOCAL_PORT"

LOG_DIR="$(mktemp -d /tmp/multi-instance-k8s-check.XXXXXX)"
PF1_PID=""
PF2_PID=""

log() {
  # Always stderr — run_full_lifecycle_check's own return value is
  # captured via command substitution and would otherwise get log lines
  # mixed into it, the same convention load/multi-instance-check.sh uses.
  echo "[$(date '+%H:%M:%S')] $*" >&2
}

cleanup() {
  log "tearing down port-forwards (logs kept at $LOG_DIR)"
  for pid in "$PF1_PID" "$PF2_PID"; do
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

# setup_port_forwards grabs the two real ws-gateway pod names and forwards
# each to its own local port — unlike load/multi-instance-check.sh's two
# distinct local processes, a plain Deployment's pods have no per-pod
# stable names to port-forward by the way a StatefulSet's do
# (`kubectl port-forward svc/ws-gateway-0` doesn't exist here), so this
# has to resolve the actual pod names first.
setup_port_forwards() {
  local pods
  pods=($(kubectl get pods -n "$NAMESPACE" -l app=ws-gateway -o jsonpath='{.items[*].metadata.name}'))
  if [ "${#pods[@]}" -lt 2 ]; then
    log "ERROR: need at least 2 ws-gateway pods, found ${#pods[@]} — is k8s-ws-gateway-deploy.md applied?"
    return 1
  fi
  GW_POD_1="${pods[0]}"
  GW_POD_2="${pods[1]}"
  log "forwarding gateway-1 -> pod/$GW_POD_1 (:$GATEWAY_1_LOCAL_PORT), gateway-2 -> pod/$GW_POD_2 (:$GATEWAY_2_LOCAL_PORT)"

  kubectl port-forward -n "$NAMESPACE" "pod/$GW_POD_1" "$GATEWAY_1_LOCAL_PORT:8080" >"$LOG_DIR/pf1.log" 2>&1 &
  PF1_PID=$!
  kubectl port-forward -n "$NAMESPACE" "pod/$GW_POD_2" "$GATEWAY_2_LOCAL_PORT:8080" >"$LOG_DIR/pf2.log" 2>&1 &
  PF2_PID=$!

  wait_for_http "$GATEWAY_1/healthz" "gateway-1 port-forward"
  wait_for_http "$GATEWAY_2/healthz" "gateway-2 port-forward"
}

register_and_login() {
  local base="$1" email="$2" password="$3" display_name="$4"
  curl -sS -X POST "$base/auth/register" \
    -H 'Content-Type: application/json' \
    -d "{\"email\":\"$email\",\"password\":\"$password\",\"display_name\":\"$display_name\"}" \
    >/dev/null

  curl -sS -X POST "$base/auth/login" \
    -H 'Content-Type: application/json' \
    -d "{\"email\":\"$email\",\"password\":\"$password\"}" \
    | jq -r '.token'
}

# owning_pod_name reports which race-service pod (race-service-0 or
# race-service-1) Redis says owns raceID — the k8s equivalent of
# load/multi-instance-check.sh's owning_instance_letter, adapted for
# k8s-race-service-deploy.md's fully-qualified INSTANCE_ID values instead
# of docker-compose.yml's literal host:port ones.
owning_pod_name() {
  local race_id="$1"
  local redis_pod val
  redis_pod="$(kubectl get pods -n "$NAMESPACE" -l app=redis -o jsonpath='{.items[0].metadata.name}')"
  val="$(kubectl exec -n "$NAMESPACE" "$redis_pod" -- redis-cli GET "room:$race_id" | tr -d '\r')"
  case "$val" in
    *race-service-0*) echo "race-service-0" ;;
    *race-service-1*) echo "race-service-1" ;;
    *) echo "NONE ($val)" ;;
  esac
}

# assert_log_shows_owner cross-checks the redis-cli assertion against the
# owning pod's own structured logs actually mentioning the race —
# load/multi-instance-check.sh's own two-independent-signals convention,
# via kubectl logs instead of a local log file.
assert_log_shows_owner() {
  local race_id="$1" pod="$2"
  case "$pod" in
    race-service-0|race-service-1) ;;
    *) log "  (skipping log cross-check, no clear owner)"; return 0 ;;
  esac
  if kubectl logs -n "$NAMESPACE" "$pod" 2>/dev/null | grep -q "/races/$race_id"; then
    log "  confirmed: $pod's own logs mention /races/$race_id"
  else
    log "  ERROR: $pod's logs never mention /races/$race_id (redis-cli/log-grep disagree)"
    return 1
  fi
}

# assert_log_shows_bus_traffic confirms the bus actually carried this
# race's traffic — the same assertion load/multi-instance-check.sh added
# specifically because a silently-dropped relay message could otherwise
# pass a small enough test race unnoticed.
assert_log_shows_bus_traffic() {
  local race_id="$1" owner_pod="$2"
  case "$owner_pod" in
    race-service-0|race-service-1) ;;
    *) log "  (skipping bus-traffic check, no clear owner)"; return 0 ;;
  esac

  if kubectl logs -n "$NAMESPACE" "$owner_pod" 2>/dev/null | grep -F "\"race_id\":\"$race_id\"" | grep -q '"msg":"roombus: published"'; then
    log "  confirmed: $owner_pod published broadcasts onto the bus for race $race_id"
  else
    log "  ERROR: $owner_pod's log never shows roombus: published for race $race_id"
    return 1
  fi

  local pod
  for pod in "$GW_POD_1" "$GW_POD_2"; do
    if kubectl logs -n "$NAMESPACE" "$pod" 2>/dev/null | grep -F "\"race_id\":\"$race_id\"" | grep -q '"msg":"wsgateway: received"'; then
      log "  confirmed: $pod received broadcasts off the bus for race $race_id"
    else
      log "  ERROR: $pod's log never shows wsgateway: received for race $race_id"
      return 1
    fi
  done
}

# run_full_lifecycle_check mirrors load/multi-instance-check.sh's own
# function almost verbatim — register two users through two *different*
# ws-gateway pods, create+join a race, confirm ownership two independent
# ways plus the bus-traffic check, then run the same k6 scenario
# (load/scenarios/multi-instance-check.js, reused unchanged — it only
# ever cared about plain HTTP/WS URLs, never about what process or pod is
# actually behind them). Prints the owning pod's name on success.
run_full_lifecycle_check() {
  local n="$1"
  local email1="k8scheck-${RUN_ID}-${n}-u1@example.com"
  local email2="k8scheck-${RUN_ID}-${n}-u2@example.com"

  local token1 token2
  token1="$(register_and_login "$GATEWAY_1" "$email1" "k8scheck-password-1" "K8sCheck U1 $n")"
  token2="$(register_and_login "$GATEWAY_2" "$email2" "k8scheck-password-1" "K8sCheck U2 $n")"

  local create_res race_id session1
  create_res="$(curl -sS -X POST "$GATEWAY_1/races" \
    -H "Content-Type: application/json" -H "Authorization: Bearer $token1" \
    -d "{\"name\":\"K8sCheck Race $n\",\"distance_meters\":$DISTANCE_METERS}")"
  race_id="$(echo "$create_res" | jq -r '.id')"
  session1="$(echo "$create_res" | jq -r '.session_token')"

  if [ -z "$race_id" ] || [ "$race_id" = "null" ]; then
    log "ERROR: race creation failed: $create_res"
    return 1
  fi

  local join_res session2
  join_res="$(curl -sS -X POST "$GATEWAY_2/races/$race_id/join" -H "Authorization: Bearer $token2")"
  session2="$(echo "$join_res" | jq -r '.session_token')"

  local owner
  owner="$(owning_pod_name "$race_id")"
  log "race $race_id owned by $owner"
  # Explicit if, not a bare statement — same macOS /bin/bash 3.2
  # inherit_errexit gap load/multi-instance-check.sh's own comment
  # already documents.
  if ! assert_log_shows_owner "$race_id" "$owner"; then
    return 1
  fi

  RACE_ID="$race_id" SESSION_TOKEN_1="$session1" SESSION_TOKEN_2="$session2" \
    BASE_URL_1="$GATEWAY_1" BASE_URL_2="$GATEWAY_2" \
    DISTANCE_METERS="$DISTANCE_METERS" \
    k6 run "$REPO_ROOT/load/scenarios/multi-instance-check.js" >"$LOG_DIR/k6-run-$n.log" 2>&1 &
  local k6_pid=$!

  sleep 2 # let both WS connections open and send join_race
  curl -sS -X POST "$GATEWAY_1/races/$race_id/start" -H "Authorization: Bearer $token1" >/dev/null

  if ! wait "$k6_pid"; then
    log "ERROR: k6 run for race $race_id failed — see $LOG_DIR/k6-run-$n.log"
    return 1
  fi

  if ! assert_log_shows_bus_traffic "$race_id" "$owner"; then
    return 1
  fi

  echo "$owner"
}

# run_rolling_update_check is new to this spec, not in
# load/multi-instance-check.sh: starts a race, triggers a real rolling
# update on $1 (statefulset/race-service or deployment/ws-gateway)
# mid-race. $2 is the *expected outcome*, since the two targets genuinely
# don't mean the same thing by "graceful" here — found out the hard way
# on this script's own first live run against deployment/ws-gateway
# (see below):
#
#   - race_finished (statefulset/race-service): the race itself must
#     survive — graceful-shutdown.md's "let in-progress races finish
#     naturally" decision means the room actor keeps running on the old
#     pod until it completes, so both clients receive a proper
#     race_finished on the same connection, not the silent-hang-until-TTL
#     symptom load/multi-instance-check.sh's own kill test documented for
#     an *ungraceful* crash.
#   - clean_disconnect (deployment/ws-gateway): ws-gateway holds no race
#     state of its own, so its graceful-shutdown design intentionally
#     force-disconnects every local connection (raceHubRegistry.Shutdown())
#     rather than keeping it alive across its own pod's termination — a
#     real client is expected to reconnect through a surviving gateway pod
#     (project-overview.md §4.3), not see race_finished on this exact
#     dying connection. This script's first real run against
#     deployment/ws-gateway asserted race_finished here too and reported
#     a false failure — confirmed a real, correct, prompt disconnect
#     (~16s session duration), not a hang, once actually inspected. Fixed
#     by checking session duration instead of the k6 scenario's own
#     race_finished check for this target specifically.
run_rolling_update_check() {
  local target="$1" expected_outcome="$2"
  local email1="k8scheck-${RUN_ID}-roll-${target//\//-}-u1@example.com"
  local email2="k8scheck-${RUN_ID}-roll-${target//\//-}-u2@example.com"

  local token1 token2
  token1="$(register_and_login "$GATEWAY_1" "$email1" "k8scheck-password-1" "K8sCheck Roll U1")"
  token2="$(register_and_login "$GATEWAY_2" "$email2" "k8scheck-password-1" "K8sCheck Roll U2")"

  local create_res race_id session1
  create_res="$(curl -sS -X POST "$GATEWAY_1/races" \
    -H "Content-Type: application/json" -H "Authorization: Bearer $token1" \
    -d "{\"name\":\"K8sCheck Roll Race\",\"distance_meters\":$ROLLOUT_TEST_DISTANCE_METERS}")"
  race_id="$(echo "$create_res" | jq -r '.id')"
  session1="$(echo "$create_res" | jq -r '.session_token')"

  local join_res session2
  join_res="$(curl -sS -X POST "$GATEWAY_2/races/$race_id/join" -H "Authorization: Bearer $token2")"
  session2="$(echo "$join_res" | jq -r '.session_token')"

  log "rolling-update test race $race_id owned by $(owning_pod_name "$race_id")"

  RACE_ID="$race_id" SESSION_TOKEN_1="$session1" SESSION_TOKEN_2="$session2" \
    BASE_URL_1="$GATEWAY_1" BASE_URL_2="$GATEWAY_2" \
    DISTANCE_METERS="$ROLLOUT_TEST_DISTANCE_METERS" \
    k6 run "$REPO_ROOT/load/scenarios/multi-instance-check.js" >"$LOG_DIR/k6-roll-${target//\//-}.log" 2>&1 &
  local k6_pid=$!

  sleep 2
  curl -sS -X POST "$GATEWAY_1/races/$race_id/start" -H "Authorization: Bearer $token1" >/dev/null
  sleep 3 # let a few telemetry messages flow before rolling

  log "triggering rolling update: kubectl rollout restart $target"
  kubectl rollout restart "$target" -n "$NAMESPACE"

  # Give the rollout a real chance to complete on its own schedule rather
  # than racing it — bounded, not indefinite: if this times out, the
  # rollout itself is stuck, which is worth knowing separately from
  # whether the race survived it.
  if ! kubectl rollout status "$target" -n "$NAMESPACE" --timeout=90s; then
    log "ERROR: kubectl rollout status $target did not complete in time"
    return 1
  fi
  log "rollout of $target complete"

  if [ "$expected_outcome" = "race_finished" ]; then
    if wait "$k6_pid"; then
      log "  OK: race $race_id survived the $target rollout — race_finished delivered to both clients (graceful-shutdown.md's design held)"
    else
      log "  ERROR: race $race_id did NOT survive the $target rollout cleanly — see $LOG_DIR/k6-roll-${target//\//-}.log"
      log "  See context/features/phase5/multi-instance-k8s-verification.md's \"What a rolling-update failure here actually means\" section for how to triage this"
      return 1
    fi
  else
    # clean_disconnect: k6's own scenario always reports its `checks`
    # threshold as failed here (it waits for race_finished specifically),
    # so that's not the real signal — a prompt, clean close (well under
    # the scenario's 2-minute maxDuration) is. A session that instead
    # rides out the full maxDuration is the silent-hang symptom this
    # design exists to avoid.
    wait "$k6_pid" || true
    local duration_s
    duration_s="$(grep -o 'ws_session_duration[^,]*avg=[0-9.]*s' "$LOG_DIR/k6-roll-${target//\//-}.log" | grep -o 'avg=[0-9.]*' | cut -d= -f2 || true)"
    if [ -z "$duration_s" ]; then
      log "  ERROR: could not read ws_session_duration from $LOG_DIR/k6-roll-${target//\//-}.log"
      return 1
    fi
    log "  ws_session_duration avg=${duration_s}s"
    if awk -v d="$duration_s" 'BEGIN { exit !(d < 60) }'; then
      log "  OK: connections through $target closed cleanly and promptly (${duration_s}s, not a 2m hang) — ws-gateway's force-disconnect design held"
    else
      log "  ERROR: connections through $target took ${duration_s}s to close — looks like the silent-hang symptom, not a clean disconnect"
      return 1
    fi
  fi

  # After a StatefulSet rollout specifically, ws-gateway's own pod names
  # don't change (it's a Deployment, not rolled by this call) — but a
  # ws-gateway rollout *does* replace GW_POD_1/GW_POD_2, so refresh the
  # port-forwards afterward rather than leaving them pointed at pods that
  # no longer exist.
  if [ "$target" = "deployment/ws-gateway" ]; then
    log "refreshing port-forwards after ws-gateway rollout"
    kill "$PF1_PID" "$PF2_PID" 2>/dev/null || true
    setup_port_forwards
  fi
}

main() {
  setup_port_forwards

  log "=== repeated full lifecycle checks ==="
  local seen_0=0 seen_1=0
  for i in $(seq 1 "$REPEAT_RUNS"); do
    log "--- run $i/$REPEAT_RUNS ---"
    local owner
    owner="$(run_full_lifecycle_check "$i")"
    case "$owner" in
      race-service-0) seen_0=1 ;;
      race-service-1) seen_1=1 ;;
    esac
  done

  if [ "$seen_0" -eq 1 ] && [ "$seen_1" -eq 1 ]; then
    log "OK: saw both race-service-0 and race-service-1 own a race across $REPEAT_RUNS runs, with both gateway pods relaying successfully"
  else
    log "WARNING: only saw one pod own a race across $REPEAT_RUNS runs — re-run with a higher REPEAT_RUNS"
  fi

  log "=== rolling update: race-service ==="
  run_rolling_update_check "statefulset/race-service" "race_finished"

  log "=== rolling update: ws-gateway ==="
  run_rolling_update_check "deployment/ws-gateway" "clean_disconnect"

  log "ALL STEPS PASSED. Logs kept at $LOG_DIR"
}

main "$@"
