package race

import "context"

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
}
