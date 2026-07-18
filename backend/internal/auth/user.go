package auth

import (
	"errors"
	"time"
)

type User struct {
	ID          string
	Email       string
	DisplayName string
	CreatedAt   time.Time
}

var ErrEmailTaken = errors.New("auth: email already taken")
