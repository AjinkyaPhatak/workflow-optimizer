package httpx

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"workflow-optimizer/internal/api/responses"
	"workflow-optimizer/internal/application"
	"workflow-optimizer/internal/observability"
)

// API error codes.
const (
	CodeInvalidRequest     = "INVALID_REQUEST"
	CodeUnauthenticated    = "UNAUTHENTICATED"
	CodeForbidden          = "FORBIDDEN"
	CodeNotFound           = "NOT_FOUND"
	CodeMethodNotAllowed   = "METHOD_NOT_ALLOWED"
	CodeConflict           = "CONFLICT"
	CodeValidation         = "VALIDATION_ERROR"
	CodeRateLimited        = "RATE_LIMITED"
	CodeInternal           = "INTERNAL_ERROR"
	CodeServiceUnavailable = "SERVICE_UNAVAILABLE"
)

// Error is an error the transport layer already classified.
type Error struct {
	Status  int
	Code    string
	Message string
	Details any
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

// BadRequest is a 400 INVALID_REQUEST.
func BadRequest(message string) *Error {
	return &Error{Status: http.StatusBadRequest, Code: CodeInvalidRequest, Message: message}
}

// Unauthenticated is a 401 UNAUTHENTICATED.
func Unauthenticated(message string) *Error {
	return &Error{Status: http.StatusUnauthorized, Code: CodeUnauthenticated, Message: message}
}

// Classify maps any error to a safe API error. Unclassified errors become
// 500 INTERNAL_ERROR with a generic message: internal details (SQL,
// infrastructure, stack traces) never reach the client.
func Classify(err error) *Error {
	var (
		httpErr    *Error
		notFound   *application.NotFoundError
		invalid    *application.InvalidError
		conflict   *application.ConflictError
		validation *application.ValidationFailedError
	)
	switch {
	case errors.As(err, &httpErr):
		return httpErr
	case errors.As(err, &notFound):
		msg := notFound.Resource + " not found"
		return &Error{Status: http.StatusNotFound, Code: notFound.Code(), Message: strings.ToUpper(msg[:1]) + msg[1:]}
	case errors.As(err, &validation):
		return &Error{Status: http.StatusUnprocessableEntity, Code: CodeValidation, Message: "Workflow definition is invalid",
			Details: map[string]any{"errors": validation.Errors}}
	case errors.As(err, &conflict):
		return &Error{Status: http.StatusConflict, Code: conflict.Code, Message: conflict.Message}
	case errors.As(err, &invalid):
		return BadRequest(invalid.Message)
	case errors.Is(err, application.ErrForbidden):
		return &Error{Status: http.StatusForbidden, Code: CodeForbidden, Message: "You do not have permission to perform this action"}
	case errors.Is(err, application.ErrInvalidCredentials):
		return Unauthenticated("Invalid email or password")
	case errors.Is(err, application.ErrUnavailable):
		return &Error{Status: http.StatusServiceUnavailable, Code: CodeServiceUnavailable, Message: "This feature is not configured on the server"}
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return &Error{Status: http.StatusServiceUnavailable, Code: CodeServiceUnavailable, Message: "The request could not be completed in time"}
	default:
		return &Error{Status: http.StatusInternalServerError, Code: CodeInternal, Message: "Internal server error"}
	}
}

// WriteError writes err in the API error shape. Server errors are logged
// (with the request ID) because the client only sees a generic message.
func WriteError(w http.ResponseWriter, r *http.Request, logger *slog.Logger, err error) {
	e := Classify(err)
	if e.Status >= 500 && logger != nil {
		logger.ErrorContext(r.Context(), "request failed",
			"request_id", RequestID(r.Context()), "method", r.Method, "path", r.URL.Path, "status", e.Status, "error", err)
	}
	if e.Status == http.StatusUnauthorized {
		w.Header().Set("WWW-Authenticate", `Bearer`)
	}
	WriteJSON(w, e.Status, responses.ErrorBody{Error: responses.ErrorDetail{Code: e.Code, Message: e.Message, Details: e.Details}})
}

// WithRequestID stores the request ID in ctx (the observability context key,
// so loggers wrapped by observability.NewContextLogger include it).
func WithRequestID(ctx context.Context, id string) context.Context {
	return observability.WithRequestID(ctx, id)
}

// RequestID returns the request ID in ctx ("" when none).
func RequestID(ctx context.Context) string { return observability.RequestID(ctx) }
