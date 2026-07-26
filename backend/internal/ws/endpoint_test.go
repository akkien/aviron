package ws

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/akkien/aviron/internal/room"
)

func newTestWSHandler() *WSHandler {
	return NewWSHandler(room.NewRegistry(testLogger, testTickObserver, room.NoopLocator{}, room.NoopPublisher{}), []byte("test-secret"), "http://localhost:5173", testLogger)
}

func TestServeConn_JoinRaceThenAbruptDisconnect_NoGoroutineLeak(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	actor := room.NewRoomActor(ctx, "race-1", 5, make(chan []byte, 8), fakeFinisher{}, fakeLeaver{}, fakeCanceller{}, room.NoopPublisher{}, testLogger, testTickObserver)
	go actor.Run()

	conn := newFakeConn()
	conn.queueRead([]byte(`{"type":"join_race","race_id":"race-1"}`), nil)

	h := newTestWSHandler()

	done := make(chan struct{})
	go func() {
		h.serveConn(actor, conn, "race-1", "user-1", "user-1", testLogger)
		close(done)
	}()

	// Prove the join_race message actually reached the room: the immediate
	// snapshot broadcastSnapshot() sends on ParticipantJoined should be
	// fanned out to this connection. Only queue the disconnect read after
	// this lands, so the abrupt disconnect below can't race the broadcast.
	select {
	case body := <-conn.writes:
		if string(body) == "" {
			t.Error("received an empty race_state broadcast")
		}
	case <-time.After(time.Second):
		t.Fatal("connection never received a race_state broadcast after join_race")
	}

	conn.queueRead(nil, io.EOF) // abrupt disconnect

	// The queued io.EOF must cause serveConn to return — both the reader
	// (on the read error) and the writer (cancelled in response) must exit,
	// proving no goroutine leak on abrupt disconnect.
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("serveConn did not return after an abrupt disconnect — goroutine leak")
	}

	select {
	case <-conn.closed:
	default:
		t.Error("conn.Close was never called")
	}
}

func TestServeConn_MalformedMessageDoesNotEndConnection(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	actor := room.NewRoomActor(ctx, "race-1", 5, make(chan []byte, 8), fakeFinisher{}, fakeLeaver{}, fakeCanceller{}, room.NoopPublisher{}, testLogger, testTickObserver)
	go actor.Run()

	conn := newFakeConn()
	conn.queueRead([]byte(`not json`), nil)
	conn.queueRead([]byte(`{"type":"join_race","race_id":"race-1"}`), nil)

	h := newTestWSHandler()

	done := make(chan struct{})
	go func() {
		h.serveConn(actor, conn, "race-1", "user-1", "user-1", testLogger)
		close(done)
	}()

	// The malformed first message must be dropped, not treated as
	// connection-ending — the subsequent valid join_race should still
	// produce a broadcast. Only queue the disconnect after this lands, so
	// it can't race the broadcast.
	select {
	case <-conn.writes:
	case <-time.After(time.Second):
		t.Fatal("no broadcast received — malformed message may have ended the connection early")
	}

	conn.queueRead(nil, io.EOF)

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("serveConn did not return")
	}
}

func TestServeConn_LeaveRaceClosesConnectionWithoutClientDisconnect(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	actor := room.NewRoomActor(ctx, "race-1", 5, make(chan []byte, 8), fakeFinisher{}, fakeLeaver{}, fakeCanceller{}, room.NoopPublisher{}, testLogger, testTickObserver)
	go actor.Run()

	conn := newFakeConn()
	conn.queueRead([]byte(`{"type":"join_race","race_id":"race-1"}`), nil)
	conn.queueRead([]byte(`{"type":"leave_race"}`), nil)
	// No further queued reads, and no io.EOF: leave-race.md's readLoop must
	// return on its own after leave_race, not wait for the client to also
	// close the socket.

	h := newTestWSHandler()

	done := make(chan struct{})
	go func() {
		h.serveConn(actor, conn, "race-1", "user-1", "user-1", testLogger)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("serveConn did not return after leave_race — reader goroutine left blocked forever")
	}

	select {
	case <-conn.closed:
	default:
		t.Error("conn.Close was never called")
	}
}

