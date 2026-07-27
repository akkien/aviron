package wsgateway

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/akkien/aviron/internal/roomrelay"
)

func TestRaceHub_FansOutToAllRegisteredConns(t *testing.T) {
	fake := roomrelay.NewFakeBus()
	out, unsubscribe, err := fake.SubscribeOut(context.Background(), "race-1")
	if err != nil {
		t.Fatalf("SubscribeOut: %v", err)
	}
	h := newRaceHub("race-1", out, unsubscribe, func() {}, testLogger)

	connA := make(chan []byte, connBufferSize)
	connB := make(chan []byte, connBufferSize)
	h.registerConn(connA)
	h.registerConn(connB)

	if err := fake.PublishOut(context.Background(), "race-1", roomrelay.OutboundEnvelope{
		Kind: roomrelay.OutboundKindBroadcast, RaceID: "race-1", Payload: []byte(`{"type":"race_state"}`),
	}); err != nil {
		t.Fatalf("PublishOut: %v", err)
	}

	for name, c := range map[string]chan []byte{"A": connA, "B": connB} {
		select {
		case msg := <-c:
			if string(msg) != `{"type":"race_state"}` {
				t.Errorf("conn %s got %q, want the broadcast message", name, msg)
			}
		case <-time.After(time.Second):
			t.Fatalf("conn %s never received the broadcast", name)
		}
	}
}

func TestRaceHub_UnregisterStopsDelivery(t *testing.T) {
	fake := roomrelay.NewFakeBus()
	out, unsubscribe, err := fake.SubscribeOut(context.Background(), "race-1")
	if err != nil {
		t.Fatalf("SubscribeOut: %v", err)
	}
	h := newRaceHub("race-1", out, unsubscribe, func() {}, testLogger)

	conn := make(chan []byte, connBufferSize)
	h.registerConn(conn)
	h.unregisterConn(conn)

	if err := fake.PublishOut(context.Background(), "race-1", roomrelay.OutboundEnvelope{
		Kind: roomrelay.OutboundKindBroadcast, RaceID: "race-1", Payload: []byte(`{"type":"race_state"}`),
	}); err != nil {
		t.Fatalf("PublishOut: %v", err)
	}

	select {
	case msg := <-conn:
		t.Fatalf("unregistered conn received %q, want nothing", msg)
	case <-time.After(100 * time.Millisecond):
		// Expected: no delivery.
	}
}

func TestRaceHub_FullConnDoesNotBlockOthers(t *testing.T) {
	fake := roomrelay.NewFakeBus()
	out, unsubscribe, err := fake.SubscribeOut(context.Background(), "race-1")
	if err != nil {
		t.Fatalf("SubscribeOut: %v", err)
	}
	h := newRaceHub("race-1", out, unsubscribe, func() {}, testLogger)

	slow := make(chan []byte, connBufferSize)
	fast := make(chan []byte, connBufferSize)
	h.registerConn(slow)
	h.registerConn(fast)

	publish := func(payload string) {
		if err := fake.PublishOut(context.Background(), "race-1", roomrelay.OutboundEnvelope{
			Kind: roomrelay.OutboundKindBroadcast, RaceID: "race-1", Payload: []byte(payload),
		}); err != nil {
			t.Fatalf("PublishOut: %v", err)
		}
	}

	// Fill the slow connection's buffer completely without ever draining it.
	for i := 0; i < connBufferSize; i++ {
		publish("filler")
		time.Sleep(5 * time.Millisecond)
	}
	// Drain fast so it doesn't also fill up and mask the assertion below.
	for i := 0; i < connBufferSize; i++ {
		<-fast
	}

	// One more broadcast: must still reach fast even though slow is full.
	publish("final")
	select {
	case msg := <-fast:
		if string(msg) != "final" {
			t.Errorf("fast conn got %q, want %q", msg, "final")
		}
	case <-time.After(time.Second):
		t.Fatal("fast conn never received the broadcast sent while slow conn's buffer was full")
	}
}

func TestRaceHub_RoomClosed_ClosesHub(t *testing.T) {
	fake := roomrelay.NewFakeBus()
	out, unsubscribe, err := fake.SubscribeOut(context.Background(), "race-1")
	if err != nil {
		t.Fatalf("SubscribeOut: %v", err)
	}
	closed := make(chan struct{})
	h := newRaceHub("race-1", out, unsubscribe, func() { close(closed) }, testLogger)

	if err := fake.PublishOut(context.Background(), "race-1", roomrelay.OutboundEnvelope{
		Kind: roomrelay.OutboundKindRoomClosed, RaceID: "race-1",
	}); err != nil {
		t.Fatalf("PublishOut: %v", err)
	}

	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("onClose was not called after room_closed arrived")
	}

	select {
	case <-h.closed:
	case <-time.After(time.Second):
		t.Fatal("h.closed never closed after room_closed arrived")
	}
}

