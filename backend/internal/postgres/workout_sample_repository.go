package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/akkien/aviron/internal/consumer"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// foreignKeyViolation is workout_samples' most likely permanent write
// failure (kafka-consumer-postgres-sink.md): a race_id/user_id that
// doesn't exist. Distinct from uniqueViolation (auth_repository.go) —
// this table has no unique constraint of its own to violate.
const foreignKeyViolation = "23503"

type WorkoutSampleRepository struct {
	pool *pgxpool.Pool
}

func NewWorkoutSampleRepository(pool *pgxpool.Pool) *WorkoutSampleRepository {
	return &WorkoutSampleRepository{pool: pool}
}

// InsertBatch bulk-loads samples via the Postgres copy protocol (pgx's
// CopyFrom) rather than one INSERT per row, per project-overview.md §3's
// batch-insert guidance. stroke_rate is left unwritten (always NULL) —
// unused for this project's typing-race mechanic (§13). A foreign-key
// violation (a race_id/user_id that doesn't exist) is wrapped in
// consumer.ErrPermanentWrite so the caller can dead-letter it instead of
// redelivering forever; any other error (e.g. a dropped connection) is
// left as a plain error, treated as transient.
func (r *WorkoutSampleRepository) InsertBatch(ctx context.Context, samples []consumer.WorkoutSample) error {
	rows := make([][]any, len(samples))
	for i, s := range samples {
		rows[i] = []any{s.RaceID, s.UserID, s.Ts, s.DistanceM, s.PaceWatt}
	}

	_, err := r.pool.CopyFrom(ctx,
		pgx.Identifier{"workout_samples"},
		[]string{"race_id", "user_id", "ts", "distance_m", "pace_watt"},
		pgx.CopyFromRows(rows),
	)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == foreignKeyViolation {
			return fmt.Errorf("postgres: insert workout sample batch: %w: %v", consumer.ErrPermanentWrite, err)
		}
		return fmt.Errorf("postgres: insert workout sample batch: %w", err)
	}
	return nil
}