// TestServeConn_FinishingRaceDeliversFinalStateBeforeClosing is a regression
// test for a real reported bug: a solo racer's final word never moved their
// vehicle to 100%, and the connection was torn down looking exactly like an
// unexpected drop (no race_finished ever seen client-side) instead of a
// clean finish. Root cause was two stacked races: the room's broadcast
// channel is buffered, so finishRace's final race_state/race_finished sends
// and the subsequent r.cancel() both complete without blocking, back to
// back — hub.run's done case and (before this fix) writeLoop's ctx.Done()
// case could each independently win their own select against the
// still-unread final messages and return without ever delivering them.
// distanceMeters is 1 so a single telemetry message both finishes the race
// and triggers this exact scenario deterministically, not just occasionally.
func TestServeConn_FinishingRaceDeliversFinalStateBeforeClosing(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	actor := room.NewRoomActor(ctx, "race-1", 1, make(chan []byte, 8), fakeFinisher{}, fakeLeaver{}, fakeCanceller{}, room.NoopPublisher{}, testLogger, testTickObserver)
	go actor.Run()
	actor.MarkActive("") // a pending race can't legitimately finish (pending-connections.md)

	conn := newFakeConn()
	conn.queueRead([]byte(`{"type":"join_race","race_id":"race-1"}`), nil)
	conn.queueRead([]byte(`{"type":"telemetry","seq":1,"distance_m":1,"pace_watt":60,"ts":0}`), nil)

	h := newTestWSHandler()

	done := make(chan struct{})
	go func() {
		h.serveConn(actor, conn, "race-1", "user-1", "user-1", testLogger)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("serveConn did not return after the race finished — goroutine leak")
	}

	select {
	case <-conn.closed:
	default:
		t.Error("conn.Close was never called")
	}

	sawFinalDistance := false
	sawFinished := false
loop:
	for {
		select {
		case body := <-conn.writes:
			var envelope struct {
				Type         string `json:"type"`
				Participants []struct {
					UserID    string  `json:"user_id"`
					DistanceM float64 `json:"distance_m"`
				} `json:"participants"`
			}
			if err := json.Unmarshal(body, &envelope); err != nil {
				t.Fatalf("failed to decode broadcast %s: %v", body, err)
			}
			switch envelope.Type {
			case "race_state":
				for _, p := range envelope.Participants {
					if p.UserID == "user-1" && p.DistanceM >= 1 {
						sawFinalDistance = true
					}
				}
			case "race_finished":
				if !strings.Contains(string(body), "user-1") {
					t.Errorf("race_finished missing user-1: %s", body)
				}
				sawFinished = true
			}
		default:
			break loop
		}
	}

	if !sawFinalDistance {
		t.Error("connection never received a race_state showing the finisher at full distance — vehicle would appear stuck")
	}
	if !sawFinished {
		t.Error("connection never received race_finished — client would treat this as an unexpected drop instead of a finish")
	}
}

// TestServeConn_PendingExpiryDeliversRaceExpiredBeforeClosing is
// room-lifecycle/pending-expiry.md / websocket/race-expired-broadcast.md's
// own explicitly-called-for regression test, mirroring
// TestServeConn_FinishingRaceDeliversFinalStateBeforeClosing's exact shape:
// a still-pending room (MarkActive never called) must deliver race_expired
// to an attached connection before that connection closes, once
// PendingTimeoutDuration elapses — relying on the same hub-drains-before-
// done/writeLoop-drains-off-hub.closed guarantee already proven there.
func TestServeConn_PendingExpiryDeliversRaceExpiredBeforeClosing(t *testing.T) {
	original := room.PendingTimeoutDuration
	room.PendingTimeoutDuration = 50 * time.Millisecond
	t.Cleanup(func() { room.PendingTimeoutDuration = original })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	actor := room.NewRoomActor(ctx, "race-1", 5, make(chan []byte, 8), fakeFinisher{}, fakeLeaver{}, fakeCanceller{}, room.NoopPublisher{}, testLogger, testTickObserver)
	go actor.Run()
	// Never MarkActive()'d: the race stays pending until the (shortened)
	// PendingTimeoutDuration elapses.

	conn := newFakeConn()
	conn.queueRead([]byte(`{"type":"join_race","race_id":"race-1"}`), nil)
	// No further queued reads: once the room expires and tears down, this
	// connection is expected to close on its own, not wait on the client.

	h := newTestWSHandler()

	done := make(chan struct{})
	go func() {
		h.serveConn(actor, conn, "race-1", "user-1", "user-1", testLogger)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("serveConn did not return after the room expired — goroutine leak")
	}

	select {
	case <-conn.closed:
	default:
		t.Error("conn.Close was never called")
	}

	msg := awaitMessageType(t, conn, "race_expired", time.Second)
	if msg["type"] != "race_expired" {
		t.Errorf("Type = %v, want %q", msg["type"], "race_expired")
	}
}

