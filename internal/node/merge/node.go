package merge

import (
	"context"

	"workflow-optimizer/internal/node"
)

// NodeType is the stable identifier for Merge nodes.
const NodeType = "merge"

// Definition returns the canonical metadata definition for a Merge node.
func Definition() node.NodeDefinition {
	return node.NodeDefinition{
		Type:        NodeType,
		Name:        "Merge",
		Description: "Combines data from multiple upstream execution branches into a single output.",
		Category:    node.CategoryGeneral,
		Inputs: []node.PortDefinition{
			node.NewMultiPortDefinition("left", node.ValueTypeJSON, false, "First upstream branch data"),
			node.NewMultiPortDefinition("right", node.ValueTypeJSON, false, "Second upstream branch data"),
		},
		Outputs: []node.PortDefinition{
			node.NewPortDefinition("merged", node.ValueTypeJSON, true, "Combined merged output"),
		},
		Config: []node.ConfigField{
			node.NewConfigField("mode", node.ValueTypeString, false, "combine", "Merge strategy (e.g. combine, first_available, array)"),
		},
	}
}

// Node is the executable contract implementation for Merge nodes.
// In Phase 4, this is an architectural contract stub.
type Node struct{}

// New constructs an executable Merge node.
func New() *Node {
	return &Node{}
}

// Type returns the unique node type identifier.
func (n *Node) Type() string {
	return NodeType
}

// Execute combines inputs from branches into the merged output port.
func (n *Node) Execute(ctx context.Context, in node.NodeInput) (node.NodeOutput, error) {
	if err := ctx.Err(); err != nil {
		return node.NodeOutput{}, err
	}

	mergedData := make(map[string]any)
	if left, ok := in.GetPort("left"); ok {
		mergedData["left"] = left.Data
	}
	if right, ok := in.GetPort("right"); ok {
		mergedData["right"] = right.Data
	}

	out := node.NewNodeOutput(nil)
	out.SetPort("merged", node.NewJSONValue(mergedData))
	return out, nil
}
