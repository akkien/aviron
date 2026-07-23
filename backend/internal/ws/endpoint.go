package ws

import (
	"context"
	"log/slog"
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
	logger        *slog.Logger
}

// NewWSHandler constructs a WSHandler. allowedOrigin is reused from the same
// config value the REST CORS middleware already uses (config.CORSAllowedOrigin)
// — without it, coder/websocket's default same-origin check would reject the
// frontend's cross-origin WebSocket handshake in local dev.
func NewWSHandler(registry *room.Registry, jwtSecret []byte, allowedOrigin string, logger *slog.Logger) *WSHandler {
	return &WSHandler{
		registry:      registry,
		jwtSecret:     jwtSecret,
		hubs:          newHubRegistry(),
		allowedOrigin: allowedOrigin,
		logger:        logger,
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

	// Only checks that an actor exists, not its status — a pending room
	// actor (spawned at race creation, early-spawn.md) is an intentionally
	// valid attach target, not an oversight: it's what lets every
	// participant hold a live connection before the race starts
	// (pending-connections.md).
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
	connLogger := h.logger.With(slog.String("race_id", raceID), slog.String("user_id", userID))
	h.serveConn(actor, conn, raceID, userID, userID, connLogger)
}

// serveConn drives one connection until it's done, then returns — callers
// (ServeHTTP, and this file's tests) can rely on that return meaning both
// the reader and writer goroutines have actually exited, not just been told
// to.
func (h *WSHandler) serveConn(actor *room.RoomActor, conn wsConn, raceID, userID, displayName string, logger *slog.Logger) {
	hub := h.hubs.getOrCreate(raceID, actor)

	// Deliberately NOT context.WithCancel(actor.Context()): that would fire
	// the instant the room's context is cancelled — at essentially the same
	// moment hub.closed does, with no ordering between the two, since both
	// are direct observers of the same cancellation. hub.closed is only
	// guaranteed to close *after* hub.run has fully drained and forwarded
	// every pending broadcast (including the room's final
	// race_state/race_finished) into every registered connCh; a connCtx
	// racing that signal independently could still win and tear this
	// connection down before writeLoop ever reads what hub just forwarded —
	// the same lost-final-message bug this was meant to fix, just one layer
	// up. Cancellation here is purely for this connection's own reasons
	// (read/write error); room-wide winding-down reaches it exclusively
	// through hub.closed, passed into writeLoop below.
	connCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	connCh := make(chan []byte, connBufferSize)
	hub.registerConn(connCh)
	defer hub.unregisterConn(connCh)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		defer cancel()
		readLoop(connCtx, conn, actor, userID, displayName, logger)
	}()
	go func() {
		defer wg.Done()
		defer cancel()
		writeLoop(connCtx, hub.closed, conn, connCh)
	}()
	wg.Wait()

	conn.Close(websocket.StatusNormalClosure, "")
}

// readLoop never touches room state directly — every decoded message
// becomes a room.RoomEvent handed to actor.Send, which is the only
// cross-goroutine entry point into the room's single-writer state.
func readLoop(ctx context.Context, conn wsConn, actor *room.RoomActor, userID, displayName string, logger *slog.Logger) {
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
			logger.Warn("dropping malformed message", slog.Any("error", err))
			continue
		}

		ev, err := msg.toRoomEvent(userID, displayName)
		if err != nil {
			logger.Warn("dropping message", slog.Any("error", err))
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
// hub) — draining it and writing frames out until ctx is done or hubClosed
// fires.
func writeLoop(ctx context.Context, hubClosed <-chan struct{}, conn wsConn, connCh <-chan []byte) {
	for {
		select {
		case msg := <-connCh:
			if !writeMsg(ctx, conn, msg) {
				return
			}
		case <-hubClosed:
			// hub.run only closes hub.closed after fully draining and
			// forwarding every pending broadcast — including the room's
			// final race_state/race_finished — into every registered
			// connCh, so anything still meant for this connection is
			// already sitting in connCh's buffer by now. ctx isn't tied to
			// the room's own context (see serveConn), so it's still valid
			// here: drain deterministically instead of leaving this select
			// to race hubClosed against connCh the same way hub.run's own
			// broadcast/done select used to.
			for {
				select {
				case msg := <-connCh:
					if !writeMsg(ctx, conn, msg) {
						return
					}
				default:
					return
				}
			}
		case <-ctx.Done():
			return
		}
	}
}

func writeMsg(ctx context.Context, conn wsConn, msg []byte) bool {
	writeCtx, cancel := context.WithTimeout(ctx, writeTimeout)
	err := conn.Write(writeCtx, websocket.MessageText, msg)
	cancel()
	return err == nil
}
