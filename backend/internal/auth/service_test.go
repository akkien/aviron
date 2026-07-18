package auth_test

import (
	"context"
	"errors"
	"testing"

	"github.com/akkien/aviron/internal/auth"
	"golang.org/x/crypto/bcrypt"
)

func TestService_Register_Success(t *testing.T) {
	repo := newFakeRepository()
	svc := auth.NewService(repo)

	user, fieldErrs, err := svc.Register(context.Background(), "Alice@Example.com", "supersecret", "Alice")
	if err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	if len(fieldErrs) != 0 {
		t.Fatalf("fieldErrs = %v, want none", fieldErrs)
	}
	if user.Email != "alice@example.com" {
		t.Errorf("Email = %q, want lowercased", user.Email)
	}
	if user.DisplayName != "Alice" {
		t.Errorf("DisplayName = %q, want %q", user.DisplayName, "Alice")
	}
	if err := bcrypt.CompareHashAndPassword([]byte(repo.lastPasswordHash), []byte("supersecret")); err != nil {
		t.Errorf("stored hash does not match original password: %v", err)
	}
}

func TestService_Register_ValidationErrors(t *testing.T) {
	tests := []struct {
		name        string
		email       string
		password    string
		displayName string
		wantField   string
	}{
		{"invalid email", "not-an-email", "supersecret", "Alice", "email"},
		{"short password", "alice@example.com", "short", "Alice", "password"},
		{"empty display name", "alice@example.com", "supersecret", "   ", "display_name"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := newFakeRepository()
			svc := auth.NewService(repo)

			_, fieldErrs, err := svc.Register(context.Background(), tt.email, tt.password, tt.displayName)
			if err != nil {
				t.Fatalf("Register() error = %v, want nil", err)
			}
			if _, ok := fieldErrs[tt.wantField]; !ok {
				t.Errorf("fieldErrs = %v, want key %q", fieldErrs, tt.wantField)
			}
		})
	}
}

func TestService_Register_EmailTaken(t *testing.T) {
	repo := newFakeRepository()
	svc := auth.NewService(repo)
	ctx := context.Background()

	if _, _, err := svc.Register(ctx, "alice@example.com", "supersecret", "Alice"); err != nil {
		t.Fatalf("first Register() error = %v", err)
	}

	_, fieldErrs, err := svc.Register(ctx, "alice@example.com", "supersecret", "Alice Again")
	if len(fieldErrs) != 0 {
		t.Fatalf("fieldErrs = %v, want none", fieldErrs)
	}
	if !errors.Is(err, auth.ErrEmailTaken) {
		t.Errorf("err = %v, want ErrEmailTaken", err)
	}
}
