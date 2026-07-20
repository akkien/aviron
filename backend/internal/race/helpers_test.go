package race_test

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/akkien/aviron/internal/race"
)

// participantRecord tracks a join, including which race it belongs to and
// when it happened, so fakeRepository can both detect duplicate joins and
// reconstruct a race's participant list.
type participantRecord struct {
	raceID   string
	userID   string
	joinedAt time.Time
}

// fakeRepository is an in-memory race.RaceRepository used by both
// service_test.go and handler_test.go, so neither needs a real Postgres connection.
type fakeRepository struct {
	mu           sync.Mutex
	races        []race.Race
	participants []participantRecord
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

	for _, p := range f.participants {
		if p.raceID == raceID && p.userID == userID {
			return race.ErrAlreadyJoined
		}
	}
	f.participants = append(f.participants, participantRecord{
		raceID:   raceID,
		userID:   userID,
		joinedAt: time.Now(),
	})
	return nil
}

func (f *fakeRepository) CountParticipants(ctx context.Context, raceID string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	count := 0
	for _, p := range f.participants {
		if p.raceID == raceID {
			count++
		}
	}
	return count, nil
}

func (f *fakeRepository) StartRace(ctx context.Context, raceID, promptText string) (race.Race, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	for i := range f.races {
		if f.races[i].ID == raceID {
			now := time.Now()
			f.races[i].Status = "active"
			f.races[i].StartedAt = &now
			f.races[i].PromptText = &promptText
			return f.races[i], nil
		}
	}
	return race.Race{}, race.ErrRaceNotFound
}

func (f *fakeRepository) GetRaceText(ctx context.Context, raceID string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	for _, r := range f.races {
		if r.ID == raceID {
			if r.PromptText == nil {
				return "", race.ErrPromptNotReady
			}
			return *r.PromptText, nil
		}
	}
	return "", race.ErrRaceNotFound
}

func (f *fakeRepository) GetRaceWithParticipants(ctx context.Context, raceID string) (race.RaceDetail, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	var found *race.Race
	for i := range f.races {
		if f.races[i].ID == raceID {
			found = &f.races[i]
			break
		}
	}
	if found == nil {
		return race.RaceDetail{}, race.ErrRaceNotFound
	}

	detail := race.RaceDetail{Race: *found}
	for _, p := range f.participants {
		if p.raceID == raceID {
			detail.Participants = append(detail.Participants, race.Participant{
				UserID:      p.userID,
				DisplayName: p.userID, // fake stand-in; no real user records in this fake
				JoinedAt:    p.joinedAt,
			})
		}
	}
	return detail, nil
}
