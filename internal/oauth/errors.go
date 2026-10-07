package oauth

import "errors"

// Kind is the normalized category of an OAuth failure.
type Kind string

const (
	// KindInvalidState: the state is unknown, expired, already used, bound
	// to another provider or browser, or its user may no longer connect
	// accounts in the workspace.
	KindInvalidState Kind = "invalid_state"
	// KindAuthorizationDenied: the user (or the provider) refused consent.
	KindAuthorizationDenied Kind = "authorization_denied"
	// KindInvalidCode: the provider rejected the authorization code.
	KindInvalidCode Kind = "invalid_code"
	// KindTokenExchangeFailed: the code exchange failed otherwise (including
	// malformed token responses).
	KindTokenExchangeFailed Kind = "token_exchange_failed"
	// KindTokenRefreshFailed: a refresh failed for a non-transient reason
	// other than revocation (e.g. a malformed response).
	KindTokenRefreshFailed Kind = "token_refresh_failed"
	// KindRevoked: the grant is gone (invalid_grant, revoked consent).
	KindRevoked Kind = "revoked"
	// KindTokenExpired: the access token expired and cannot be refreshed
	// (the provider issued no refresh token).
	KindTokenExpired Kind = "token_expired"
	// KindProviderUnavailable: the provider could not be reached or failed
	// temporarily (network error, 5xx, 429). The only transient kind.
	KindProviderUnavailable Kind = "provider_unavailable"
	// KindInvalidConfiguration: the provider is unknown or misconfigured.
	KindInvalidConfiguration Kind = "invalid_configuration"
	// KindRevocationUnsupported: the provider cannot revoke tokens.
	KindRevocationUnsupported Kind = "revocation_unsupported"
)

// Retryable reports whether the failure is transient.
func (k Kind) Retryable() bool { return k == KindProviderUnavailable }

// Error is a normalized OAuth failure. Message is written by this backend
// (never a provider's raw response body, error_description, token, code or
// secret). Err is an optional cause for errors.Is/As; Error() never prints it.
type Error struct {
	Kind    Kind
	Message string
	Err     error
}

func newError(kind Kind, message string) *Error { return &Error{Kind: kind, Message: message} }

// NewError builds an Error (for Provider implementations).
func NewError(kind Kind, message string) *Error { return newError(kind, message) }

func (e *Error) Error() string {
	if e.Message == "" {
		return "oauth: " + string(e.Kind)
	}
	return "oauth: " + string(e.Kind) + ": " + e.Message
}

func (e *Error) Unwrap() error { return e.Err }

// KindOf returns the Kind of an OAuth error, or "" for other errors.
func KindOf(err error) Kind {
	var e *Error
	if errors.As(err, &e) {
		return e.Kind
	}
	return ""
}
