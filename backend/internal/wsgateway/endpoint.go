package wsgateway

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/akkien/aviron/internal/roomrelay"
	"github.com/akkien/aviron/internal/ws"
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

// WSHandler serves GET /ws?race_id=...&session_token=.... No Handler/
// Service/Repository layering (same reasoning as room-actor-core.md and
// websocket/protocol.md): there's no DB round-trip here, only Redis
// lookups and a signed-JWT verification. Terminates the connection itself
// and relays decoded messages over internal/roomrelay
// (room-message-bus.md) instead of proxying the raw connection through to
// whichever race-service instance owns the room — ws-gateway.md's actual
// pivot relative to the deleted race-router.
type WSHandler struct {
	locator       RoomLocator
	relay         relayBus
	hubs          *raceHubRegistry
	jwtSecret     []byte
	allowedOrigin string
	logger        *slog.Logger
}

// NewWSHandler constructs a WSHandler. allowedOrigin is reused from the
// same config value the REST CORS middleware already uses — without it,
// coder/websocket's default same-origin check would reject the frontend's
// cross-origin WebSocket handshake in local dev. hubs is shared with
// whatever else in this process needs raceHubRegistry (nothing else does
// today, but constructing it here keeps WSHandler's own lifecycle
// self-contained).
func NewWSHandler(locator RoomLocator, relay relayBus, hubs *raceHubRegistry, jwtSecret []byte, allowedOrigin string, logger *slog.Logger) *WSHandler {
	return &WSHandler{
		locator:       locator,
		relay:         relay,
		hubs:          hubs,
		jwtSecret:     jwtSecret,
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

	// A genuine miss, mirroring Gateway.ServeHTTP's own REST-routing check
	// (via the same RoomLocator) — not part of ws-gateway.md's own numbered
	// step list, but without it a client would upgrade successfully against
	// a race that doesn't exist and simply never receive anything, since
	// there's no race-service subscriber on room.{race_id}.in/.out to
	// notice or report the mismatch.
	if _, found, err := h.locator.Owner(r.Context(), raceID); err != nil {
		h.logger.Error("wsgateway: owner lookup failed", slog.String("race_id", raceID), slog.Any("error", err))
		http.Error(w, "routing lookup failed", http.StatusServiceUnavailable)
		return
	} else if !found {
		http.Error(w, "race not found", http.StatusNotFound)
		return
	}

	// A user_id whose grace period already expired
	// (reconnection/grace-period.md) is rejected the same way an invalid
	// token is — not silently let back in as a fresh participant. Redis,
	// synchronous, bypassing internal/roomrelay entirely
	// (room-message-bus.md's "Evicted-reconnect checks bypass the bus
	// entirely").
	if evicted, err := h.locator.IsEvicted(r.Context(), raceID, userID); err != nil {
		h.logger.Error("wsgateway: evicted check failed", slog.String("race_id", raceID), slog.Any("error", err))
		http.Error(w, "routing lookup failed", http.StatusServiceUnavailable)
		return
	} else if evicted {
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
	// round-trip, contradicting this project's "no new Postgres access"
	// stance for the WS path.
	connLogger := h.logger.With(slog.String("race_id", raceID), slog.String("user_id", userID))
	h.serveConn(conn, raceID, userID, userID, connLogger)
}

// serveConn drives one connection until it's done, then returns — callers
// (ServeHTTP, and this file's tests) can rely on that return meaning both
// the reader and writer goroutines have actually exited, not just been
// told to.
func (h *WSHandler) serveConn(conn wsConn, raceID, userID, displayName string, logger *slog.Logger) {
	hub, err := h.hubs.attach(raceID)
	if err != nil {
		logger.Error("wsgateway: attach to race hub failed", slog.Any("error", err))
		conn.Close(websocket.StatusInternalError, "")
		return
	}
	defer h.hubs.detach(raceID)

	// Deliberately NOT tied to hub's own lifetime directly: this context is
	// purely for this connection's own reasons (read/write error).
	// Room-wide winding-down reaches it exclusively through hub.closed,
	// passed into writeLoop below — same separation the former in-process
	// serveConn already established between a connection's own context and
	// the room-wide hub.closed signal.
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
		readLoop(connCtx, conn, h.relay, raceID, userID, displayName, logger)
	}()
	go func() {
		defer wg.Done()
		defer cancel()
		writeLoop(connCtx, hub.closed, conn, connCh)
	}()
	wg.Wait()

	conn.Close(websocket.StatusNormalClosure, "")
}

// readLoop never touches room state directly — every decoded frame becomes
// an InboundEnvelope published on room.{race_id}.in, the only cross-process
// entry point into the room's state now (room-message-bus.md). Only
// ws.DecodeClientMessage is reused here, not ToRoomEvent — that conversion
// stays room-service-adapter.md's job, on the other side of the bus; this
// side only validates the frame is well-formed before ever publishing it.
func readLoop(ctx context.Context, conn wsConn, relay relayBus, raceID, userID, displayName string, logger *slog.Logger) {
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			// Read error covers both an abrupt close and ctx cancellation
			// (the writer side failing, or the race ending) — either way,
			// the participant is gone from this connection's perspective.
			// context.Background(), not ctx: ctx may already be cancelled
			// at exactly this point (that's often what caused Read to
			// return an error in the first place), and relay.PublishIn
			// checks ctx.Err() first — using the connection's own
			// (possibly-cancelled) ctx here would silently drop the one
			// notification this path exists to deliver, the same class of
			// bug room-service-adapter.md's drainBroadcast/cleanupWhenDone
			// already had to guard against on the publishing side.
			if pubErr := relay.PublishIn(context.Background(), raceID, roomrelay.InboundEnvelope{
				Kind: roomrelay.InboundKindDisconnected, RaceID: raceID, UserID: userID, DisplayName: displayName,
			}); pubErr != nil {
				logger.Error("wsgateway: publish disconnected failed", slog.Any("error", pubErr))
			} else {
				logger.Info("wsgateway: published", slog.String("race_id", raceID), slog.String("kind", string(roomrelay.InboundKindDisconnected)))
			}
			return
		}

		msg, err := ws.DecodeClientMessage(data)
		if err != nil {
			logger.Warn("wsgateway: dropping malformed message", slog.Any("error", err))
			continue
		}

		if err := relay.PublishIn(ctx, raceID, roomrelay.InboundEnvelope{
			Kind: roomrelay.InboundKindMessage, RaceID: raceID, UserID: userID, DisplayName: displayName, Message: data,
		}); err != nil {
			logger.Error("wsgateway: publish message failed", slog.Any("error", err))
			continue
		}
		logger.Info("wsgateway: published", slog.String("race_id", raceID), slog.String("kind", string(roomrelay.InboundKindMessage)))

		if msg.Type == "leave_race" {
			// An intentional quit (leave-race.md): the bus already has the
			// event, so there's nothing left to read from this participant
			// — close the connection from the server side too, rather than
			// waiting for the client to hang up (or not) on its own.
			return
		}
	}
}

// writeLoop owns connCh, this connection's slice of the race's fan-out (see
// raceHub) — draining it and writing frames out until ctx is done or
// hubClosed fires.
func writeLoop(ctx context.Context, hubClosed <-chan struct{}, conn wsConn, connCh <-chan []byte) {
	for {
		select {
		case msg := <-connCh:
			if !writeMsg(ctx, conn, msg) {
				return
			}
		case <-hubClosed:
			// raceHub.run only closes hub.closed after fully processing
			// every already-arrived broadcast (including the race's final
			// race_state/race_finished) — including forwarding them into
			// every registered connCh — so anything still meant for this
			// connection is already sitting in connCh's buffer by now. ctx
			// isn't tied to the race's own lifetime (see serveConn): drain
			// deterministically instead of leaving this select to race
			// hubClosed against connCh.
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
