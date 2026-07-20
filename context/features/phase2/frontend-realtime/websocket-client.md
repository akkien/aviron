# WebSocket Client

## Overview

Phase 1's `TypingView` (`frontend/src/components/races/TypingView.tsx`) already renders one lane per participant, but only the local player's lane actually moves — everyone else is a static placeholder, and progress is tracked entirely in local component state with nowhere to send it (documented explicitly in `context/features/phase1/frontend/create-join-race-page.md`'s Data section). This spec replaces that local-only tracking with a real WebSocket connection to `websocket/ws-endpoint.md`, so every participant's lane moves live for everyone watching. Depends on the entire backend half of Phase 2 already working.

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

## Validation

- Client-side: if the WebSocket fails to open at all (e.g., backend down, invalid `session_token`), fall back to showing the connection error inline — same "no toast library" convention Phase 1 established, not a new pattern

## Data

- New types mirroring `websocket/protocol.md`'s server messages (`RaceStateMessage`, `RaceFinishedMessage`, `ParticipantStateJSON`, `RaceResultJSON`) — same hand-written-to-match-the-Go-DTOs approach Phase 1 used for REST types (`frontend/src/types/race.ts`), not a shared codegen step
- `RacesPage` needs to hold onto the `session_token` from `JoinRaceForm`'s response (currently discarded after the join call succeeds) — a small but real change to existing Phase 1 state, not new architecture

## Notes

- This is the feature that finally makes context/project-overview.md §12's Phase 2 line "React app gains a WebSocket client, showing participants' positions updating in real time" true — everything before this in Phase 2 is backend-only.
- No reconnect logic in this spec — a dropped WebSocket here just shows as disconnected until `frontend-realtime/reconnect-ui.md` (the next feature) adds retry/resync behavior.
- Still "good enough for visual manual testing, doesn't need to be polished" per the same roadmap line — no animation library, no easing, just position driven directly by the latest `race_state` tick.
