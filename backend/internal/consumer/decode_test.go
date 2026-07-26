package consumer

import (
	"testing"
	"time"
)

func TestDecodeWorkoutSample_OK(t *testing.T) {
	ts := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	value := []byte(`{"race_id":"race-1","user_id":"user-1","ts":"` + ts.Format(time.RFC3339) + `","distance_m":42,"pace_watt":55.5}`)

	got, err := decodeWorkoutSample(value)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := WorkoutSample{RaceID: "race-1", UserID: "user-1", Ts: ts, DistanceM: 42, PaceWatt: 55.5}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestDecodeWorkoutSample_MalformedJSON(t *testing.T) {
	if _, err := decodeWorkoutSample([]byte(`not json`)); err == nil {
		t.Fatal("expected an error for malformed JSON")
	}
}

func TestDecodeRaceFinished_OK(t *testing.T) {
	rank := 1
	value := []byte(`{"race_id":"race-1","results":[{"user_id":"user-1","finish_rank":1,"finish_time_ms":null,"avg_pace_watt":60.2}]}`)

	got, err := decodeRaceFinished(value)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.RaceID != "race-1" {
		t.Fatalf("got race_id %q, want race-1", got.RaceID)
	}
	if len(got.Results) != 1 {
		t.Fatalf("got %d results, want 1", len(got.Results))
	}
	res := got.Results[0]
	if res.UserID != "user-1" || res.FinishRank == nil || *res.FinishRank != rank || res.FinishTimeMs != nil || res.AvgPaceWatt != 60.2 {
		t.Fatalf("unexpected result: %+v", res)
	}
}

func TestDecodeRaceFinished_MalformedJSON(t *testing.T) {
	if _, err := decodeRaceFinished([]byte(`not json`)); err == nil {
		t.Fatal("expected an error for malformed JSON")
	}
}
