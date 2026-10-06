package handlers

import (
	"context"
	"net/http"
	"time"

	"workflow-optimizer/internal/api/httpx"
	"workflow-optimizer/internal/api/responses"
)

// ReadyTimeout bounds each readiness check.
const ReadyTimeout = 2 * time.Second

// Health handles GET /health: the process is up. It checks no dependency.
func (h *Handlers) Health(w http.ResponseWriter, _ *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// Readiness handles GET /ready: PostgreSQL and Redis must answer. Providers
// (OpenAI) are deliberately not checked.
func (h *Handlers) Readiness(w http.ResponseWriter, r *http.Request) {
	checks := map[string]string{}
	ready := true
	for _, c := range h.Ready {
		ctx, cancel := context.WithTimeout(r.Context(), ReadyTimeout)
		err := c.Check(ctx)
		cancel()
		if err != nil {
			ready = false
			checks[c.Name] = "unavailable"
			h.Logger.WarnContext(r.Context(), "readiness check failed", "check", c.Name, "error", err)
			continue
		}
		checks[c.Name] = "ok"
	}
	if !ready {
		httpx.WriteJSON(w, http.StatusServiceUnavailable, responses.ErrorBody{Error: responses.ErrorDetail{
			Code: httpx.CodeServiceUnavailable, Message: "A required dependency is unavailable", Details: map[string]any{"checks": checks}}})
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"status": "ready", "checks": checks})
}
