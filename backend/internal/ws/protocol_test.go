package ws

import (
	"encoding/json"
	"testing"

	"github.com/akkien/aviron/internal/room"
)

func TestDecodeClientMessage_JoinRace_OK(t *testing.T) {
	m, err := decodeClientMessage([]byte(`{"type":"join_race","race_id":"race-1"}`))
	if err != nil {
		t.Fatalf("decodeClientMessage() error = %v", err)
	}
	if m.Type != "join_race" {
		t.Errorf("Type = %q, want %q", m.Type, "join_race")
	}
	if m.RaceID != "race-1" {
		t.Errorf("RaceID = %q, want %q", m.RaceID, "race-1")
	}
}

func TestDecodeClientMessage_Telemetry_OK(t *testing.T) {
	m, err := decodeClientMessage([]byte(`{"type":"telemetry","seq":42,"distance_m":812,"pace_watt":210,"ts":1234}`))
	if err != nil {
		t.Fatalf("decodeClientMessage() error = %v", err)
	}
	if m.Type != "telemetry" {
		t.Errorf("Type = %q, want %q", m.Type, "telemetry")
	}
	if m.Seq != 42 {
		t.Errorf("Seq = %d, want 42", m.Seq)
	}
	if m.DistanceM != 812 {
		t.Errorf("DistanceM = %v, want 812", m.DistanceM)
	}
	if m.PaceWatt != 210 {
		t.Errorf("PaceWatt = %v, want 210", m.PaceWatt)
	}
	if m.TS != 1234 {
		t.Errorf("TS = %d, want 1234", m.TS)
	}
}

func TestDecodeClientMessage_MalformedJSON(t *testing.T) {
	_, err := decodeClientMessage([]byte(`not json`))
	if err == nil {
		t.Fatal("decodeClientMessage() error = nil, want an error for malformed JSON")
	}
}

func TestDecodeClientMessage_UnknownType(t *testing.T) {
	_, err := decodeClientMessage([]byte(`{"type":"ping"}`))
	if err == nil {
		t.Fatal("decodeClientMessage() error = nil, want an error for an unknown type")
	}
}

func TestDecodeClientMessage_EmptyType(t *testing.T) {
	_, err := decodeClientMessage([]byte(`{"race_id":"race-1"}`))
	if err == nil {
		t.Fatal("decodeClientMessage() error = nil, want an error for a missing/empty type")
	}
}

func TestClientMessage_ToRoomEvent_JoinRace(t *testing.T) {
	m := ClientMessage{Type: "join_race", RaceID: "race-1"}

	ev, err := m.toRoomEvent("user-1", "Alice")
	if err != nil {
		t.Fatalf("toRoomEvent() error = %v", err)
	}

	joined, ok := ev.(room.ParticipantJoined)
	if !ok {
		t.Fatalf("event type = %T, want room.ParticipantJoined", ev)
	}
	if joined.UserID != "user-1" {
		t.Errorf("UserID = %q, want %q", joined.UserID, "user-1")
	}
	if joined.DisplayName != "Alice" {
		t.Errorf("DisplayName = %q, want %q", joined.DisplayName, "Alice")
	}
}

func TestClientMessage_ToRoomEvent_Telemetry(t *testing.T) {
	m := ClientMessage{Type: "telemetry", Seq: 7, DistanceM: 12}

	ev, err := m.toRoomEvent("user-1", "Alice")
	if err != nil {
		t.Fatalf("toRoomEvent() error = %v", err)
	}

	telemetry, ok := ev.(room.TelemetryReceived)
	if !ok {
		t.Fatalf("event type = %T, want room.TelemetryReceived", ev)
	}
	if telemetry.UserID != "user-1" {
		t.Errorf("UserID = %q, want %q", telemetry.UserID, "user-1")
	}
	if telemetry.Seq != 7 {
		t.Errorf("Seq = %d, want 7", telemetry.Seq)
	}
	if telemetry.WordsCorrect != 12 {
		t.Errorf("WordsCorrect = %d, want 12", telemetry.WordsCorrect)
	}
}

func TestClientMessage_ToRoomEvent_UnknownType(t *testing.T) {
	m := ClientMessage{Type: "ping"}

	_, err := m.toRoomEvent("user-1", "Alice")
	if err == nil {
		t.Fatal("toRoomEvent() error = nil, want an error for an unknown type")
	}
}

func TestEncodeRaceFinishedMessage(t *testing.T) {
	results := []RaceResultJSON{
		{UserID: "user-1", FinishRank: 1, FinishTimeMs: 45000, AvgPaceWatt: 62.5},
		{UserID: "user-2", FinishRank: 2, FinishTimeMs: 51000, AvgPaceWatt: 55.1},
	}

	body, err := encodeRaceFinishedMessage(results)
	if err != nil {
		t.Fatalf("encodeRaceFinishedMessage() error = %v", err)
	}

	var msg RaceFinishedMessage
	if err := json.Unmarshal(body, &msg); err != nil {
		t.Fatalf("unmarshal encoded message: %v", err)
	}
	if msg.Type != "race_finished" {
		t.Errorf("Type = %q, want %q", msg.Type, "race_finished")
	}
	if len(msg.Results) != 2 {
		t.Fatalf("Results = %d, want 2", len(msg.Results))
	}
	if msg.Results[0].UserID != "user-1" || msg.Results[0].FinishRank != 1 {
		t.Errorf("Results[0] = %+v, want user-1 rank 1", msg.Results[0])
	}
}
