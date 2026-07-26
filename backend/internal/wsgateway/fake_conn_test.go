package wsgateway

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/coder/websocket"

	"github.com/akkien/aviron/internal/room"
)

// testLogger discards output — this package's tests assert on connection
// plumbing and delivered messages, not log lines.
var testLogger = slog.New(slog.DiscardHandler)

// testTickObserver discards tick-latency observations — this package's
// tests assert on connection plumbing and delivered messages, not metrics.
type testTickObserverType struct{}

func (testTickObserverType) ObserveTick(d time.Duration) {}

var testTickObserver = testTickObserverType{}

// fakeFinisher satisfies room.RaceFinisher without touching Postgres — this
// package's tests exercise connection plumbing, not
// race-completion/finish-race.md's persistence step.
type fakeFinisher struct{}

func (fakeFinisher) FinishRace(ctx context.Context, raceID string, distanceMeters int, results []room.ParticipantResult) error {
	return nil
}

// fakeLeaver satisfies room.RaceLeaver without touching Postgres, same
// reasoning as fakeFinisher.
type fakeLeaver struct{}

func (fakeLeaver) LeaveRace(ctx context.Context, raceID, userID string) error {
	return nil
}

// fakeCanceller satisfies room.RaceCanceller without touching Postgres, same
// reasoning as fakeFinisher.
type fakeCanceller struct{}

func (fakeCanceller) CancelRace(ctx context.Context, raceID string) error {
	return nil
}

// fakeConn is a wsConn test double: Read returns pre-queued results (or
// blocks on ctx like a real connection would once queued reads run out),
// and Write records what was sent (or fails, if writeErr is set). This lets
// endpoint_test.go exercise readLoop/writeLoop/serveConn's goroutine
// lifecycle and backpressure handling without a real network connection.
type fakeConn struct {
	reads    chan fakeReadResult
	writes   chan []byte
	writeErr error
	closed   chan struct{}
}

type fakeReadResult struct {
	data []byte
	err  error
}

func newFakeConn() *fakeConn {
	return &fakeConn{
		reads:  make(chan fakeReadResult, 16),
		writes: make(chan []byte, 16),
		closed: make(chan struct{}),
	}
}

func (c *fakeConn) queueRead(data []byte, err error) {
	c.reads <- fakeReadResult{data: data, err: err}
}

func (c *fakeConn) Read(ctx context.Context) (websocket.MessageType, []byte, error) {
	select {
	case res := <-c.reads:
		return websocket.MessageText, res.data, res.err
	case <-ctx.Done():
		return 0, nil, ctx.Err()
	}
}

func (c *fakeConn) Write(ctx context.Context, typ websocket.MessageType, p []byte) error {
	if c.writeErr != nil {
		return c.writeErr
	}
	select {
	case c.writes <- append([]byte(nil), p...):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *fakeConn) Close(code websocket.StatusCode, reason string) error {
	close(c.closed)
	return nil
}

var errFakeWrite = errors.New("fake write error")
