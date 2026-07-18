package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/akkien/aviron/internal/auth"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const uniqueViolation = "23505"

type AuthRepository struct {
	pool *pgxpool.Pool
}

func NewAuthRepository(pool *pgxpool.Pool) *AuthRepository {
	return &AuthRepository{pool: pool}
}

func (r *AuthRepository) CreateUser(ctx context.Context, email, displayName, passwordHash string) (auth.User, error) {
	var u auth.User

	err := r.pool.QueryRow(ctx, `
		INSERT INTO users (email, display_name, password_hash)
		VALUES ($1, $2, $3)
		RETURNING id, email, display_name, created_at
	`, email, displayName, passwordHash).Scan(&u.ID, &u.Email, &u.DisplayName, &u.CreatedAt)

	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
			return auth.User{}, auth.ErrEmailTaken
		}
		return auth.User{}, fmt.Errorf("postgres: create user: %w", err)
	}

	return u, nil
}

func (r *AuthRepository) GetUserByEmail(ctx context.Context, email string) (auth.User, error) {
	var u auth.User

	err := r.pool.QueryRow(ctx, `
		SELECT id, email, display_name, password_hash, created_at
		FROM users
		WHERE email = $1
	`, email).Scan(&u.ID, &u.Email, &u.DisplayName, &u.PasswordHash, &u.CreatedAt)

	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return auth.User{}, auth.ErrUserNotFound
		}
		return auth.User{}, fmt.Errorf("postgres: get user by email: %w", err)
	}

	return u, nil
}
