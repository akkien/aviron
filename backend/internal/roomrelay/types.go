// Package roomrelay carries every real-time, room-scoped message between
// ws-gateway (holds the client connection) and race-service (runs the
// RoomActor) over NATS Core (room-message-bus.md) — the two are never the
// same process under the WS Gateway revision, so nothing crosses between
// them except through this bus.
package roomrelay

import "encoding/json"

// InboundKind values for InboundEnvelope.Kind.
type InboundKind string

const (
	// InboundKindMessage carries a client-sent frame, published by
	// ws-gateway once per frame it decodes off the WebSocket.
	InboundKindMessage InboundKind = "message"
	// InboundKindDisconnected has no client frame behind it — ws-gateway's
	// reader loop synthesizes it directly when a read fails, mirroring what
	// internal/ws.readLoop already does today for a local disconnect.
	InboundKindDisconnected InboundKind = "disconnected"
)

// InboundEnvelope is published on room.{race_id}.in by ws-gateway, once per
// client-sent frame or detected disconnect. UserID/DisplayName are attached
// by the gateway itself — already known from session-token verification at
// connect time — never trusted from the message body, the same trust
// boundary internal/ws.readLoop already enforces by deriving them itself
// rather than parsing them out of client JSON.
type InboundEnvelope struct {
	Kind        InboundKind `json:"kind"`
	RaceID      string      `json:"race_id"`
	UserID      string      `json:"user_id"`
	DisplayName string      `json:"display_name,omitempty"`
	// Message is the raw client JSON frame, present only when
	// Kind == InboundKindMessage — kept as the exact bytes
	// ws.decodeClientMessage already knows how to parse, so a bus-message
	// consumer can reuse ws.ClientMessage.toRoomEvent(userID, displayName)
	// unchanged instead of a second wire format for the same message types.
	Message json.RawMessage `json:"message,omitempty"`
}

// OutboundKind values for OutboundEnvelope.Kind.
type OutboundKind string

const (
	// OutboundKindBroadcast carries a payload to forward verbatim to every
	// local connection ws-gateway holds for this race.
	OutboundKindBroadcast OutboundKind = "broadcast"
	// OutboundKindRoomClosed signals the race is over: close every local
	// connection for it and tear down local state. Mirrors
	// internal/ws/hub.go's existing done/hub.closed behavior, now crossing
	// a process boundary.
	OutboundKindRoomClosed OutboundKind = "room_closed"
)

// OutboundEnvelope is published on room.{race_id}.out by the race-service
// instance that owns the room.
type OutboundEnvelope struct {
	Kind   OutboundKind `json:"kind"`
	RaceID string       `json:"race_id"`
	// Payload is the exact already-marshaled []byte RoomActor.Broadcast()
	// already produces (race_state/race_started/race_finished) — present
	// only when Kind == OutboundKindBroadcast. The bus never re-encodes it.
	Payload json.RawMessage `json:"payload,omitempty"`
}

func inSubject(raceID string) string  { return "room." + raceID + ".in" }
func outSubject(raceID string) string { return "room." + raceID + ".out" }
