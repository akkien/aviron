package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/akkien/aviron/internal/race"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
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

func (r *RaceRepository) GetRace(ctx context.Context, raceID string) (race.Race, error) {
	var rc race.Race

	err := r.pool.QueryRow(ctx, `
		SELECT id, name, distance_meters, status, created_by, created_at
		FROM races
		WHERE id = $1
	`, raceID).Scan(&rc.ID, &rc.Name, &rc.DistanceMeters, &rc.Status, &rc.CreatedBy, &rc.CreatedAt)

	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return race.Race{}, race.ErrRaceNotFound
		}
		return race.Race{}, fmt.Errorf("postgres: get race: %w", err)
	}

	return rc, nil
}

func (r *RaceRepository) AddParticipant(ctx context.Context, raceID, userID string) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO race_participants (race_id, user_id)
		VALUES ($1, $2)
	`, raceID, userID)

	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
			return race.ErrAlreadyJoined
		}
		return fmt.Errorf("postgres: add participant: %w", err)
	}

	return nil
}
