package db

import (
	"context"
	"fmt"

	"github.com/exaring/otelpgx"
	"github.com/jackc/pgx/v5/pgxpool"
)

func NewPool(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("db: parse config: %w", err)
	}

	// otelpgx.NewTracer gives every query a span automatically
	// (tracing/instrumentation.md), wired once here rather than
	// hand-instrumenting every repository method. Default options only —
	// no WithIncludeQueryParameters, so a span's SQL statement stays
	// parameterized ($1, $2, ...), never the literal argument values,
	// matching this project's existing no-PII/secrets-in-logs discipline.
	config.ConnConfig.Tracer = otelpgx.NewTracer()

	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("db: create pool: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("db: ping: %w", err)
	}

	return pool, nil
}
