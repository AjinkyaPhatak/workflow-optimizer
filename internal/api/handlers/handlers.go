// Package handlers holds the API's HTTP handlers. They decode and validate
// the request, take the authenticated user from the context, call one
// application service, and map the result (or error) to JSON. They contain
// no business, authorization or execution logic.
package handlers

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/google/uuid"

	"workflow-optimizer/internal/api/httpx"
	"workflow-optimizer/internal/api/middleware"
	"workflow-optimizer/internal/api/requests"
	"workflow-optimizer/internal/application"
	"workflow-optimizer/internal/node"
)

// NodeCatalog is the node registry's metadata view.
type NodeCatalog interface {
	Definitions() []node.NodeDefinition
}

// Check is one readiness dependency check.
type Check struct {
	Name  string
	Check func(context.Context) error
}

// Handlers are the API's endpoints.
type Handlers struct {
	Auth        *application.AuthService
	Workflows   *application.WorkflowService
	Executions  *application.ExecutionService
	Credentials *application.CredentialService
	// ConnectedAccounts serves connected accounts and the OAuth flow
	// (Phase C2).
	ConnectedAccounts *application.ConnectedAccountService
	// Observability serves the execution debugger (Phase 14).
	Observability *application.ObservabilityService
	Nodes         NodeCatalog
	Ready         []Check
	Logger        *slog.Logger
}

func (h *Handlers) fail(w http.ResponseWriter, r *http.Request, err error) {
	httpx.WriteError(w, r, h.Logger, err)
}

// decode decodes the body into dst, writing the error response on failure.
func (h *Handlers) decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	if err := httpx.DecodeJSON(w, r, dst); err != nil {
		h.fail(w, r, err)
		return false
	}
	return true
}

// pathID parses a UUID path parameter, writing the error response on failure.
func (h *Handlers) pathID(w http.ResponseWriter, r *http.Request, name string) (uuid.UUID, bool) {
	id, err := requests.ParseUUID(name, r.PathValue(name))
	if err != nil {
		h.fail(w, r, err)
		return uuid.Nil, false
	}
	return id, true
}

// queryID parses a required UUID query parameter.
func (h *Handlers) queryID(w http.ResponseWriter, r *http.Request, name string) (uuid.UUID, bool) {
	v := r.URL.Query().Get(name)
	if v == "" {
		h.fail(w, r, httpx.BadRequest(name+" is required"))
		return uuid.Nil, false
	}
	id, err := requests.ParseUUID(name, v)
	if err != nil {
		h.fail(w, r, err)
		return uuid.Nil, false
	}
	return id, true
}

func user(r *http.Request) uuid.UUID { return middleware.UserID(r.Context()) }
