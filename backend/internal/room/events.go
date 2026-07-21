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
}

func (TelemetryReceived) isRoomEvent() {}

type ParticipantDisconnected struct {
	UserID string
}

func (ParticipantDisconnected) isRoomEvent() {}

// ParticipantLeft is applied when a disconnected participant's grace period
// expires without a reconnect (reconnection/grace-period.md). There is
// deliberately no ParticipantReconnected variant — a reconnect reuses
// ParticipantJoined, distinguished from a fresh join by applyEvent checking
// existing participant state instead of the event carrying a new type.
type ParticipantLeft struct {
	UserID string
}

func (ParticipantLeft) isRoomEvent() {}
