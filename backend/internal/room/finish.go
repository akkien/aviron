package room

import (
	"context"
	"time"
)

// RaceFinisher persists a race's final results. Defined here (not in
// internal/race, where the real implementation — RaceService — lives)
// because internal/race already imports internal/room (RaceHandler holds
// *Registry), so the reverse import direction would create a cycle.
// RaceService satisfies this interface structurally; no import of
// internal/room's implementer is needed here, only this package's own
// ParticipantResult type.
type RaceFinisher interface {
	FinishRace(ctx context.Context, raceID string, distanceMeters int, results []ParticipantResult) error
}

// ParticipantResult is one participant's final row in race_participants.
// FinishTimeMs is nil for a participant who never reached the target
// (evicted or quit, leave-race.md) — they were never at the finish line, so
// there's no time to record. FinishRank, by contrast, is never nil by the
// time checkRaceFinished (internal/room/room.go) builds this slice: a
// non-finisher still gets one, a single shared value equal to the total
// number of distinct participants the room ever saw (leave-race.md), rather
// than being left nil like FinishTimeMs. Both stay pointer types to match
// the schema (race_participants.finish_rank/finish_time_ms are nullable) and
// because RaceFinisher is a general-purpose seam, not something that should
// assume this one caller's invariants forever.
type ParticipantResult struct {
	UserID            string
	FinishRank        *int
	FinishTimeMs      *int64
	AvgPaceWatt       float64
	DisconnectedCount int
}

// noShowTimeoutDuration bounds how long a room waits for a first participant
// to ever connect before treating it as abandoned. Deliberately a separate
// var from reconnection/grace-period.md's gracePeriodDuration — despite
// sharing the same default length, they mean different things (has anyone
// ever shown up, vs. did a disconnected participant come back) and tests
// need to shorten them independently.
var noShowTimeoutDuration = 30 * time.Second

// noShowTimeout is applied when nobody has ever joined a room within
// noShowTimeoutDuration of it starting. It carries no data — applyEvent
// just re-runs the same finish check checkRaceFinished already does
// elsewhere, which is a no-op if anyone has joined by then.
type noShowTimeout struct{}

func (noShowTimeout) isRoomEvent() {}

// RaceFinishedMessage and RaceResultJSON are exported (not kept in
// internal/ws, where websocket/protocol.md originally placed them) for the
// same reason RaceStateMessage/ParticipantStateJSON already live here:
// RoomActor.finishRace marshals and broadcasts this message itself, the
// same way broadcastSnapshot does for race_state, rather than routing
// through internal/ws — internal/ws already depends on this package, so
// keeping the message shapes here keeps that dependency one-directional.
// FinishRank/FinishTimeMs are nullable on the wire, matching
// ParticipantResult and the underlying race_participants columns.
type RaceFinishedMessage struct {
	Type    string           `json:"type"`
	Results []RaceResultJSON `json:"results"`
}

type RaceResultJSON struct {
	UserID       string  `json:"user_id"`
	FinishRank   *int    `json:"finish_rank"`
	FinishTimeMs *int64  `json:"finish_time_ms"`
	AvgPaceWatt  float64 `json:"avg_pace_watt"`
}
