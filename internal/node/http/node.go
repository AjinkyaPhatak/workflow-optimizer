package http

import (
	"context"

	"workflow-optimizer/internal/node"
)

// NodeType is the stable identifier for HTTP nodes.
const NodeType = "http"

// Definition returns the canonical metadata definition for an HTTP node.
func Definition() node.NodeDefinition {
	return node.NodeDefinition{
		Type:        NodeType,
		Name:        "HTTP",
		Description: "Performs outbound HTTP requests to external third-party services and APIs.",
		Category:    node.CategoryIntegration,
		// Conservative until the transport exists: an HTTP call may be a
		// non-idempotent write, so a failed call is only re-run when its
		// error proves it was not applied (node.HTTPStatusError marks 429).
		// A real transport should send NodeInput.IdempotencyKey as an
		// Idempotency-Key header and mark failures of safe methods NotApplied.
		SideEffects: node.SideEffectsUnsafe,
		Inputs: []node.PortDefinition{
			node.NewPortDefinition("url", node.ValueTypeString, false, "Dynamic destination URL override"),
			node.NewPortDefinition("body", node.ValueTypeJSON, false, "HTTP request payload"),
			node.NewPortDefinition("headers", node.ValueTypeObject, false, "HTTP header key-value pairs"),
		},
		Outputs: []node.PortDefinition{
			node.NewPortDefinition("response", node.ValueTypeJSON, true, "HTTP response body"),
			node.NewPortDefinition("status_code", node.ValueTypeNumber, true, "HTTP response status code"),
		},
		Config: []node.ConfigField{
			node.NewConfigField("url", node.ValueTypeString, false, "", "Target URL endpoint"),
			node.NewConfigField("method", node.ValueTypeString, true, "GET", "HTTP method").WithLabel("Method").WithOptions(
				node.Option("GET", "GET"), node.Option("POST", "POST"), node.Option("PUT", "PUT"),
				node.Option("PATCH", "PATCH"), node.Option("DELETE", "DELETE")),
			node.NewConfigField("timeout_ms", node.ValueTypeNumber, false, 30000, "Request timeout in milliseconds"),
			node.NewConfigField("credential_id", node.ValueTypeString, false, "", "Workspace credential identifier for authentication"),
		},
	}
}

// Node is the executable contract implementation for HTTP nodes.
// In Phase 4, this is an architectural contract stub.
type Node struct{}

// New constructs an executable HTTP node.
func New() *Node {
	return &Node{}
}

// Type returns the unique node type identifier.
func (n *Node) Type() string {
	return NodeType
}

// Execute performs an HTTP invocation stub.
func (n *Node) Execute(ctx context.Context, in node.NodeInput) (node.NodeOutput, error) {
	if err := ctx.Err(); err != nil {
		return node.NodeOutput{}, err
	}

	out := node.NewNodeOutput(nil)
	out.SetPort("status_code", node.NewNumberValue(200))
	out.SetPort("response", node.NewJSONValue(map[string]any{"status": "ok"}))
	return out, nil
}
