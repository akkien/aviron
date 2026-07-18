package auth_test

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/akkien/aviron/internal/auth"
)

// fakeRepository is an in-memory auth.Repository used by both service_test.go
// and handler_test.go, so neither needs a real Postgres connection.
type fakeRepository struct {
	mu               sync.Mutex
	users            map[string]auth.User
	lastPasswordHash string
}

func newFakeRepository() *fakeRepository {
	return &fakeRepository{users: make(map[string]auth.User)}
}

func (f *fakeRepository) CreateUser(ctx context.Context, email, displayName, passwordHash string) (auth.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if _, exists := f.users[email]; exists {
		return auth.User{}, auth.ErrEmailTaken
	}

	f.lastPasswordHash = passwordHash
	u := auth.User{
		ID:          fmt.Sprintf("user-%d", len(f.users)+1),
		Email:       email,
		DisplayName: displayName,
		CreatedAt:   time.Now(),
	}
	f.users[email] = u
	return u, nil
}
