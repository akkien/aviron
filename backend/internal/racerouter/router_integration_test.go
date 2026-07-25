package racerouter

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestRouterIntegration_RoundRobinAndRoomScoped is this spec's own
// integration-style test: a real Router served behind a real
// httptest.Server (an actual TCP round trip via net/http.Client, not just
// ServeHTTP + httptest.NewRecorder), proxying to two more real
// httptest.Server backends — confirming both the round-robin and
// room-scoped paths land on the expected backend end to end.
func TestRouterIntegration_RoundRobinAndRoomScoped(t *testing.T) {
	addr1, close1 := newTestBackend(t, "b1")
	defer close1()
	addr2, close2 := newTestBackend(t, "b2")
	defer close2()

	loc := newFakeLocator()
	loc.owners["race-1"] = addr2
	rt := NewRouter(loc, []string{addr1, addr2}, 30*time.Second, testLogger)

	routerSrv := httptest.NewServer(rt)
	defer routerSrv.Close()
	client := routerSrv.Client()

	// Room-less requests round robin across both backends.
	wantSeq := []string{"b1", "b2", "b1"}
	for i, want := range wantSeq {
		resp, err := client.Get(routerSrv.URL + "/races")
		if err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
		got := resp.Header.Get("X-Backend-Name")
		resp.Body.Close()
		if got != want {
			t.Errorf("room-less request %d served by %q, want %q", i, got, want)
		}
	}

	// A room-scoped request lands on the specific instance Owner() reports,
	// not wherever round robin would have sent it next.
	resp, err := client.Get(routerSrv.URL + "/races/race-1")
	if err != nil {
		t.Fatalf("room-scoped request: %v", err)
	}
	defer resp.Body.Close()
	if got := resp.Header.Get("X-Backend-Name"); got != "b2" {
		t.Errorf("room-scoped request served by %q, want b2 (race-1's real owner)", got)
	}
}

// TestRouterIntegration_WebSocketUpgradeForwarded proves the one piece this
// design leans on ReverseProxy for: a real 101 Switching Protocols handshake
// against the resolved backend is forwarded through unchanged.
func TestRouterIntegration_WebSocketUpgradeForwarded(t *testing.T) {
	wsSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Fatal("ResponseWriter doesn't support hijacking")
		}
		conn, buf, err := hj.Hijack()
		if err != nil {
			t.Fatalf("hijack: %v", err)
		}
		defer conn.Close()
		buf.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
		buf.Flush()
	}))
	defer wsSrv.Close()
	wsAddr := strings.TrimPrefix(wsSrv.URL, "http://")

	loc := newFakeLocator()
	loc.owners["race-1"] = wsAddr
	rt := NewRouter(loc, nil, 30*time.Second, testLogger)

	routerSrv := httptest.NewServer(rt)
	defer routerSrv.Close()

	req, err := http.NewRequest(http.MethodGet, routerSrv.URL+"/ws?race_id=race-1", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")

	resp, err := routerSrv.Client().Transport.RoundTrip(req)
	if err != nil {
		t.Fatalf("round trip: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("status = %d, want 101 Switching Protocols", resp.StatusCode)
	}
}
