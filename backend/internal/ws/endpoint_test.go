package ws

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/akkien/aviron/internal/room"
)

func newTestWSHandler() *WSHandler {
	return NewWSHandler(room.NewRegistry(), []byte("test-secret"), "http://localhost:5173")
}

func TestServeConn_JoinRaceThenAbruptDisconnect_NoGoroutineLeak(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	actor := room.NewRoomActor(ctx, "race-1", "some prompt text", 5, make(chan []byte, 8), fakeFinisher{})
	go actor.Run()

	conn := newFakeConn()
	conn.queueRead([]byte(`{"type":"join_race","race_id":"race-1"}`), nil)

	h := newTestWSHandler()

	done := make(chan struct{})
	go func() {
		h.serveConn(actor, conn, "race-1", "user-1", "user-1")
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

	actor := room.NewRoomActor(ctx, "race-1", "some prompt text", 5, make(chan []byte, 8), fakeFinisher{})
	go actor.Run()

	conn := newFakeConn()
	conn.queueRead([]byte(`not json`), nil)
	conn.queueRead([]byte(`{"type":"join_race","race_id":"race-1"}`), nil)

	h := newTestWSHandler()

	done := make(chan struct{})
	go func() {
		h.serveConn(actor, conn, "race-1", "user-1", "user-1")
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

	actor := room.NewRoomActor(ctx, "race-1", "some prompt text", 5, make(chan []byte, 8), fakeFinisher{})
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
		h.serveConn(actor, conn, "race-1", "user-1", "user-1")
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
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	actor := room.NewRoomActor(ctx, "race-1", "some prompt text", 5, make(chan []byte, 8), fakeFinisher{})
	go actor.Run()

	conn := newFakeConn()
	conn.writeErr = errFakeWrite
	conn.queueRead([]byte(`{"type":"join_race","race_id":"race-1"}`), nil)
	// No further queued reads: Read would block on ctx forever unless the
	// writer's failure cancels the shared connCtx.

	h := newTestWSHandler()

	done := make(chan struct{})
	go func() {
		h.serveConn(actor, conn, "race-1", "user-1", "user-1")
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("serveConn did not return after a write error — reader goroutine left blocked forever")
	}
}
