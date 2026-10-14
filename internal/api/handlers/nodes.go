package handlers

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"workflow-optimizer/internal/api/httpx"
	"workflow-optimizer/internal/api/responses"
	"workflow-optimizer/internal/application"
)

// ListNodes handles GET /api/v1/nodes: every node type registered in the
// node registry, sorted by type.
func (h *Handlers) ListNodes(w http.ResponseWriter, r *http.Request) {
	defs := h.Nodes.Definitions()
	out := responses.List[responses.NodeType]{Items: make([]responses.NodeType, 0, len(defs))}
	for _, d := range defs {
		out.Items = append(out.Items, responses.NewNodeType(d))
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// nodeOptionsRequest is the body of POST /nodes/{nodeType}/options.
type nodeOptionsRequest struct {
	WorkspaceID string         `json:"workspace_id"`
	Source      string         `json:"source"`
	Config      map[string]any `json:"config"`
}

// ListNodeOptions handles POST /api/v1/nodes/{nodeType}/options (Phase C8):
// the choices of one setting, listed through the node's account.
func (h *Handlers) ListNodeOptions(w http.ResponseWriter, r *http.Request) {
	var req nodeOptionsRequest
	if !h.decode(w, r, &req) {
		return
	}
	ws, err := uuid.Parse(strings.TrimSpace(req.WorkspaceID))
	if err != nil {
		h.fail(w, r, httpx.BadRequest("workspace_id must be a UUID"))
		return
	}
	if strings.TrimSpace(req.Source) == "" {
		h.fail(w, r, httpx.BadRequest("source is required"))
		return
	}
	opts, err := h.NodeOptions.Options(r.Context(), user(r), application.NodeOptionsRequest{
		WorkspaceID: ws, NodeType: r.PathValue("nodeType"), Source: req.Source, Config: req.Config})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := responses.List[responses.ConfigOption]{Items: make([]responses.ConfigOption, 0, len(opts))}
	for _, o := range opts {
		label := o.Label
		if label == "" {
			label = fmt.Sprint(o.Value)
		}
		out.Items = append(out.Items, responses.ConfigOption{Value: o.Value, Label: label})
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}
