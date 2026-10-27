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
// /health, /ready, /auth/register, /auth/login and the OAuth callback are
// public; everything else
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
	private("PATCH "+Prefix+"/auth/me", h.UpdateProfile)
	private("POST "+Prefix+"/auth/password", h.ChangePassword)
	private("PATCH "+Prefix+"/workspaces/{workspaceID}", h.RenameWorkspace)
	private("GET "+Prefix+"/workspaces/{workspaceID}/usage", h.WorkspaceUsage)

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
	private("PUT "+Prefix+"/workflows/{workflowID}/versions/{versionID}", h.UpdateDraft)
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

	// Connected accounts and OAuth (Phase C2). The callback is a browser
	// redirect from the provider: it carries no bearer token and is
	// authorized by its single-use, browser-bound state instead.
	private("GET "+Prefix+"/oauth/providers", h.ListOAuthProviders)
	private("GET "+Prefix+"/connected-accounts", h.ListConnectedAccounts)
	private("GET "+Prefix+"/connected-accounts/{accountID}", h.GetConnectedAccount)
	private("DELETE "+Prefix+"/connected-accounts/{accountID}", h.DisconnectConnectedAccount)
	private("POST "+Prefix+"/connected-accounts/{provider}/authorize", h.AuthorizeConnectedAccount)
	public("GET "+Prefix+"/oauth/callback/{provider}", h.OAuthCallback)

	private("POST "+Prefix+"/files", h.UploadFile)
	private("GET "+Prefix+"/files", h.ListFiles)
	private("GET "+Prefix+"/files/{fileID}", h.GetFile)
	private("GET "+Prefix+"/files/{fileID}/content", h.DownloadFile)
	private("DELETE "+Prefix+"/files/{fileID}", h.DeleteFile)
	private("POST "+Prefix+"/workflows/{workflowID}/test", h.TestWorkflow)
	private("POST "+Prefix+"/knowledge-bases", h.CreateKnowledgeBase)
	private("GET "+Prefix+"/knowledge-bases", h.ListKnowledgeBases)
	private("GET "+Prefix+"/knowledge-bases/{baseID}", h.GetKnowledgeBase)
	private("DELETE "+Prefix+"/knowledge-bases/{baseID}", h.DeleteKnowledgeBase)
	private("POST "+Prefix+"/knowledge-bases/{baseID}/documents", h.AddKnowledgeDocument)
	private("DELETE "+Prefix+"/knowledge-bases/{baseID}/documents/{documentID}", h.DeleteKnowledgeDocument)
	private("POST "+Prefix+"/knowledge-bases/{baseID}/documents/{documentID}/reindex", h.ReindexKnowledgeDocument)
	private("POST "+Prefix+"/knowledge-bases/{baseID}/search", h.SearchKnowledgeBase)
	private("POST "+Prefix+"/knowledge-bases/{baseID}/local-copy", h.CopyKnowledgeLocal)
	private("GET "+Prefix+"/nodes", h.ListNodes)
	private("GET "+Prefix+"/assistant/tools", h.AssistantTools)
	private("GET "+Prefix+"/assistant/workflows", h.EditorWorkflows)
	private("POST "+Prefix+"/assistant/workflows", h.EditorWorkflows)
	private("GET "+Prefix+"/assistant/workflows/{workflowID}", h.GetEditorWorkflow)
	private("GET "+Prefix+"/assistant/workflows/{workflowID}/versions", h.EditorVersions)
	private("POST "+Prefix+"/assistant/workflows/{workflowID}/versions", h.EditorVersions)
	private("POST "+Prefix+"/assistant/workflows/{workflowID}/restore", h.RestoreEditorWorkflow)
	private("GET "+Prefix+"/assistant/workflows/{workflowID}/layout", h.EditorLayout)
	private("PUT "+Prefix+"/assistant/workflows/{workflowID}/layout", h.EditorLayout)
	private("POST "+Prefix+"/assistant/workflows/{workflowID}/template", h.SaveEditorTemplate)
	private("GET "+Prefix+"/assistant/workflow-templates", h.EditorTemplates)
	private("DELETE "+Prefix+"/assistant/workflow-templates/{templateID}", h.DeleteEditorTemplate)
	private("POST "+Prefix+"/assistant/import", h.ImportLegacyWorkflow)
	private("POST "+Prefix+"/assistant/sessions", h.CreateAssistantSession)
	private("GET "+Prefix+"/assistant/sessions", h.ListAssistantSessions)
	private("GET "+Prefix+"/assistant/sessions/{sessionID}", h.GetAssistantSession)
	private("GET "+Prefix+"/assistant/sessions/{sessionID}/build", h.GetAssistantBuildProgress)
	private("POST "+Prefix+"/assistant/sessions/{sessionID}/messages", h.AssistantMessage)
	private("PUT "+Prefix+"/assistant/sessions/{sessionID}/plan", h.EditAssistantPlan)
	private("POST "+Prefix+"/assistant/sessions/{sessionID}/check", h.CheckEditorPlan)
	private("POST "+Prefix+"/assistant/sessions/{sessionID}/repair", h.RepairAssistantPlan)
	private("POST "+Prefix+"/assistant/sessions/{sessionID}/answers", h.AnswerAssistantDraft)
	private("POST "+Prefix+"/assistant/sessions/{sessionID}/runs", h.StartAssistantRun)
	private("GET "+Prefix+"/automation-runs", h.ListAutomationRuns)
	private("GET "+Prefix+"/automation-runs/{runID}", h.GetAutomationRun)
	private("GET "+Prefix+"/automation-runs/{runID}/events", h.StreamAutomationRun)
	private("POST "+Prefix+"/automation-runs/{runID}/answers", h.AnswerAutomationRun)
	private("POST "+Prefix+"/automation-runs/{runID}/control", h.ControlAutomationRun)
	private("POST "+Prefix+"/automation-runs/{runID}/events", h.SendAutomationEvent)
	private("GET "+Prefix+"/automations", h.ListAutomations)
	private("PUT "+Prefix+"/automations", h.SaveAutomation)
	private("DELETE "+Prefix+"/automations/{automationID}", h.DeleteAutomation)
	public("POST "+Prefix+"/triggers/{automationID}", h.AutomationWebhook)
	private("POST "+Prefix+"/voice/token", h.VoiceToken)
	private("POST "+Prefix+"/voice/speak", h.VoiceSpeak)
	private("GET "+Prefix+"/whatsapp", h.WhatsAppStatus)
	private("POST "+Prefix+"/whatsapp/connect", h.WhatsAppConnect)
	private("POST "+Prefix+"/whatsapp/disconnect", h.WhatsAppDisconnect)
	private("POST "+Prefix+"/nodes/{nodeType}/options", h.ListNodeOptions)
	private("GET "+Prefix+"/templates", h.ListTemplates)
	private("POST "+Prefix+"/templates", h.SaveTemplate)
	private("DELETE "+Prefix+"/templates/{templateID}", h.DeleteTemplate)

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
