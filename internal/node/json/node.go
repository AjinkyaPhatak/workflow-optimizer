package json

import (
	"context"

	"workflow-optimizer/internal/node"
)

// NodeType is the stable identifier for JSON nodes.
const NodeType = "json"

// Definition returns the canonical metadata definition for a JSON node.
func Definition() node.NodeDefinition {
	return node.NodeDefinition{
		Type:        NodeType,
		Name:        "JSON",
		Description: "Parses, structures, or passes JSON data through the workflow graph.",
		Category:    node.CategoryGeneral,
		Inputs: []node.PortDefinition{
			node.NewPortDefinition("input", node.ValueTypeJSON, false, "Input JSON data"),
		},
		Outputs: []node.PortDefinition{
			node.NewPortDefinition("output", node.ValueTypeJSON, true, "Structured JSON output"),
		},
		Config: []node.ConfigField{
			node.NewConfigField("json_content", node.ValueTypeString, false, "{}", "Static JSON template or text"),
		},
	}
}

// Node is the executable contract implementation for JSON nodes.
// In Phase 4, this is an architectural contract stub.
type Node struct{}

// New constructs an executable JSON node.
func New() *Node {
	return &Node{}
}

// Type returns the unique node type identifier.
func (n *Node) Type() string {
	return NodeType
}

// Execute processes JSON data from ports or configuration.
func (n *Node) Execute(ctx context.Context, in node.NodeInput) (node.NodeOutput, error) {
	if err := ctx.Err(); err != nil {
		return node.NodeOutput{}, err
	}

	out := node.NewNodeOutput(nil)
	if val, ok := in.GetPort("input"); ok {
		out.SetPort("output", val)
	} else {
		out.SetPort("output", node.NewJSONValue(map[string]any{}))
	}
	return out, nil
}
