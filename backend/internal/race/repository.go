package race

import (
	"context"

	"github.com/akkien/aviron/internal/room"
)

// RaceRepository is the persistence seam for the race domain. Defined here,
// next to its consumer (RaceService), rather than alongside its Postgres
// implementation — the interface only grows methods a service actually calls.
type RaceRepository interface {
	CreateRace(ctx context.Context, name string, distanceMeters int, createdBy string) (Race, error)
	GetRace(ctx context.Context, raceID string) (Race, error)
	AddParticipant(ctx context.Context, raceID, userID string) error
	CountParticipants(ctx context.Context, raceID string) (int, error)
	StartRace(ctx context.Context, raceID, promptText string) (Race, error)
	GetRaceText(ctx context.Context, raceID string) (string, error)
	GetRaceWithParticipants(ctx context.Context, raceID string) (RaceDetail, error)
	// FinishRace persists a race's final results (race-completion/finish-race.md)
	// in one transaction: races.status/ended_at, each participant's final
	// race_participants row, and a leaderboard_alltime upsert. results uses
	// room.ParticipantResult (not a race-local type) since RaceService.FinishRace
	// exists specifically to satisfy room.RaceFinisher — internal/race already
	// imports internal/room, so this introduces no new dependency direction.
	FinishRace(ctx context.Context, raceID string, distanceMeters int, results []room.ParticipantResult) error
}
