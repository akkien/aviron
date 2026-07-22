package room

// RoomEvent is the closed set of inputs a RoomActor accepts through its
// inbox — every mutation to room state originates from one of these.
type RoomEvent interface {
	isRoomEvent()
}

type ParticipantJoined struct {
	UserID      string
	DisplayName string
}

func (ParticipantJoined) isRoomEvent() {}

type TelemetryReceived struct {
	UserID       string
	Seq          int
	WordsCorrect int
	PaceWatt     float64
}

func (TelemetryReceived) isRoomEvent() {}

type ParticipantDisconnected struct {
	UserID string
}

func (ParticipantDisconnected) isRoomEvent() {}

// ParticipantEvicted is applied when a disconnected participant's grace period
// expires without a reconnect (reconnection/grace-period.md). There is
// deliberately no ParticipantReconnected variant — a reconnect reuses
// ParticipantJoined, distinguished from a fresh join by applyEvent checking
// existing participant state instead of the event carrying a new type.
type ParticipantEvicted struct {
	UserID string
}

func (ParticipantEvicted) isRoomEvent() {}

// ParticipantLeft is applied when a still-connected participant intentionally
// quits mid-race (leave-race.md's WebSocket leave_race message). Unlike
// ParticipantEvicted, it carries no DisconnectedAt guard — a still-connected
// participant sending this is exactly who's expected to, so it's always
// honored immediately, with no timer and no grace period.
type ParticipantLeft struct {
	UserID string
}

func (ParticipantLeft) isRoomEvent() {}
