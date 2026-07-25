package racerouter

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/akkien/aviron/internal/roomlocator"
)

var testLogger = slog.New(slog.NewTextHandler(io.Discard, nil))

// fakeLocator is a fake RoomLocator (no real Redis) mirroring this
// project's established fake-repository testing convention.
type fakeLocator struct {
	mu         sync.Mutex
	owners     map[string]string
	ownerCalls int
	ownerErr   error
	events     chan roomlocator.RoomEvent
	subErr     error
}

func newFakeLocator() *fakeLocator {
	return &fakeLocator{
		owners: make(map[string]string),
		events: make(chan roomlocator.RoomEvent, 8),
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
	if f.subErr != nil {
		return nil, f.subErr
	}
	return f.events, nil
}

func (f *fakeLocator) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ownerCalls
}

// newTestBackend starts an httptest.Server identifying itself in every
// response via the X-Backend-Name header, and returns its bare host:port
// (what Router's backends/cache actually store and Director proxies to).
func newTestBackend(t *testing.T, name string) (addr string, closeFn func()) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Backend-Name", name)
		w.WriteHeader(http.StatusOK)
	}))
	return strings.TrimPrefix(srv.URL, "http://"), srv.Close
}

func TestRouter_RoomLessRequest_RoundRobins(t *testing.T) {
	addr1, close1 := newTestBackend(t, "b1")
	defer close1()
	addr2, close2 := newTestBackend(t, "b2")
	defer close2()

	rt := NewRouter(newFakeLocator(), []string{addr1, addr2}, 30*time.Second, testLogger)

	var got []string
	for i := 0; i < 4; i++ {
		req := httptest.NewRequest(http.MethodGet, "/races", nil)
		w := httptest.NewRecorder()
		rt.ServeHTTP(w, req)
		got = append(got, w.Result().Header.Get("X-Backend-Name"))
	}

	want := []string{"b1", "b2", "b1", "b2"}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("request %d served by %q, want %q (got sequence %v)", i, got[i], want[i], got)
		}
	}
}

func TestRouter_CacheMiss_CallsOwnerOncePopulatesCache(t *testing.T) {
	addr, closeFn := newTestBackend(t, "owner")
	defer closeFn()

	loc := newFakeLocator()
	loc.owners["race-1"] = addr
	rt := NewRouter(loc, []string{"unused:1"}, 30*time.Second, testLogger)

	req := httptest.NewRequest(http.MethodGet, "/races/race-1", nil)
	w := httptest.NewRecorder()
	rt.ServeHTTP(w, req)

	if got := w.Result().Header.Get("X-Backend-Name"); got != "owner" {
		t.Fatalf("served by %q, want owner", got)
	}
	if loc.callCount() != 1 {
		t.Fatalf("Owner called %d times, want 1", loc.callCount())
	}

	rt.mu.RLock()
	_, cached := rt.cache["race-1"]
	rt.mu.RUnlock()
	if !cached {
		t.Fatal("expected race-1 to be cached after a resolved miss")
	}
}

func TestRouter_CacheHit_DoesNotCallOwnerAgain(t *testing.T) {
	addr, closeFn := newTestBackend(t, "owner")
	defer closeFn()

	loc := newFakeLocator()
	loc.owners["race-1"] = addr
	rt := NewRouter(loc, nil, 30*time.Second, testLogger)

	for i := 0; i < 3; i++ {
		req := httptest.NewRequest(http.MethodGet, "/races/race-1", nil)
		w := httptest.NewRecorder()
		rt.ServeHTTP(w, req)
		if got := w.Result().Header.Get("X-Backend-Name"); got != "owner" {
			t.Fatalf("request %d served by %q, want owner", i, got)
		}
	}

	if loc.callCount() != 1 {
		t.Fatalf("Owner called %d times across 3 requests, want 1 (cache should serve the rest)", loc.callCount())
	}
}

