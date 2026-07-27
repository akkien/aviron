package roombus

import (
	"context"
	"log/slog"

	"github.com/akkien/aviron/internal/room"
	"github.com/akkien/aviron/internal/roomrelay"
	"github.com/akkien/aviron/internal/ws"
)

// relayBus is the subset of *roomrelay.Bus's API natsRoomBus depends on —
// satisfied by both it and *roomrelay.FakeBus, so this adapter's own
// decode/translate logic is unit-testable against FakeBus without a real
// (or embedded) NATS connection.
type relayBus interface {
	SubscribeIn(ctx context.Context, raceID string) (<-chan roomrelay.InboundEnvelope, func(), error)
	PublishOut(ctx context.Context, raceID string, env roomrelay.OutboundEnvelope) error
}

// natsRoomBus adapts a relayBus (in production, *roomrelay.Bus) to satisfy
// room.RoomBus, decoding raw client frames into room.RoomEvent via
// internal/ws.DecodeClientMessage/ToRoomEvent. Composed here, one level up
// from internal/room, because internal/ws already imports internal/room
// (for RoomEvent itself), so internal/room importing either roomrelay or ws
// back would cycle (room-service-adapter.md) — mirrors cmd/consumer/run.go's
// own precedent (internal/postgres importing internal/consumer) for the same
// import-direction constraint.
type natsRoomBus struct {
	bus    relayBus
	logger *slog.Logger
}

// NewNATSRoomBus constructs a natsRoomBus around bus.
func NewNATSRoomBus(bus relayBus, logger *slog.Logger) *natsRoomBus {
	return &natsRoomBus{bus: bus, logger: logger}
}

// SubscribeIn satisfies room.RoomBus. Malformed frames are logged and
// dropped, never forwarded — the same behavior internal/wsgateway's
// readLoop already applies today, just moved to this side of the bus.
func (b *natsRoomBus) SubscribeIn(ctx context.Context, raceID string) (<-chan room.RoomEvent, func(), error) {
	sub, unsubscribe, err := b.bus.SubscribeIn(ctx, raceID)
	if err != nil {
		return nil, nil, err
	}

	out := make(chan room.RoomEvent)
	go func() {
		defer close(out)
		for env := range sub {
			ev, ok := b.toRoomEvent(raceID, env)
			if !ok {
				continue
			}
			select {
			case out <- ev:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, unsubscribe, nil
}

func (b *natsRoomBus) toRoomEvent(raceID string, env roomrelay.InboundEnvelope) (room.RoomEvent, bool) {
	switch env.Kind {
	case roomrelay.InboundKindMessage:
		msg, err := ws.DecodeClientMessage(env.Message)
		if err != nil {
			b.logger.Warn("roombus: dropping malformed message", slog.String("race_id", raceID), slog.Any("error", err))
			return nil, false
		}
		ev, err := msg.ToRoomEvent(env.UserID, env.DisplayName)
		if err != nil {
			b.logger.Warn("roombus: dropping message", slog.String("race_id", raceID), slog.Any("error", err))
			return nil, false
		}
		return ev, true
	case roomrelay.InboundKindDisconnected:
		return room.ParticipantDisconnected{UserID: env.UserID}, true
	default:
		return nil, false
	}
}

// PublishOut satisfies room.RoomBus.
func (b *natsRoomBus) PublishOut(ctx context.Context, raceID string, payload []byte) error {
	return b.bus.PublishOut(ctx, raceID, roomrelay.OutboundEnvelope{
		Kind:    roomrelay.OutboundKindBroadcast,
		RaceID:  raceID,
		Payload: payload,
	})
}

// PublishRoomClosed satisfies room.RoomBus.
func (b *natsRoomBus) PublishRoomClosed(ctx context.Context, raceID string) error {
	return b.bus.PublishOut(ctx, raceID, roomrelay.OutboundEnvelope{
		Kind:   roomrelay.OutboundKindRoomClosed,
		RaceID: raceID,
	})
}