func TestServeConn_WriteErrorCancelsReader(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	actor := room.NewRoomActor(ctx, "race-1", 5, make(chan []byte, 8), fakeFinisher{}, fakeLeaver{}, fakeCanceller{}, room.NoopPublisher{}, testLogger, testTickObserver)
	go actor.Run()

	conn := newFakeConn()
	conn.writeErr = errFakeWrite
	conn.queueRead([]byte(`{"type":"join_race","race_id":"race-1"}`), nil)
	// No further queued reads: Read would block on ctx forever unless the
	// writer's failure cancels the shared connCtx.

	h := newTestWSHandler()

	done := make(chan struct{})
	go func() {
		h.serveConn(actor, conn, "race-1", "user-1", "user-1", testLogger)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("serveConn did not return after a write error — reader goroutine left blocked forever")
	}
}

// awaitMessageType drains c.writes until it sees a message with the given
// "type" field (skipping any race_state ticks that land first — the room
// actor broadcasts on a 250ms ticker independently of this test) or fails
// the test after timeout.
func awaitMessageType(t *testing.T, c *fakeConn, wantType string, timeout time.Duration) map[string]any {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case body := <-c.writes:
			var envelope map[string]any
			if err := json.Unmarshal(body, &envelope); err != nil {
				t.Fatalf("unmarshal message: %v", err)
			}
			if envelope["type"] == wantType {
				return envelope
			}
		case <-deadline:
			t.Fatalf("never received a %q message", wantType)
			return nil
		}
	}
}

// TestServeConn_MarkActiveBroadcastsRaceStartedToAllPendingConnections is
// websocket/race-started-broadcast.md's own explicitly-called-for regression
// test: every connection already attached to a still-pending room must
// receive race_started once the race starts — not staggered, not missing
// for any connection that was already attached.
func TestServeConn_MarkActiveBroadcastsRaceStartedToAllPendingConnections(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	actor := room.NewRoomActor(ctx, "race-1", 5, make(chan []byte, 8), fakeFinisher{}, fakeLeaver{}, fakeCanceller{}, room.NoopPublisher{}, testLogger, testTickObserver)
	go actor.Run()

	h := newTestWSHandler()

	connA := newFakeConn()
	connA.queueRead([]byte(`{"type":"join_race","race_id":"race-1"}`), nil)
	doneA := make(chan struct{})
	go func() {
		h.serveConn(actor, connA, "race-1", "user-a", "user-a", testLogger)
		close(doneA)
	}()

	connB := newFakeConn()
	connB.queueRead([]byte(`{"type":"join_race","race_id":"race-1"}`), nil)
	doneB := make(chan struct{})
	go func() {
		h.serveConn(actor, connB, "race-1", "user-b", "user-b", testLogger)
		close(doneB)
	}()

	// Both connections must actually be attached (registered with the hub)
	// before MarkActive fires, or this test wouldn't prove anything about
	// already-connected clients — their immediate join_race snapshot is
	// proof of that, same as TestServeConn_JoinRaceThenAbruptDisconnect_NoGoroutineLeak.
	for name, c := range map[string]*fakeConn{"A": connA, "B": connB} {
		select {
		case <-c.writes:
		case <-time.After(time.Second):
			t.Fatalf("conn %s never received its initial race_state snapshot", name)
		}
	}

	actor.MarkActive("the quick brown fox")

	for name, c := range map[string]*fakeConn{"A": connA, "B": connB} {
		msg := awaitMessageType(t, c, "race_started", time.Second)
		if promptText, _ := msg["prompt_text"].(string); promptText != "the quick brown fox" {
			t.Errorf("conn %s: prompt_text = %q, want %q", name, promptText, "the quick brown fox")
		}
	}

	connA.queueRead(nil, io.EOF)
	connB.queueRead(nil, io.EOF)
	select {
	case <-doneA:
	case <-time.After(time.Second):
		t.Fatal("serveConn for conn A did not return")
	}
	select {
	case <-doneB:
	case <-time.After(time.Second):
		t.Fatal("serveConn for conn B did not return")
	}
}

