package condition

import (
	"context"

	"workflow-optimizer/internal/node"
)

// NodeType is the stable identifier for Condition nodes.
const NodeType = "condition"

// Definition returns the canonical metadata definition for a Condition node.
func Definition() node.NodeDefinition {
	return node.NodeDefinition{
		Type:        NodeType,
		Name:        "Condition",
		Description: "Evaluates branch predicates and routes execution flow to true or false output ports.",
		Category:    node.CategoryGeneral,
		Inputs: []node.PortDefinition{
			node.NewPortDefinition("value", node.ValueTypeJSON, true, "Value evaluated against the condition predicate"),
		},
		Outputs: []node.PortDefinition{
			node.NewPortDefinition("true", node.ValueTypeJSON, false, "Output emitted when predicate evaluates to true"),
			node.NewPortDefinition("false", node.ValueTypeJSON, false, "Output emitted when predicate evaluates to false"),
		},
		Config: []node.ConfigField{
			node.NewConfigField("operator", node.ValueTypeString, false, "equals", "Comparison operator (e.g. equals, not_equals, contains, gt, lt)"),
			node.NewConfigField("right_operand", node.ValueTypeJSON, false, nil, "Target value to compare against"),
		},
	}
}

// Node is the executable contract implementation for Condition nodes.
// In Phase 4, this is an architectural contract stub.
type Node struct{}

// New constructs an executable Condition node.
func New() *Node {
	return &Node{}
}

// Type returns the unique node type identifier.
func (n *Node) Type() string {
	return NodeType
}

// Execute evaluates the condition and routes the value to the appropriate port.
func (n *Node) Execute(ctx context.Context, in node.NodeInput) (node.NodeOutput, error) {
	if err := ctx.Err(); err != nil {
		return node.NodeOutput{}, err
	}

	out := node.NewNodeOutput(nil)
	val, ok := in.GetPort("value")
	if !ok {
		val = node.NewBooleanValue(true)
	}

	// Minimal stub evaluation: boolean true or non-empty string routes to true port
	isTrue := true
	if b, ok := val.Boolean(); ok {
		isTrue = b
	}

	if isTrue {
		out.SetPort("true", val)
	} else {
		out.SetPort("false", val)
	}
	return out, nil
}
