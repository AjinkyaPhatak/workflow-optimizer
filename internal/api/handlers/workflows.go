package handlers

import (
	"net/http"

	"workflow-optimizer/internal/api/httpx"
	"workflow-optimizer/internal/api/requests"
	"workflow-optimizer/internal/api/responses"
	"workflow-optimizer/internal/templates"
)

// CreateProject handles POST /api/v1/projects.
func (h *Handlers) CreateProject(w http.ResponseWriter, r *http.Request) {
	var req requests.CreateProject
	if !h.decode(w, r, &req) {
		return
	}
	ws, name, desc, err := req.Validate()
	if err != nil {
		h.fail(w, r, err)
		return
	}
	p, err := h.Workflows.CreateProject(r.Context(), user(r), ws, name, desc)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, responses.NewProject(p))
}

// ListProjects handles GET /api/v1/projects?workspace_id=.
func (h *Handlers) ListProjects(w http.ResponseWriter, r *http.Request) {
	ws, ok := h.queryID(w, r, "workspace_id")
	if !ok {
		return
	}
	ps, err := h.Workflows.ListProjects(r.Context(), user(r), ws)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := responses.List[responses.Project]{Items: make([]responses.Project, 0, len(ps))}
	for _, p := range ps {
		out.Items = append(out.Items, responses.NewProject(p))
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// GetProject handles GET /api/v1/projects/{projectID}.
func (h *Handlers) GetProject(w http.ResponseWriter, r *http.Request) {
	id, ok := h.pathID(w, r, "projectID")
	if !ok {
		return
	}
	p, err := h.Workflows.GetProject(r.Context(), user(r), id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, responses.NewProject(p))
}

// CreateWorkflow handles POST /api/v1/workflows (metadata only).
func (h *Handlers) CreateWorkflow(w http.ResponseWriter, r *http.Request) {
	var req requests.CreateWorkflow
	if !h.decode(w, r, &req) {
		return
	}
	project, name, desc, err := req.Validate()
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if req.TemplateID != nil && *req.TemplateID != "" {
		wf, v, err := h.Workflows.CreateWorkflowFromTemplate(r.Context(), user(r), project, name, desc, *req.TemplateID)
		if err != nil {
			h.fail(w, r, err)
			return
		}
		httpx.WriteJSON(w, http.StatusCreated, responses.NewWorkflowFromTemplate(wf, v))
		return
	}
	wf, err := h.Workflows.CreateWorkflow(r.Context(), user(r), project, name, desc)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, responses.NewWorkflow(wf))
}

// ListTemplates handles GET /api/v1/templates: the workflow templates a new
// workflow can start from.
func (h *Handlers) ListTemplates(w http.ResponseWriter, r *http.Request) {
	out := responses.List[responses.Template]{Items: []responses.Template{}}
	for _, t := range templates.List() {
		out.Items = append(out.Items, responses.NewTemplate(t))
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// ListWorkflows handles GET /api/v1/workflows?project_id=&page=&page_size=.
func (h *Handlers) ListWorkflows(w http.ResponseWriter, r *http.Request) {
	project, ok := h.queryID(w, r, "project_id")
	if !ok {
		return
	}
	page, err := httpx.ParsePage(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	wfs, total, err := h.Workflows.ListWorkflows(r.Context(), user(r), project, page)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := responses.PageOf[responses.Workflow]{Items: make([]responses.Workflow, 0, len(wfs)), Page: page.Number, PageSize: page.Size, Total: total}
	for _, wf := range wfs {
		out.Items = append(out.Items, responses.NewWorkflow(wf))
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// GetWorkflow handles GET /api/v1/workflows/{workflowID}.
func (h *Handlers) GetWorkflow(w http.ResponseWriter, r *http.Request) {
	id, ok := h.pathID(w, r, "workflowID")
	if !ok {
		return
	}
	wf, err := h.Workflows.GetWorkflow(r.Context(), user(r), id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, responses.NewWorkflow(wf))
}

// UpdateWorkflow handles PATCH /api/v1/workflows/{workflowID}.
func (h *Handlers) UpdateWorkflow(w http.ResponseWriter, r *http.Request) {
	id, ok := h.pathID(w, r, "workflowID")
	if !ok {
		return
	}
	var req requests.UpdateWorkflow
	if !h.decode(w, r, &req) {
		return
	}
	u, err := req.Validate()
	if err != nil {
		h.fail(w, r, err)
		return
	}
	wf, err := h.Workflows.UpdateWorkflow(r.Context(), user(r), id, u)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, responses.NewWorkflow(wf))
}

// DeleteWorkflow handles DELETE /api/v1/workflows/{workflowID}.
func (h *Handlers) DeleteWorkflow(w http.ResponseWriter, r *http.Request) {
	id, ok := h.pathID(w, r, "workflowID")
	if !ok {
		return
	}
	if err := h.Workflows.DeleteWorkflow(r.Context(), user(r), id); err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.NoContent(w)
}
