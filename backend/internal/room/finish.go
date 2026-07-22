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

// RaceLeaver persists a participant intentionally leaving a still-pending
// race (pending-connections.md). Mirrors RaceFinisher exactly, for the same
// import-cycle reason: internal/race already imports internal/room, so the
// concrete implementation (RaceService.LeaveRace) satisfies this interface
// structurally rather than internal/room importing internal/race.
type RaceLeaver interface {
	LeaveRace(ctx context.Context, raceID, userID string) error
}

// RaceCanceller persists a pending race's cancellation
// (room-lifecycle/cancelled-race-status.md) — called when a room tears down
// before ever going active (pending-expiry timeout, no-show timeout, or a
// pending lobby emptied out by departing participants). Mirrors
// RaceFinisher/RaceLeaver exactly, for the same import-cycle reason.
type RaceCanceller interface {
	CancelRace(ctx context.Context, raceID string) error
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

// RaceStartedMessage is broadcast the instant a room goes active
// (websocket/race-started-broadcast.md), reaching every connection already
// attached to the pending lobby. Lives here rather than internal/ws for the
// same reason RaceFinishedMessage/RaceStateMessage do: RoomActor broadcasts
// it directly (see broadcastRaceStarted in room.go), so a duplicate
// definition in internal/ws would be unused dead code. PromptText is carried
// directly so a client can start typing without a separate
// GET /races/{id}/text round-trip.
type RaceStartedMessage struct {
	Type       string `json:"type"`
	PromptText string `json:"prompt_text"`
}

// PendingTimeoutDuration bounds how long a room may sit pending before it's
// torn down regardless of how many players are in the lobby
// (room-lifecycle/pending-expiry.md) — a fixed window anchored to room
// creation, not reset by any activity. Exported (unlike noShowTimeoutDuration
// and reconnection/grace-period.md's gracePeriodDuration) for two reasons:
// internal/race/handler.go reads it to compute GET /races/{id}'s
// pending_expires_at, and internal/ws's own regression test needs to shorten
// it directly, since that test lives in a different package than the one
// owning the var.
var PendingTimeoutDuration = 5 * time.Minute

// pendingExpired fires once PendingTimeoutDuration elapses from room
// creation. It carries no data — applyEvent's guard is what decides whether
// it actually tears anything down.
type pendingExpired struct{}

func (pendingExpired) isRoomEvent() {}

// RaceExpiredMessage is broadcast when a pending room's lifetime runs out
// without the race starting (websocket/race-expired-broadcast.md) — the
// counterpart to RaceStartedMessage for the opposite outcome. No payload
// beyond the type discriminator: there's only one way a room expires while
// pending, so a reason field would be speculative.
type RaceExpiredMessage struct {
	Type string `json:"type"`
}
