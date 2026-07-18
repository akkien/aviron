package race

import "context"

// RaceRepository is the persistence seam for the race domain. Defined here,
// next to its consumer (RaceService), rather than alongside its Postgres
// implementation — the interface only grows methods a service actually calls.
type RaceRepository interface {
	CreateRace(ctx context.Context, name string, distanceMeters int, createdBy string) (Race, error)
}