func TestRaceHub_SignalStop_ClosesHub(t *testing.T) {
	fake := roomrelay.NewFakeBus()
	out, unsubscribe, err := fake.SubscribeOut(context.Background(), "race-1")
	if err != nil {
		t.Fatalf("SubscribeOut: %v", err)
	}
	closed := make(chan struct{})
	h := newRaceHub("race-1", out, unsubscribe, func() { close(closed) }, testLogger)

	h.signalStop()
	h.signalStop() // must not panic on double-call

	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("onClose was not called after signalStop")
	}

	// registerConn/unregisterConn must not block forever against a closed hub.
	regDone := make(chan struct{})
	go func() {
		h.registerConn(make(chan []byte, connBufferSize))
		h.unregisterConn(make(chan []byte, connBufferSize))
		close(regDone)
	}()

	select {
	case <-regDone:
	case <-time.After(time.Second):
		t.Fatal("registerConn/unregisterConn blocked after the hub closed")
	}
}

func TestRaceHubRegistry_Attach_ReturnsSameHubAndIncrementsRefCount(t *testing.T) {
	fake := roomrelay.NewFakeBus()
	hr := NewRaceHubRegistry(context.Background(), fake, testLogger)

	h1, err := hr.attach("race-1")
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	h2, err := hr.attach("race-1")
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	if h1 != h2 {
		t.Error("attach returned different hubs for the same race_id")
	}

	hr.mu.Lock()
	refCount := hr.hubs["race-1"].refCount
	hr.mu.Unlock()
	if refCount != 2 {
		t.Errorf("refCount = %d, want 2 after two attaches", refCount)
	}
}

func TestRaceHubRegistry_Attach_DifferentRacesGetDifferentHubs(t *testing.T) {
	fake := roomrelay.NewFakeBus()
	hr := NewRaceHubRegistry(context.Background(), fake, testLogger)

	h1, err := hr.attach("race-1")
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	h2, err := hr.attach("race-2")
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	if h1 == h2 {
		t.Error("attach returned the same hub for two different race_ids")
	}
}

func TestRaceHubRegistry_Detach_OnlyStopsHubWhenRefCountHitsZero(t *testing.T) {
	fake := roomrelay.NewFakeBus()
	hr := NewRaceHubRegistry(context.Background(), fake, testLogger)

	h, err := hr.attach("race-1")
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	if _, err := hr.attach("race-1"); err != nil {
		t.Fatalf("attach: %v", err)
	}

	hr.detach("race-1")
	select {
	case <-h.closed:
		t.Fatal("hub closed after only one of two attaches detached")
	case <-time.After(50 * time.Millisecond):
	}

	hr.detach("race-1")
	select {
	case <-h.closed:
	case <-time.After(time.Second):
		t.Fatal("hub never closed after the last attach detached")
	}

	hr.mu.Lock()
	_, ok := hr.hubs["race-1"]
	hr.mu.Unlock()
	if ok {
		t.Error("raceHubRegistry still has an entry for race-1 after its last detach")
	}
}

func TestRaceHubRegistry_Detach_IsNoOpForUnknownRace(t *testing.T) {
	fake := roomrelay.NewFakeBus()
	hr := NewRaceHubRegistry(context.Background(), fake, testLogger)

	hr.detach("race-never-attached") // must not panic
}

// TestRaceHubRegistry_RoomClosed_RemovesEntryIndependentOfRefCount proves a
// room finishing removes this gateway's entry even while local connections
// are still attached (refCount > 0) — raceHub's own onClose callback, not
// ref-counting, is what fires here.
func TestRaceHubRegistry_RoomClosed_RemovesEntryIndependentOfRefCount(t *testing.T) {
	fake := roomrelay.NewFakeBus()
	hr := NewRaceHubRegistry(context.Background(), fake, testLogger)

	if _, err := hr.attach("race-1"); err != nil {
		t.Fatalf("attach: %v", err)
	}

	if err := fake.PublishOut(context.Background(), "race-1", roomrelay.OutboundEnvelope{
		Kind: roomrelay.OutboundKindRoomClosed, RaceID: "race-1",
	}); err != nil {
		t.Fatalf("PublishOut: %v", err)
	}

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		hr.mu.Lock()
		_, ok := hr.hubs["race-1"]
		hr.mu.Unlock()
		if !ok {
			return // cleaned up
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("raceHubRegistry did not remove race-1's entry after room_closed")
}

// TestRaceHubRegistry_ConcurrentAttachDetach is the -race coverage
// ws-gateway.md's own Concurrency section calls for: concurrent
// connect/disconnect racing the ref-count transition to/from zero.
func TestRaceHubRegistry_ConcurrentAttachDetach(t *testing.T) {
	fake := roomrelay.NewFakeBus()
	hr := NewRaceHubRegistry(context.Background(), fake, testLogger)

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := hr.attach("race-1"); err != nil {
				t.Errorf("attach: %v", err)
				return
			}
			hr.detach("race-1")
		}()
	}
	wg.Wait()
}
