package handlers

import (
	"net/http"
	"strconv"

	"workflow-optimizer/internal/api/httpx"
	"workflow-optimizer/internal/api/requests"
	"workflow-optimizer/internal/api/responses"
)

// Account settings and usage (Phase M).

// UpdateProfile handles PATCH /api/v1/auth/me: the user's name.
func (h *Handlers) UpdateProfile(w http.ResponseWriter, r *http.Request) {
	var req requests.UpdateProfile
	if !h.decode(w, r, &req) {
		return
	}
	u, err := h.Account.UpdateProfile(r.Context(), user(r), req.Name)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, responses.NewUser(u))
}

// ChangePassword handles POST /api/v1/auth/password.
func (h *Handlers) ChangePassword(w http.ResponseWriter, r *http.Request) {
	var req requests.ChangePassword
	if !h.decode(w, r, &req) {
		return
	}
	if err := h.Account.ChangePassword(r.Context(), user(r), req.CurrentPassword, req.NewPassword); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// RenameWorkspace handles PATCH /api/v1/workspaces/{workspaceID}.
func (h *Handlers) RenameWorkspace(w http.ResponseWriter, r *http.Request) {
	id, ok := h.pathID(w, r, "workspaceID")
	if !ok {
		return
	}
	var req requests.RenameWorkspace
	if !h.decode(w, r, &req) {
		return
	}
	ws, role, err := h.Account.RenameWorkspace(r.Context(), user(r), id, req.Name)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, responses.Workspace{ID: ws.ID, Name: ws.Name, Role: string(role)})
}

// WorkspaceUsage handles GET /api/v1/workspaces/{workspaceID}/usage?days=30.
func (h *Handlers) WorkspaceUsage(w http.ResponseWriter, r *http.Request) {
	id, ok := h.pathID(w, r, "workspaceID")
	if !ok {
		return
	}
	days := 30
	if v := r.URL.Query().Get("days"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			h.fail(w, r, httpx.BadRequest("days must be a whole number"))
			return
		}
		days = n
	}
	rep, err := h.Account.Usage(r.Context(), user(r), id, days)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, responses.NewUsage(rep, days))
}
