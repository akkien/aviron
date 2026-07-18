package auth

import "context"

// Repository is the persistence seam for the auth domain. It's defined here,
// next to its consumer (Service), rather than alongside its Postgres
// implementation — the interface only grows methods a service actually calls.
type Repository interface {
	CreateUser(ctx context.Context, email, displayName, passwordHash string) (User, error)
}
