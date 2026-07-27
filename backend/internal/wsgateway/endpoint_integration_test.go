package wsgateway

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/golang-jwt/jwt/v5"

	"github.com/akkien/aviron/internal/roomrelay"
)

// This file exercises the real github.com/coder/websocket client/server
// round trip end to end (handshake, join_race, one race_state message) —
// everything else in this package tests against the fake wsConn double for
// speed and determinism, but this proves the real wire-level integration
// actually works. race-service itself is simulated with a small fake-echo
// goroutine (startFakeRaceService below) publishing directly onto
// internal/roomrelay, per ws-gateway.md's own Testing section: "a fake
// race-service side that just echoes bus messages back."

func newIntegrationTestServer(t *testing.T, secret []byte) (*httptest.Server, *fakeLocator, *roomrelay.FakeBus) {
	t.Helper()
	locator := newFakeLocator()
	relay := roomrelay.NewFakeBus()
	handler := NewWSHandler(locator, relay, NewRaceHubRegistry(context.Background(), relay, testLogger), secret, "http://localhost:5173", testLogger)
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return server, locator, relay
}

// startFakeRaceService subscribes to raceID's inbound subject and, for
// every join_race message it sees, publishes back one canned race_state
// broadcast — just enough of a fake race-service to prove a client's frame
// round-trips through this gateway and back out again over a real NATS-
// shaped (here, in-memory) bus, not that any real room logic runs.
func startFakeRaceService(t *testing.T, relay *roomrelay.FakeBus, raceID string) {
	t.Helper()
	in, _, err := relay.SubscribeIn(context.Background(), raceID)
	if err != nil {
		t.Fatalf("SubscribeIn: %v", err)
	}
	go func() {
		for env := range in {
			if env.Kind != roomrelay.InboundKindMessage {
				continue
			}
			_ = relay.PublishOut(context.Background(), raceID, roomrelay.OutboundEnvelope{
				Kind: roomrelay.OutboundKindBroadcast, RaceID: raceID,
				Payload: []byte(`{"type":"race_state","tick":1,"participants":[]}`),
			})
		}
	}()
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
	server, locator, relay := newIntegrationTestServer(t, secret)
	locator.setOwner("race-1", "race-service-a:8080")
	startFakeRaceService(t, relay, "race-1")

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
	var body struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(data, &body); err != nil || body.Type != "race_state" {
		t.Errorf("received %s, want a race_state message", data)
	}

	conn.Close(websocket.StatusNormalClosure, "")
}

func TestIntegration_RejectsInvalidToken(t *testing.T) {
	secret := []byte("test-secret")
	server, locator, _ := newIntegrationTestServer(t, secret)
	locator.setOwner("race-1", "race-service-a:8080")

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
	server, locator, _ := newIntegrationTestServer(t, secret)
	locator.setOwner("race-1", "race-service-a:8080")
	locator.setOwner("race-2", "race-service-a:8080")

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

func TestIntegration_RejectsReconnectAfterEviction(t *testing.T) {
	secret := []byte("test-secret")
	server, locator, _ := newIntegrationTestServer(t, secret)
	locator.setOwner("race-1", "race-service-a:8080")
	locator.setEvicted("race-1", "user-1")

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
	server, _, _ := newIntegrationTestServer(t, secret)

	// No locator.setOwner call for this race — it's not running anywhere.
	token := signIntegrationSessionToken(t, secret, "race-never-started", "user-1")
	url := "ws" + server.URL[len("http"):] + "/ws?race_id=race-never-started&session_token=" + token

	dialCtx, dialCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer dialCancel()
	_, _, err := websocket.Dial(dialCtx, url, nil)
	if err == nil {
		t.Fatal("Dial() error = nil, want the handshake to be rejected for a race no instance owns")
	}
}
