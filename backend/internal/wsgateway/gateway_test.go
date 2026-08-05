package wsgateway

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/akkien/aviron/internal/roomlocator"
)

// newTestBackend starts an httptest.Server identifying itself in every
// response via the X-Backend-Name header, and returns its bare host:port
// (what Gateway's backends/cache actually store and Director proxies to).
func newTestBackend(t *testing.T, name string) (addr string, closeFn func()) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Backend-Name", name)
		w.WriteHeader(http.StatusOK)
	}))
	return strings.TrimPrefix(srv.URL, "http://"), srv.Close
}

func TestGateway_RoomLessRequest_RoundRobins(t *testing.T) {
	addr1, close1 := newTestBackend(t, "b1")
	defer close1()
	addr2, close2 := newTestBackend(t, "b2")
	defer close2()

	gw := NewGateway(newFakeLocator(), StaticBackends{addr1, addr2}, 30*time.Second, testLogger)

	var got []string
	for i := 0; i < 4; i++ {
		req := httptest.NewRequest(http.MethodGet, "/races", nil)
		w := httptest.NewRecorder()
		gw.ServeHTTP(w, req)
		got = append(got, w.Result().Header.Get("X-Backend-Name"))
	}

	want := []string{"b1", "b2", "b1", "b2"}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("request %d served by %q, want %q (got sequence %v)", i, got[i], want[i], got)
		}
	}
}

func TestGateway_CacheMiss_CallsOwnerOncePopulatesCache(t *testing.T) {
	addr, closeFn := newTestBackend(t, "owner")
	defer closeFn()

	loc := newFakeLocator()
	loc.owners["race-1"] = addr
	gw := NewGateway(loc, StaticBackends{"unused:1"}, 30*time.Second, testLogger)

	req := httptest.NewRequest(http.MethodGet, "/races/race-1", nil)
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	if got := w.Result().Header.Get("X-Backend-Name"); got != "owner" {
		t.Fatalf("served by %q, want owner", got)
	}
	if loc.callCount() != 1 {
		t.Fatalf("Owner called %d times, want 1", loc.callCount())
	}

	gw.mu.RLock()
	_, cached := gw.cache["race-1"]
	gw.mu.RUnlock()
	if !cached {
		t.Fatal("expected race-1 to be cached after a resolved miss")
	}
}

func TestGateway_CacheHit_DoesNotCallOwnerAgain(t *testing.T) {
	addr, closeFn := newTestBackend(t, "owner")
	defer closeFn()

	loc := newFakeLocator()
	loc.owners["race-1"] = addr
	gw := NewGateway(loc, StaticBackends(nil), 30*time.Second, testLogger)

	for i := 0; i < 3; i++ {
		req := httptest.NewRequest(http.MethodGet, "/races/race-1", nil)
		w := httptest.NewRecorder()
		gw.ServeHTTP(w, req)
		if got := w.Result().Header.Get("X-Backend-Name"); got != "owner" {
			t.Fatalf("request %d served by %q, want owner", i, got)
		}
	}

	if loc.callCount() != 1 {
		t.Fatalf("Owner called %d times across 3 requests, want 1 (cache should serve the rest)", loc.callCount())
	}
}

// TestGateway_RoomLessRequest_EmptyBackends_Returns503 proves a room-less
// request fails cleanly with 503, not a panic (index out of range /
// modulo by zero), when the discovery source's pool is momentarily empty
// — possible now that the pool can change at runtime
// (dynamic-backend-discovery.md), unlike when it was a fixed,
// LoadConfig-validated non-empty slice.
func TestGateway_RoomLessRequest_EmptyBackends_Returns503(t *testing.T) {
	gw := NewGateway(newFakeLocator(), StaticBackends(nil), 30*time.Second, testLogger)

	req := httptest.NewRequest(http.MethodGet, "/races", nil)
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	if w.Result().StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", w.Result().StatusCode)
	}
}

func TestGateway_GenuineMiss_Returns404_ProxyNeverInvoked(t *testing.T) {
	gw := NewGateway(newFakeLocator(), StaticBackends(nil), 30*time.Second, testLogger)

	req := httptest.NewRequest(http.MethodGet, "/races/nonexistent", nil)
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	if w.Result().StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Result().StatusCode)
	}
}

