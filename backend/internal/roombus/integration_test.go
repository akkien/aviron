package roombus

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	natsservertest "github.com/nats-io/nats-server/v2/test"
	"github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/akkien/aviron/internal/metrics"
	"github.com/akkien/aviron/internal/room"
	"github.com/akkien/aviron/internal/roomrelay"
)

type spyFinisher struct {
	mu    sync.Mutex
	calls int
}

func (f *spyFinisher) FinishRace(ctx context.Context, raceID string, distanceMeters int, results []room.ParticipantResult) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return nil
}

func (f *spyFinisher) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

type noopLeaver struct{}

func (noopLeaver) LeaveRace(ctx context.Context, raceID, userID string) error { return nil }

type noopCanceller struct{}

func (noopCanceller) CancelRace(ctx context.Context, raceID string) error { return nil }

// TestNATSRoomBus_EndToEnd_RealNATSDrivesRoomActorToFinish is the test
// room-message-bus.md's own Notes insisted on: proof the envelope format
// actually round-trips between two independent connections over a real
// NATS server — not just that natsRoomBus's decode logic works against
// roomrelay.FakeBus (adapter_test.go already covers that).
// Simulates ws-gateway's side with a second, independent *roomrelay.Bus
// publishing raw client frames onto room.race-1.in and observing
// room.race-1.out, exactly as ws-gateway.md's own eventual adapter will.
func TestNATSRoomBus_EndToEnd_RealNATSDrivesRoomActorToFinish(t *testing.T) {
	srv := natsservertest.RunRandClientPortServer()
	t.Cleanup(srv.Shutdown)

	nc, err := nats.Connect(srv.ClientURL())
	if err != nil {
		t.Fatalf("nats.Connect: %v", err)
	}
	t.Cleanup(nc.Close)

	m := metrics.NewMetrics()
	bus := NewNATSRoomBus(roomrelay.NewBus(nc, m.Registerer()), testLogger)
	registry := room.NewRegistry(testLogger, m, room.NoopLocator{}, room.NoopPublisher{}, bus, room.NoopEvictionRecorder{})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	gateway := roomrelay.NewBus(nc, prometheus.NewRegistry())
	out, _, err := gateway.SubscribeOut(ctx, "race-1")
	if err != nil {
		t.Fatalf("SubscribeOut: %v", err)
	}

	finisher := &spyFinisher{}
	actor := registry.Spawn(ctx, "race-1", 1, finisher, noopLeaver{}, noopCanceller{})
	actor.MarkActive("hello world")

	joinMsg, _ := json.Marshal(map[string]any{"type": "join_race", "race_id": "race-1"})
	if err := gateway.PublishIn(ctx, "race-1", roomrelay.InboundEnvelope{
		Kind: roomrelay.InboundKindMessage, RaceID: "race-1", UserID: "user-1", DisplayName: "Alice", Message: joinMsg,
	}); err != nil {
		t.Fatalf("PublishIn join: %v", err)
	}

	telemetryMsg, _ := json.Marshal(map[string]any{"type": "telemetry", "seq": 1, "distance_m": 1, "pace_watt": 60})
	if err := gateway.PublishIn(ctx, "race-1", roomrelay.InboundEnvelope{
		Kind: roomrelay.InboundKindMessage, RaceID: "race-1", UserID: "user-1", Message: telemetryMsg,
	}); err != nil {
		t.Fatalf("PublishIn telemetry: %v", err)
	}

	sawBroadcastBeforeClosed := false
	deadline := time.After(3 * time.Second)
loop:
	for {
		select {
		case env := <-out:
			switch env.Kind {
			case roomrelay.OutboundKindBroadcast:
				sawBroadcastBeforeClosed = true
			case roomrelay.OutboundKindRoomClosed:
				break loop
			}
		case <-deadline:
			t.Fatal("timed out waiting for room_closed on room.race-1.out")
		}
	}

	if !sawBroadcastBeforeClosed {
		t.Error("expected at least one broadcast before room_closed, got none")
	}
	if finisher.count() != 1 {
		t.Errorf("finisher.FinishRace calls = %d, want 1 — the room actor never actually finished", finisher.count())
	}
}
