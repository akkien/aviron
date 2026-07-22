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
	return NewWSHandler(room.NewRegistry(), []byte("test-secret"), "http://localhost:5173")
}

func TestServeConn_JoinRaceThenAbruptDisconnect_NoGoroutineLeak(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	actor := room.NewRoomActor(ctx, "race-1", 5, make(chan []byte, 8), fakeFinisher{}, fakeLeaver{})
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

	actor := room.NewRoomActor(ctx, "race-1", 5, make(chan []byte, 8), fakeFinisher{}, fakeLeaver{})
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

	actor := room.NewRoomActor(ctx, "race-1", 5, make(chan []byte, 8), fakeFinisher{}, fakeLeaver{})
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

	actor := room.NewRoomActor(ctx, "race-1", 1, make(chan []byte, 8), fakeFinisher{}, fakeLeaver{})
	go actor.Run()
	actor.MarkActive() // a pending race can't legitimately finish (pending-connections.md)

	conn := newFakeConn()
	conn.queueRead([]byte(`{"type":"join_race","race_id":"race-1"}`), nil)
	conn.queueRead([]byte(`{"type":"telemetry","seq":1,"distance_m":1,"pace_watt":60,"ts":0}`), nil)

	h := newTestWSHandler()

	done := make(chan struct{})
	go func() {
		h.serveConn(actor, conn, "race-1", "user-1", "user-1")
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

func TestServeConn_WriteErrorCancelsReader(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	actor := room.NewRoomActor(ctx, "race-1", 5, make(chan []byte, 8), fakeFinisher{}, fakeLeaver{})
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
