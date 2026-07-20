# Reconnect UI

## Overview

The client-side half of `reconnection/grace-period.md`: when the WebSocket drops mid-race (wifi blip, laptop sleep, mobile app backgrounded), the frontend should try to get back into the race automatically within the grace period, instead of leaving the player stuck on a frozen view. Depends on `frontend-realtime/websocket-client.md` already existing — there has to be a connection before there can be a reconnect.

## Requirements

### Detecting a Drop

- Listen for the WebSocket's `onclose`/`onerror` events from `frontend-realtime/websocket-client.md`'s connection; distinguish "the race actually finished and the server closed the connection on purpose" (a `race_finished` message was already received) from "the connection dropped unexpectedly" — only the latter triggers reconnect behavior
- Show a small inline status ("Reconnecting...") rather than anything blocking — the typing view and lanes stay visible with their last-known state while a reconnect is attempted, not replaced by a loading screen

### Reconnecting

- Retry opening a new `WebSocket` against the same `/ws?race_id=...&session_token=...` — the same `session_token` from Phase 1's join response that `websocket-client.md` already retains, since that's exactly what the backend's grace-period reattachment (`reconnection/grace-period.md`) checks against
- Use a short bounded backoff (e.g., a few attempts a couple seconds apart) rather than hammering the server in a tight loop — this is a side-project test harness, not a production-grade reconnect strategy, so a simple fixed-delay retry a handful of times is enough; give up and show an error after that
- On a successful reconnect, the server immediately resends a full `race_state` snapshot (per `reconnection/grace-period.md`'s reattachment behavior) — the client just needs to apply it like any other `race_state` message, no special "resync" code path required beyond the reconnect itself

### Grace Period Expiry

- If every retry attempt fails (grace period on the server side has lapsed, or the server explicitly refuses the reattach), show that the player has left the race — matching what the other participants would already see per `reconnection/grace-period.md`'s `ParticipantLeft` broadcast

## Validation

- No new client-side input validation — this feature is about connection lifecycle, not forms

## Data

- No new types beyond what `frontend-realtime/websocket-client.md` already introduced — reconnection reuses the exact same `race_state`/`race_finished` message shapes, since a resync is just "another `race_state` message, sent immediately instead of on the next tick"

## Notes

- This is the frontend counterpart to the JD's most-emphasized skill (reconnection) — but deliberately kept simple on the client side (bounded retry, no exponential backoff, no offline queueing of missed telemetry) since the real complexity this project is practicing lives in the backend's grace-period/single-writer handling, not in frontend reconnect polish.
- Telemetry sent while disconnected is simply lost, not queued for replay on reconnect — acceptable per this project's trust model (context/project-overview.md §13: the server never verifies typed text anyway, and a resync snapshot immediately re-establishes the source of truth for everyone).
