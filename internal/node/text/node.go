package text

import (
	"context"

	"workflow-optimizer/internal/node"
)

// NodeType is the stable identifier for Text nodes.
const NodeType = "text"

// Definition returns the canonical metadata definition for a Text node.
func Definition() node.NodeDefinition {
	return node.NodeDefinition{
		Type:        NodeType,
		Name:        "Text",
		Description: "Produces or manipulates textual content from ports or static configuration.",
		Category:    node.CategoryGeneral,
		Inputs: []node.PortDefinition{
			node.NewPortDefinition("input", node.ValueTypeString, false, "Input string data"),
		},
		Outputs: []node.PortDefinition{
			node.NewPortDefinition("output", node.ValueTypeString, true, "Text output result"),
		},
		Config: []node.ConfigField{
			node.NewConfigField("text", node.ValueTypeString, false, "", "Static text string"),
			node.NewConfigField("template", node.ValueTypeString, false, "", "Text template pattern"),
		},
	}
}

// Node is the executable contract implementation for Text nodes.
// In Phase 4, this is an architectural contract stub.
type Node struct{}

// New constructs an executable Text node.
func New() *Node {
	return &Node{}
}

// Type returns the unique node type identifier.
func (n *Node) Type() string {
	return NodeType
}

// Execute produces text from input ports or configuration.
func (n *Node) Execute(ctx context.Context, in node.NodeInput) (node.NodeOutput, error) {
	if err := ctx.Err(); err != nil {
		return node.NodeOutput{}, err
	}

	out := node.NewNodeOutput(nil)
	if val, ok := in.GetPort("input"); ok {
		out.SetPort("output", val)
	} else if staticText, ok := in.GetStringConfig("text"); ok && staticText != "" {
		out.SetPort("output", node.NewStringValue(staticText))
	} else {
		out.SetPort("output", node.NewStringValue(""))
	}
	return out, nil
}
