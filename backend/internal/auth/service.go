package auth

import (
	"context"
	"fmt"
	"net/mail"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

type Service struct {
	repo Repository
}

func NewService(repo Repository) *Service {
	return &Service{repo: repo}
}

// Register validates and creates a new user. A non-empty fieldErrs return
// means validation failed and err is always nil in that case; err is only
// set for downstream failures (e.g. ErrEmailTaken, or an unexpected repo error).
func (s *Service) Register(ctx context.Context, email, password, displayName string) (user User, fieldErrs map[string]string, err error) {
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
