package wsgateway

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/akkien/aviron/internal/roomrelay"
)

func newTestWSHandler(locator RoomLocator, relay *roomrelay.FakeBus) *WSHandler {
	return NewWSHandler(locator, relay, NewRaceHubRegistry(context.Background(), relay, testLogger), []byte("test-secret"), "http://localhost:5173", testLogger)
}

func TestServeConn_JoinRaceThenAbruptDisconnect_NoGoroutineLeak(t *testing.T) {
	relay := roomrelay.NewFakeBus()
	in, _, err := relay.SubscribeIn(context.Background(), "race-1")
	if err != nil {
		t.Fatalf("SubscribeIn: %v", err)
	}

	conn := newFakeConn()
	conn.queueRead([]byte(`{"type":"join_race","race_id":"race-1"}`), nil)

	h := newTestWSHandler(newFakeLocator(), relay)

	done := make(chan struct{})
	go func() {
		h.serveConn(conn, "race-1", "user-1", "user-1", testLogger)
		close(done)
	}()

	// Prove the join_race frame actually reached room.race-1.in. Only queue
	// the disconnect read after this lands, so it can't race the publish.
	select {
	case env := <-in:
		if env.Kind != roomrelay.InboundKindMessage || env.UserID != "user-1" {
			t.Fatalf("unexpected inbound envelope: %+v", env)
		}
	case <-time.After(time.Second):
		t.Fatal("join_race frame was never published onto room.race-1.in")
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

	// The disconnect must also be published, using a context independent of
	// the now-cancelled connection context (see readLoop's own comment).
	select {
	case env := <-in:
		if env.Kind != roomrelay.InboundKindDisconnected || env.UserID != "user-1" {
			t.Fatalf("unexpected inbound envelope: %+v", env)
		}
	case <-time.After(time.Second):
		t.Fatal("disconnect was never published onto room.race-1.in")
	}
}

func TestServeConn_MalformedMessageDoesNotEndConnection(t *testing.T) {
	relay := roomrelay.NewFakeBus()
	in, _, err := relay.SubscribeIn(context.Background(), "race-1")
	if err != nil {
		t.Fatalf("SubscribeIn: %v", err)
	}

	conn := newFakeConn()
	conn.queueRead([]byte(`not json`), nil)
	conn.queueRead([]byte(`{"type":"join_race","race_id":"race-1"}`), nil)

	h := newTestWSHandler(newFakeLocator(), relay)

	done := make(chan struct{})
	go func() {
		h.serveConn(conn, "race-1", "user-1", "user-1", testLogger)
		close(done)
	}()

	// The malformed first message must be dropped, not published and not
	// connection-ending — the subsequent valid join_race should still be
	// published. Only queue the disconnect after this lands.
	select {
	case env := <-in:
		if env.Kind != roomrelay.InboundKindMessage {
			t.Fatalf("unexpected inbound envelope: %+v", env)
		}
	case <-time.After(time.Second):
		t.Fatal("no publish received — malformed message may have ended the connection early")
	}

	conn.queueRead(nil, io.EOF)

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("serveConn did not return")
	}
}

