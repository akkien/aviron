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
