package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/akkien/aviron/internal/race"
	"github.com/akkien/aviron/internal/room"
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

// maxCreateRaceAttempts bounds retries when a freshly generated race id
// collides with an existing row (race.GenerateRaceID's 12-character base58
// space makes this vanishingly unlikely, but the id is no longer guaranteed
// unique by Postgres's own gen_random_uuid() default, so the retry is what
// actually keeps that guarantee true end to end).
const maxCreateRaceAttempts = 5

func (r *RaceRepository) CreateRace(ctx context.Context, name string, distanceMeters int, createdBy string) (race.Race, error) {
	var rc race.Race

	for attempt := 0; attempt < maxCreateRaceAttempts; attempt++ {
		id, err := race.GenerateRaceID()
		if err != nil {
			return race.Race{}, fmt.Errorf("postgres: create race: %w", err)
		}

		err = r.pool.QueryRow(ctx, `
			INSERT INTO races (id, name, distance_meters, created_by)
			VALUES ($1, $2, $3, $4)
			RETURNING id, name, distance_meters, status, created_by, created_at
		`, id, name, distanceMeters, createdBy).Scan(&rc.ID, &rc.Name, &rc.DistanceMeters, &rc.Status, &rc.CreatedBy, &rc.CreatedAt)
		if err == nil {
			return rc, nil
		}

		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
			continue // id collision — retry with a freshly generated one
		}
		return race.Race{}, fmt.Errorf("postgres: create race: %w", err)
	}

	return race.Race{}, fmt.Errorf("postgres: create race: exhausted %d attempts generating a unique id", maxCreateRaceAttempts)
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

// RemoveParticipant deletes the race_participants row for raceID/userID
// (leave-race.md's REST leave path). A real DELETE, not a soft-remove — a
// pending race has no result worth preserving for someone who backs out
// before it starts. Returns race.ErrNotParticipant if no such row existed
// (never joined, or already left).
func (r *RaceRepository) RemoveParticipant(ctx context.Context, raceID, userID string) error {
	tag, err := r.pool.Exec(ctx, `
		DELETE FROM race_participants WHERE race_id = $1 AND user_id = $2
	`, raceID, userID)
	if err != nil {
		return fmt.Errorf("postgres: remove participant: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return race.ErrNotParticipant
	}
	return nil
}

func (r *RaceRepository) CountParticipants(ctx context.Context, raceID string) (int, error) {
	var count int
	err := r.pool.QueryRow(ctx, `
		SELECT count(*) FROM race_participants WHERE race_id = $1
	`, raceID).Scan(&count)

	if err != nil {
		return 0, fmt.Errorf("postgres: count participants: %w", err)
	}

	return count, nil
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

// FinishRace commits races/race_participants/leaderboard_alltime together —
// the first multi-statement transaction in this repository; every other
// method here is a single statement. All three writes succeed or none do,
// since a race left "finished" with incomplete participant/leaderboard rows
// would be a real data-integrity bug (race-completion/finish-race.md).
func (r *RaceRepository) FinishRace(ctx context.Context, raceID string, distanceMeters int, results []room.ParticipantResult) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("postgres: finish race: begin tx: %w", err)
	}
	defer tx.Rollback(ctx) // no-op once Commit succeeds

	if _, err := tx.Exec(ctx, `
		UPDATE races SET status = 'finished', ended_at = now() WHERE id = $1
	`, raceID); err != nil {
		return fmt.Errorf("postgres: finish race: update race: %w", err)
	}

	for _, res := range results {
		if _, err := tx.Exec(ctx, `
			UPDATE race_participants
			SET finish_rank = $1, finish_time_ms = $2, avg_pace_watt = $3, disconnected_count = $4
			WHERE race_id = $5 AND user_id = $6
		`, res.FinishRank, res.FinishTimeMs, res.AvgPaceWatt, res.DisconnectedCount, raceID, res.UserID); err != nil {
			return fmt.Errorf("postgres: finish race: update participant %s: %w", res.UserID, err)
		}

		// best_2000m_ms is a holdover name from the original fitness-telemetry
		// schema (project-overview.md §13) — this project has no fixed-distance
		// race type, so it's treated simply as "best finish time recorded so
		// far": kept as-is if this race's result is NULL (didn't finish) or
		// worse than the existing value, taken fresh if there's no prior value.
		if _, err := tx.Exec(ctx, `
			INSERT INTO leaderboard_alltime (user_id, best_2000m_ms, total_races, total_distance_m, updated_at)
			VALUES ($1, $2, 1, $3, now())
			ON CONFLICT (user_id) DO UPDATE SET
				best_2000m_ms = CASE
					WHEN EXCLUDED.best_2000m_ms IS NULL THEN leaderboard_alltime.best_2000m_ms
					WHEN leaderboard_alltime.best_2000m_ms IS NULL THEN EXCLUDED.best_2000m_ms
					WHEN EXCLUDED.best_2000m_ms < leaderboard_alltime.best_2000m_ms THEN EXCLUDED.best_2000m_ms
					ELSE leaderboard_alltime.best_2000m_ms
				END,
				total_races = leaderboard_alltime.total_races + 1,
				total_distance_m = leaderboard_alltime.total_distance_m + EXCLUDED.total_distance_m,
				updated_at = now()
		`, res.UserID, res.FinishTimeMs, distanceMeters); err != nil {
			return fmt.Errorf("postgres: finish race: upsert leaderboard for %s: %w", res.UserID, err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("postgres: finish race: commit: %w", err)
	}
	return nil
}
