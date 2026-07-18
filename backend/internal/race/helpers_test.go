package race_test

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/akkien/aviron/internal/race"
)

// fakeRepository is an in-memory race.RaceRepository used by both
// service_test.go and handler_test.go, so neither needs a real Postgres connection.
type fakeRepository struct {
	mu           sync.Mutex
	races        []race.Race
	participants map[string]bool
}

func newFakeRepository() *fakeRepository {
	return &fakeRepository{}
}

func (f *fakeRepository) CreateRace(ctx context.Context, name string, distanceMeters int, createdBy string) (race.Race, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	r := race.Race{
		// UUID-shaped so it satisfies the handler's isValidUUID check, the
		// same way a real Postgres-generated id would.
		ID:             fmt.Sprintf("00000000-0000-0000-0000-%012d", len(f.races)+1),
		Name:           name,
		DistanceMeters: distanceMeters,
		Status:         "pending",
		CreatedBy:      createdBy,
		CreatedAt:      time.Now(),
	}
	f.races = append(f.races, r)
	return r, nil
}

func (f *fakeRepository) GetRace(ctx context.Context, raceID string) (race.Race, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	for _, r := range f.races {
		if r.ID == raceID {
			return r, nil
		}
	}
	return race.Race{}, race.ErrRaceNotFound
}

func (f *fakeRepository) AddParticipant(ctx context.Context, raceID, userID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.participants == nil {
		f.participants = make(map[string]bool)
	}

	key := raceID + ":" + userID
	if f.participants[key] {
		return race.ErrAlreadyJoined
	}
	f.participants[key] = true
	return nil
}
