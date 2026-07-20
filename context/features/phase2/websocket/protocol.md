# WebSocket Protocol

## Overview

The JSON message schema exchanged over the WebSocket connection (context/project-overview.md §4.2), kept as its own spec so the wire format can be scanned and reasoned about independently of connection-handling plumbing (`websocket/ws-endpoint.md`) — the same reasoning that keeps `internal/race/dtos.go` separate from `internal/race/handler.go` in Phase 1.

## Requirements

### Messages

```text
Client -> Server: {"type":"join_race","race_id":"..."}
Client -> Server: {"type":"telemetry","seq":42,"distance_m":812.5,"pace_watt":210,"ts":...}
Server -> Client: {"type":"race_state","tick":1234,"participants":[{"user_id":"...","distance_m":...,"rank":1}, ...]}
Server -> Client: {"type":"race_finished","results":[...]}
```

- `race_id` is already established by the WebSocket handshake's query string (`GET /ws?race_id=...&session_token=...`, §8) — the client's `join_race` message is not how the server learns *which* race, it's the explicit trigger for the server to attach this connection to the room actor's state and send back one immediate `race_state` snapshot, rather than waiting up to 250ms for the next tick. Without it, a client that connects between ticks would sit looking at nothing for up to one tick interval.
- For this project's typing race (§13), the client sends one `telemetry` message per word typed correctly, not on a fixed timer — `distance_m` is the running count of correct words so far (not a delta), `pace_watt` is current WPM. Human typing speed (roughly 0.4–2s between messages) means this never needs special-casing for burst traffic.
- `seq` is a per-connection monotonically increasing counter set by the client, used by the room actor (`room-actor/room-actor-core.md`) to reject stale/duplicate/out-of-order telemetry — a message is only applied if its `seq` is greater than the last one accepted for that participant.
- `race_finished`'s `results` shape mirrors `race-completion/finish-race.md`'s persisted `race_participants` row per player: `{user_id, finish_rank, finish_time_ms, avg_pace_watt}`.

### Decode/Encode

- One `decodeClientMessage([]byte) (ClientMessage, error)` on the inbound side, dispatching on the `type` field into whichever event `room-actor/room-actor-core.md`'s `RoomEvent` variants apply — a `telemetry` message becomes a `TelemetryReceived` event, `join_race` becomes a `ParticipantJoined`/attach signal
- One `encodeServerMessage` per outbound type (`race_state`, `race_finished`) — plain `encoding/json`, no protobuf yet (§4.2 explicitly allows switching later, not needed now)
- Malformed inbound JSON (unknown `type`, missing required field) is logged and dropped, not treated as a connection-ending error — a single bad message from one client's frontend bug shouldn't tear down the whole WebSocket

## Data

```go
type ClientMessage struct {
    Type      string  `json:"type"`
    RaceID    string  `json:"race_id,omitempty"`
    Seq       int     `json:"seq,omitempty"`
    DistanceM float64 `json:"distance_m,omitempty"`
    PaceWatt  float64 `json:"pace_watt,omitempty"`
    TS        int64   `json:"ts,omitempty"`
}

type RaceStateMessage struct {
    Type         string                  `json:"type"`
    Tick         int                     `json:"tick"`
    Participants []ParticipantStateJSON  `json:"participants"`
}

type ParticipantStateJSON struct {
    UserID    string  `json:"user_id"`
    DistanceM float64 `json:"distance_m"`
    Rank      int     `json:"rank"`
}

type RaceFinishedMessage struct {
    Type    string           `json:"type"`
    Results []RaceResultJSON `json:"results"`
}
```

## Notes

- Field names (`distance_m`, `pace_watt`) reuse the fitness-telemetry vocabulary per context/project-overview.md §13, same as Phase 1's REST DTOs — no divergence to call out here.
- This is intentionally the simplest possible envelope (flat JSON object with a `type` discriminator) — no versioning field, no nested envelope. Revisit only if a real need for backward-compatible protocol evolution shows up; this is a side project's single-team WebSocket, not a public API.
