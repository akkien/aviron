package room

import "context"

// EvictionRecorder makes a participant's eviction (or mid-race quit)
// durably visible to ws-gateway's reconnect-check via Redis
// (room-message-bus.md's "Evicted-reconnect checks bypass the bus
// entirely", ws-gateway.md). A Registry-level dependency (like
// TickObserver/RoomLocator/EventPublisher), not a per-Spawn argument —
// every room shares the same process-wide recorder. Defined here, not in
// internal/roomlocator, for the same import-cycle reason as
// RaceFinisher/RaceLeaver/RaceCanceller (lifecycle.go): the concrete
// implementation only needs this package's own primitive parameters, not
// the reverse.
type EvictionRecorder interface {
	MarkEvicted(ctx context.Context, raceID, userID string) error
}

// NoopEvictionRecorder is the EvictionRecorder for single-instance dev and
// every existing test fixture that constructs a Registry/RoomActor —
// mirrors NoopLocator/NoopPublisher exactly.
type NoopEvictionRecorder struct{}

func (NoopEvictionRecorder) MarkEvicted(ctx context.Context, raceID, userID string) error {
	return nil
}
