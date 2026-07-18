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
	mu    sync.Mutex
	races []race.Race
}

func newFakeRepository() *fakeRepository {
	return &fakeRepository{}
}

func (f *fakeRepository) CreateRace(ctx context.Context, name string, distanceMeters int, createdBy string) (race.Race, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	r := race.Race{
		ID:             fmt.Sprintf("race-%d", len(f.races)+1),
		Name:           name,
		DistanceMeters: distanceMeters,
		Status:         "pending",
		CreatedBy:      createdBy,
		CreatedAt:      time.Now(),
	}
	f.races = append(f.races, r)
	return r, nil
}
