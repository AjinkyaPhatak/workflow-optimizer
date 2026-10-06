package handlers

import (
	"net/http"

	"workflow-optimizer/internal/api/httpx"
	"workflow-optimizer/internal/api/requests"
	"workflow-optimizer/internal/api/responses"
)

// Register handles POST /api/v1/auth/register: a new user with a workspace
// they own, signed in.
func (h *Handlers) Register(w http.ResponseWriter, r *http.Request) {
	var req requests.Register
	if !h.decode(w, r, &req) {
		return
	}
	in, err := req.Validate()
	if err != nil {
		h.fail(w, r, err)
		return
	}
	sess, ws, err := h.Auth.Register(r.Context(), in)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, responses.NewToken(sess, &ws))
}

// Login handles POST /api/v1/auth/login.
func (h *Handlers) Login(w http.ResponseWriter, r *http.Request) {
	var req requests.Login
	if !h.decode(w, r, &req) {
		return
	}
	if err := req.Validate(); err != nil {
		h.fail(w, r, err)
		return
	}
	sess, err := h.Auth.Login(r.Context(), req.Email, req.Password)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, responses.NewToken(sess, nil))
}

// Me handles GET /api/v1/auth/me.
func (h *Handlers) Me(w http.ResponseWriter, r *http.Request) {
	u, ms, err := h.Auth.Me(r.Context(), user(r))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, responses.NewMe(u, ms))
}
