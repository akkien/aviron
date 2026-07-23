package ws

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/golang-jwt/jwt/v5"

	"github.com/akkien/aviron/internal/room"
)

// This file exercises the real github.com/coder/websocket client/server
// round trip end to end (handshake, join_race, one race_state message) —
// everything else in this package tests against the fake wsConn double for
// speed and determinism, but this proves the real wire-level integration
// actually works.

func newIntegrationTestServer(t *testing.T, secret []byte) (*httptest.Server, *room.Registry) {
	t.Helper()
	registry := room.NewRegistry(testLogger, testTickObserver)
	handler := NewWSHandler(registry, secret, "http://localhost:5173", testLogger)
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return server, registry
}

func signIntegrationSessionToken(t *testing.T, secret []byte, raceID, userID string) string {
	t.Helper()
	claims := jwt.MapClaims{
		"race_id": raceID,
		"user_id": userID,
		"exp":     time.Now().Add(time.Hour).Unix(),
	}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(secret)
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	return signed
}

func TestIntegration_HandshakeJoinAndReceiveSnapshot(t *testing.T) {
	secret := []byte("test-secret")
	server, registry := newIntegrationTestServer(t, secret)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	registry.Spawn(ctx, "race-1", 5, fakeFinisher{}, fakeLeaver{}, fakeCanceller{})

	token := signIntegrationSessionToken(t, secret, "race-1", "user-1")
	url := "ws" + server.URL[len("http"):] + "/ws?race_id=race-1&session_token=" + token

	dialCtx, dialCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer dialCancel()
	conn, _, err := websocket.Dial(dialCtx, url, nil)
	if err != nil {
		t.Fatalf("Dial() error = %v", err)
	}
	defer conn.CloseNow()

	if err := conn.Write(dialCtx, websocket.MessageText, []byte(`{"type":"join_race","race_id":"race-1"}`)); err != nil {
		t.Fatalf("Write(join_race) error = %v", err)
	}

	readCtx, readCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer readCancel()
	_, data, err := conn.Read(readCtx)
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if len(data) == 0 {
		t.Error("received an empty race_state message")
	}

	conn.Close(websocket.StatusNormalClosure, "")
}

// TestIntegration_ConnectsToPendingRace documents pending-connections.md's
// grounding finding: GET /ws's rejection rule only ever checked whether an
// actor exists, never race status — since early-spawn.md moved Spawn to
// race creation, a still-pending race (actor.MarkActive() deliberately never
// called here) already has a running actor and is a valid attach target,
// not a rejection case.
func TestIntegration_ConnectsToPendingRace(t *testing.T) {
	secret := []byte("test-secret")
	server, registry := newIntegrationTestServer(t, secret)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	registry.Spawn(ctx, "race-1", 5, fakeFinisher{}, fakeLeaver{}, fakeCanceller{}) // pending: MarkActive() never called

	token := signIntegrationSessionToken(t, secret, "race-1", "user-1")
	url := "ws" + server.URL[len("http"):] + "/ws?race_id=race-1&session_token=" + token

	dialCtx, dialCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer dialCancel()
	conn, _, err := websocket.Dial(dialCtx, url, nil)
	if err != nil {
		t.Fatalf("Dial() error = %v, want a pending race's WebSocket connection to be accepted", err)
	}
	defer conn.CloseNow()

	if err := conn.Write(dialCtx, websocket.MessageText, []byte(`{"type":"join_race","race_id":"race-1"}`)); err != nil {
		t.Fatalf("Write(join_race) error = %v", err)
	}

	readCtx, readCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer readCancel()
	_, data, err := conn.Read(readCtx)
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if len(data) == 0 {
		t.Error("received an empty race_state message")
	}

	conn.Close(websocket.StatusNormalClosure, "")
}

