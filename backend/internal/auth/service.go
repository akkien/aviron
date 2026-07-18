package auth

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

type AuthService struct {
	repo      AuthRepository
	jwtSecret []byte
}

func NewAuthService(repo AuthRepository, jwtSecret []byte) *AuthService {
	return &AuthService{repo: repo, jwtSecret: jwtSecret}
}

// Register validates and creates a new user. A non-empty fieldErrs return
// means validation failed and err is always nil in that case; err is only
// set for downstream failures (e.g. ErrEmailTaken, or an unexpected repo error).
func (s *AuthService) Register(ctx context.Context, email, password, displayName string) (user User, fieldErrs map[string]string, err error) {
	if errs := validateRegister(email, password, displayName); len(errs) > 0 {
		return User{}, errs, nil
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), 12)
	if err != nil {
		return User{}, nil, fmt.Errorf("auth: hash password: %w", err)
	}

	user, err = s.repo.CreateUser(ctx, strings.ToLower(email), displayName, string(hash))
	if err != nil {
		return User{}, nil, err
	}
	return user, nil, nil
}

// Login verifies email/password and, on success, returns a signed JWT and its
// expiry. Both "user not found" and "wrong password" collapse to
// ErrInvalidCredentials so the caller can't distinguish which was wrong.
func (s *AuthService) Login(ctx context.Context, email, password string) (token string, expiresAt time.Time, err error) {
	if email == "" || password == "" {
		return "", time.Time{}, ErrInvalidCredentials
	}

	user, err := s.repo.GetUserByEmail(ctx, strings.ToLower(email))
	if err != nil {
		if errors.Is(err, ErrUserNotFound) {
			return "", time.Time{}, ErrInvalidCredentials
		}
		return "", time.Time{}, err
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)); err != nil {
		return "", time.Time{}, ErrInvalidCredentials
	}

	expiresAt = time.Now().Add(24 * time.Hour)
	claims := jwt.MapClaims{
		"sub":   user.ID,
		"email": user.Email,
		"exp":   expiresAt.Unix(),
	}

	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(s.jwtSecret)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("auth: sign token: %w", err)
	}

	return signed, expiresAt, nil
}

func validateRegister(email, password, displayName string) map[string]string {
	errs := map[string]string{}

	if _, err := mail.ParseAddress(email); err != nil {
		errs["email"] = "invalid format"
	}
	if len(password) < 8 {
		errs["password"] = "must be at least 8 characters"
	}
	trimmed := strings.TrimSpace(displayName)
	if len(trimmed) == 0 || len(displayName) > 50 {
		errs["display_name"] = "must be 1-50 characters"
	}

	return errs
}
