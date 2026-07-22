# Phase 2 — Real-time Core Plan

## Overview

Phase 2 (context/project-overview.md §12) turns the REST-only typing race from Phase 1 into an actual real-time multiplayer experience: every race room becomes a single goroutine ("room actor," §4.1) that owns all of that room's state, clients attach to it over WebSocket (§4.2) instead of polling `GET /races/{id}`, and a dropped connection no longer means "you quit" — it means "you have a grace period to reconnect" (§4.3). This is the phase the JD (`docs/jd.md`) cares about most: goroutines, channels, `context`, data races, goroutine leaks, and reconnection. Redis/Kafka/Kubernetes horizontal-scaling concerns are explicitly out of scope until Phase 4 — Phase 2 is single-instance.

## Features, in build order

1. **Room Actor** (folder: `room-actor/`) — the core in-memory state machine, split into two
   1. `room-actor/room-actor-core.md` — the per-race goroutine, its `inbox` channel, event application (join/telemetry/leave), and the 250ms broadcast ticker
   2. `room-actor/room-registry.md` — mapping a `race_id` to its running `RoomActor`, spawning one when a race starts, cleaning it up when the race ends or empties out
2. **WebSocket** (folder: `websocket/`) — split into two
   1. `websocket/protocol.md` — the JSON message schema (`join_race`, `telemetry`, `race_state`, `race_finished`) as a pure encode/decode concern, kept separate from connection plumbing
   2. `websocket/ws-endpoint.md` — `GET /ws?race_id=...&session_token=...`, the upgrade handshake, and the per-connection reader/writer goroutines wired to a room actor's `inbox`/broadcast channels
3. **Reconnection** (folder: `reconnection/`)
   1. `reconnection/grace-period.md` — abrupt-close handling, the grace-period timer, and reattachment when a client reconnects with a valid `session_token` before it expires
4. **Race Completion** (folder: `race-completion/`) — referenced as "Phase 2's race-completion logic" in `context/features/phase1/phase-1-plan.md`'s out-of-scope note
   1. `race-completion/finish-race.md` — the room actor detecting a race is done and writing results to Postgres in one transaction (§3); the Kafka event emission mentioned in the same step of §2 is explicitly deferred to Phase 4 (§6)
5. **Leave Race** (folder: `leave-race/`) — not part of the original §12 roadmap, added by explicit request after the rest of Phase 2's backend was done
   1. `leave-race/leave-race.md` — a REST endpoint to leave a still-`pending` race, an immediate (non-grace-period) WebSocket path to quit mid-race, and an update to `race-completion/finish-race.md`'s rank-assignment rule so quitters get an explicit shared last-place rank instead of vanishing from the results silently
6. **Frontend Realtime** (folder: `frontend-realtime/`) — split into two
   1. `frontend-realtime/websocket-client.md` — the React app opens the WebSocket after a race starts, sends `telemetry` per correct word, and renders every participant's car moving live from `race_state` ticks (removing Phase 1's "only your own car moves" limitation)
   2. `frontend-realtime/reconnect-ui.md` — detecting a dropped WebSocket, retrying, and resyncing from the full snapshot the server resends on reattach

## Dependency order

- Room Registry depends on Room Actor Core existing (it manages instances of it).
- The WebSocket Endpoint depends on the Protocol schema being defined and on a Room Registry to attach a connection to.
- Reconnection depends on the WebSocket Endpoint already working — there's nothing to reconnect to otherwise.
- Race Completion depends on the room actor already ingesting real telemetry over WebSocket, since "is this race done" is a question about live progress, not REST-only state.
- Leave Race depends on Race Completion (it changes that feature's rank-assignment rule) and Reconnection (it reuses the `evicted`/`IsEvicted` mechanism for quitters).
- Both Frontend Realtime features depend on the entire backend half of Phase 2 being done — Leave Race included: `websocket-client.md` now specs the mid-race "Quit Race" affordance directly (it's the same WS connection), and `phase1/frontend/create-join-race-page.md` picked up a small addendum for the pre-start "Leave" button.
- Recommended build order: Room Actor (2 features) → WebSocket (2 features) → Reconnection (1 feature) → Race Completion (1 feature) → Leave Race (1 feature) → Frontend Realtime (2 features).

## Explicitly out of scope for Phase 2

- Redis pub/sub, the room-ownership registry, and anything about running ≥2 instances (Phase 4, context/project-overview.md §5) — Phase 2 is single-instance; a room actor only ever needs to coordinate goroutines within one process
- Kafka event pipeline (`workout.sample`, `race.finished` topics) and the ClickHouse consumer (Phase 4, §6) — Race Completion still writes the finish transaction to Postgres directly, per §2 step 4, but does not publish anything
- A leaderboard/stats endpoint — `leaderboard_alltime` gets written to as part of Race Completion's finish transaction, but no endpoint reads it yet; that's Phase 2.5 territory once there's enough finished-race data to make it meaningful
- Kubernetes, Prometheus metrics, structured logging, `pprof` (Phase 3, §9) — Phase 2 is about correctness and concurrency safety first; observability instrumentation comes once the real-time core is stable
- gRPC between Race Service and a separate Analytics/Leaderboard service (§8) — no second service exists yet to talk to
