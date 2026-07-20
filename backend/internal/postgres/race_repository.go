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

func (r *RaceRepository) StartRace(ctx context.Context, raceID, promptText string) (race.Race, error) {
	var rc race.Race

	err := r.pool.QueryRow(ctx, `
		UPDATE races
		SET status = 'active', started_at = now(), prompt_text = $2
		WHERE id = $1
		RETURNING id, name, distance_meters, status, created_by, created_at, started_at, prompt_text
	`, raceID, promptText).Scan(&rc.ID, &rc.Name, &rc.DistanceMeters, &rc.Status, &rc.CreatedBy, &rc.CreatedAt, &rc.StartedAt, &rc.PromptText)

	if err != nil {
		return race.Race{}, fmt.Errorf("postgres: start race: %w", err)
	}

	return rc, nil
}

func (r *RaceRepository) GetRaceText(ctx context.Context, raceID string) (string, error) {
	var promptText *string

	err := r.pool.QueryRow(ctx, `SELECT prompt_text FROM races WHERE id = $1`, raceID).Scan(&promptText)

	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", race.ErrRaceNotFound
		}
		return "", fmt.Errorf("postgres: get race text: %w", err)
	}
	if promptText == nil {
		return "", race.ErrPromptNotReady
	}

	return *promptText, nil
}

func (r *RaceRepository) GetRaceWithParticipants(ctx context.Context, raceID string) (race.RaceDetail, error) {
	var detail race.RaceDetail

	err := r.pool.QueryRow(ctx, `
		SELECT id, name, distance_meters, status, created_by, created_at
		FROM races WHERE id = $1
	`, raceID).Scan(&detail.ID, &detail.Name, &detail.DistanceMeters, &detail.Status, &detail.CreatedBy, &detail.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return race.RaceDetail{}, race.ErrRaceNotFound
		}
		return race.RaceDetail{}, fmt.Errorf("postgres: get race: %w", err)
	}

	rows, err := r.pool.Query(ctx, `
		SELECT rp.user_id, u.display_name, rp.joined_at
		FROM race_participants rp
		JOIN users u ON u.id = rp.user_id
		WHERE rp.race_id = $1
		ORDER BY rp.joined_at
	`, raceID)
	if err != nil {
		return race.RaceDetail{}, fmt.Errorf("postgres: get race participants: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var p race.Participant
		if err := rows.Scan(&p.UserID, &p.DisplayName, &p.JoinedAt); err != nil {
			return race.RaceDetail{}, fmt.Errorf("postgres: scan participant: %w", err)
		}
		detail.Participants = append(detail.Participants, p)
	}
	if err := rows.Err(); err != nil {
		return race.RaceDetail{}, fmt.Errorf("postgres: iterate participants: %w", err)
	}

	return detail, nil
}
