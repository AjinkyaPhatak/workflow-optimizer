package middleware

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"workflow-optimizer/internal/api/httpx"
	"workflow-optimizer/internal/auth"
)

type claimsKey struct{}

// Authenticate requires "Authorization: Bearer <token>" and validates the
// token through the TokenService; handlers only ever see the claims.
func Authenticate(tokens auth.TokenService, logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			scheme, token, ok := strings.Cut(r.Header.Get("Authorization"), " ")
			if !ok || !strings.EqualFold(scheme, "Bearer") || strings.TrimSpace(token) == "" {
				httpx.WriteError(w, r, logger, httpx.Unauthenticated("Authentication is required"))
				return
			}
			claims, err := tokens.Validate(r.Context(), strings.TrimSpace(token))
			switch {
			case errors.Is(err, auth.ErrTokenExpired):
				httpx.WriteError(w, r, logger, httpx.Unauthenticated("Authentication token has expired"))
				return
			case err != nil:
				httpx.WriteError(w, r, logger, httpx.Unauthenticated("Authentication token is invalid"))
				return
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), claimsKey{}, claims)))
		})
	}
}

// UserID returns the authenticated user (uuid.Nil outside Authenticate).
func UserID(ctx context.Context) uuid.UUID {
	c, _ := ctx.Value(claimsKey{}).(auth.Claims)
	return c.UserID
}