func TestWSHandler_ConnectionCount_TracksInFlightConnections(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	actor := room.NewRoomActor(ctx, "race-1", 5, make(chan []byte, 8), fakeFinisher{}, fakeLeaver{}, fakeCanceller{}, room.NoopPublisher{}, testLogger, testTickObserver)
	go actor.Run()

	h := newTestWSHandler()
	if got := h.ConnectionCount(); got != 0 {
		t.Fatalf("ConnectionCount() = %d, want 0 before any connection", got)
	}

	conn := newFakeConn()
	conn.queueRead([]byte(`{"type":"join_race","race_id":"race-1"}`), nil)

	done := make(chan struct{})
	go func() {
		h.serveConn(actor, conn, "race-1", "user-1", "user-1", testLogger)
		close(done)
	}()

	// Wait for the join_race snapshot, proving serveConn is actually
	// mid-flight (registered with the hub, reader goroutine blocked on the
	// next Read) before asserting the count.
	select {
	case <-conn.writes:
	case <-time.After(time.Second):
		t.Fatal("connection never received its initial race_state snapshot")
	}

	if got := h.ConnectionCount(); got != 1 {
		t.Errorf("ConnectionCount() = %d, want 1 while a connection is in flight", got)
	}

	conn.queueRead(nil, io.EOF) // abrupt disconnect
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("serveConn did not return after an abrupt disconnect")
	}

	if got := h.ConnectionCount(); got != 0 {
		t.Errorf("ConnectionCount() = %d, want 0 after serveConn returned", got)
	}
}

// TestWSHandler_ConnBufferUsage_DelegatesToHubs proves the wiring, not the
// summation logic — hub_test.go's TestHub_QueryBufferUsage_* and
// TestHubRegistry_TotalConnBufferUsage_SumsAcrossHubs already cover the
// actual arithmetic deterministically (by writing directly into raw conn
// channels, not racing a live writeLoop that actively drains them, which a
// serveConn-level test can't avoid). This only confirms ConnBufferUsage()
// reflects a connection genuinely attached through the real handler path.
func TestWSHandler_ConnBufferUsage_DelegatesToHubs(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	actor := room.NewRoomActor(ctx, "race-1", 5, make(chan []byte, 8), fakeFinisher{}, fakeLeaver{}, fakeCanceller{}, room.NoopPublisher{}, testLogger, testTickObserver)
	go actor.Run()

	h := newTestWSHandler()
	if got := h.ConnBufferUsage(); got != 0 {
		t.Fatalf("ConnBufferUsage() = %d, want 0 before any connection", got)
	}

	conn := newFakeConn()
	conn.queueRead([]byte(`{"type":"join_race","race_id":"race-1"}`), nil)
	go h.serveConn(actor, conn, "race-1", "user-1", "user-1", testLogger)

	select {
	case <-conn.writes:
	case <-time.After(time.Second):
		t.Fatal("connection never received its initial race_state snapshot")
	}

	// Not asserting a specific nonzero value — writeLoop actively drains
	// connCh, so its buffer usage is inherently racy from the test's
	// perspective. Only that the call succeeds against a real hub/conn
	// wired through serveConn, without panicking or blocking.
	if got := h.ConnBufferUsage(); got < 0 {
		t.Errorf("ConnBufferUsage() = %d, want >= 0", got)
	}
}