func TestServeConn_LeaveRaceClosesConnectionWithoutClientDisconnect(t *testing.T) {
	relay := roomrelay.NewFakeBus()

	conn := newFakeConn()
	conn.queueRead([]byte(`{"type":"join_race","race_id":"race-1"}`), nil)
	conn.queueRead([]byte(`{"type":"leave_race"}`), nil)
	// No further queued reads, and no io.EOF: leave-race.md's readLoop must
	// return on its own after leave_race, not wait for the client to also
	// close the socket.

	h := newTestWSHandler(newFakeLocator(), relay)

	done := make(chan struct{})
	go func() {
		h.serveConn(conn, "race-1", "user-1", "user-1", testLogger)
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

func TestServeConn_WriteErrorCancelsReader(t *testing.T) {
	relay := roomrelay.NewFakeBus()

	conn := newFakeConn()
	conn.writeErr = errFakeWrite
	conn.queueRead([]byte(`{"type":"join_race","race_id":"race-1"}`), nil)
	// No further queued reads: Read would block on ctx forever unless the
	// writer's failure cancels the shared connCtx.

	h := newTestWSHandler(newFakeLocator(), relay)

	// Subscribe before starting serveConn's goroutine — FakeBus has no
	// replay, so subscribing after risks missing the join publish below if
	// the reader goroutine gets there first.
	in, _, err := relay.SubscribeIn(context.Background(), "race-1")
	if err != nil {
		t.Fatalf("SubscribeIn: %v", err)
	}

	done := make(chan struct{})
	go func() {
		h.serveConn(conn, "race-1", "user-1", "user-1", testLogger)
		close(done)
	}()

	// Wait for attach (registerConn) to actually happen before publishing,
	// proven by the join_race frame reaching room.race-1.in — otherwise the
	// broadcast below could race ahead of registration and never reach
	// writeLoop at all.
	select {
	case <-in:
	case <-time.After(time.Second):
		t.Fatal("join_race was never published")
	}

	// Publish one broadcast so writeLoop actually attempts a write (and
	// fails) rather than sitting idle waiting for one.
	if err := relay.PublishOut(context.Background(), "race-1", roomrelay.OutboundEnvelope{
		Kind: roomrelay.OutboundKindBroadcast, RaceID: "race-1", Payload: []byte(`{"type":"race_state"}`),
	}); err != nil {
		t.Fatalf("PublishOut: %v", err)
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("serveConn did not return after a write error — reader goroutine left blocked forever")
	}
}

func TestServeConn_BroadcastReachesSocket(t *testing.T) {
	relay := roomrelay.NewFakeBus()

	conn := newFakeConn()
	conn.queueRead([]byte(`{"type":"join_race","race_id":"race-1"}`), nil)

	h := newTestWSHandler(newFakeLocator(), relay)

	// Subscribe before starting serveConn's goroutine — FakeBus has no
	// replay, so subscribing after risks missing the join publish below if
	// the reader goroutine gets there first.
	in, _, err := relay.SubscribeIn(context.Background(), "race-1")
	if err != nil {
		t.Fatalf("SubscribeIn: %v", err)
	}

	done := make(chan struct{})
	go func() {
		h.serveConn(conn, "race-1", "user-1", "user-1", testLogger)
		close(done)
	}()

	// Wait for the reader to actually publish the join, proving attach (and
	// therefore registerConn) already happened before publishing the
	// broadcast below — otherwise this test wouldn't prove the fan-out path
	// deterministically.
	select {
	case <-in:
	case <-time.After(time.Second):
		t.Fatal("join_race was never published")
	}

	if err := relay.PublishOut(context.Background(), "race-1", roomrelay.OutboundEnvelope{
		Kind: roomrelay.OutboundKindBroadcast, RaceID: "race-1", Payload: []byte(`{"type":"race_state"}`),
	}); err != nil {
		t.Fatalf("PublishOut: %v", err)
	}

	select {
	case body := <-conn.writes:
		if string(body) != `{"type":"race_state"}` {
			t.Errorf("conn received %q, want the broadcast payload", body)
		}
	case <-time.After(time.Second):
		t.Fatal("connection never received the broadcast")
	}

	conn.queueRead(nil, io.EOF)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("serveConn did not return")
	}
}

func TestServeConn_RoomClosedClosesConnectionWithoutClientDisconnect(t *testing.T) {
	relay := roomrelay.NewFakeBus()

	conn := newFakeConn()
	conn.queueRead([]byte(`{"type":"join_race","race_id":"race-1"}`), nil)
	// No further queued reads, and no io.EOF: room_closed alone must be
	// enough to tear this connection down.

	h := newTestWSHandler(newFakeLocator(), relay)

	// Subscribe before starting serveConn's goroutine — FakeBus has no
	// replay, so subscribing after risks missing the join publish below if
	// the reader goroutine gets there first.
	in, _, err := relay.SubscribeIn(context.Background(), "race-1")
	if err != nil {
		t.Fatalf("SubscribeIn: %v", err)
	}

	done := make(chan struct{})
	go func() {
		h.serveConn(conn, "race-1", "user-1", "user-1", testLogger)
		close(done)
	}()

	select {
	case <-in:
	case <-time.After(time.Second):
		t.Fatal("join_race was never published")
	}

	if err := relay.PublishOut(context.Background(), "race-1", roomrelay.OutboundEnvelope{
		Kind: roomrelay.OutboundKindRoomClosed, RaceID: "race-1",
	}); err != nil {
		t.Fatalf("PublishOut: %v", err)
	}

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("serveConn did not return after room_closed — writer goroutine left blocked forever")
	}

	select {
	case <-conn.closed:
	default:
		t.Error("conn.Close was never called")
	}
}
