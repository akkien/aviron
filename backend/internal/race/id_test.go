package race_test

import (
	"regexp"
	"testing"

	"github.com/akkien/aviron/internal/race"
)

var base58RaceIDPattern = regexp.MustCompile(`^[1-9A-HJ-NP-Za-km-z]{12}$`)

func TestGenerateRaceID_MatchesExpectedShape(t *testing.T) {
	for i := 0; i < 1000; i++ {
		id, err := race.GenerateRaceID()
		if err != nil {
			t.Fatalf("GenerateRaceID() error = %v", err)
		}
		if len(id) != 12 {
			t.Fatalf("len(id) = %d, want 12 (id = %q)", len(id), id)
		}
		if !base58RaceIDPattern.MatchString(id) {
			t.Fatalf("id = %q, want 12 base58 characters (no 0/O/I/l)", id)
		}
	}
}

func TestGenerateRaceID_Unique(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 1000; i++ {
		id, err := race.GenerateRaceID()
		if err != nil {
			t.Fatalf("GenerateRaceID() error = %v", err)
		}
		if seen[id] {
			t.Fatalf("GenerateRaceID() produced a duplicate id %q across 1000 calls", id)
		}
		seen[id] = true
	}
}
