package wsgateway

import (
	"context"
	"errors"
	"log/slog"
	"sync"

	"github.com/coder/websocket"

	"github.com/akkien/aviron/internal/roomlocator"
)

// testLogger discards output — this package's tests assert on connection
// plumbing and delivered messages, not log lines.
var testLogger = slog.New(slog.DiscardHandler)

// fakeLocator is a RoomLocator test double (no real Redis), mirroring this
// project's established fake-repository testing convention — merges what
// the deleted internal/racerouter's own fakeLocator needed (Owner call
// counting, injectable lookup/subscribe errors, a real events channel for
// WatchRoomEvents coverage) with WSHandler's IsEvicted need.
type fakeLocator struct {
	mu         sync.Mutex
	owners     map[string]string
	ownerCalls int
	ownerErr   error
	evicted    map[string]bool
	events     chan roomlocator.RoomEvent
	subErr     error
}

func newFakeLocator() *fakeLocator {
	return &fakeLocator{
		owners:  make(map[string]string),
		evicted: make(map[string]bool),
		events:  make(chan roomlocator.RoomEvent, 8),
	}
}

func (f *fakeLocator) Owner(ctx context.Context, raceID string) (string, bool, error) {
	f.mu.Lock()
	f.ownerCalls++
	err := f.ownerErr
	instance, ok := f.owners[raceID]
	f.mu.Unlock()
	if err != nil {
		return "", false, err
	}
	return instance, ok, nil
}

func (f *fakeLocator) SubscribeRoomEvents(ctx context.Context) (<-chan roomlocator.RoomEvent, error) {
	f.mu.Lock()
	err := f.subErr
	f.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return f.events, nil
}

func (f *fakeLocator) IsEvicted(ctx context.Context, raceID, userID string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.evicted[raceID+"/"+userID], nil
}

func (f *fakeLocator) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ownerCalls
}

// setOwner marks raceID as owned by instance, satisfying WSHandler's
// race-exists check and Gateway's routing lookup.
func (f *fakeLocator) setOwner(raceID, instance string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.owners[raceID] = instance
}

// setEvicted marks userID as evicted from raceID.
func (f *fakeLocator) setEvicted(raceID, userID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.evicted[raceID+"/"+userID] = true
}

// setOwnerErr injects a lookup failure into every subsequent Owner call.
func (f *fakeLocator) setOwnerErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ownerErr = err
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
