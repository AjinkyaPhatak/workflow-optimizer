package handlers

import (
	"net/http"

	"workflow-optimizer/internal/api/httpx"
	"workflow-optimizer/internal/api/requests"
	"workflow-optimizer/internal/api/responses"
)

// Execute handles POST /api/v1/workflows/{workflowID}/execute. It persists a
// PENDING execution, enqueues it and answers 202 at once; a worker runs it.
func (h *Handlers) Execute(w http.ResponseWriter, r *http.Request) {
	id, ok := h.pathID(w, r, "workflowID")
	if !ok {
		return
	}
	var req requests.Execute
	if !h.decode(w, r, &req) {
		return
	}
	version, input, err := req.Validate()
	if err != nil {
		h.fail(w, r, err)
		return
	}
	e, err := h.Executions.Execute(r.Context(), user(r), id, version, input)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/v1/executions/"+e.ID.String())
	httpx.WriteJSON(w, http.StatusAccepted, responses.ExecutionAccepted{ExecutionID: e.ID, Status: string(e.Status)})
}

// GetExecution handles GET /api/v1/executions/{executionID}.
func (h *Handlers) GetExecution(w http.ResponseWriter, r *http.Request) {
	id, ok := h.pathID(w, r, "executionID")
	if !ok {
		return
	}
	e, err := h.Executions.Get(r.Context(), user(r), id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, responses.NewExecution(e))
}

// ListNodeExecutions handles GET /api/v1/executions/{executionID}/nodes.
func (h *Handlers) ListNodeExecutions(w http.ResponseWriter, r *http.Request) {
	id, ok := h.pathID(w, r, "executionID")
	if !ok {
		return
	}
	ns, err := h.Executions.Nodes(r.Context(), user(r), id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := responses.List[responses.NodeExecution]{Items: make([]responses.NodeExecution, 0, len(ns))}
	for _, n := range ns {
		out.Items = append(out.Items, responses.NewNodeExecution(n))
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// CancelExecution handles POST /api/v1/executions/{executionID}/cancel.
func (h *Handlers) CancelExecution(w http.ResponseWriter, r *http.Request) {
	id, ok := h.pathID(w, r, "executionID")
	if !ok {
		return
	}
	e, err := h.Executions.Cancel(r.Context(), user(r), id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusAccepted, responses.NewExecution(e))
}
