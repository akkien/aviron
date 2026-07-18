package auth

import "context"

// AuthRepository is the persistence seam for the auth domain. It's defined
// here, next to its consumer (AuthService), rather than alongside its
// Postgres implementation — the interface only grows methods a service
// actually calls.
type AuthRepository interface {
	CreateUser(ctx context.Context, email, displayName, passwordHash string) (User, error)
	GetUserByEmail(ctx context.Context, email string) (User, error)
}
