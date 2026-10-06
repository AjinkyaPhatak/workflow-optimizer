// Package application holds the use-case services behind the REST API
// (Phase 12). Each operation takes the authenticated user, enforces
// workspace membership and role, and delegates to the existing domain
// services and repositories (workflow validation, execution lifecycle,
// queue submission, credential service). It has no HTTP dependency.
package application

import (
	"errors"
	"strings"

	"workflow-optimizer/internal/workflow"
)

var (
	// ErrNotFound is matched by every NotFoundError.
	ErrNotFound = errors.New("not found")
	// ErrForbidden: the user is a member of the workspace but their role does
	// not allow the operation.
	ErrForbidden = errors.New("forbidden")
	// ErrInvalidCredentials: login failed (unknown email or wrong password).
	ErrInvalidCredentials = errors.New("invalid email or password")
	// ErrUnavailable: a required capability is not configured (for example
	// the credential encryption key).
	ErrUnavailable = errors.New("service unavailable")
)

// NotFoundError reports a missing resource. Resources in workspaces the user
// does not belong to are reported the same way, so IDs cannot be probed.
type NotFoundError struct{ Resource string }

func (e *NotFoundError) Error() string { return e.Resource + " not found" }

func (e *NotFoundError) Is(target error) bool { return target == ErrNotFound }

// Code is the API error code (e.g. WORKFLOW_NOT_FOUND).
func (e *NotFoundError) Code() string {
	return strings.ToUpper(strings.ReplaceAll(e.Resource, " ", "_")) + "_NOT_FOUND"
}

func notFound(resource string) error { return &NotFoundError{Resource: resource} }

// InvalidError rejects input the domain refuses (beyond transport checks).
type InvalidError struct{ Message string }

func (e *InvalidError) Error() string { return e.Message }

// ConflictError rejects an operation that the resource's current state does
// not allow (e.g. executing an unpublished version).
type ConflictError struct {
	Code    string
	Message string
}

func (e *ConflictError) Error() string { return e.Message }

// ValidationFailedError carries the graph validator's findings.
type ValidationFailedError struct {
	Errors []workflow.ValidationError
}

func (e *ValidationFailedError) Error() string { return "workflow definition is invalid" }
