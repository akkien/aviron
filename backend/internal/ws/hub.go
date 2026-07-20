package ws

import (
	"sync"

	"github.com/akkien/aviron/internal/room"
)

// connBufferSize bounds each connection's own outbound channel. A slow
// client's buffer filling up means that connection drops new broadcasts
// (see hub.run's non-blocking send) rather than ever stalling the shared
// fan-out loop or the room actor behind it.
const connBufferSize = 8

// hub fans a single room's RoomActor.Broadcast() channel out to every
// connection currently attached to that room. RoomActor.Broadcast() returns
// one channel per room — if every connection's writer goroutine read from it
// directly, each broadcast message would be delivered to exactly one
// arbitrary connection (Go channels distribute, they don't fan out), not all
// of them. hub exists solely to fix that.
//
// Like RoomActor itself, hub uses a single goroutine (run) as the only
// mutator of its connection set, driven by channels rather than a mutex —
// consistent with this project's single-writer concurrency style.
type hub struct {
	register   chan chan []byte
	unregister chan chan []byte
	closed     chan struct{}
}

// newHub starts fanning broadcast out to registered connections until done
// fires (the room actor's context — see RoomActor.Context). onClose runs
// exactly once, after run's loop exits, so the caller (hubRegistry) can
// forget this hub instead of leaking a map entry for a room that's gone.
func newHub(broadcast <-chan []byte, done <-chan struct{}, onClose func()) *hub {
	h := &hub{
		register:   make(chan chan []byte),
		unregister: make(chan chan []byte),
		closed:     make(chan struct{}),
	}
	go h.run(broadcast, done, onClose)
	return h
}

func (h *hub) run(broadcast <-chan []byte, done <-chan struct{}, onClose func()) {
	defer onClose()
	defer close(h.closed)

	conns := make(map[chan []byte]struct{})
	for {
		select {
		case msg := <-broadcast:
			for c := range conns {
				select {
				case c <- msg:
				default:
					// That connection's buffer is full — drop for it only;
					// every other connection in the room still gets this
					// broadcast, and the room actor was never blocked.
				}
			}
		case c := <-h.register:
			conns[c] = struct{}{}
		case c := <-h.unregister:
			delete(conns, c)
		case <-done:
			return
		}
	}
}

// registerConn attaches c to the fan-out. Safe to call even if the hub has
// already closed (room gone) — it just becomes a no-op instead of blocking
// forever on an unread channel.
func (h *hub) registerConn(c chan []byte) {
	select {
	case h.register <- c:
	case <-h.closed:
	}
}

// unregisterConn detaches c. Same closed-hub guard as registerConn.
func (h *hub) unregisterConn(c chan []byte) {
	select {
	case h.unregister <- c:
	case <-h.closed:
	}
}

// hubRegistry lazily creates one hub per race_id and hands out the same one
// to every connection that attaches to that race, cleaning up once the
// room's hub closes. Kept separate from room.Registry (which only ever maps
// race_id to *RoomActor) so internal/room stays free of WebSocket-specific
// fan-out concerns.
type hubRegistry struct {
	mu   sync.Mutex
	hubs map[string]*hub
}

func newHubRegistry() *hubRegistry {
	return &hubRegistry{hubs: make(map[string]*hub)}
}

func (hr *hubRegistry) getOrCreate(raceID string, actor *room.RoomActor) *hub {
	hr.mu.Lock()
	defer hr.mu.Unlock()

	if h, ok := hr.hubs[raceID]; ok {
		return h
	}

	h := newHub(actor.Broadcast(), actor.Context().Done(), func() {
		hr.mu.Lock()
		delete(hr.hubs, raceID)
		hr.mu.Unlock()
	})
	hr.hubs[raceID] = h
	return h
}
