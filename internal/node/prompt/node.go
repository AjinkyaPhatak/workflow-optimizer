package prompt

import (
	"context"

	"workflow-optimizer/internal/node"
)

// NodeType is the stable identifier for Prompt nodes.
const NodeType = "prompt"

// Definition returns the canonical metadata definition for a Prompt node.
func Definition() node.NodeDefinition {
	return node.NodeDefinition{
		Type:        NodeType,
		Name:        "Prompt",
		Description: "Constructs and formats AI prompt strings from templates and dynamic variables.",
		Category:    node.CategoryAI,
		Inputs: []node.PortDefinition{
			node.NewPortDefinition("variables", node.ValueTypeObject, false, "Interpolation variables for template substitution"),
		},
		Outputs: []node.PortDefinition{
			node.NewPortDefinition("prompt", node.ValueTypeString, true, "Formatted prompt ready for LLM consumption"),
		},
		Config: []node.ConfigField{
			node.NewConfigField("template", node.ValueTypeString, true, "", "Prompt template with placeholder expressions"),
		},
	}
}

// Node is the executable contract implementation for Prompt nodes.
// In Phase 4, this is an architectural contract stub.
type Node struct{}

// New constructs an executable Prompt node.
func New() *Node {
	return &Node{}
}

// Type returns the unique node type identifier.
func (n *Node) Type() string {
	return NodeType
}

// Execute formats the prompt string from configuration and input ports.
func (n *Node) Execute(ctx context.Context, in node.NodeInput) (node.NodeOutput, error) {
	if err := ctx.Err(); err != nil {
		return node.NodeOutput{}, err
	}

	template, _ := in.GetStringConfig("template")
	out := node.NewNodeOutput(nil)
	out.SetPort("prompt", node.NewStringValue(template))
	return out, nil
}
