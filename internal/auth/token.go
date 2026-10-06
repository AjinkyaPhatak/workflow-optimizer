package auth

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

var (
	// ErrInvalidToken rejects a token that is malformed, signed with another
	// key or algorithm, or carries invalid claims.
	ErrInvalidToken = errors.New("auth: invalid token")
	// ErrTokenExpired rejects a well-formed token whose lifetime ended.
	ErrTokenExpired = errors.New("auth: token expired")
)

// Claims is what a token asserts: who the user is and the token's lifetime.
// Tokens carry nothing else (no secrets, workflow data or workspace state).
type Claims struct {
	UserID    uuid.UUID
	IssuedAt  time.Time
	ExpiresAt time.Time
}

// TokenService issues and validates bearer tokens.
type TokenService interface {
	Generate(ctx context.Context, user User) (token string, claims Claims, err error)
	Validate(ctx context.Context, token string) (Claims, error)
}

// MinTokenSecretLength is the minimum HMAC key size (256 bits).
const MinTokenSecretLength = 32

// HMACTokenService issues compact JWTs (RFC 7519) signed with HMAC-SHA256.
// Validation accepts only HS256 tokens signed with its own key.
type HMACTokenService struct {
	secret []byte
	ttl    time.Duration
	now    func() time.Time
}

var _ TokenService = (*HMACTokenService)(nil)

// NewHMACTokenService returns a token service with the given key and token
// lifetime. now may be nil (time.Now).
func NewHMACTokenService(secret []byte, ttl time.Duration, now func() time.Time) (*HMACTokenService, error) {
	if len(secret) < MinTokenSecretLength {
		return nil, fmt.Errorf("auth: token secret must be at least %d bytes", MinTokenSecretLength)
	}
	if ttl <= 0 {
		return nil, errors.New("auth: token lifetime must be positive")
	}
	if now == nil {
		now = time.Now
	}
	return &HMACTokenService{secret: append([]byte(nil), secret...), ttl: ttl, now: now}, nil
}

// jwtHeader is the only header this service issues or accepts.
const jwtHeader = `{"alg":"HS256","typ":"JWT"}`

type jwtClaims struct {
	Subject   string `json:"sub"`
	IssuedAt  int64  `json:"iat"`
	ExpiresAt int64  `json:"exp"`
}

var b64 = base64.RawURLEncoding

// Generate issues a token for user.
func (s *HMACTokenService) Generate(_ context.Context, user User) (string, Claims, error) {
	if user.ID == uuid.Nil {
		return "", Claims{}, fmt.Errorf("%w: user id is required", ErrInvalidToken)
	}
	now := s.now().UTC().Truncate(time.Second)
	c := Claims{UserID: user.ID, IssuedAt: now, ExpiresAt: now.Add(s.ttl)}
	payload, err := json.Marshal(jwtClaims{Subject: user.ID.String(), IssuedAt: c.IssuedAt.Unix(), ExpiresAt: c.ExpiresAt.Unix()})
	if err != nil {
		return "", Claims{}, err
	}
	signing := b64.EncodeToString([]byte(jwtHeader)) + "." + b64.EncodeToString(payload)
	return signing + "." + b64.EncodeToString(s.sign(signing)), c, nil
}

// Validate checks the token's signature, algorithm and lifetime.
func (s *HMACTokenService) Validate(_ context.Context, token string) (Claims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return Claims{}, ErrInvalidToken
	}
	header, err := b64.DecodeString(parts[0])
	if err != nil {
		return Claims{}, ErrInvalidToken
	}
	var h struct {
		Alg string `json:"alg"`
		Typ string `json:"typ"`
	}
	if json.Unmarshal(header, &h) != nil || h.Alg != "HS256" || (h.Typ != "" && h.Typ != "JWT") {
		return Claims{}, ErrInvalidToken
	}
	sig, err := b64.DecodeString(parts[2])
	if err != nil || !hmac.Equal(sig, s.sign(parts[0]+"."+parts[1])) {
		return Claims{}, ErrInvalidToken
	}
	payload, err := b64.DecodeString(parts[1])
	if err != nil {
		return Claims{}, ErrInvalidToken
	}
	var jc jwtClaims
	if json.Unmarshal(payload, &jc) != nil || jc.ExpiresAt == 0 || jc.IssuedAt == 0 {
		return Claims{}, ErrInvalidToken
	}
	id, err := uuid.Parse(jc.Subject)
	if err != nil || id == uuid.Nil {
		return Claims{}, ErrInvalidToken
	}
	c := Claims{UserID: id, IssuedAt: time.Unix(jc.IssuedAt, 0).UTC(), ExpiresAt: time.Unix(jc.ExpiresAt, 0).UTC()}
	now := s.now()
	if c.IssuedAt.After(now.Add(time.Minute)) {
		return Claims{}, ErrInvalidToken // issued in the future
	}
	if !now.Before(c.ExpiresAt) {
		return Claims{}, ErrTokenExpired
	}
	return c, nil
}

func (s *HMACTokenService) sign(signing string) []byte {
	m := hmac.New(sha256.New, s.secret)
	m.Write([]byte(signing))
	return m.Sum(nil)
}
