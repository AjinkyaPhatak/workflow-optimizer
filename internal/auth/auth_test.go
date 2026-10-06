package auth_test

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"workflow-optimizer/internal/auth"
)

var secret = []byte(strings.Repeat("k", 32))

func tokens(t *testing.T, now func() time.Time) *auth.HMACTokenService {
	t.Helper()
	s, err := auth.NewHMACTokenService(secret, time.Hour, now)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestTokenRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := tokens(t, nil)
	u := auth.User{ID: uuid.New(), Email: "a@example.com", PasswordHash: "never-in-token"}
	tok, claims, err := s.Generate(ctx, u)
	if err != nil {
		t.Fatal(err)
	}
	if claims.UserID != u.ID || claims.ExpiresAt.Sub(claims.IssuedAt) != time.Hour {
		t.Fatalf("claims = %+v", claims)
	}
	got, err := s.Validate(ctx, tok)
	if err != nil || got != claims {
		t.Fatalf("validate = %+v %v (want %+v)", got, err, claims)
	}
	// The token carries the user ID and lifetime only.
	payload, _ := base64.RawURLEncoding.DecodeString(strings.Split(tok, ".")[1])
	if strings.Contains(string(payload), "example.com") || strings.Contains(string(payload), "never-in-token") {
		t.Fatalf("token payload leaks user data: %s", payload)
	}
}

func TestTokenRejections(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	s := tokens(t, func() time.Time { return now })
	tok, _, err := s.Generate(ctx, auth.User{ID: uuid.New()})
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(tok, ".")
	enc := base64.RawURLEncoding.EncodeToString
	other, _ := auth.NewHMACTokenService([]byte(strings.Repeat("x", 32)), time.Hour, nil)
	foreign, _, _ := other.Generate(ctx, auth.User{ID: uuid.New()})

	cases := map[string]string{
		"empty":           "",
		"garbage":         "not-a-token",
		"two parts":       parts[0] + "." + parts[1],
		"bad signature":   parts[0] + "." + parts[1] + "." + enc([]byte("forged")),
		"other key":       foreign,
		"tampered claims": parts[0] + "." + enc([]byte(`{"sub":"`+uuid.NewString()+`","iat":1,"exp":99999999999}`)) + "." + parts[2],
		"alg none":        enc([]byte(`{"alg":"none","typ":"JWT"}`)) + "." + parts[1] + ".",
		"alg HS512":       enc([]byte(`{"alg":"HS512","typ":"JWT"}`)) + "." + parts[1] + "." + parts[2],
	}
	for name, tok := range cases {
		if _, err := s.Validate(ctx, tok); !errors.Is(err, auth.ErrInvalidToken) {
			t.Errorf("%s: err = %v, want ErrInvalidToken", name, err)
		}
	}

	// Expired: the same token one hour later.
	later := tokens(t, func() time.Time { return now.Add(time.Hour + time.Second) })
	if _, err := later.Validate(ctx, tok); !errors.Is(err, auth.ErrTokenExpired) {
		t.Fatalf("expired: %v", err)
	}
	// Issued in the future.
	earlier := tokens(t, func() time.Time { return now.Add(-time.Hour) })
	if _, err := earlier.Validate(ctx, tok); !errors.Is(err, auth.ErrInvalidToken) {
		t.Fatalf("future token: %v", err)
	}
}

func TestTokenServiceConfiguration(t *testing.T) {
	if _, err := auth.NewHMACTokenService([]byte("short"), time.Hour, nil); err == nil {
		t.Fatal("short secret accepted")
	}
	if _, err := auth.NewHMACTokenService(secret, 0, nil); err == nil {
		t.Fatal("zero lifetime accepted")
	}
	if _, _, err := tokens(t, nil).Generate(context.Background(), auth.User{}); err == nil {
		t.Fatal("token for nil user")
	}
}

func TestPasswords(t *testing.T) {
	h, err := auth.HashPassword("correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(h, "correct horse") || !auth.CheckPassword(h, "correct horse") || auth.CheckPassword(h, "wrong horse") {
		t.Fatal("password hashing broken")
	}
	for _, bad := range []string{"short", strings.Repeat("p", 73)} {
		if _, err := auth.HashPassword(bad); !errors.Is(err, auth.ErrInvalidPassword) {
			t.Fatalf("%d-byte password: %v", len(bad), err)
		}
	}
	if auth.CheckPasswordAgainstNothing("anything") {
		t.Fatal("dummy comparison succeeded")
	}
}
