package handlers

import (
	"net/http"

	"workflow-optimizer/internal/api/httpx"
	"workflow-optimizer/internal/api/requests"
	"workflow-optimizer/internal/api/responses"
	"workflow-optimizer/internal/execution"
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
	d, err := h.Observability.GetExecution(r.Context(), user(r), id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, responses.NewExecutionDetails(d))
}

// ListNodeExecutions handles GET /api/v1/executions/{executionID}/nodes.
func (h *Handlers) ListNodeExecutions(w http.ResponseWriter, r *http.Request) {
	id, ok := h.pathID(w, r, "executionID")
	if !ok {
		return
	}
	ns, err := h.Observability.GetNodeExecutions(r.Context(), user(r), id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := responses.List[responses.NodeExecutionDetails]{Items: make([]responses.NodeExecutionDetails, 0, len(ns))}
	for _, n := range ns {
		out.Items = append(out.Items, responses.NewNodeExecutionDetails(n))
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// ListEvents handles GET /api/v1/executions/{executionID}/events
// (?page=&page_size=, at most 100 per page; chronological).
func (h *Handlers) ListEvents(w http.ResponseWriter, r *http.Request) {
	id, ok := h.pathID(w, r, "executionID")
	if !ok {
		return
	}
	page, err := httpx.ParsePageDefault(r, 50)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	events, total, err := h.Observability.GetEvents(r.Context(), user(r), id, execution.EventQuery{Page: page.Number, PageSize: page.Size})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := responses.Events{Events: make([]responses.Event, 0, len(events)), Page: page.Number, PageSize: page.Size, Total: total}
	for _, e := range events {
		out.Events = append(out.Events, responses.NewEvent(e))
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// ListWorkflowExecutions handles GET /api/v1/workflows/{workflowID}/executions.
func (h *Handlers) ListWorkflowExecutions(w http.ResponseWriter, r *http.Request) {
	id, ok := h.pathID(w, r, "workflowID")
	if !ok {
		return
	}
	page, err := httpx.ParsePage(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	execs, total, err := h.Observability.ListExecutions(r.Context(), user(r), id, page)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := responses.PageOf[responses.Execution]{Items: make([]responses.Execution, 0, len(execs)), Page: page.Number, PageSize: page.Size, Total: total}
	for _, e := range execs {
		out.Items = append(out.Items, responses.NewExecution(e))
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
