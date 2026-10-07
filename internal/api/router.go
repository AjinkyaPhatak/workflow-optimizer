package api

import (
	"log/slog"
	"net/http"

	"workflow-optimizer/internal/api/handlers"
	"workflow-optimizer/internal/api/httpx"
	"workflow-optimizer/internal/api/middleware"
	"workflow-optimizer/internal/auth"
	"workflow-optimizer/internal/observability"
)

// Prefix is the versioned API root.
const Prefix = "/api/v1"

// NewRouter wires every endpoint:
//
//	request ID -> access log -> panic recovery -> route -> [bearer auth] -> handler
//
// /health, /ready, /auth/register and /auth/login are public; everything else
// requires a bearer token, and resources are authorized per workspace by the
// application services.
func NewRouter(h *handlers.Handlers, tokens auth.TokenService, logger *slog.Logger) http.Handler {
	// Every context-aware log line carries the request's X-Request-ID.
	logger = observability.NewContextLogger(logger)
	if h.Logger == nil {
		h.Logger = logger
	}
	mux := http.NewServeMux()
	authed := middleware.Authenticate(tokens, logger)
	public := func(pattern string, fn http.HandlerFunc) { mux.Handle(pattern, fn) }
	private := func(pattern string, fn http.HandlerFunc) { mux.Handle(pattern, authed(fn)) }

	public("GET /health", h.Health)
	public("GET /ready", h.Readiness)

	public("POST "+Prefix+"/auth/register", h.Register)
	public("POST "+Prefix+"/auth/login", h.Login)
	private("GET "+Prefix+"/auth/me", h.Me)

	private("POST "+Prefix+"/projects", h.CreateProject)
	private("GET "+Prefix+"/projects", h.ListProjects)
	private("GET "+Prefix+"/projects/{projectID}", h.GetProject)

	private("POST "+Prefix+"/workflows", h.CreateWorkflow)
	private("GET "+Prefix+"/workflows", h.ListWorkflows)
	private("GET "+Prefix+"/workflows/{workflowID}", h.GetWorkflow)
	private("PATCH "+Prefix+"/workflows/{workflowID}", h.UpdateWorkflow)
	private("DELETE "+Prefix+"/workflows/{workflowID}", h.DeleteWorkflow)

	private("POST "+Prefix+"/workflows/{workflowID}/versions", h.CreateVersion)
	private("GET "+Prefix+"/workflows/{workflowID}/versions", h.ListVersions)
	private("GET "+Prefix+"/workflows/{workflowID}/versions/{versionID}", h.GetVersion)
	private("POST "+Prefix+"/workflows/{workflowID}/versions/{versionID}/validate", h.ValidateVersion)
	private("POST "+Prefix+"/workflows/{workflowID}/versions/{versionID}/publish", h.PublishVersion)

	private("POST "+Prefix+"/workflows/{workflowID}/execute", h.Execute)
	private("GET "+Prefix+"/executions/{executionID}", h.GetExecution)
	private("GET "+Prefix+"/executions/{executionID}/nodes", h.ListNodeExecutions)
	private("POST "+Prefix+"/executions/{executionID}/cancel", h.CancelExecution)
	private("GET "+Prefix+"/executions/{executionID}/events", h.ListEvents)
	private("GET "+Prefix+"/workflows/{workflowID}/executions", h.ListWorkflowExecutions)

	private("POST "+Prefix+"/credentials", h.CreateCredential)
	private("GET "+Prefix+"/credentials", h.ListCredentials)
	private("DELETE "+Prefix+"/credentials/{credentialID}", h.DeleteCredential)

	private("GET "+Prefix+"/nodes", h.ListNodes)

	mux.Handle("/", unmatched(mux, logger))

	var root http.Handler = mux
	root = middleware.Recover(logger)(root)
	root = middleware.Log(logger)(root)
	root = middleware.RequestID(root)
	return root
}

// unmatched answers requests no route takes: 405 when the path exists for
// another method, 404 otherwise, both in the API error shape.
func unmatched(mux *http.ServeMux, logger *slog.Logger) http.Handler {
	methods := []string{http.MethodGet, http.MethodPost, http.MethodPatch, http.MethodPut, http.MethodDelete}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var allowed []string
		for _, m := range methods {
			probe := r.Clone(r.Context())
			probe.Method = m
			if _, pattern := mux.Handler(probe); pattern != "" && pattern != "/" {
				allowed = append(allowed, m)
			}
		}
		if len(allowed) > 0 {
			for _, m := range allowed {
				w.Header().Add("Allow", m)
			}
			httpx.WriteError(w, r, logger, &httpx.Error{Status: http.StatusMethodNotAllowed, Code: httpx.CodeMethodNotAllowed, Message: "Method not allowed"})
			return
		}
		httpx.WriteError(w, r, logger, &httpx.Error{Status: http.StatusNotFound, Code: httpx.CodeNotFound, Message: "Route not found"})
	})
}
