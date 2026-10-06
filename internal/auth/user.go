package auth

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

var (
	// ErrUserNotFound is returned when no user matches.
	ErrUserNotFound = errors.New("auth: user not found")
	// ErrEmailTaken rejects registering an email that already has an account.
	ErrEmailTaken = errors.New("auth: email is already registered")
)

// User is the durable representation of an account.
type User struct {
	ID           uuid.UUID
	Email        string
	Name         string
	PasswordHash string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// UserRepository is the persistence boundary for users.
type UserRepository interface {
	FindByID(context.Context, uuid.UUID) (User, error)
	FindByEmail(context.Context, string) (User, error)
}
