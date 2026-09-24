package input

import (
	"context"

	"workflow-optimizer/internal/node"
)

// NodeType is the stable identifier for Input nodes.
const NodeType = "input"

// Definition returns the canonical metadata definition for an Input node.
func Definition() node.NodeDefinition {
	return node.NodeDefinition{
		Type:        NodeType,
		Name:        "Input",
		Description: "Entrypoint node providing external trigger or workflow input data.",
		Category:    node.CategoryGeneral,
		Role:        node.SemanticRoleEntry,
		Inputs:      []node.PortDefinition{},
		Outputs: []node.PortDefinition{
			node.NewPortDefinition("data", node.ValueTypeJSON, true, "Workflow execution input payload"),
		},
		Config: []node.ConfigField{},
	}
}

// Node is the executable contract implementation for Input nodes.
// In Phase 4, this is an architectural contract stub.
type Node struct{}

// New constructs an executable Input node.
func New() *Node {
	return &Node{}
}

// Type returns the unique node type identifier.
func (n *Node) Type() string {
	return NodeType
}

// Execute emits the incoming data to the output port.
func (n *Node) Execute(ctx context.Context, in node.NodeInput) (node.NodeOutput, error) {
	if err := ctx.Err(); err != nil {
		return node.NodeOutput{}, err
	}

	out := node.NewNodeOutput(nil)
	if val, ok := in.GetPort("data"); ok {
		out.SetPort("data", val)
	} else if in.Config != nil {
		out.SetPort("data", node.NewJSONValue(in.Config))
	} else {
		out.SetPort("data", node.NewJSONValue(map[string]any{}))
	}
	return out, nil
}
