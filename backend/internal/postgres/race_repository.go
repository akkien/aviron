package postgres

import (
	"context"
	"fmt"

	"github.com/akkien/aviron/internal/race"
	"github.com/jackc/pgx/v5/pgxpool"
)

type RaceRepository struct {
	pool *pgxpool.Pool
}

func NewRaceRepository(pool *pgxpool.Pool) *RaceRepository {
	return &RaceRepository{pool: pool}
}

func (r *RaceRepository) CreateRace(ctx context.Context, name string, distanceMeters int, createdBy string) (race.Race, error) {
	var rc race.Race

	err := r.pool.QueryRow(ctx, `
		INSERT INTO races (name, distance_meters, created_by)
		VALUES ($1, $2, $3)
		RETURNING id, name, distance_meters, status, created_by, created_at
	`, name, distanceMeters, createdBy).Scan(&rc.ID, &rc.Name, &rc.DistanceMeters, &rc.Status, &rc.CreatedBy, &rc.CreatedAt)

	if err != nil {
		return race.Race{}, fmt.Errorf("postgres: create race: %w", err)
	}

	return rc, nil
}
