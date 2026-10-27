package handlers

import (
	"github.com/google/uuid"
	"net/http"
	"workflow-optimizer/internal/api/httpx"
	"workflow-optimizer/internal/application"
	"workflow-optimizer/internal/automation"
)

func (h *Handlers) EditorWorkflows(w http.ResponseWriter, r *http.Request) {
	if !h.assistantReady(w, r) {
		return
	}
	if r.Method == http.MethodGet {
		ws, ok := h.queryID(w, r, "workspace_id")
		if !ok {
			return
		}
		out, err := h.Assistant.Workflows(r.Context(), user(r), ws)
		if err != nil {
			h.fail(w, r, err)
			return
		}
		httpx.WriteJSON(w, 200, map[string]any{"items": out})
		return
	}
	var in application.CreateAutomationWorkflow
	if !h.decode(w, r, &in) {
		return
	}
	out, err := h.Assistant.CreateWorkflow(r.Context(), user(r), in, h.Workflows)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, 201, out)
}
func (h *Handlers) editorID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	if !h.assistantReady(w, r) {
		return uuid.Nil, false
	}
	return h.pathID(w, r, "workflowID")
}
func (h *Handlers) GetEditorWorkflow(w http.ResponseWriter, r *http.Request) {
	id, ok := h.editorID(w, r)
	if !ok {
		return
	}
	out, err := h.Assistant.Editor(r.Context(), user(r), id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, 200, out)
}
func (h *Handlers) EditorVersions(w http.ResponseWriter, r *http.Request) {
	id, ok := h.editorID(w, r)
	if !ok {
		return
	}
	if r.Method == http.MethodGet {
		out, err := h.Assistant.WorkflowVersions(r.Context(), user(r), id)
		if err != nil {
			h.fail(w, r, err)
			return
		}
		httpx.WriteJSON(w, 200, map[string]any{"items": out})
		return
	}
	var in struct {
		Revision int `json:"revision"`
	}
	if !h.decode(w, r, &in) {
		return
	}
	out, err := h.Assistant.PublishWorkflow(r.Context(), user(r), id, in.Revision)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, 201, out)
}
func (h *Handlers) RestoreEditorWorkflow(w http.ResponseWriter, r *http.Request) {
	id, ok := h.editorID(w, r)
	if !ok {
		return
	}
	var in struct {
		Revision  int       `json:"revision"`
		VersionID uuid.UUID `json:"version_id"`
	}
	if !h.decode(w, r, &in) {
		return
	}
	out, err := h.Assistant.RestoreWorkflow(r.Context(), user(r), id, in.VersionID, in.Revision)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, 200, out)
}
func (h *Handlers) EditorLayout(w http.ResponseWriter, r *http.Request) {
	id, ok := h.editorID(w, r)
	if !ok {
		return
	}
	if r.Method == http.MethodGet {
		out, err := h.Assistant.WorkflowLayout(r.Context(), user(r), id)
		if err != nil {
			h.fail(w, r, err)
			return
		}
		httpx.WriteJSON(w, 200, out)
		return
	}
	var in automation.EditorLayout
	if !h.decode(w, r, &in) {
		return
	}
	out, err := h.Assistant.SaveWorkflowLayout(r.Context(), user(r), id, in)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, 200, out)
}
func (h *Handlers) EditorTemplates(w http.ResponseWriter, r *http.Request) {
	if !h.assistantReady(w, r) {
		return
	}
	ws, ok := h.queryID(w, r, "workspace_id")
	if !ok {
		return
	}
	out, err := h.Assistant.WorkflowTemplates(r.Context(), user(r), ws)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, 200, map[string]any{"items": out})
}
func (h *Handlers) SaveEditorTemplate(w http.ResponseWriter, r *http.Request) {
	id, ok := h.editorID(w, r)
	if !ok {
		return
	}
	var in struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if !h.decode(w, r, &in) {
		return
	}
	out, err := h.Assistant.SaveWorkflowTemplate(r.Context(), user(r), id, in.Name, in.Description)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, 201, out)
}
func (h *Handlers) CheckEditorPlan(w http.ResponseWriter, r *http.Request) {
	if !h.assistantReady(w, r) {
		return
	}
	id, ok := h.pathID(w, r, "sessionID")
	if !ok {
		return
	}
	var in struct {
		Revision int             `json:"revision"`
		Plan     automation.Plan `json:"plan"`
	}
	if !h.decode(w, r, &in) {
		return
	}
	out, err := h.Assistant.CheckPlan(r.Context(), user(r), id, in.Revision, in.Plan)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, 200, out)
}

func (h *Handlers) GetAssistantBuildProgress(w http.ResponseWriter, r *http.Request) {
	if !h.assistantReady(w, r) {
		return
	}
	id, ok := h.pathID(w, r, "sessionID")
	if !ok {
		return
	}
	out, err := h.Assistant.BuildProgress(r.Context(), user(r), id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, 200, out)
}

func (h *Handlers) DeleteEditorTemplate(w http.ResponseWriter, r *http.Request) {
	if !h.assistantReady(w, r) {
		return
	}
	id, ok := h.pathID(w, r, "templateID")
	if !ok {
		return
	}
	if err := h.Assistant.DeleteWorkflowTemplate(r.Context(), user(r), id); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
