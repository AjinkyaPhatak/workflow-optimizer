package transform

import (
	"context"

	"workflow-optimizer/internal/node"
)

// NodeType is the stable identifier for Transform nodes.
const NodeType = "transform"

// Definition returns the canonical metadata definition for a Transform node.
func Definition() node.NodeDefinition {
	return node.NodeDefinition{
		Type:        NodeType,
		Name:        "Transform",
		Description: "Transforms, maps, or projects structured data according to a transformation rule.",
		Category:    node.CategoryGeneral,
		Inputs: []node.PortDefinition{
			node.NewPortDefinition("input", node.ValueTypeJSON, true, "Payload to be transformed"),
		},
		Outputs: []node.PortDefinition{
			node.NewPortDefinition("output", node.ValueTypeJSON, true, "Transformed output payload"),
		},
		Config: []node.ConfigField{
			node.NewConfigField("expression", node.ValueTypeString, true, "", "Transformation expression or jq/jsonpath string"),
		},
	}
}

// Node is the executable contract implementation for Transform nodes.
// In Phase 4, this is an architectural contract stub.
type Node struct{}

// New constructs an executable Transform node.
func New() *Node {
	return &Node{}
}

// Type returns the unique node type identifier.
func (n *Node) Type() string {
	return NodeType
}

// Execute performs the transformation operation.
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