func TestIntegration_RejectsInvalidToken(t *testing.T) {
	secret := []byte("test-secret")
	server, registry := newIntegrationTestServer(t, secret)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	registry.Spawn(ctx, "race-1", 5, fakeFinisher{}, fakeLeaver{}, fakeCanceller{})

	url := "ws" + server.URL[len("http"):] + "/ws?race_id=race-1&session_token=not-a-real-token"

	dialCtx, dialCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer dialCancel()
	_, resp, err := websocket.Dial(dialCtx, url, nil)
	if err == nil {
		t.Fatal("Dial() error = nil, want the handshake to be rejected")
	}
	if resp != nil && resp.StatusCode == 101 {
		t.Errorf("handshake was upgraded (status 101), want a rejection")
	}
}

func TestIntegration_RejectsRaceIDMismatch(t *testing.T) {
	secret := []byte("test-secret")
	server, registry := newIntegrationTestServer(t, secret)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	registry.Spawn(ctx, "race-1", 5, fakeFinisher{}, fakeLeaver{}, fakeCanceller{})

	// Token is valid for race-1, but the query string asks to join race-2.
	token := signIntegrationSessionToken(t, secret, "race-1", "user-1")
	url := "ws" + server.URL[len("http"):] + "/ws?race_id=race-2&session_token=" + token

	dialCtx, dialCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer dialCancel()
	_, _, err := websocket.Dial(dialCtx, url, nil)
	if err == nil {
		t.Fatal("Dial() error = nil, want the handshake to be rejected on race_id mismatch")
	}
}

func TestIntegration_RejectsReconnectAfterGracePeriodExpired(t *testing.T) {
	secret := []byte("test-secret")
	server, registry := newIntegrationTestServer(t, secret)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	actor := registry.Spawn(ctx, "race-1", 5, fakeFinisher{}, fakeLeaver{}, fakeCanceller{})

	// A second, still-racing participant keeps the room alive once user-1 is
	// evicted — race-completion/finish-race.md means an empty room finishes
	// and self-cancels, at which point IsEvicted always answers false ("room's
	// gone, nothing left to be evicted from"), which would make this test's
	// own setup unable to observe the eviction it's trying to simulate.
	actor.Send(room.ParticipantJoined{UserID: "user-2", DisplayName: "Bob"})

	// Simulate user-1 having joined, disconnected, and had their grace
	// period expire — without waiting the real 30s: reconnection/grace-period.md's
	// ParticipantEvicted is exported specifically so this is reachable from
	// here, the same as a real expiry would apply it.
	actor.Send(room.ParticipantJoined{UserID: "user-1", DisplayName: "Alice"})
	actor.Send(room.ParticipantDisconnected{UserID: "user-1"})
	actor.Send(room.ParticipantEvicted{UserID: "user-1"})

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && !actor.IsEvicted("user-1") {
		time.Sleep(5 * time.Millisecond)
	}
	if !actor.IsEvicted("user-1") {
		t.Fatal("setup failed: user-1 was not evicted")
	}

	token := signIntegrationSessionToken(t, secret, "race-1", "user-1")
	url := "ws" + server.URL[len("http"):] + "/ws?race_id=race-1&session_token=" + token

	dialCtx, dialCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer dialCancel()
	_, resp, err := websocket.Dial(dialCtx, url, nil)
	if err == nil {
		t.Fatal("Dial() error = nil, want the handshake to be rejected for an evicted user")
	}
	if resp != nil && resp.StatusCode == 101 {
		t.Errorf("handshake was upgraded (status 101), want a rejection")
	}
}

func TestIntegration_RejectsUnknownRace(t *testing.T) {
	secret := []byte("test-secret")
	server, _ := newIntegrationTestServer(t, secret)

	// No registry.Spawn call for this race — it's not running.
	token := signIntegrationSessionToken(t, secret, "race-never-started", "user-1")
	url := "ws" + server.URL[len("http"):] + "/ws?race_id=race-never-started&session_token=" + token

	dialCtx, dialCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer dialCancel()
	_, _, err := websocket.Dial(dialCtx, url, nil)
	if err == nil {
		t.Fatal("Dial() error = nil, want the handshake to be rejected for a race with no running actor")
	}
}
