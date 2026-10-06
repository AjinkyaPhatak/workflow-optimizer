package handlers

import (
	"net/http"

	"workflow-optimizer/internal/api/httpx"
	"workflow-optimizer/internal/api/requests"
	"workflow-optimizer/internal/api/responses"
)

// CreateCredential handles POST /api/v1/credentials. The secret goes straight
// to the credential service (encrypted before storage) and is never echoed.
func (h *Handlers) CreateCredential(w http.ResponseWriter, r *http.Request) {
	var req requests.CreateCredential
	if !h.decode(w, r, &req) {
		return
	}
	in, err := req.Validate()
	if err != nil {
		h.fail(w, r, err)
		return
	}
	c, err := h.Credentials.Create(r.Context(), user(r), in)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, responses.NewCredential(c))
}

// ListCredentials handles GET /api/v1/credentials?workspace_id= (metadata
// only).
func (h *Handlers) ListCredentials(w http.ResponseWriter, r *http.Request) {
	ws, ok := h.queryID(w, r, "workspace_id")
	if !ok {
		return
	}
	cs, err := h.Credentials.List(r.Context(), user(r), ws)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := responses.List[responses.Credential]{Items: make([]responses.Credential, 0, len(cs))}
	for _, c := range cs {
		out.Items = append(out.Items, responses.NewCredential(c))
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// DeleteCredential handles DELETE /api/v1/credentials/{credentialID}.
func (h *Handlers) DeleteCredential(w http.ResponseWriter, r *http.Request) {
	id, ok := h.pathID(w, r, "credentialID")
	if !ok {
		return
	}
	if err := h.Credentials.Delete(r.Context(), user(r), id); err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.NoContent(w)
}
