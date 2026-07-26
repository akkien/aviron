package internal

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/akkien/aviron/internal/room"
	"github.com/akkien/aviron/internal/roomrelay"
)

var testLogger = slog.New(slog.DiscardHandler)

func TestNATSRoomBus_SubscribeIn_DecodesJoinRaceMessage(t *testing.T) {
	fake := roomrelay.NewFakeBus()
	adapter := newNATSRoomBus(fake, testLogger)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	events, _, err := adapter.SubscribeIn(ctx, "race-1")
	if err != nil {
		t.Fatalf("SubscribeIn: %v", err)
	}

	if err := fake.PublishIn(ctx, "race-1", roomrelay.InboundEnvelope{
		Kind:        roomrelay.InboundKindMessage,
		RaceID:      "race-1",
		UserID:      "user-1",
		DisplayName: "Alice",
		Message:     []byte(`{"type":"join_race","race_id":"race-1"}`),
	}); err != nil {
		t.Fatalf("PublishIn: %v", err)
	}

	select {
	case ev := <-events:
		joined, ok := ev.(room.ParticipantJoined)
		if !ok {
			t.Fatalf("event type = %T, want room.ParticipantJoined", ev)
		}
		if joined.UserID != "user-1" || joined.DisplayName != "Alice" {
			t.Fatalf("unexpected event: %+v", joined)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the decoded RoomEvent")
	}
}

func TestNATSRoomBus_SubscribeIn_DisconnectedHasNoMessageBody(t *testing.T) {
	fake := roomrelay.NewFakeBus()
	adapter := newNATSRoomBus(fake, testLogger)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	events, _, err := adapter.SubscribeIn(ctx, "race-1")
	if err != nil {
		t.Fatalf("SubscribeIn: %v", err)
	}

	if err := fake.PublishIn(ctx, "race-1", roomrelay.InboundEnvelope{
		Kind:   roomrelay.InboundKindDisconnected,
		RaceID: "race-1",
		UserID: "user-1",
	}); err != nil {
		t.Fatalf("PublishIn: %v", err)
	}

	select {
	case ev := <-events:
		disc, ok := ev.(room.ParticipantDisconnected)
		if !ok || disc.UserID != "user-1" {
			t.Fatalf("event = %+v, want room.ParticipantDisconnected{UserID: user-1}", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for ParticipantDisconnected")
	}
}

// TestNATSRoomBus_SubscribeIn_MalformedMessageIsDroppedNotFatal mirrors the
// "log and drop" behavior internal/wsgateway's readLoop already applies
// today — a hostile or buggy client shouldn't be able to kill a room.
func TestNATSRoomBus_SubscribeIn_MalformedMessageIsDroppedNotFatal(t *testing.T) {
	fake := roomrelay.NewFakeBus()
	adapter := newNATSRoomBus(fake, testLogger)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	events, _, err := adapter.SubscribeIn(ctx, "race-1")
	if err != nil {
		t.Fatalf("SubscribeIn: %v", err)
	}

	if err := fake.PublishIn(ctx, "race-1", roomrelay.InboundEnvelope{
		Kind:    roomrelay.InboundKindMessage,
		RaceID:  "race-1",
		UserID:  "user-1",
		Message: []byte(`not json`),
	}); err != nil {
		t.Fatalf("PublishIn malformed: %v", err)
	}
	if err := fake.PublishIn(ctx, "race-1", roomrelay.InboundEnvelope{
		Kind:        roomrelay.InboundKindMessage,
		RaceID:      "race-1",
		UserID:      "user-1",
		DisplayName: "Alice",
		Message:     []byte(`{"type":"join_race","race_id":"race-1"}`),
	}); err != nil {
		t.Fatalf("PublishIn valid: %v", err)
	}

	select {
	case ev := <-events:
		if _, ok := ev.(room.ParticipantJoined); !ok {
			t.Fatalf("expected the malformed message to be dropped and the valid one delivered, got %T", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the valid event after a malformed one was dropped")
	}
}

func TestNATSRoomBus_PublishOut_WrapsBroadcastKind(t *testing.T) {
	fake := roomrelay.NewFakeBus()
	adapter := newNATSRoomBus(fake, testLogger)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	out, _, err := fake.SubscribeOut(ctx, "race-1")
	if err != nil {
		t.Fatalf("SubscribeOut: %v", err)
	}

	if err := adapter.PublishOut(ctx, "race-1", []byte(`{"type":"race_state"}`)); err != nil {
		t.Fatalf("PublishOut: %v", err)
	}

	select {
	case env := <-out:
		if env.Kind != roomrelay.OutboundKindBroadcast {
			t.Fatalf("Kind = %q, want %q", env.Kind, roomrelay.OutboundKindBroadcast)
		}
		if string(env.Payload) != `{"type":"race_state"}` {
			t.Fatalf("Payload = %s, want the exact bytes passed in", env.Payload)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the outbound envelope")
	}
}

func TestNATSRoomBus_PublishRoomClosed_WrapsRoomClosedKind(t *testing.T) {
	fake := roomrelay.NewFakeBus()
	adapter := newNATSRoomBus(fake, testLogger)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	out, _, err := fake.SubscribeOut(ctx, "race-1")
	if err != nil {
		t.Fatalf("SubscribeOut: %v", err)
	}

	if err := adapter.PublishRoomClosed(ctx, "race-1"); err != nil {
		t.Fatalf("PublishRoomClosed: %v", err)
	}

	select {
	case env := <-out:
		if env.Kind != roomrelay.OutboundKindRoomClosed {
			t.Fatalf("Kind = %q, want %q", env.Kind, roomrelay.OutboundKindRoomClosed)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the outbound envelope")
	}
}
