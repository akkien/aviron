package race_test

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/akkien/aviron/internal/race"
	"github.com/akkien/aviron/internal/room"
)

// testLogger discards output — this package's tests assert on responses and
// fake-repository state, not log lines.
var testLogger = slog.New(slog.DiscardHandler)

// testTickObserver discards tick-latency observations — this package's
// tests never construct a real RoomActor, but room.NewRegistry still
// requires a non-nil room.TickObserver.
type testTickObserverType struct{}

func (testTickObserverType) ObserveTick(d time.Duration) {}

var testTickObserver = testTickObserverType{}

// finishRaceCall records one FinishRace invocation, so tests can assert what
// the service actually delegated to the repository.
type finishRaceCall struct {
	raceID         string
	distanceMeters int
	results        []room.ParticipantResult
}

// participantRecord tracks a join, including which race it belongs to and
// when it happened, so fakeRepository can both detect duplicate joins and
// reconstruct a race's participant list. finishRank/finishTimeMs/avgPaceWatt
// stay zero-value until FinishRace populates them (race-detail-cold-visit.md).
type participantRecord struct {
	raceID       string
	userID       string
	joinedAt     time.Time
	finishRank   *int
	finishTimeMs *int64
	avgPaceWatt  float64
}

// fakeRepository is an in-memory race.RaceRepository used by both
// service_test.go and handler_test.go, so neither needs a real Postgres connection.
type fakeRepository struct {
	mu           sync.Mutex
	races        []race.Race
	participants []participantRecord
	finishCalls  []finishRaceCall
	cancelCalls  []string // raceIDs
}

func newFakeRepository() *fakeRepository {
	return &fakeRepository{}
}

// fakeRaceID returns a 12-character base58-shaped id for the nth race,
// matching what race.GenerateRaceID actually produces (and what
// isValidRaceID actually accepts) — a fixed "race" prefix plus an 8-digit
// counter, left-padded with '1' rather than '0' since base58 excludes '0'.
func fakeRaceID(n int) string {
	digits := fmt.Sprintf("%d", n)
	for len(digits) < 8 {
		digits = "1" + digits
	}
	return "race" + digits
}

func (f *fakeRepository) CreateRace(ctx context.Context, name string, distanceMeters int, createdBy string) (race.Race, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	r := race.Race{
		ID:             fakeRaceID(len(f.races) + 1),
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

func (f *fakeRepository) RemoveParticipant(ctx context.Context, raceID, userID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	for i, p := range f.participants {
		if p.raceID == raceID && p.userID == userID {
			f.participants = append(f.participants[:i], f.participants[i+1:]...)
			return nil
		}
	}
	return race.ErrNotParticipant
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

func (f *fakeRepository) IsParticipant(ctx context.Context, raceID, userID string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	for _, p := range f.participants {
		if p.raceID == raceID && p.userID == userID {
			return true, nil
		}
	}
	return false, nil
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
				UserID:       p.userID,
				DisplayName:  p.userID, // fake stand-in; no real user records in this fake
				JoinedAt:     p.joinedAt,
				FinishRank:   p.finishRank,
				FinishTimeMs: p.finishTimeMs,
				AvgPaceWatt:  p.avgPaceWatt,
			})
		}
	}
	return detail, nil
}

func (f *fakeRepository) FinishRace(ctx context.Context, raceID string, distanceMeters int, results []room.ParticipantResult) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.finishCalls = append(f.finishCalls, finishRaceCall{raceID: raceID, distanceMeters: distanceMeters, results: results})

	for i, r := range f.races {
		if r.ID == raceID {
			f.races[i].Status = "finished"
		}
	}
	for _, res := range results {
		for i, p := range f.participants {
			if p.raceID == raceID && p.userID == res.UserID {
				f.participants[i].finishRank = res.FinishRank
				f.participants[i].finishTimeMs = res.FinishTimeMs
				f.participants[i].avgPaceWatt = res.AvgPaceWatt
			}
		}
	}
	return nil
}

// ListOpenRaces mirrors the real repository's rules: pending, not full, and
// excludeUserID isn't already a participant. HostDisplayName falls back to
// the creator's user id, same "fake stand-in" convention
// GetRaceWithParticipants's DisplayName already uses in this fake.
func (f *fakeRepository) ListOpenRaces(ctx context.Context, excludeUserID string) ([]race.OpenRace, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	var open []race.OpenRace
	for _, r := range f.races {
		if r.Status != "pending" {
			continue
		}
		count := 0
		excluded := false
		for _, p := range f.participants {
			if p.raceID != r.ID {
				continue
			}
			count++
			if p.userID == excludeUserID {
				excluded = true
			}
		}
		if excluded || count >= race.MaxParticipants {
			continue
		}
		open = append(open, race.OpenRace{
			ID:              r.ID,
			Name:            r.Name,
			DistanceMeters:  r.DistanceMeters,
			HostDisplayName: r.CreatedBy,
			PlayerCount:     count,
			MaxPlayers:      race.MaxParticipants,
			CreatedAt:       r.CreatedAt,
		})
	}
	sort.Slice(open, func(i, j int) bool { return open[i].CreatedAt.After(open[j].CreatedAt) })
	return open, nil
}

// CancelRace mirrors the real repository's status='pending' guard — a no-op
// (not an error) if the race already moved on.
func (f *fakeRepository) CancelRace(ctx context.Context, raceID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.cancelCalls = append(f.cancelCalls, raceID)
	for i, r := range f.races {
		if r.ID == raceID && r.Status == "pending" {
			f.races[i].Status = "cancelled"
		}
	}
	return nil
}
