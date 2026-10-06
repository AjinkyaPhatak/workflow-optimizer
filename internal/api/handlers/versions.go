package handlers

import (
	"net/http"

	"github.com/google/uuid"

	"workflow-optimizer/internal/api/httpx"
	"workflow-optimizer/internal/api/requests"
	"workflow-optimizer/internal/api/responses"
)

func (h *Handlers) versionPath(w http.ResponseWriter, r *http.Request) (uuid.UUID, uuid.UUID, bool) {
	wf, ok := h.pathID(w, r, "workflowID")
	if !ok {
		return uuid.Nil, uuid.Nil, false
	}
	v, ok := h.pathID(w, r, "versionID")
	return wf, v, ok
}

// CreateVersion handles POST /api/v1/workflows/{workflowID}/versions.
func (h *Handlers) CreateVersion(w http.ResponseWriter, r *http.Request) {
	id, ok := h.pathID(w, r, "workflowID")
	if !ok {
		return
	}
	var req requests.CreateVersion
	if !h.decode(w, r, &req) {
		return
	}
	def, err := req.Validate()
	if err != nil {
		h.fail(w, r, err)
		return
	}
	v, err := h.Workflows.CreateVersion(r.Context(), user(r), id, def)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, responses.NewVersion(v))
}

// ListVersions handles GET /api/v1/workflows/{workflowID}/versions.
func (h *Handlers) ListVersions(w http.ResponseWriter, r *http.Request) {
	id, ok := h.pathID(w, r, "workflowID")
	if !ok {
		return
	}
	page, err := httpx.ParsePage(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	vs, total, err := h.Workflows.ListVersions(r.Context(), user(r), id, page)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := responses.PageOf[responses.VersionSummary]{Items: make([]responses.VersionSummary, 0, len(vs)), Page: page.Number, PageSize: page.Size, Total: total}
	for _, v := range vs {
		out.Items = append(out.Items, responses.NewVersionSummary(v))
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// GetVersion handles GET /api/v1/workflows/{workflowID}/versions/{versionID}.
func (h *Handlers) GetVersion(w http.ResponseWriter, r *http.Request) {
	wf, v, ok := h.versionPath(w, r)
	if !ok {
		return
	}
	ver, err := h.Workflows.GetVersion(r.Context(), user(r), wf, v)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, responses.NewVersion(ver))
}

// ValidateVersion handles POST .../versions/{versionID}/validate.
func (h *Handlers) ValidateVersion(w http.ResponseWriter, r *http.Request) {
	wf, v, ok := h.versionPath(w, r)
	if !ok {
		return
	}
	res, err := h.Workflows.ValidateVersion(r.Context(), user(r), wf, v)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, responses.NewValidation(res))
}

// PublishVersion handles POST .../versions/{versionID}/publish.
func (h *Handlers) PublishVersion(w http.ResponseWriter, r *http.Request) {
	wf, v, ok := h.versionPath(w, r)
	if !ok {
		return
	}
	ver, err := h.Workflows.PublishVersion(r.Context(), user(r), wf, v)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, responses.NewVersionSummary(ver))
}
