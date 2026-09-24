package auth

import (
	"context"
	"time"

	"github.com/google/uuid"
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
