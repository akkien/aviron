# WebSocket Client

## Overview

Phase 1's `TypingView` (`frontend/src/components/races/TypingView.tsx`) already renders one lane per participant, but only the local player's lane actually moves — everyone else is a static placeholder, and progress is tracked entirely in local component state with nowhere to send it (documented explicitly in `context/features/phase1/frontend/create-join-race-page.md`'s Data section). This spec replaces that local-only tracking with a real WebSocket connection to `websocket/ws-endpoint.md`, so every participant's lane moves live for everyone watching. Depends on the entire backend half of Phase 2 already working, including `leave-race/leave-race.md` — the WS connection this spec opens is the same one a "Quit Race" affordance sends `leave_race` over, so it's covered here rather than in a separate spec.

## Requirements

### Connecting

- Once `RaceStatusView` starts a race (or `RacesPage` loads an already-`active` race), open `new WebSocket(...)` against the backend's `/ws` endpoint with `race_id` and the race's `session_token` (returned by Phase 1's `POST /races/{id}/join`, currently fetched and thrown away after the join call — it needs to be retained in `RacesPage`'s state now instead) as query params
- Immediately on open, send `{"type":"join_race","race_id":...}` (`websocket/protocol.md`) so the server attaches this connection and sends back one immediate snapshot instead of waiting for the next 250ms tick

### Sending Telemetry

- Replace `TypingView`'s current local-only `countCompletedWords` tracking: every time the completed-word count increases, send `{"type":"telemetry","seq":<incrementing>,"distance_m":<words correct>,"pace_watt":<current wpm>,"ts":<epoch ms>}` — one message per completed word, matching context/project-overview.md §13's per-word cadence, not a fixed timer
- `seq` is a simple incrementing counter kept in a ref (not state, to avoid it being part of the render cycle) — starts at 0 for each connection, per `websocket/protocol.md`'s ordering contract

### Receiving Updates

- On `race_state` messages, update every participant's lane position (`TypingView`'s existing `laneColor`-per-participant rendering already exists from Phase 1 — this feature only changes *where the progress number comes from*, not the lane visuals themselves) — no more `p.user_id === currentUserId` special-casing to zero out other lanes
- On `race_finished`, stop sending telemetry, show each participant's final rank/time (a small results view — new, since Phase 1 had no concept of a race ever finishing), and close the connection
- **A `race_finished` result entry does not always mean that participant finished.** `leave-race/leave-race.md` means the results list can include participants who were evicted or quit — they share one last-place `finish_rank` and have `finish_time_ms: null`. The results view must render those rows as "DNF" (or similar) rather than assuming every entry has a real time, and must not crash/blank on a `null` `finish_time_ms`.

### Leaving Mid-Race

- A "Quit Race" button, visible any time the WebSocket connection is open, sends `{"type":"leave_race"}` (`leave-race/leave-race.md`, `websocket/protocol.md`) — no confirmation dialog needed per this project's "no polish" scope, though a simple `window.confirm` is acceptable if it's cheap to add
- The server closes the connection right after processing `leave_race` (`internal/ws`'s reader loop returns on its own, no client-side `ws.close()` needed, though calling it is harmless) — treat the resulting `onclose` the same as the "race actually finished" case in `frontend-realtime/reconnect-ui.md`, not as a dropped connection: no reconnect attempt should be made after a self-initiated quit
- After quitting, show the player their own result immediately from the local click (no need to wait for the eventual `race_finished` broadcast, which may not arrive until everyone else finishes or leaves too) — e.g. "You left the race," rather than leaving the typing view frozen mid-word

## Validation

- Client-side: if the WebSocket fails to open at all (e.g., backend down, invalid `session_token`), fall back to showing the connection error inline — same "no toast library" convention Phase 1 established, not a new pattern

## Data

- New types mirroring `websocket/protocol.md`'s server messages (`RaceStateMessage`, `RaceFinishedMessage`, `ParticipantStateJSON`, `RaceResultJSON`) — same hand-written-to-match-the-Go-DTOs approach Phase 1 used for REST types (`frontend/src/types/race.ts`), not a shared codegen step. `RaceResultJSON`'s `finish_rank`/`finish_time_ms` are typed as `number | null`, matching the Go DTO's nullable `*int`/`*int64` (`internal/room/finish.go`) — not `number`, since `leave-race/leave-race.md` means a real (not just theoretical) `null` case now exists.
- `RacesPage` needs to hold onto the `session_token` from `JoinRaceForm`'s response (currently discarded after the join call succeeds) — a small but real change to existing Phase 1 state, not new architecture
- The outbound `leave_race` message needs no payload beyond `{"type":"leave_race"}` — no new client-sent type beyond what `websocket/protocol.md` already defines

## Notes

- This is the feature that finally makes context/project-overview.md §12's Phase 2 line "React app gains a WebSocket client, showing participants' positions updating in real time" true — everything before this in Phase 2 is backend-only.
- No reconnect logic in this spec — a dropped WebSocket here just shows as disconnected until `frontend-realtime/reconnect-ui.md` (the next feature) adds retry/resync behavior.
- Still "good enough for visual manual testing, doesn't need to be polished" per the same roadmap line — no animation library, no easing, just position driven directly by the latest `race_state` tick.
- Depends on `leave-race/leave-race.md` (done) for the `leave_race` message this spec's "Leaving Mid-Race" section sends, and for the nullable `finish_rank`/`finish_time_ms` shape the results view must render.
