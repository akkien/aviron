// Package ws defines the WebSocket wire protocol (context/project-overview.md
// §4.2): decoding inbound client messages, converting them into
// internal/room.RoomEvent values, and encoding outbound server messages.
// Kept independent of connection plumbing (websocket/ws-endpoint.md, not yet
// built), mirroring how internal/race keeps dtos.go separate from
// handler.go.
package ws

import (
	"encoding/json"
	"fmt"

	"github.com/akkien/aviron/internal/room"
)

// ClientMessage is the inbound envelope for both message types the client
// sends. Fields irrelevant to a given Type are left zero-valued.
type ClientMessage struct {
	Type      string  `json:"type"`
	RaceID    string  `json:"race_id,omitempty"`
	Seq       int     `json:"seq,omitempty"`
	DistanceM float64 `json:"distance_m,omitempty"`
	PaceWatt  float64 `json:"pace_watt,omitempty"`
	TS        int64   `json:"ts,omitempty"`
}

// decodeClientMessage parses raw inbound JSON into a ClientMessage. It only
// validates the envelope itself (valid JSON, a known Type) — malformed JSON
// or an unrecognized Type is returned as an error for the caller
// (websocket/ws-endpoint.md) to log and drop without ending the connection,
// per this feature's spec.
func decodeClientMessage(data []byte) (ClientMessage, error) {
	var m ClientMessage
	if err := json.Unmarshal(data, &m); err != nil {
		return ClientMessage{}, fmt.Errorf("ws: decode client message: %w", err)
	}

	switch m.Type {
	case "join_race", "telemetry":
	default:
		return ClientMessage{}, fmt.Errorf("ws: unknown message type %q", m.Type)
	}

	return m, nil
}

// toRoomEvent converts a decoded ClientMessage into the room.RoomEvent it
// represents. userID and displayName come from the connection's already
// -authenticated session (the session_token from the WS handshake query
// string), not from the message itself — the wire format's join_race message
// only carries race_id (already known from the handshake), so it can't
// supply a display name on its own.
func (m ClientMessage) toRoomEvent(userID, displayName string) (room.RoomEvent, error) {
	switch m.Type {
	case "join_race":
		return room.ParticipantJoined{UserID: userID, DisplayName: displayName}, nil
	case "telemetry":
		return room.TelemetryReceived{UserID: userID, Seq: m.Seq, WordsCorrect: int(m.DistanceM)}, nil
	default:
		return nil, fmt.Errorf("ws: unknown message type %q", m.Type)
	}
}

// RaceFinishedMessage is the outbound envelope sent once when a race
// completes. Results mirrors race-completion/finish-race.md's persisted
// race_participants row per player.
type RaceFinishedMessage struct {
	Type    string           `json:"type"`
	Results []RaceResultJSON `json:"results"`
}

type RaceResultJSON struct {
	UserID       string  `json:"user_id"`
	FinishRank   int     `json:"finish_rank"`
	FinishTimeMs int64   `json:"finish_time_ms"`
	AvgPaceWatt  float64 `json:"avg_pace_watt"`
}

// encodeRaceFinishedMessage encodes the final results of a race. There is no
// equivalent encodeRaceStateMessage here: internal/room.RoomActor already
// marshals its race_state ticks internally (via the now-exported
// room.RaceStateMessage/room.ParticipantStateJSON) and hands out pre-encoded
// bytes over Broadcast() — duplicating that here would just be a second,
// unused code path for the same JSON shape.
func encodeRaceFinishedMessage(results []RaceResultJSON) ([]byte, error) {
	return json.Marshal(RaceFinishedMessage{Type: "race_finished", Results: results})
}
