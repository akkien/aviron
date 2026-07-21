package ws

import (
	"context"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/akkien/aviron/internal/room"
)

// writeTimeout bounds a single frame write so one hung client can't leave a
// writer goroutine (and the http.Handler it's tied to) blocked forever.
const writeTimeout = 5 * time.Second

// wsConn is the subset of *websocket.Conn this package depends on. Letting
// tests supply a fake implementation means the reader/writer/backpressure
// logic can be exercised without a real network connection — only the one
// end-to-end test in endpoint_integration_test.go needs the real thing.
type wsConn interface {
	Read(ctx context.Context) (websocket.MessageType, []byte, error)
	Write(ctx context.Context, typ websocket.MessageType, p []byte) error
	Close(code websocket.StatusCode, reason string) error
}

// WSHandler serves GET /ws?race_id=...&session_token=.... No Handler/Service
// /Repository layering (same reasoning as room-actor-core.md and
// websocket/protocol.md): there's no DB round-trip here, only an in-memory
// registry lookup and a signed-JWT verification.
type WSHandler struct {
	registry      *room.Registry
	jwtSecret     []byte
	hubs          *hubRegistry
	allowedOrigin string
}

// NewWSHandler constructs a WSHandler. allowedOrigin is reused from the same
// config value the REST CORS middleware already uses (config.CORSAllowedOrigin)
// — without it, coder/websocket's default same-origin check would reject the
// frontend's cross-origin WebSocket handshake in local dev.
func NewWSHandler(registry *room.Registry, jwtSecret []byte, allowedOrigin string) *WSHandler {
	return &WSHandler{
		registry:      registry,
		jwtSecret:     jwtSecret,
		hubs:          newHubRegistry(),
		allowedOrigin: allowedOrigin,
	}
}

func (h *WSHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raceID := r.URL.Query().Get("race_id")
	sessionToken := r.URL.Query().Get("session_token")

	userID, tokenRaceID, err := verifySessionToken(sessionToken, h.jwtSecret)
	if err != nil || tokenRaceID != raceID {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	actor, ok := h.registry.Get(raceID)
	if !ok {
		http.Error(w, "race not found", http.StatusNotFound)
		return
	}

	// A user_id whose grace period already expired (reconnection/grace-period.md)
	// is rejected the same way an invalid token is — not silently let back
	// in as a fresh participant.
	if actor.IsEvicted(userID) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		OriginPatterns: []string{h.allowedOrigin},
	})
	if err != nil {
		// Accept has already written an appropriate HTTP error response.
		return
	}

	// displayName falls back to userID: the session token only carries
	// race_id/user_id (internal/race/service.go's JoinRace), and looking up
	// users.display_name here would be this endpoint's only Postgres
	// round-trip, contradicting ws-endpoint.md's "no new Postgres access."
	h.serveConn(actor, conn, raceID, userID, userID)
}

// serveConn drives one connection until it's done, then returns — callers
// (ServeHTTP, and this file's tests) can rely on that return meaning both
// the reader and writer goroutines have actually exited, not just been told
// to.
func (h *WSHandler) serveConn(actor *room.RoomActor, conn wsConn, raceID, userID, displayName string) {
	hub := h.hubs.getOrCreate(raceID, actor)

	connCtx, cancel := context.WithCancel(actor.Context())
	defer cancel()

	connCh := make(chan []byte, connBufferSize)
	hub.registerConn(connCh)
	defer hub.unregisterConn(connCh)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		defer cancel()
		readLoop(connCtx, conn, actor, userID, displayName)
	}()
	go func() {
		defer wg.Done()
		defer cancel()
		writeLoop(connCtx, conn, connCh)
	}()
	wg.Wait()

	conn.Close(websocket.StatusNormalClosure, "")
}

// readLoop never touches room state directly — every decoded message
// becomes a room.RoomEvent handed to actor.Send, which is the only
// cross-goroutine entry point into the room's single-writer state.
func readLoop(ctx context.Context, conn wsConn, actor *room.RoomActor, userID, displayName string) {
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			// Read error covers both an abrupt close and ctx cancellation
			// (the writer side failing, or the room closing) — either way,
			// the participant is gone from this connection's perspective.
			actor.Send(room.ParticipantDisconnected{UserID: userID})
			return
		}

		msg, err := decodeClientMessage(data)
		if err != nil {
			log.Printf("ws: dropping malformed message from user %s: %v", userID, err)
			continue
		}

		ev, err := msg.toRoomEvent(userID, displayName)
		if err != nil {
			log.Printf("ws: dropping message from user %s: %v", userID, err)
			continue
		}

		actor.Send(ev)

		if _, ok := ev.(room.ParticipantLeft); ok {
			// An intentional quit (leave-race.md): the room already has the
			// event, so there's nothing left to read from this participant —
			// close the connection from the server side too, rather than
			// waiting for the client to hang up (or not) on its own.
			return
		}
	}
}

// writeLoop owns connCh, this connection's slice of the room's fan-out (see
// hub) — draining it and writing frames out until ctx is done.
func writeLoop(ctx context.Context, conn wsConn, connCh <-chan []byte) {
	for {
		select {
		case msg := <-connCh:
			writeCtx, cancel := context.WithTimeout(ctx, writeTimeout)
			err := conn.Write(writeCtx, websocket.MessageText, msg)
			cancel()
			if err != nil {
				return
			}
		case <-ctx.Done():
			return
		}
	}
}
