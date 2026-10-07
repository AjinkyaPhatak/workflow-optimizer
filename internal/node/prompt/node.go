package prompt

import (
	"context"
	"strings"

	"workflow-optimizer/internal/node"
)

// NodeType is the stable identifier for Prompt Template nodes.
const NodeType = "prompt"

// Definition returns the canonical metadata definition for a Prompt Template
// node.
func Definition() node.NodeDefinition {
	return node.NodeDefinition{
		Type:        NodeType,
		Name:        "Prompt Template",
		Description: "Builds prompt text from a template. {{variables}} are filled in from the workflow input, workflow variables and earlier nodes; connect it to an LLM to run the prompt.",
		Category:    node.CategoryAI,
		Inputs: []node.PortDefinition{
			node.NewPortDefinition("variables", node.ValueTypeObject, false, "Upstream data the template refers to (connects the template into the flow)"),
		},
		Outputs: []node.PortDefinition{
			node.NewPortDefinition("prompt", node.ValueTypeString, true, "The finished prompt text"),
		},
		Config: []node.ConfigField{
			node.NewConfigField("template", node.ValueTypeString, true, "",
				"The prompt text. Use {{input.field}}, {{variable_name}} or {{node_id.output}} to insert values.").
				WithLabel("Template").WithMultiline(),
		},
	}
}

// Node builds prompt text. Variable substitution is not done here: the
// execution engine resolves {{references}} in the configuration before the
// node runs, so the template it receives is already filled in.
type Node struct{}

// New constructs an executable Prompt Template node.
func New() *Node {
	return &Node{}
}

// Type returns the unique node type identifier.
func (n *Node) Type() string {
	return NodeType
}

// Execute emits the resolved template as the prompt.
func (n *Node) Execute(ctx context.Context, in node.NodeInput) (node.NodeOutput, error) {
	if err := ctx.Err(); err != nil {
		return node.NodeOutput{}, err
	}
	template, _ := in.GetStringConfig("template")
	if strings.TrimSpace(template) == "" {
		return node.NodeOutput{}, node.NewNodeError(node.ErrCodeConfiguration, "template is empty", false)
	}
	out := node.NewNodeOutput(nil)
	out.SetPort("prompt", node.NewStringValue(template))
	return out, nil
}
