package output

import (
	"context"

	"workflow-optimizer/internal/node"
)

// NodeType is the stable identifier for Output nodes.
const NodeType = "output"

// Definition returns the canonical metadata definition for an Output node.
func Definition() node.NodeDefinition {
	return node.NodeDefinition{
		Type:        NodeType,
		Name:        "Output",
		Description: "Terminal node capturing the final result of a workflow execution branch.",
		Category:    node.CategoryGeneral,
		Role:        node.SemanticRoleExit,
		Inputs: []node.PortDefinition{
			node.NewPortDefinition("value", node.ValueTypeJSON, true, "Final output value to emit"),
		},
		Outputs: []node.PortDefinition{},
		Config:  []node.ConfigField{},
	}
}

// Node is the executable contract implementation for Output nodes.
// In Phase 4, this is an architectural contract stub.
type Node struct{}

// New constructs an executable Output node.
func New() *Node {
	return &Node{}
}

// Type returns the unique node type identifier.
func (n *Node) Type() string {
	return NodeType
}

// Execute consumes the input port value and returns the completed output envelope.
func (n *Node) Execute(ctx context.Context, in node.NodeInput) (node.NodeOutput, error) {
	if err := ctx.Err(); err != nil {
		return node.NodeOutput{}, err
	}

	out := node.NewNodeOutput(nil)
	if val, ok := in.GetPort("value"); ok {
		out.SetPort("result", val)
	}
	return out, nil
}
