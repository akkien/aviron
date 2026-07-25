package racerouter

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

// RoomLocator is the small structural interface Router needs — mirrors
// internal/room.RoomLocator's naming/shape for a different capability set
// (different package, no import overlap: race-router never imports
// internal/room) — lets tests substitute a fake instead of a real
// *roomlocator.Locator.
type RoomLocator interface {
	Owner(ctx context.Context, raceID string) (string, bool, error)
	SubscribeRoomEvents(ctx context.Context) (<-chan roomlocator.RoomEvent, error)
}

type cacheEntry struct {
	instance  string
	expiresAt time.Time
}

type contextKey int

const targetContextKey contextKey = 0

// Router is an http.Handler that proxies every request to the race-service
// instance that owns its race_id (room-scoped), or round-robins across
// every configured backend (room-less) — race-router.md.
type Router struct {
	mu       sync.RWMutex
	cache    map[string]cacheEntry
	locator  RoomLocator
	backends []string
	next     atomic.Uint64
	cacheTTL time.Duration
	proxy    *httputil.ReverseProxy
	logger   *slog.Logger
}

// NewRouter constructs a Router. Run watchRoomEvents in its own goroutine
// (Run does this) to keep the cache warm from room:events — the fast path;
// resolveTarget's lazy Owner()+TTL is the safety net for whatever that
// subscription misses (a dropped connection, a router restart).
func NewRouter(locator RoomLocator, backends []string, cacheTTL time.Duration, logger *slog.Logger) *Router {
	rt := &Router{
		cache:    make(map[string]cacheEntry),
		locator:  locator,
		backends: backends,
		cacheTTL: cacheTTL,
		logger:   logger,
	}
	rt.proxy = &httputil.ReverseProxy{Director: rt.Director}
	return rt
}

// ServeHTTP resolves the target backend for r and delegates to the shared
// ReverseProxy. Resolution happens here, not in Director — Director's
// func(*http.Request) signature can't itself short-circuit a response, so
// the 404 (genuine miss) and 503 (lookup failure) cases below have to be
// handled before the proxy is ever invoked.
func (rt *Router) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raceID, roomScoped := extractRaceID(r)

	var target string
	if !roomScoped {
		target = rt.nextBackend()
	} else {
		addr, found, err := rt.resolveTarget(r.Context(), raceID)
		if err != nil {
			rt.logger.Error("racerouter: resolve target failed", slog.String("race_id", raceID), slog.Any("error", err))
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
	rt.proxy.ServeHTTP(w, r.WithContext(ctx))
}

// Director rewrites req.URL to the address ServeHTTP already resolved and
// stashed on the request's context.
func (rt *Router) Director(req *http.Request) {
	target, _ := req.Context().Value(targetContextKey).(string)
	req.URL.Scheme = "http"
	req.URL.Host = target
}

// extractRaceID reports the race_id a request is scoped to, if any. GET /ws
// carries it as a query param; /races/{id}... carries it as the second path
// segment. Everything else (register, login, POST /races, GET /races,
// leaderboard, ...) is room-less.
func extractRaceID(r *http.Request) (string, bool) {
	if r.URL.Path == "/ws" {
		raceID := r.URL.Query().Get("race_id")
		return raceID, raceID != ""
	}

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
// immediately; a miss consults the registry directly and writes through the
// cache. Three-way result, not two: a genuine miss (found=false, err=nil)
// and a lookup failure (err != nil) must produce different HTTP statuses,
// so ServeHTTP needs to tell them apart rather than collapsing both into a
// single bool.
func (rt *Router) resolveTarget(ctx context.Context, raceID string) (string, bool, error) {
	rt.mu.RLock()
	entry, ok := rt.cache[raceID]
	rt.mu.RUnlock()
	if ok && time.Now().Before(entry.expiresAt) {
		return entry.instance, true, nil
	}

	instanceID, found, err := rt.locator.Owner(ctx, raceID)
	if err != nil {
		return "", false, err
	}
	if !found {
		return "", false, nil
	}

	rt.mu.Lock()
	rt.cache[raceID] = cacheEntry{instance: instanceID, expiresAt: time.Now().Add(rt.cacheTTL)}
	rt.mu.Unlock()
	return instanceID, true, nil
}

func (rt *Router) nextBackend() string {
	n := rt.next.Add(1)
	return rt.backends[(n-1)%uint64(len(rt.backends))]
}

// watchRoomEvents subscribes to room:events for the router's entire process
// lifetime, keeping the cache warm as rooms are created/removed. Blocks
// until ctx is done or the subscription ends; run it in its own goroutine.
func (rt *Router) watchRoomEvents(ctx context.Context) {
	events, err := rt.locator.SubscribeRoomEvents(ctx)
	if err != nil {
		rt.logger.Error("racerouter: subscribe room events failed", slog.Any("error", err))
		return
	}
	for ev := range events {
		switch ev.Type {
		case roomlocator.RoomEventCreated:
			rt.mu.Lock()
			rt.cache[ev.RaceID] = cacheEntry{instance: ev.InstanceID, expiresAt: time.Now().Add(rt.cacheTTL)}
			rt.mu.Unlock()
		case roomlocator.RoomEventRemoved:
			rt.mu.Lock()
			delete(rt.cache, ev.RaceID)
			rt.mu.Unlock()
		}
	}
}
