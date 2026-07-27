package wsgateway

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/akkien/aviron/internal/roomlocator"
)

// RoomLocator is the small structural interface Gateway/WSHandler need
// against Redis — mirrors internal/room.RoomLocator's naming/shape for a
// different capability set (different package, no import overlap:
// ws-gateway never imports internal/room). Owner/SubscribeRoomEvents are
// ported unchanged from the deleted internal/racerouter.RoomLocator;
// IsEvicted is new — the connection-time reconnect-eviction check
// (room-message-bus.md's "Evicted-reconnect checks bypass the bus
// entirely", ws-gateway.md) — added to this same interface rather than a
// second one, matching the spec's own Data section (one locator field
// serves both Owner() routing lookups and the evicted check, against the
// same Redis instance).
type RoomLocator interface {
	Owner(ctx context.Context, raceID string) (string, bool, error)
	SubscribeRoomEvents(ctx context.Context) (<-chan roomlocator.RoomEvent, error)
	IsEvicted(ctx context.Context, raceID, userID string) (bool, error)
}

type cacheEntry struct {
	instance  string
	expiresAt time.Time
}

type contextKey int

const targetContextKey contextKey = 0

// Gateway is an http.Handler that proxies every REST request to the
// race-service instance that owns its race_id (room-scoped), or
// round-robins across every configured backend (room-less) —
// ws-gateway.md, ported unchanged from the deleted
// internal/racerouter.Router. GET /ws is registered as its own mux entry
// (WSHandler, endpoint.go) and never reaches this proxy at all — see
// extractRaceID.
type Gateway struct {
	mu       sync.RWMutex
	cache    map[string]cacheEntry
	locator  RoomLocator
	backends []string
	next     atomic.Uint64
	cacheTTL time.Duration
	proxy    *httputil.ReverseProxy
	logger   *slog.Logger
}

// NewGateway constructs a Gateway. Run WatchRoomEvents in its own goroutine
// to keep the cache warm from room:events — the fast path; resolveTarget's
// lazy Owner()+TTL is the safety net for whatever that subscription misses
// (a dropped connection, a gateway restart).
func NewGateway(locator RoomLocator, backends []string, cacheTTL time.Duration, logger *slog.Logger) *Gateway {
	gw := &Gateway{
		cache:    make(map[string]cacheEntry),
		locator:  locator,
		backends: backends,
		cacheTTL: cacheTTL,
		logger:   logger,
	}
	gw.proxy = &httputil.ReverseProxy{Director: gw.Director}
	return gw
}

// ServeHTTP resolves the target backend for r and delegates to the shared
// ReverseProxy. Resolution happens here, not in Director — Director's
// func(*http.Request) signature can't itself short-circuit a response, so
// the 404 (genuine miss) and 503 (lookup failure) cases below have to be
// handled before the proxy is ever invoked.
func (gw *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raceID, roomScoped := extractRaceID(r)

	var target string
	if !roomScoped {
		target = gw.nextBackend()
	} else {
		addr, found, err := gw.resolveTarget(r.Context(), raceID)
		if err != nil {
			gw.logger.Error("wsgateway: resolve target failed", slog.String("race_id", raceID), slog.Any("error", err))
			http.Error(w, "routing lookup failed", http.StatusServiceUnavailable)
			return
		}
		if !found {
			http.Error(w, "race not found", http.StatusNotFound)
			return
		}
		target = addr
	}

	ctx := context.WithValue(r.Context(), targetContextKey, target)
	gw.proxy.ServeHTTP(w, r.WithContext(ctx))
}

// Director rewrites req.URL to the address ServeHTTP already resolved and
// stashed on the request's context.
func (gw *Gateway) Director(req *http.Request) {
	target, _ := req.Context().Value(targetContextKey).(string)
	req.URL.Scheme = "http"
	req.URL.Host = target
}

// extractRaceID reports the race_id a REST request is scoped to, if any.
// /races/{id}... carries it as the second path segment. Everything else
// (register, login, POST /races, GET /races, leaderboard, ...) is
// room-less. Deliberately no GET /ws case, unlike the deleted
// internal/racerouter's version: ws-gateway.md terminates that connection
// itself (WSHandler, registered separately on the mux) rather than
// proxying it, so a request for it never reaches this REST proxy's
// Director at all — porting that branch here would be dead code that can
// never fire.
func extractRaceID(r *http.Request) (string, bool) {
	const prefix = "/races/"
	if !strings.HasPrefix(r.URL.Path, prefix) {
		return "", false
	}
	rest := strings.TrimPrefix(r.URL.Path, prefix)
	if rest == "" {
		return "", false
	}
	raceID, _, _ := strings.Cut(rest, "/")
	return raceID, raceID != ""
}

// resolveTarget resolves raceID to a backend address. A cache hit returns
// immediately; a miss consults the registry directly and writes through
// the cache. Three-way result, not two: a genuine miss (found=false,
// err=nil) and a lookup failure (err != nil) must produce different HTTP
// statuses, so ServeHTTP needs to tell them apart rather than collapsing
// both into a single bool.
func (gw *Gateway) resolveTarget(ctx context.Context, raceID string) (string, bool, error) {
	gw.mu.RLock()
	entry, ok := gw.cache[raceID]
	gw.mu.RUnlock()
	if ok && time.Now().Before(entry.expiresAt) {
		return entry.instance, true, nil
	}

	instanceID, found, err := gw.locator.Owner(ctx, raceID)
	if err != nil {
		return "", false, err
	}
	if !found {
		return "", false, nil
	}

	gw.mu.Lock()
	gw.cache[raceID] = cacheEntry{instance: instanceID, expiresAt: time.Now().Add(gw.cacheTTL)}
	gw.mu.Unlock()
	return instanceID, true, nil
}

func (gw *Gateway) nextBackend() string {
	n := gw.next.Add(1)
	return gw.backends[(n-1)%uint64(len(gw.backends))]
}

// WatchRoomEvents subscribes to room:events for the gateway's entire
// process lifetime, keeping the cache warm as rooms are created/removed.
// Blocks until ctx is done or the subscription ends; run it in its own
// goroutine.
func (gw *Gateway) WatchRoomEvents(ctx context.Context) {
	events, err := gw.locator.SubscribeRoomEvents(ctx)
	if err != nil {
		gw.logger.Error("wsgateway: subscribe room events failed", slog.Any("error", err))
		return
	}
	for ev := range events {
		switch ev.Type {
		case roomlocator.RoomEventCreated:
			gw.mu.Lock()
			gw.cache[ev.RaceID] = cacheEntry{instance: ev.InstanceID, expiresAt: time.Now().Add(gw.cacheTTL)}
			gw.mu.Unlock()
		case roomlocator.RoomEventRemoved:
			gw.mu.Lock()
			delete(gw.cache, ev.RaceID)
			gw.mu.Unlock()
		}
	}
}
