package handlers

import (
	"net/http"

	"workflow-optimizer/internal/api/httpx"
	"workflow-optimizer/internal/api/responses"
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