func TestGateway_LookupError_Returns503(t *testing.T) {
	loc := newFakeLocator()
	loc.setOwnerErr(errors.New("redis unreachable"))
	gw := NewGateway(loc, StaticBackends(nil), 30*time.Second, testLogger)

	req := httptest.NewRequest(http.MethodGet, "/races/race-1", nil)
	w := httptest.NewRecorder()
	gw.ServeHTTP(w, req)

	if w.Result().StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", w.Result().StatusCode)
	}
}

// TestExtractRaceID_NoLongerHandlesWS confirms GET /ws is deliberately
// room-less from this REST proxy's own point of view (ws-gateway.md):
// WSHandler is registered as its own separate mux entry and terminates
// that connection directly, so a request for it never reaches Gateway's
// Director at all. Porting the deleted internal/racerouter's "/ws uses the
// race_id query param" branch into extractRaceID would be dead code that
// can never fire under this revision.
func TestExtractRaceID_NoLongerHandlesWS(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/ws?race_id=race-1", nil)
	raceID, ok := extractRaceID(req)
	if ok || raceID != "" {
		t.Errorf("extractRaceID(/ws?race_id=race-1) = (%q, %v), want (\"\", false)", raceID, ok)
	}
}

func TestExtractRaceID(t *testing.T) {
	cases := []struct {
		path       string
		wantRaceID string
		wantOK     bool
	}{
		{path: "/races/race-1", wantRaceID: "race-1", wantOK: true},
		{path: "/races/race-1/join", wantRaceID: "race-1", wantOK: true},
		{path: "/races/race-1/start", wantRaceID: "race-1", wantOK: true},
		{path: "/races", wantRaceID: "", wantOK: false},
		{path: "/auth/login", wantRaceID: "", wantOK: false},
		{path: "/leaderboard/me", wantRaceID: "", wantOK: false},
	}

	for _, tc := range cases {
		req := httptest.NewRequest(http.MethodGet, tc.path, nil)
		raceID, ok := extractRaceID(req)
		if raceID != tc.wantRaceID || ok != tc.wantOK {
			t.Errorf("extractRaceID(%q) = (%q, %v), want (%q, %v)", tc.path, raceID, ok, tc.wantRaceID, tc.wantOK)
		}
	}
}

// TestGateway_ConcurrentServeHTTP_RacesWatchRoomEvents gives -race the
// coverage ws-gateway.md's own Concurrency section calls for (ported
// unchanged from the deleted internal/racerouter): concurrent ServeHTTP
// reads racing the room:events subscriber goroutine's cache writes.
func TestGateway_ConcurrentServeHTTP_RacesWatchRoomEvents(t *testing.T) {
	addr, closeFn := newTestBackend(t, "owner")
	defer closeFn()

	loc := newFakeLocator()
	gw := NewGateway(loc, StaticBackends{addr}, 30*time.Second, testLogger)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go gw.WatchRoomEvents(ctx)

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			loc.events <- roomlocator.RoomEvent{
				Type:       roomlocator.RoomEventCreated,
				RaceID:     fmt.Sprintf("race-%d", i%5),
				InstanceID: addr,
			}
		}(i)
	}

	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/races/race-%d", i%5), nil)
			w := httptest.NewRecorder()
			gw.ServeHTTP(w, req)
		}(i)
	}
	wg.Wait()
}

// TestGateway_ConcurrentCacheMissesForSameRaceID_NoRace gives -race
// coverage for the other case the spec calls for: multiple concurrent
// cache misses for the same raceID racing each other's write-after-lookup.
func TestGateway_ConcurrentCacheMissesForSameRaceID_NoRace(t *testing.T) {
	addr, closeFn := newTestBackend(t, "owner")
	defer closeFn()

	loc := newFakeLocator()
	loc.owners["race-1"] = addr
	gw := NewGateway(loc, StaticBackends(nil), 30*time.Second, testLogger)

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodGet, "/races/race-1", nil)
			w := httptest.NewRecorder()
			gw.ServeHTTP(w, req)
		}()
	}
	wg.Wait()
}
