package ws

import (
	"context"
	"testing"
	"time"

	"github.com/akkien/aviron/internal/room"
)

func TestHub_FansOutToAllRegisteredConns(t *testing.T) {
	broadcast := make(chan []byte, 4)
	done := make(chan struct{})
	defer close(done)

	h := newHub(broadcast, done, func() {})

	connA := make(chan []byte, connBufferSize)
	connB := make(chan []byte, connBufferSize)
	h.registerConn(connA)
	h.registerConn(connB)

	broadcast <- []byte(`{"type":"race_state"}`)

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

func TestHub_UnregisterStopsDelivery(t *testing.T) {
	broadcast := make(chan []byte, 4)
	done := make(chan struct{})
	defer close(done)

	h := newHub(broadcast, done, func() {})

	conn := make(chan []byte, connBufferSize)
	h.registerConn(conn)
	h.unregisterConn(conn)

	broadcast <- []byte(`{"type":"race_state"}`)

	select {
	case msg := <-conn:
		t.Fatalf("unregistered conn received %q, want nothing", msg)
	case <-time.After(100 * time.Millisecond):
		// Expected: no delivery.
	}
}

func TestHub_FullConnDoesNotBlockOthersOrTheRoom(t *testing.T) {
	broadcast := make(chan []byte, 4)
	done := make(chan struct{})
	defer close(done)

	h := newHub(broadcast, done, func() {})

	slow := make(chan []byte, connBufferSize)
	fast := make(chan []byte, connBufferSize)
	h.registerConn(slow)
	h.registerConn(fast)

	// Fill the slow connection's buffer completely without ever draining it.
	for i := 0; i < connBufferSize; i++ {
		broadcast <- []byte("filler")
		// Give run's select a moment to drain broadcast into both conns
		// before sending the next filler message.
		time.Sleep(5 * time.Millisecond)
	}
	// Drain fast so it doesn't also fill up and mask the assertion below.
	for i := 0; i < connBufferSize; i++ {
		<-fast
	}

	// One more broadcast: must still reach fast even though slow is full.
	broadcast <- []byte("final")
	select {
	case msg := <-fast:
		if string(msg) != "final" {
			t.Errorf("fast conn got %q, want %q", msg, "final")
		}
	case <-time.After(time.Second):
		t.Fatal("fast conn never received the broadcast sent while slow conn's buffer was full")
	}
}

func TestHub_ClosesAndCallsOnCloseWhenDone(t *testing.T) {
	broadcast := make(chan []byte, 4)
	done := make(chan struct{})

	closed := make(chan struct{})
	h := newHub(broadcast, done, func() { close(closed) })

	close(done)

	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("onClose was not called after done fired")
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

func TestHub_QueryBufferUsage_SumsAcrossRegisteredConns(t *testing.T) {
	broadcast := make(chan []byte, 4)
	done := make(chan struct{})
	defer close(done)

	h := newHub(broadcast, done, func() {})

	connA := make(chan []byte, connBufferSize)
	connB := make(chan []byte, connBufferSize)
	h.registerConn(connA)
	h.registerConn(connB)

	connA <- []byte("1")
	connA <- []byte("2")
	connB <- []byte("3")

	if got := h.queryBufferUsage(); got != 3 {
		t.Errorf("queryBufferUsage() = %d, want 3", got)
	}
}

func TestHub_QueryBufferUsage_ZeroAfterClose(t *testing.T) {
	broadcast := make(chan []byte, 4)
	done := make(chan struct{})

	h := newHub(broadcast, done, func() {})
	close(done)

	select {
	case <-h.closed:
	case <-time.After(time.Second):
		t.Fatal("hub never closed after done fired")
	}

	if got := h.queryBufferUsage(); got != 0 {
		t.Errorf("queryBufferUsage() after close = %d, want 0", got)
	}
}

func TestHubRegistry_TotalConnBufferUsage_SumsAcrossHubs(t *testing.T) {
	hr := newHubRegistry()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	actor1 := room.NewRoomActor(ctx, "race-1", 3, make(chan []byte, 4), fakeFinisher{}, fakeLeaver{}, fakeCanceller{}, testLogger, testTickObserver)
	actor2 := room.NewRoomActor(ctx, "race-2", 3, make(chan []byte, 4), fakeFinisher{}, fakeLeaver{}, fakeCanceller{}, testLogger, testTickObserver)

	h1 := hr.getOrCreate("race-1", actor1)
	h2 := hr.getOrCreate("race-2", actor2)

	connA := make(chan []byte, connBufferSize)
	connB := make(chan []byte, connBufferSize)
	h1.registerConn(connA)
	h2.registerConn(connB)

	connA <- []byte("1")
	connB <- []byte("2")
	connB <- []byte("3")

	if got := hr.totalConnBufferUsage(); got != 3 {
		t.Errorf("totalConnBufferUsage() = %d, want 3", got)
	}
}

func TestHubRegistry_GetOrCreate_ReturnsSameHubForSameRace(t *testing.T) {
	hr := newHubRegistry()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	actor := room.NewRoomActor(ctx, "race-1", 3, make(chan []byte, 4), fakeFinisher{}, fakeLeaver{}, fakeCanceller{}, testLogger, testTickObserver)

	h1 := hr.getOrCreate("race-1", actor)
	h2 := hr.getOrCreate("race-1", actor)
	if h1 != h2 {
		t.Error("getOrCreate returned different hubs for the same race_id")
	}
}

func TestHubRegistry_GetOrCreate_DifferentRacesGetDifferentHubs(t *testing.T) {
	hr := newHubRegistry()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	actor1 := room.NewRoomActor(ctx, "race-1", 3, make(chan []byte, 4), fakeFinisher{}, fakeLeaver{}, fakeCanceller{}, testLogger, testTickObserver)
	actor2 := room.NewRoomActor(ctx, "race-2", 3, make(chan []byte, 4), fakeFinisher{}, fakeLeaver{}, fakeCanceller{}, testLogger, testTickObserver)

	h1 := hr.getOrCreate("race-1", actor1)
	h2 := hr.getOrCreate("race-2", actor2)
	if h1 == h2 {
		t.Error("getOrCreate returned the same hub for two different race_ids")
	}
}

func TestHubRegistry_CleansUpAfterRoomContextCancelled(t *testing.T) {
	hr := newHubRegistry()
	ctx, cancel := context.WithCancel(context.Background())

	actor := room.NewRoomActor(ctx, "race-1", 3, make(chan []byte, 4), fakeFinisher{}, fakeLeaver{}, fakeCanceller{}, testLogger, testTickObserver)
	hr.getOrCreate("race-1", actor)

	cancel()

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
	t.Fatal("hubRegistry did not remove race-1's hub after its room context was cancelled")
}
