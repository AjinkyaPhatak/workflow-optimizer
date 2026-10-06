// Package middleware holds the API's cross-cutting HTTP handlers: request
// IDs, access logging, panic recovery and bearer authentication.
// Authorization is per resource (workspace membership) and lives in the
// application services.
package middleware

import (
	"net/http"
	"regexp"

	"github.com/google/uuid"

	"workflow-optimizer/internal/api/httpx"
)

// RequestIDHeader carries the request ID in both directions.
const RequestIDHeader = "X-Request-ID"

var validRequestID = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// RequestID adopts a well-formed incoming X-Request-ID or generates one, and
// echoes it on the response.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(RequestIDHeader)
		if !validRequestID.MatchString(id) {
			id = uuid.NewString()
		}
		w.Header().Set(RequestIDHeader, id)
		next.ServeHTTP(w, r.WithContext(httpx.WithRequestID(r.Context(), id)))
	})
}
