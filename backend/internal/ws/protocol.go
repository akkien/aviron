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

// DecodeClientMessage parses raw inbound JSON into a ClientMessage. It only
// validates the envelope itself (valid JSON, a known Type) — malformed JSON
// or an unrecognized Type is returned as an error for the caller
// (websocket/ws-endpoint.md, internal/room's bus-message handler) to log and
// drop without ending the connection or the room, per this feature's spec.
func DecodeClientMessage(data []byte) (ClientMessage, error) {
	var m ClientMessage
	if err := json.Unmarshal(data, &m); err != nil {
		return ClientMessage{}, fmt.Errorf("ws: decode client message: %w", err)
	}

	switch m.Type {
	case "join_race", "telemetry", "leave_race":
	default:
		return ClientMessage{}, fmt.Errorf("ws: unknown message type %q", m.Type)
	}

	return m, nil
}

// ToRoomEvent converts a decoded ClientMessage into the room.RoomEvent it
// represents. userID and displayName come from the connection's already
// -authenticated session (the session_token from the WS handshake query
// string), not from the message itself — the wire format's join_race message
// only carries race_id (already known from the handshake), so it can't
// supply a display name on its own.
func (m ClientMessage) ToRoomEvent(userID, displayName string) (room.RoomEvent, error) {
	switch m.Type {
	case "join_race":
		return room.ParticipantJoined{UserID: userID, DisplayName: displayName}, nil
	case "telemetry":
		return room.TelemetryReceived{UserID: userID, Seq: m.Seq, WordsCorrect: int(m.DistanceM), PaceWatt: m.PaceWatt}, nil
	case "leave_race":
		return room.ParticipantLeft{UserID: userID}, nil
	default:
		return nil, fmt.Errorf("ws: unknown message type %q", m.Type)
	}
}

// RaceFinishedMessage/RaceResultJSON now live in internal/room
// (room.RaceFinishedMessage/room.RaceResultJSON) — race-completion/finish-race.md's
// RoomActor.finishRace marshals and broadcasts that message itself, the same
// way broadcastSnapshot already does for race_state, instead of routing
// through this package.
