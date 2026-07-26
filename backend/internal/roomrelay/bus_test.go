package roomrelay

import (
	"context"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/test"
	"github.com/nats-io/nats.go"
)

// newTestBus spins up an in-process NATS server (the NATS equivalent of
// miniredis.RunT — see room-message-bus.md's Testing notes) so these tests
// prove the wire format round-trips over a real NATS connection, not just
// against FakeBus.
func newTestBus(t *testing.T) *Bus {
	t.Helper()
	srv := natsserver.RunRandClientPortServer()
	t.Cleanup(srv.Shutdown)

	nc, err := nats.Connect(srv.ClientURL())
	if err != nil {
		t.Fatalf("nats.Connect: %v", err)
	}
	t.Cleanup(nc.Close)

	return NewBus(nc)
}

func TestBus_PublishIn_DeliversToSubscriber(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b := newTestBus(t)

	events, _, err := b.SubscribeIn(ctx, "race-1")
	if err != nil {
		t.Fatalf("SubscribeIn: %v", err)
	}

	want := InboundEnvelope{
		Kind:        InboundKindMessage,
		RaceID:      "race-1",
		UserID:      "u1",
		DisplayName: "Alice",
		Message:     []byte(`{"type":"telemetry","seq":1}`),
	}
	if err := b.PublishIn(ctx, "race-1", want); err != nil {
		t.Fatalf("PublishIn: %v", err)
	}

	got := receiveInOrTimeout(t, events)
	if got.Kind != want.Kind || got.RaceID != want.RaceID || got.UserID != want.UserID || got.DisplayName != want.DisplayName {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	if string(got.Message) != string(want.Message) {
		t.Fatalf("got message %s, want %s", got.Message, want.Message)
	}
}

func TestBus_PublishIn_NotDeliveredOnDifferentRaceSubject(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b := newTestBus(t)

	events, _, err := b.SubscribeIn(ctx, "race-1")
	if err != nil {
		t.Fatalf("SubscribeIn: %v", err)
	}

	if err := b.PublishIn(ctx, "race-2", InboundEnvelope{RaceID: "race-2"}); err != nil {
		t.Fatalf("PublishIn: %v", err)
	}

	select {
	case ev := <-events:
		t.Fatalf("expected no delivery for a different race's subject, got %+v", ev)
	case <-time.After(200 * time.Millisecond):
	}
}

// TestBus_PublishOut_FansOutToEverySubscriber proves the fan-out property
// room-message-bus.md's Subject design deliberately relies on plain
// Subscribe (not QueueSubscribe) for: every gateway holding local clients
// for a race must receive every broadcast, not just one of them.
func TestBus_PublishOut_FansOutToEverySubscriber(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b := newTestBus(t)

	a, _, err := b.SubscribeOut(ctx, "race-1")
	if err != nil {
		t.Fatalf("SubscribeOut a: %v", err)
	}
	c, _, err := b.SubscribeOut(ctx, "race-1")
	if err != nil {
		t.Fatalf("SubscribeOut c: %v", err)
	}

	want := OutboundEnvelope{Kind: OutboundKindBroadcast, RaceID: "race-1", Payload: []byte(`{"type":"race_state","tick":42}`)}
	if err := b.PublishOut(ctx, "race-1", want); err != nil {
		t.Fatalf("PublishOut: %v", err)
	}

	gotA := receiveOutOrTimeout(t, a)
	gotC := receiveOutOrTimeout(t, c)
	if string(gotA.Payload) != string(want.Payload) || string(gotC.Payload) != string(want.Payload) {
		t.Fatalf("expected both subscribers to receive %s, got a=%s c=%s", want.Payload, gotA.Payload, gotC.Payload)
	}
}

func TestBus_PublishOut_RoomClosedSignal(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b := newTestBus(t)

	events, _, err := b.SubscribeOut(ctx, "race-1")
	if err != nil {
		t.Fatalf("SubscribeOut: %v", err)
	}

	if err := b.PublishOut(ctx, "race-1", OutboundEnvelope{Kind: OutboundKindRoomClosed, RaceID: "race-1"}); err != nil {
		t.Fatalf("PublishOut: %v", err)
	}

	got := receiveOutOrTimeout(t, events)
	if got.Kind != OutboundKindRoomClosed {
		t.Fatalf("got kind %q, want %q", got.Kind, OutboundKindRoomClosed)
	}
}

func TestBus_Unsubscribe_ClosesChannel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b := newTestBus(t)

	events, unsubscribe, err := b.SubscribeIn(ctx, "race-1")
	if err != nil {
		t.Fatalf("SubscribeIn: %v", err)
	}

	unsubscribe()

	select {
	case _, ok := <-events:
		if ok {
			t.Fatal("expected the channel to close, got a value instead")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the channel to close after unsubscribe")
	}
}

func TestBus_ContextCancel_ClosesChannel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	b := newTestBus(t)

	events, _, err := b.SubscribeOut(ctx, "race-1")
	if err != nil {
		t.Fatalf("SubscribeOut: %v", err)
	}

	cancel()

	select {
	case _, ok := <-events:
		if ok {
			t.Fatal("expected the channel to close, got a value instead")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the channel to close after context cancel")
	}
}

func TestBus_PublishIn_MalformedPayloadIsSkippedNotFatal(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b := newTestBus(t)

	events, _, err := b.SubscribeIn(ctx, "race-1")
	if err != nil {
		t.Fatalf("SubscribeIn: %v", err)
	}

	if err := b.nc.Publish(inSubject("race-1"), []byte("not json")); err != nil {
		t.Fatalf("raw publish: %v", err)
	}

	want := InboundEnvelope{Kind: InboundKindMessage, RaceID: "race-1", UserID: "u1"}
	if err := b.PublishIn(ctx, "race-1", want); err != nil {
		t.Fatalf("PublishIn: %v", err)
	}

	got := receiveInOrTimeout(t, events)
	if got.UserID != want.UserID {
		t.Fatalf("expected the malformed message to be skipped and the valid one delivered, got %+v", got)
	}
}
