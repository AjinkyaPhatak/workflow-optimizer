package auth

import (
	"errors"
	"fmt"
	"sync"

	"golang.org/x/crypto/bcrypt"
)

// Password limits. bcrypt only uses the first 72 bytes, so longer passwords
// are rejected rather than silently truncated.
const (
	MinPasswordLength = 8
	MaxPasswordLength = 72
)

// ErrInvalidPassword rejects passwords outside the length limits.
var ErrInvalidPassword = errors.New("auth: invalid password")

// HashPassword returns the bcrypt hash of password.
func HashPassword(password string) (string, error) {
	if len(password) < MinPasswordLength || len(password) > MaxPasswordLength {
		return "", fmt.Errorf("%w: must be %d to %d bytes", ErrInvalidPassword, MinPasswordLength, MaxPasswordLength)
	}
	h, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", fmt.Errorf("auth: hash password: %w", err)
	}
	return string(h), nil
}

// CheckPassword reports whether password matches hash.
func CheckPassword(hash, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

// dummyHash is compared against when a login names an unknown user, so an
// unknown email costs as much time as a wrong password.
var dummyHash = sync.OnceValue(func() []byte {
	h, _ := bcrypt.GenerateFromPassword([]byte("timing-equalizer"), bcrypt.DefaultCost)
	return h
})

// CheckPasswordAgainstNothing spends one bcrypt comparison and returns false.
func CheckPasswordAgainstNothing(password string) bool {
	_ = bcrypt.CompareHashAndPassword(dummyHash(), []byte(password))
	return false
}
