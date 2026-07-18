package auth_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/akkien/aviron/internal/auth"
	"golang.org/x/crypto/bcrypt"
)

func TestService_Register_Success(t *testing.T) {
	repo := newFakeRepository()
	svc := auth.NewAuthService(repo, []byte("test-secret"))

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
			svc := auth.NewAuthService(repo, []byte("test-secret"))

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
	svc := auth.NewAuthService(repo, []byte("test-secret"))
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

func TestService_Login_Success(t *testing.T) {
	repo := newFakeRepository()
	svc := auth.NewAuthService(repo, []byte("test-secret"))
	ctx := context.Background()

	if _, _, err := svc.Register(ctx, "alice@example.com", "supersecret", "Alice"); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	token, expiresAt, err := svc.Login(ctx, "Alice@Example.com", "supersecret")
	if err != nil {
		t.Fatalf("Login() error = %v", err)
	}
	if token == "" {
		t.Error("token is empty, want a signed JWT")
	}
	if wantMin, wantMax := time.Now().Add(23*time.Hour), time.Now().Add(25*time.Hour); expiresAt.Before(wantMin) || expiresAt.After(wantMax) {
		t.Errorf("expiresAt = %v, want ~24h from now", expiresAt)
	}
}

func TestService_Login_WrongPassword(t *testing.T) {
	repo := newFakeRepository()
	svc := auth.NewAuthService(repo, []byte("test-secret"))
	ctx := context.Background()

	if _, _, err := svc.Register(ctx, "alice@example.com", "supersecret", "Alice"); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	_, _, err := svc.Login(ctx, "alice@example.com", "wrongpassword")
	if !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Errorf("err = %v, want ErrInvalidCredentials", err)
	}
}

func TestService_Login_UnknownEmail(t *testing.T) {
	repo := newFakeRepository()
	svc := auth.NewAuthService(repo, []byte("test-secret"))

	_, _, err := svc.Login(context.Background(), "nobody@example.com", "supersecret")
	if !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Errorf("err = %v, want ErrInvalidCredentials", err)
	}
}

func TestService_Login_EmptyFields(t *testing.T) {
	repo := newFakeRepository()
	svc := auth.NewAuthService(repo, []byte("test-secret"))

	_, _, err := svc.Login(context.Background(), "", "")
	if !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Errorf("err = %v, want ErrInvalidCredentials", err)
	}
}