func TestRouter_GenuineMiss_Returns404_ProxyNeverInvoked(t *testing.T) {
	rt := NewRouter(newFakeLocator(), nil, 30*time.Second, testLogger)

	req := httptest.NewRequest(http.MethodGet, "/races/nonexistent", nil)
	w := httptest.NewRecorder()
	rt.ServeHTTP(w, req)

	if w.Result().StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Result().StatusCode)
	}
}

func TestRouter_LookupError_Returns503(t *testing.T) {
	loc := newFakeLocator()
	loc.ownerErr = errors.New("redis unreachable")
	rt := NewRouter(loc, nil, 30*time.Second, testLogger)

	req := httptest.NewRequest(http.MethodGet, "/races/race-1", nil)
	w := httptest.NewRecorder()
	rt.ServeHTTP(w, req)

	if w.Result().StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", w.Result().StatusCode)
	}
}

func TestRouter_WSRequest_UsesRaceIDQueryParam(t *testing.T) {
	addr, closeFn := newTestBackend(t, "owner")
	defer closeFn()

	loc := newFakeLocator()
	loc.owners["race-1"] = addr
	rt := NewRouter(loc, nil, 30*time.Second, testLogger)

	req := httptest.NewRequest(http.MethodGet, "/ws?race_id=race-1&session_token=tok", nil)
	w := httptest.NewRecorder()
	rt.ServeHTTP(w, req)

	if got := w.Result().Header.Get("X-Backend-Name"); got != "owner" {
		t.Fatalf("served by %q, want owner", got)
	}
}

func TestExtractRaceID(t *testing.T) {
	cases := []struct {
		path       string
		query      string
		wantRaceID string
		wantOK     bool
	}{
		{path: "/ws", query: "race_id=race-1", wantRaceID: "race-1", wantOK: true},
		{path: "/ws", query: "", wantRaceID: "", wantOK: false},
		{path: "/races/race-1", wantRaceID: "race-1", wantOK: true},
		{path: "/races/race-1/join", wantRaceID: "race-1", wantOK: true},
		{path: "/races/race-1/start", wantRaceID: "race-1", wantOK: true},
		{path: "/races", wantRaceID: "", wantOK: false},
		{path: "/auth/login", wantRaceID: "", wantOK: false},
		{path: "/leaderboard/me", wantRaceID: "", wantOK: false},
	}

	for _, tc := range cases {
		url := tc.path
		if tc.query != "" {
			url += "?" + tc.query
		}
		req := httptest.NewRequest(http.MethodGet, url, nil)
		raceID, ok := extractRaceID(req)
		if raceID != tc.wantRaceID || ok != tc.wantOK {
			t.Errorf("extractRaceID(%q) = (%q, %v), want (%q, %v)", url, raceID, ok, tc.wantRaceID, tc.wantOK)
		}
	}
}

// TestRouter_ConcurrentServeHTTP_RacesWatchRoomEvents gives -race the
// coverage the spec calls for: concurrent ServeHTTP reads racing the
// room:events subscriber goroutine's cache writes.
func TestRouter_ConcurrentServeHTTP_RacesWatchRoomEvents(t *testing.T) {
	addr, closeFn := newTestBackend(t, "owner")
	defer closeFn()

	loc := newFakeLocator()
	rt := NewRouter(loc, []string{addr}, 30*time.Second, testLogger)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go rt.watchRoomEvents(ctx)

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
			rt.ServeHTTP(w, req)
		}(i)
	}
	wg.Wait()
}

// TestRouter_ConcurrentCacheMissesForSameRaceID_NoRace gives -race coverage
// for the other case the spec calls for: multiple concurrent cache misses
// for the same raceID racing each other's write-after-lookup.
func TestRouter_ConcurrentCacheMissesForSameRaceID_NoRace(t *testing.T) {
	addr, closeFn := newTestBackend(t, "owner")
	defer closeFn()

	loc := newFakeLocator()
	loc.owners["race-1"] = addr
	rt := NewRouter(loc, nil, 30*time.Second, testLogger)

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodGet, "/races/race-1", nil)
			w := httptest.NewRecorder()
			rt.ServeHTTP(w, req)
		}()
	}
	wg.Wait()
}
