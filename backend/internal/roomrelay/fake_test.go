package roomrelay

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestFakeBus_PublishIn_DeliversToSubscriber(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b := NewFakeBus()

	events, _, err := b.SubscribeIn(ctx, "race-1")
	if err != nil {
		t.Fatalf("SubscribeIn: %v", err)
	}

	want := InboundEnvelope{Kind: InboundKindMessage, RaceID: "race-1", UserID: "u1"}
	if err := b.PublishIn(ctx, "race-1", want); err != nil {
		t.Fatalf("PublishIn: %v", err)
	}

	got := receiveInOrTimeout(t, events)
	if got.Kind != want.Kind || got.RaceID != want.RaceID || got.UserID != want.UserID {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestFakeBus_PublishIn_NotDeliveredToOtherRace(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b := NewFakeBus()

	events, _, err := b.SubscribeIn(ctx, "race-1")
	if err != nil {
		t.Fatalf("SubscribeIn: %v", err)
	}

	if err := b.PublishIn(ctx, "race-2", InboundEnvelope{RaceID: "race-2"}); err != nil {
		t.Fatalf("PublishIn: %v", err)
	}

	select {
	case ev := <-events:
		t.Fatalf("expected no delivery for a different race, got %+v", ev)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestFakeBus_PublishOut_FansOutToEverySubscriber(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b := NewFakeBus()

	a, _, err := b.SubscribeOut(ctx, "race-1")
	if err != nil {
		t.Fatalf("SubscribeOut a: %v", err)
	}
	c, _, err := b.SubscribeOut(ctx, "race-1")
	if err != nil {
		t.Fatalf("SubscribeOut c: %v", err)
	}

	want := OutboundEnvelope{Kind: OutboundKindBroadcast, RaceID: "race-1", Payload: []byte(`{"tick":1}`)}
	if err := b.PublishOut(ctx, "race-1", want); err != nil {
		t.Fatalf("PublishOut: %v", err)
	}

	if got := receiveOutOrTimeout(t, a); string(got.Payload) != string(want.Payload) {
		t.Fatalf("subscriber a got %+v, want %+v", got, want)
	}
	if got := receiveOutOrTimeout(t, c); string(got.Payload) != string(want.Payload) {
		t.Fatalf("subscriber c got %+v, want %+v", got, want)
	}
}

func TestFakeBus_Unsubscribe_ClosesChannel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b := NewFakeBus()

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

func TestFakeBus_Unsubscribe_IsIdempotent(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b := NewFakeBus()

	_, unsubscribe, err := b.SubscribeIn(ctx, "race-1")
	if err != nil {
		t.Fatalf("SubscribeIn: %v", err)
	}

	unsubscribe()
	unsubscribe() // must not panic on double-close
}

func TestFakeBus_ContextCancel_ClosesChannel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	b := NewFakeBus()

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

// TestFakeBus_ConcurrentPublishWhileSubscribing exercises the
// concurrent-publish-while-subscribing scenario room-message-bus.md's own
// Concurrency section calls for — go test -race is the actual assertion
// here, not any single value checked below.
func TestFakeBus_ConcurrentPublishWhileSubscribing(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b := NewFakeBus()

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			events, unsubscribe, err := b.SubscribeOut(ctx, "race-1")
			if err != nil {
				t.Errorf("SubscribeOut: %v", err)
				return
			}
			defer unsubscribe()
			for j := 0; j < 5; j++ {
				select {
				case <-events:
				case <-time.After(100 * time.Millisecond):
				}
			}
		}()
	}

	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = b.PublishOut(ctx, "race-1", OutboundEnvelope{Kind: OutboundKindBroadcast, RaceID: "race-1"})
		}()
	}

	wg.Wait()
}

// TestFakeBus_UnsubscribeRacesPublish covers subscribe/unsubscribe racing a
// publish landing mid-transition — the second concurrency scenario
// room-message-bus.md's Concurrency section calls for.
func TestFakeBus_UnsubscribeRacesPublish(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b := NewFakeBus()

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, unsubscribe, err := b.SubscribeIn(ctx, "race-1")
			if err != nil {
				t.Errorf("SubscribeIn: %v", err)
				return
			}
			unsubscribe()
		}()
	}

	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = b.PublishIn(ctx, "race-1", InboundEnvelope{Kind: InboundKindMessage, RaceID: "race-1"})
		}()
	}

	wg.Wait()
}

func receiveInOrTimeout(t *testing.T, ch <-chan InboundEnvelope) InboundEnvelope {
	t.Helper()
	select {
	case ev := <-ch:
		return ev
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for an inbound envelope")
		return InboundEnvelope{}
	}
}

func receiveOutOrTimeout(t *testing.T, ch <-chan OutboundEnvelope) OutboundEnvelope {
	t.Helper()
	select {
	case ev := <-ch:
		return ev
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for an outbound envelope")
		return OutboundEnvelope{}
	}
}
