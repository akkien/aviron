package auth

import (
	"errors"
	"time"
)

type User struct {
	ID           string
	Email        string
	DisplayName  string
	PasswordHash string
	CreatedAt    time.Time
}

var ErrEmailTaken = errors.New("auth: email already taken")
var ErrUserNotFound = errors.New("auth: user not found")
var ErrInvalidCredentials = errors.New("auth: invalid credentials")
